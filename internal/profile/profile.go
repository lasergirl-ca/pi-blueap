package profile

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"

	"palm-gw/internal/allow"
	"palm-gw/internal/bluez"
	"palm-gw/internal/config"
	"palm-gw/internal/event"
	"palm-gw/internal/ppp"
)

// PathFor is the object path of a profile: /palm/spp, /palm/lap, /palm/dun.
func PathFor(k Kind) dbus.ObjectPath { return dbus.ObjectPath("/palm/" + string(k)) }

// Status is one profile's line in `palm-gwctl status`.
type Status struct {
	Kind       Kind `json:"kind"`
	Enabled    bool `json:"enabled"`
	Channel    int  `json:"channel"`
	Registered bool `json:"registered"`
}

// Server owns the three Profile1 objects.
type Server struct {
	bus     *bluez.Bus
	cfg     *config.Config
	mgr     *ppp.Manager
	allow   *allow.List
	events  *event.Bus
	log     *slog.Logger
	adapter func() dbus.ObjectPath // the gateway's adapter; connections elsewhere are refused

	// OnReleased runs when BlueZ calls Profile1.Release.
	OnReleased func()

	mu         sync.Mutex
	registered map[Kind]bool
	handshake  *config.Handshake // set by SetHandshake on reload; nil = cfg's (D-060)
}

// SetHandshake applies new [handshake] settings to the next connection.
func (s *Server) SetHandshake(h config.Handshake) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handshake = &h
}

func (s *Server) handshakeCfg() config.Handshake {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handshake != nil {
		return *s.handshake
	}
	return s.cfg.Handshake
}

// New builds the server; nothing touches D-Bus until Export.
func New(bus *bluez.Bus, cfg *config.Config, mgr *ppp.Manager, al *allow.List, events *event.Bus, log *slog.Logger, adapter func() dbus.ObjectPath) *Server {
	return &Server{
		bus: bus, cfg: cfg, mgr: mgr, allow: al, events: events,
		log: log.With("component", "profile"), adapter: adapter, registered: map[Kind]bool{},
	}
}

func (s *Server) profileCfg(k Kind) config.Profile {
	switch k {
	case SPP:
		return s.cfg.SPP
	case LAP:
		return s.cfg.LAP
	default:
		return s.cfg.DUN
	}
}

// Export puts /palm/{spp,lap,dun} on the bus with introspection data
// (PLAN.md section 12: without it BlueZ may never call NewConnection).
func (s *Server) Export() error {
	for _, k := range Kinds {
		if err := s.bus.Export(&profileObj{s: s, kind: k}, PathFor(k), bluez.IfaceProfile, bluez.Profile1XML); err != nil {
			return fmt.Errorf("profile %s: %w", k, err)
		}
	}
	return nil
}

// Register registers every enabled profile with BlueZ's ProfileManager1.
func (s *Server) Register() error {
	mgr := s.bus.Object(bluez.RootPath)
	for _, k := range Kinds {
		pc := s.profileCfg(k)
		if !pc.Enabled {
			continue
		}
		rec, err := Record(k, pc.Channel)
		if err != nil {
			return err
		}
		opts := map[string]dbus.Variant{
			"Name":                  dbus.MakeVariant(k.ServiceName()),
			"Role":                  dbus.MakeVariant("server"),
			"Channel":               dbus.MakeVariant(uint16(pc.Channel)),
			"RequireAuthentication": dbus.MakeVariant(true),
			"RequireAuthorization":  dbus.MakeVariant(true),
			"AutoConnect":           dbus.MakeVariant(false),
			"ServiceRecord":         dbus.MakeVariant(rec),
		}
		if err := mgr.Call(bluez.IfaceProfileMgr+".RegisterProfile", 0, PathFor(k), k.UUID(), opts).Err; err != nil {
			var derr dbus.Error
			if !errors.As(err, &derr) || derr.Name != "org.bluez.Error.AlreadyExists" {
				return fmt.Errorf("profile %s: RegisterProfile: %w", k, err)
			}
			s.log.Info("profile was already registered", "profile", k)
		}
		s.setRegistered(k, true)
		s.log.Info("profile registered", "profile", k, "channel", pc.Channel, "uuid", k.UUID())
	}
	return nil
}

// Unregister removes the profiles from BlueZ (daemon shutdown).
func (s *Server) Unregister() {
	mgr := s.bus.Object(bluez.RootPath)
	for _, k := range Kinds {
		if s.isRegistered(k) {
			_ = mgr.Call(bluez.IfaceProfileMgr+".UnregisterProfile", 0, PathFor(k)).Err
		}
	}
	s.MarkUnregistered()
}

// MarkUnregistered forgets the registrations (BlueZ went away).
func (s *Server) MarkUnregistered() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered = map[Kind]bool{}
}

func (s *Server) setRegistered(k Kind, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered[k] = v
}

func (s *Server) isRegistered(k Kind) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registered[k]
}

// Registered reports whether every enabled profile is registered.
func (s *Server) Registered() bool {
	for _, k := range Kinds {
		if s.profileCfg(k).Enabled && !s.isRegistered(k) {
			return false
		}
	}
	return true
}

// Status lists the profiles for `palm-gwctl status`.
func (s *Server) Status() []Status {
	out := make([]Status, 0, len(Kinds))
	for _, k := range Kinds {
		pc := s.profileCfg(k)
		out = append(out, Status{Kind: k, Enabled: pc.Enabled, Channel: pc.Channel, Registered: s.isRegistered(k)})
	}
	return out
}

// profileObj is one exported org.bluez.Profile1.
type profileObj struct {
	s    *Server
	kind Kind
}

func (p *profileObj) Release() *dbus.Error {
	p.s.log.Warn("BlueZ released the profile", "profile", p.kind)
	p.s.setRegistered(p.kind, false)
	if p.s.OnReleased != nil {
		p.s.OnReleased()
	}
	return nil
}

func (p *profileObj) RequestDisconnection(device dbus.ObjectPath) *dbus.Error {
	addr := bluez.AddrFromPath(device)
	p.s.log.Info("BlueZ asked to disconnect", "profile", p.kind, "addr", addr)
	go p.s.mgr.Disconnect(addr, "bluez_request")
	return nil
}

// NewConnection takes the RFCOMM fd and returns at once; the handshake and
// pppd run in a goroutine (PLAN.md section 8.3, step 7).
func (p *profileObj) NewConnection(device dbus.ObjectPath, fd dbus.UnixFD, _ map[string]dbus.Variant) *dbus.Error {
	s := p.s
	f, err := bluez.FileFromFD(fd, "rfcomm-"+string(p.kind))
	if err != nil {
		s.log.Error("could not take the RFCOMM fd", "profile", p.kind, "err", err)
		return bluez.Rejected()
	}
	addr, name := bluez.AddrFromPath(device), ""
	if dev, err := s.bus.Device(device); err == nil {
		addr, name = dev.Address, dev.DisplayName()
	}
	refuse := func(reason string) *dbus.Error {
		ppp.HangUp(f)
		s.log.Warn("session refused", "event", "session_refused", "reason", reason, "profile", p.kind, "addr", addr, "name", name)
		s.events.Emit("session_refused", addr, name, "reason", reason, "profile", string(p.kind))
		return bluez.Rejected()
	}
	if !bluez.UnderAdapter(device, s.adapter()) {
		return refuse("other_adapter")
	}
	// BlueZ skips AuthorizeService for Trusted devices, so check here too.
	if !s.allow.Allowed(addr) {
		return refuse("not_allowed")
	}
	sess, replaced, err := s.mgr.Begin(addr, name, string(p.kind), f)
	if errors.Is(err, ppp.ErrFull) {
		return refuse("max_sessions")
	}
	if err != nil {
		return refuse(err.Error())
	}
	s.log.Info("RFCOMM connection", "event", "connection", "profile", p.kind, "addr", addr, "name", name, "channel", s.profileCfg(p.kind).Channel)
	go s.serve(p.kind, sess, replaced, f)
	return nil
}

func (s *Server) serve(k Kind, sess *ppp.Session, replaced []*ppp.Session, f *os.File) {
	info := sess.Info()
	for _, old := range replaced {
		oi := old.Info()
		s.log.Info("replacing the device's previous session", "event", "session_replaced", "addr", info.Addr, "old_profile", oi.Profile, "old_ifname", oi.Ifname)
		s.events.Emit("session_replaced", info.Addr, info.Name, "old_profile", oi.Profile)
		old.Terminate("replaced")
	}
	res, err := Run(sess.Context(), int(f.Fd()), k, s.handshakeCfg())
	if err != nil {
		s.log.Info("connection ended during the handshake", "addr", info.Addr, "profile", k, "err", err)
		sess.Abort("handshake: " + err.Error())
		return
	}
	attrs := []any{"event", "handshake", "mode", res.Mode, "profile", k, "addr", info.Addr, "name", info.Name}
	if res.Clients > 0 {
		attrs = append(attrs, "clients", res.Clients)
	}
	if res.ATCommands > 0 {
		attrs = append(attrs, "at_commands", res.ATCommands, "dial", res.Dial)
	}
	if res.FirstBytes != "" {
		attrs = append(attrs, "first_bytes", res.FirstBytes)
	}
	switch res.Mode {
	case ModeTimeout:
		s.log.Warn("handshake timed out, starting pppd blind", attrs...)
	case ModeUnknown:
		s.log.Warn("handshake saw unknown bytes, leaving them for pppd", attrs...)
	default:
		s.log.Info("handshake done", attrs...)
	}
	s.events.Emit("handshake", info.Addr, info.Name, "mode", res.Mode, "profile", string(k))
	if res.Mode == ModeHangup {
		sess.Abort("hangup")
		return
	}
	_ = sess.Start(res.Mode)
}
