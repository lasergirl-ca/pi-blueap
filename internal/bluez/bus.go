// Package bluez is the daemon's view of BlueZ over the system D-Bus: the
// connection, waiting for org.bluez to appear and noticing when it goes away,
// ObjectManager helpers, adapters and devices (PLAN.md Appendix B).
package bluez

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Service is the BlueZ well-known bus name.
const Service = "org.bluez"

// Interfaces and paths used throughout.
const (
	IfaceAdapter        = "org.bluez.Adapter1"
	IfaceDevice         = "org.bluez.Device1"
	IfaceAgentManager   = "org.bluez.AgentManager1"
	IfaceProfileMgr     = "org.bluez.ProfileManager1"
	IfaceNetworkServer  = "org.bluez.NetworkServer1"
	IfaceAgent          = "org.bluez.Agent1"
	IfaceProfile        = "org.bluez.Profile1"
	IfaceProperties     = "org.freedesktop.DBus.Properties"
	IfaceObjectManager  = "org.freedesktop.DBus.ObjectManager"
	IfaceIntrospectable = "org.freedesktop.DBus.Introspectable"
	RootPath            = dbus.ObjectPath("/org/bluez")
)

// Errors we return to BlueZ (Appendix B).
const (
	ErrRejected = "org.bluez.Error.Rejected"
	ErrCanceled = "org.bluez.Error.Canceled"
)

// Rejected builds the org.bluez.Error.Rejected reply.
func Rejected() *dbus.Error { return dbus.NewError(ErrRejected, nil) }

// Canceled builds the org.bluez.Error.Canceled reply.
func Canceled() *dbus.Error { return dbus.NewError(ErrCanceled, nil) }

// ManagedObjects is what ObjectManager.GetManagedObjects returns.
type ManagedObjects map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// Bus is a private connection to the system bus with signal fan-out.
type Bus struct {
	conn *dbus.Conn
	log  *slog.Logger

	mu       sync.Mutex
	handlers map[int]func(*dbus.Signal)
	next     int
	sigs     chan *dbus.Signal
}

// Connect opens a private system-bus connection and subscribes to the signals
// the daemon cares about: org.bluez name ownership, PropertiesChanged and
// InterfacesAdded/Removed from BlueZ.
func Connect(log *slog.Logger) (*Bus, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("bluez: system bus: %w", err)
	}
	b := &Bus{
		conn:     conn,
		log:      log.With("component", "bluez"),
		handlers: map[int]func(*dbus.Signal){},
		sigs:     make(chan *dbus.Signal, 256),
	}
	matches := [][]dbus.MatchOption{
		{dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchInterface("org.freedesktop.DBus"),
			dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, Service)},
		{dbus.WithMatchSender(Service), dbus.WithMatchInterface(IfaceProperties), dbus.WithMatchMember("PropertiesChanged")},
		{dbus.WithMatchSender(Service), dbus.WithMatchInterface(IfaceObjectManager)},
	}
	for _, m := range matches {
		if err := conn.AddMatchSignal(m...); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("bluez: add match: %w", err)
		}
	}
	conn.Signal(b.sigs)
	go b.dispatch()
	return b, nil
}

// Conn exposes the raw connection for exports and calls.
func (b *Bus) Conn() *dbus.Conn { return b.conn }

// Close closes the connection.
func (b *Bus) Close() error { return b.conn.Close() }

func (b *Bus) dispatch() {
	for sig := range b.sigs {
		b.mu.Lock()
		hs := make([]func(*dbus.Signal), 0, len(b.handlers))
		for _, h := range b.handlers {
			hs = append(hs, h)
		}
		b.mu.Unlock()
		for _, h := range hs {
			h(sig)
		}
	}
}

// OnSignal registers a handler for every signal; it filters itself. The
// returned function removes it.
func (b *Bus) OnSignal(h func(*dbus.Signal)) func() {
	b.mu.Lock()
	id := b.next
	b.next++
	b.handlers[id] = h
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.handlers, id)
		b.mu.Unlock()
	}
}

// HasBlueZ reports whether org.bluez currently has an owner.
func (b *Bus) HasBlueZ() (bool, error) {
	var has bool
	err := b.conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, Service).Store(&has)
	if err != nil {
		return false, fmt.Errorf("bluez: NameHasOwner: %w", err)
	}
	return has, nil
}

// WaitForBlueZ blocks until org.bluez has an owner or ctx ends. It combines
// the NameOwnerChanged signal with a 2 s poll so a signal lost during startup
// cannot wedge the daemon (PLAN.md section 5, lifecycle step 2).
func (b *Bus) WaitForBlueZ(ctx context.Context) error {
	gained := make(chan struct{}, 1)
	cancel := b.OnSignal(func(s *dbus.Signal) {
		if s.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(s.Body) != 3 {
			return
		}
		if name, _ := s.Body[0].(string); name != Service {
			return
		}
		if newOwner, _ := s.Body[2].(string); newOwner != "" {
			select {
			case gained <- struct{}{}:
			default:
			}
		}
	})
	defer cancel()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		has, err := b.HasBlueZ()
		if err != nil {
			return err
		}
		if has {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-gained:
		case <-t.C:
		}
	}
}

// WatchBlueZ returns a channel that receives false when org.bluez loses its
// owner and true when it gains one, until ctx ends.
func (b *Bus) WatchBlueZ(ctx context.Context) <-chan bool {
	out := make(chan bool, 4)
	cancel := b.OnSignal(func(s *dbus.Signal) {
		if s.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(s.Body) != 3 {
			return
		}
		if name, _ := s.Body[0].(string); name != Service {
			return
		}
		newOwner, _ := s.Body[2].(string)
		select {
		case out <- newOwner != "":
		default:
		}
	})
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return out
}

// ManagedObjects calls GetManagedObjects on BlueZ's root.
func (b *Bus) ManagedObjects() (ManagedObjects, error) {
	var objs ManagedObjects
	err := b.conn.Object(Service, "/").Call(IfaceObjectManager+".GetManagedObjects", 0).Store(&objs)
	if err != nil {
		return nil, fmt.Errorf("bluez: GetManagedObjects: %w", err)
	}
	return objs, nil
}

// Object returns a BlueZ bus object.
func (b *Bus) Object(path dbus.ObjectPath) dbus.BusObject { return b.conn.Object(Service, path) }

// Export exports obj at path with the given interface and its introspection XML.
func (b *Bus) Export(obj any, path dbus.ObjectPath, iface, introspectXML string) error {
	if err := b.conn.Export(obj, path, iface); err != nil {
		return fmt.Errorf("bluez: export %s: %w", path, err)
	}
	if err := b.conn.Export(introspectable(introspectXML), path, IfaceIntrospectable); err != nil {
		return fmt.Errorf("bluez: export introspection %s: %w", path, err)
	}
	return nil
}

// Unexport removes an exported object.
func (b *Bus) Unexport(path dbus.ObjectPath, iface string) {
	_ = b.conn.Export(nil, path, iface)
	_ = b.conn.Export(nil, path, IfaceIntrospectable)
}

// --- variant helpers ---

func vString(m map[string]dbus.Variant, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.Value().(string); ok {
			return s
		}
	}
	return ""
}

func vBool(m map[string]dbus.Variant, k string) bool {
	if v, ok := m[k]; ok {
		if b, ok := v.Value().(bool); ok {
			return b
		}
	}
	return false
}

func vUint32(m map[string]dbus.Variant, k string) uint32 {
	if v, ok := m[k]; ok {
		switch n := v.Value().(type) {
		case uint32:
			return n
		case uint16:
			return uint32(n)
		case int32:
			return uint32(n)
		}
	}
	return 0
}

func vStrings(m map[string]dbus.Variant, k string) []string {
	if v, ok := m[k]; ok {
		if s, ok := v.Value().([]string); ok {
			return s
		}
	}
	return nil
}
