package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/fw"
	"palm-gw/internal/setup"
)

// handleConfigGet answers config.get (PLAN.md section 8.8): the value at a
// "section.key" path, a whole section, or everything for an empty path, from
// the configuration in effect with secrets redacted.
func (d *Daemon) handleConfigGet(_ context.Context, req ctl.Request, w *ctl.Writer) error {
	all := flatten(d.config().Redacted())
	if req.Path == "" {
		b, _ := json.Marshal(d.config().Redacted())
		return w.Send(ctl.ConfigValue{Path: "", Value: b})
	}
	if v, ok := all[req.Path]; ok {
		b, _ := json.Marshal(v)
		return w.Send(ctl.ConfigValue{Path: req.Path, Value: b})
	}
	section := map[string]any{}
	for k, v := range all {
		if s, key, _ := strings.Cut(k, "."); s == req.Path {
			section[key] = v
		}
	}
	if len(section) == 0 {
		return fmt.Errorf("config.get: %w %q", setup.ErrUnknownKey, req.Path)
	}
	b, _ := json.Marshal(section)
	return w.Send(ctl.ConfigValue{Path: req.Path, Value: b})
}

// handleConfigSet answers config.set: edits config.toml, refuses anything
// config.Validate() refuses, writes the file atomically and reloads (PLAN.md
// section 8.8). Keys that reload cannot apply come back in needs_restart.
func (d *Daemon) handleConfigSet(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	if req.Path == "" || len(req.Value) == 0 {
		return errors.New("config.set: path and value are required")
	}
	d.cfgEditMu.Lock()
	defer d.cfgEditMu.Unlock()
	path := d.opts.ConfigPath
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text, err := setup.EditConfig(string(old), req.Path, req.Value)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(text)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		return fmt.Errorf("not saved: %w", err)
	}
	if text != string(old) {
		if err := writeAtomic(path, []byte(text)); err != nil {
			return err
		}
		d.log.Info("configuration changed", "event", "config_set", "key", req.Path)
	}
	r, err := d.reload(ctx)
	if err != nil {
		return fmt.Errorf("saved, but reload failed: %w", err)
	}
	return w.Send(ctl.ConfigSetReply{OK: true, Path: req.Path, Reload: r})
}

// writeAtomic replaces path with data, keeping its mode and owner.
func writeAtomic(path string, data []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.toml-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), fi.Mode().Perm()); err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(tmp.Name(), int(st.Uid), int(st.Gid))
	}
	return os.Rename(tmp.Name(), path)
}

// handleFirewallCounters lists the palm_gw table's counted rules.
func (d *Daemon) handleFirewallCounters(ctx context.Context, _ ctl.Request, w *ctl.Writer) error {
	cs, err := fw.Nft{}.Counters(ctx)
	if err != nil {
		return err
	}
	r := ctl.CountersReply{Counters: []ctl.FirewallCounter{}}
	for _, c := range cs {
		r.Counters = append(r.Counters, ctl.FirewallCounter(c))
	}
	return w.Send(r)
}

// handleFirewallTest makes a TCP connection from the Pi to host:port, the
// firewall page's "test reachability" (PLAN.md section 8.12), and says
// whether an allow rule covers it.
func (d *Daemon) handleFirewallTest(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	if req.Host == "" || req.Port < 1 || req.Port > 65535 {
		return errors.New("firewall.test: host and a port from 1 to 65535 are required")
	}
	r := ctl.TestReply{Host: req.Host, Port: req.Port, Allowed: allowed(d.config().Firewall.Allow, req.Host, req.Port)}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort(req.Host, strconv.Itoa(req.Port)))
	r.Millis = time.Since(start).Milliseconds()
	if err != nil {
		r.Problem = err.Error()
	} else {
		r.OK = true
		_ = conn.Close()
	}
	d.log.Info("reachability test", "event", "firewall_test", "host", req.Host, "port", req.Port, "ok", r.OK, "allowed", r.Allowed)
	return w.Send(r)
}

// allowed reports whether a rule lets Palms reach host:port over TCP.
func allowed(rules []config.AllowRule, host string, port int) bool {
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	want := "tcp/" + strconv.Itoa(port)
	for _, r := range rules {
		p, err := netip.ParsePrefix(r.Host)
		if err != nil {
			if ra, err2 := netip.ParseAddr(r.Host); err2 == nil {
				p = netip.PrefixFrom(ra, ra.BitLen())
			} else {
				continue
			}
		}
		if !p.Contains(a) {
			continue
		}
		for _, pp := range r.Ports {
			if pp == want {
				return true
			}
		}
	}
	return false
}

// handleRestart answers, then stops palm-gwd cleanly; systemd's
// Restart=always starts it again (the settings page's Restart button).
func (d *Daemon) handleRestart(_ context.Context, _ ctl.Request, w *ctl.Writer) error {
	d.log.Info("restart requested", "event", "restart_requested")
	if err := w.Send(ctl.OKReply{OK: true}); err != nil {
		return nil
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	return nil
}
