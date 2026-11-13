package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/setup"
)

// The WireGuard tunnel from the daemon's side (D-069, D-072): its state for
// status, wg.set/import/generate (which save [wireguard] and start the same apply
// as the network settings), wg.up/down, and a watch that logs wg_stale when the
// tunnel goes quiet while Palms are connected. NetworkManager owns the tunnel;
// the daemon only asks it to go up or down.

// wgStaleAfter is how long the tunnel may receive nothing before it counts as
// stale. With a keepalive every 25 s, a live peer answers within seconds.
const wgStaleAfter = 3 * time.Minute

// wgWatch is what the daemon remembers about the tunnel between checks.
type wgWatch struct {
	mu      sync.Mutex
	iface   string
	rx      uint64
	heard   time.Time // when rx last grew
	stale   bool
	ifbytes func(string) (uint64, uint64)
	now     func() time.Time
}

// observe takes a reading and says whether the tunnel just went stale (true,
// false) or came back (false, true).
func (w *wgWatch) observe(iface string, live bool) (wentStale, cameBack bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if iface != w.iface { // a new interface starts afresh (the mutex stays held)
		w.iface, w.rx, w.heard, w.stale = iface, 0, time.Time{}, false
	}
	if !live {
		w.rx, w.heard = 0, time.Time{}
		was := w.stale
		w.stale = false
		return false, was
	}
	rx, _ := w.ifbytes(iface)
	now := w.now()
	if w.heard.IsZero() || rx != w.rx { // first sight counts from now
		w.rx, w.heard = rx, now
	}
	stale := now.Sub(w.heard) > wgStaleAfter
	switch {
	case stale && !w.stale:
		w.stale = true
		return true, false
	case !stale && w.stale:
		w.stale = false
		return false, true
	}
	return false, false
}

func (w *wgWatch) lastHeard() (seconds int64, stale bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.heard.IsZero() {
		return -1, w.stale
	}
	return int64(w.now().Sub(w.heard).Seconds()), w.stale
}

func (d *Daemon) wg() *wgWatch {
	d.wgOnce.Do(func() {
		d.wgw = &wgWatch{ifbytes: ifBytes, now: time.Now}
		if d.opts.IfBytes != nil {
			d.wgw.ifbytes = d.opts.IfBytes
		}
	})
	return d.wgw
}

// wgState asks NetworkManager whether the tunnel's profile exists and is up.
func (d *Daemon) wgState() string {
	if !d.config().WireGuard.Enabled {
		return "off"
	}
	n, err := d.netManager()
	if err != nil {
		return "down"
	}
	defer n.Close()
	exists, active, err := n.ConnectionActive(setup.WGProfile)
	switch {
	case err != nil:
		return "down"
	case !exists:
		return "missing"
	case active:
		return "up"
	}
	return "down"
}

// wgStatus is the tunnel for status and wg.show.
func (d *Daemon) wgStatus(ctx context.Context) *ctl.WGStatus {
	g := d.config().WireGuard
	st := &ctl.WGStatus{
		Enabled: g.Enabled, State: d.wgState(), Interface: g.Interface, Mode: g.Mode, OnDown: g.OnDown,
		Address: g.Address, Endpoint: g.Endpoint, PublicKey: g.PublicKey, PeerPublicKey: g.PeerPublicKey,
		AllowedIPs: append([]string{}, g.AllowedIPs...), DNS: append([]string{}, g.DNS...),
		LastHeardSeconds: -1, Applying: d.applyRunning(ctx),
	}
	if st.State == "up" {
		st.RxBytes, st.TxBytes = d.wg().ifbytes(g.Interface)
		st.LastHeardSeconds, st.Stale = d.wg().lastHeard()
	}
	return st
}

// keepWG checks the tunnel every 30 s while it is enabled: wg_stale when it has
// received nothing for wgStaleAfter while Palms are connected, wg_ok when it
// comes back.
func (d *Daemon) keepWG(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		g := d.config().WireGuard
		if g.Enabled {
			up := d.wgState() == "up"
			wentStale, cameBack := d.wg().observe(g.Interface, up)
			if wentStale && len(d.ppp.Sessions())+d.panClients() > 0 {
				d.log.Warn("the WireGuard tunnel has received nothing for 3 minutes", "event", "wg_stale",
					"iface", g.Interface, "endpoint", g.Endpoint)
				d.events.Emit("wg_stale", "", "")
			}
			if cameBack {
				d.log.Info("the WireGuard tunnel is receiving again", "event", "wg_ok", "iface", g.Interface)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Daemon) handleWG(ctx context.Context, _ ctl.Request, w *ctl.Writer) error {
	return w.Send(d.wgStatus(ctx))
}

// saveWG writes [wireguard] edits, validated, and starts the apply that builds
// the tunnel; config.toml is put back if the apply cannot start.
func (d *Daemon) saveWG(ctx context.Context, edits []setup.KV) (ctl.WGSetReply, error) {
	no := ctl.WGSetReply{}
	path := d.opts.ConfigPath
	old, err := os.ReadFile(path)
	if err != nil {
		return no, err
	}
	text := setup.SetKeys(string(old), edits)
	if text == string(old) {
		return ctl.WGSetReply{OK: true, Changed: []string{}}, nil
	}
	cfg, err := config.Parse(text)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		return no, fmt.Errorf("not saved: %w", err)
	}
	if err := writeAtomic(path, []byte(text)); err != nil {
		return no, err
	}
	var changed []string
	for _, e := range edits {
		changed = append(changed, e.Section+"."+e.Key)
	}
	return d.applyWG(ctx, path, old, changed)
}

func (d *Daemon) applyWG(ctx context.Context, path string, old []byte, changed []string) (ctl.WGSetReply, error) {
	out, err := d.startApply(ctx)
	if err != nil {
		if rerr := writeAtomic(path, old); rerr != nil {
			return ctl.WGSetReply{}, fmt.Errorf("could not start palm-gw setup --apply (%w: %s), and could not put config.toml back: %w", err, strings.TrimSpace(string(out)), rerr)
		}
		return ctl.WGSetReply{}, fmt.Errorf("not saved: could not start palm-gw setup --apply: %w: %s", err, strings.TrimSpace(string(out)))
	}
	d.log.Info("WireGuard settings saved; applying", "event", "wg_set", "keys", changed, "unit", applyUnit)
	return ctl.WGSetReply{OK: true, Changed: changed, Applying: true, Unit: applyUnit, PublicKey: d.readWGPublicKey(path)}, nil
}

func (d *Daemon) readWGPublicKey(path string) string {
	if c, err := config.Load(path); err == nil {
		return c.WireGuard.PublicKey
	}
	return ""
}

// lockForApply takes the config edit lock unless an apply is already running.
func (d *Daemon) lockForApply(ctx context.Context) error {
	d.cfgEditMu.Lock()
	if d.applyRunning(ctx) {
		d.cfgEditMu.Unlock()
		return errors.New("settings are being applied now; palm-gwd restarts when that is done")
	}
	return nil
}

// handleWGSet changes the tunnel's settings: on or off, full or split, what
// happens when it is down, its networks, and the peer's details.
func (d *Daemon) handleWGSet(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	r := req.WG
	if r == nil {
		return errors.New("wg.set: nothing to change")
	}
	var edits []setup.KV
	str := func(key, v string) {
		if v = strings.TrimSpace(v); v != "" {
			edits = append(edits, setup.KV{Section: "wireguard", Key: key, Value: setup.TOMLString(v)})
		}
	}
	str("mode", r.Mode)
	str("on_down", r.OnDown)
	str("address", r.Address)
	str("endpoint", r.Endpoint)
	str("peer_public_key", r.PeerPublicKey)
	if r.AllowedIPs != nil {
		edits = append(edits, setup.KV{Section: "wireguard", Key: "allowed_ips", Value: setup.TOMLStrings(r.AllowedIPs)})
	}
	if r.Enabled != nil {
		edits = append(edits, setup.KV{Section: "wireguard", Key: "enabled", Value: fmt.Sprint(*r.Enabled)})
	}
	if len(edits) == 0 {
		return errors.New("wg.set: nothing to change")
	}
	if err := d.lockForApply(ctx); err != nil {
		return err
	}
	defer d.cfgEditMu.Unlock()
	rep, err := d.saveWG(ctx, edits)
	if err != nil {
		return err
	}
	return w.Send(rep)
}

// handleWGImport reads a .conf (its key goes root-only to the state directory
// until the apply hands it to NetworkManager) and applies it.
func (d *Daemon) handleWGImport(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	if req.WG == nil || strings.TrimSpace(req.WG.Conf) == "" {
		return errors.New("wg.import: no .conf")
	}
	if err := d.lockForApply(ctx); err != nil {
		return err
	}
	defer d.cfgEditMu.Unlock()
	path := d.opts.ConfigPath
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	res, err := setup.ImportWG("", path, filepath.Dir(d.opts.StatePath), req.WG.Conf)
	if err != nil {
		return err
	}
	rep, err := d.applyWG(ctx, path, old, []string{"wireguard"})
	if err != nil {
		return err
	}
	rep.Mode = res.Mode
	d.log.Info("WireGuard .conf imported", "event", "wg_imported", "mode", res.Mode, "public_key", res.PublicKey)
	return w.Send(rep)
}

// handleWGGenerate makes the gateway a new key pair. While the tunnel is off it
// only records the public key; while it is on, the new key is applied.
func (d *Daemon) handleWGGenerate(ctx context.Context, _ ctl.Request, w *ctl.Writer) error {
	if err := d.lockForApply(ctx); err != nil {
		return err
	}
	defer d.cfgEditMu.Unlock()
	path := d.opts.ConfigPath
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	res, err := setup.GenerateWG("", path, filepath.Dir(d.opts.StatePath))
	if err != nil {
		return err
	}
	d.log.Info("WireGuard key pair made", "event", "wg_key", "public_key", res.PublicKey)
	if !d.readWGEnabled(path) {
		return w.Send(ctl.WGSetReply{OK: true, Changed: []string{"wireguard.public_key"}, PublicKey: res.PublicKey})
	}
	rep, err := d.applyWG(ctx, path, old, []string{"wireguard.public_key"})
	if err != nil {
		return err
	}
	return w.Send(rep)
}

func (d *Daemon) readWGEnabled(path string) bool {
	c, err := config.Load(path)
	return err == nil && c.WireGuard.Enabled
}

// handleWGUpDown brings the tunnel up or down now, without changing the
// settings: NetworkManager brings it back up at the next boot.
func (d *Daemon) handleWGUpDown(up bool) func(context.Context, ctl.Request, *ctl.Writer) error {
	return func(ctx context.Context, _ ctl.Request, w *ctl.Writer) error {
		if !d.config().WireGuard.Enabled {
			return errors.New("WireGuard is off in config.toml ([wireguard] enabled = false)")
		}
		n, err := d.netManager()
		if err != nil {
			return err
		}
		defer n.Close()
		if up {
			err = n.ConnectionUp(setup.WGProfile)
		} else {
			err = n.ConnectionDown(setup.WGProfile)
		}
		if err != nil {
			return err
		}
		d.log.Info("WireGuard tunnel "+map[bool]string{true: "up", false: "down"}[up], "event", map[bool]string{true: "wg_up", false: "wg_down"}[up])
		return w.Send(d.wgStatus(ctx))
	}
}
