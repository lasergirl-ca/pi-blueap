package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"palm-gw/deploy"
	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/fw"
)

// Defaults for Options.DnsmasqConf and Options.DnsmasqUnit (section 8.7, D-005).
const (
	defaultDnsmasqConf = "/etc/palm-gw/dnsmasq.conf"
	defaultDnsmasqUnit = "palm-gw-dnsmasq.service"
	// firewallEvery re-applies the sets: a `systemctl reload nftables`
	// reloads the table from disk and empties them.
	firewallEvery = 30 * time.Second
)

// applyFirewall replaces the daemon-managed nftables sets from cfg. It logs
// only when the outcome changes, so the periodic re-apply is quiet.
func (d *Daemon) applyFirewall(ctx context.Context, cfg *config.Config, why string) error {
	if !cfg.Firewall.ManageNftables {
		d.fwMu.Lock()
		d.fw = ctl.FirewallStatus{Managed: false}
		d.fwMu.Unlock()
		return nil
	}
	err := fw.Nft{}.Apply(ctx, fw.Script(cfg.Firewall))
	lan, icmp := 0, 0
	for _, r := range cfg.Firewall.Allow {
		for _, p := range r.Ports {
			if p == "icmp" {
				icmp++
			} else {
				lan++
			}
		}
	}
	d.fwMu.Lock()
	prev := d.fw
	d.fw = ctl.FirewallStatus{Managed: true, AllowRules: len(cfg.Firewall.Allow), LANPorts: lan, LANICMP: icmp,
		PalmToPi: len(cfg.Firewall.PalmToPi), AppliedAt: prev.AppliedAt}
	if err != nil {
		d.fw.Error = err.Error()
	} else {
		d.fw.AppliedAt = time.Now()
	}
	d.fwMu.Unlock()
	switch {
	case err != nil && prev.Error != err.Error():
		d.log.Error("firewall sets not applied", "event", "firewall_error", "why", why, "err", err)
		d.events.Emit("firewall_error", "", "", "err", err.Error())
	case err == nil && (why != "periodic" || prev.Error != ""):
		d.log.Info("firewall sets applied", "event", "firewall_applied", "why", why,
			"allow_rules", len(cfg.Firewall.Allow), "palm_to_pi", len(cfg.Firewall.PalmToPi))
		d.events.Emit("firewall_applied", "", "", "why", why)
	}
	return err
}

// keepFirewall applies the sets now and every firewallEvery until ctx ends.
func (d *Daemon) keepFirewall(ctx context.Context) {
	_ = d.applyFirewall(ctx, d.config(), "start")
	t := time.NewTicker(firewallEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = d.applyFirewall(ctx, d.config(), "periodic")
		}
	}
}

// syncDnsmasq renders the dnsmasq configuration from cfg and, if it differs
// from the file on disk, writes it and restarts the dnsmasq unit.
func (d *Daemon) syncDnsmasq(ctx context.Context, cfg *config.Config) (changed bool, err error) {
	tmpl, err := deploy.FS.ReadFile(deploy.Dnsmasq)
	if err != nil {
		return false, err
	}
	text, err := cfg.Render(deploy.Dnsmasq, string(tmpl))
	if err != nil {
		return false, err
	}
	path := d.opts.DnsmasqConf
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, []byte(text)) {
		return false, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dnsmasq.conf.")
	if err != nil {
		return false, err
	}
	_, werr := tmp.WriteString(text)
	cerr := tmp.Close()
	if err := firstErr(werr, cerr, os.Chmod(tmp.Name(), 0o644)); err != nil {
		_ = os.Remove(tmp.Name())
		return false, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "systemctl", "restart", d.opts.DnsmasqUnit).CombinedOutput(); err != nil {
		return true, fmt.Errorf("wrote %s but systemctl restart %s failed: %w: %s", path, d.opts.DnsmasqUnit, err, bytes.TrimSpace(out))
	}
	d.log.Info("dnsmasq configuration rewritten", "event", "dnsmasq_reloaded", "path", path, "unit", d.opts.DnsmasqUnit)
	d.events.Emit("dnsmasq_reloaded", "", "")
	return true, nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// liveMerge copies onto a copy of cur the settings that take effect without a
// restart (DECISIONS.md D-030): everything the daemon reads per use (pairing
// window, PIN, trust), the allowlist, the firewall sets, and what dnsmasq
// renders from. Everything else stays as it is until palm-gwd restarts.
func liveMerge(cur, next *config.Config) *config.Config {
	m := *cur
	m.Bluetooth.PIN = next.Bluetooth.PIN
	m.Bluetooth.PINLength = next.Bluetooth.PINLength
	m.Bluetooth.PairWindowDefaultMinutes = next.Bluetooth.PairWindowDefaultMinutes
	m.Bluetooth.AllowAny = next.Bluetooth.AllowAny
	m.Bluetooth.Allow = next.Bluetooth.Allow
	m.Bluetooth.TrustOnPair = next.Bluetooth.TrustOnPair
	m.Network.DNSUpstream = next.Network.DNSUpstream
	m.Network.DHCPPool = next.Network.DHCPPool
	m.Firewall = next.Firewall
	return &m
}

// diffKeys lists the dotted keys whose values differ between a and b, using
// the JSON names (which match the TOML keys).
func diffKeys(a, b *config.Config) []string {
	fa, fb := flatten(a), flatten(b)
	var keys []string
	for k, va := range fa {
		if vb, ok := fb[k]; !ok || !reflect.DeepEqual(va, vb) {
			keys = append(keys, k)
		}
	}
	for k := range fb {
		if _, ok := fa[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// flatten maps a configuration to "section.key" -> value; arrays (such as
// firewall.allow) stay whole.
func flatten(c *config.Config) map[string]any {
	raw, _ := json.Marshal(c)
	var top map[string]any
	_ = json.Unmarshal(raw, &top)
	out := map[string]any{}
	for section, v := range top {
		if m, ok := v.(map[string]any); ok {
			for k, vv := range m {
				out[section+"."+k] = vv
			}
			continue
		}
		out[section] = v
	}
	return out
}

// reload re-reads the configuration file and applies what can change live
// (PLAN.md section 8.8, D-030).
func (d *Daemon) reload(ctx context.Context) (ctl.ReloadReply, error) {
	next, err := config.Load(d.opts.ConfigPath)
	if err != nil {
		return ctl.ReloadReply{}, err
	}
	cur := d.config()
	merged := liveMerge(cur, next)
	if err := merged.Validate(); err != nil {
		return ctl.ReloadReply{}, fmt.Errorf("the live part of the new configuration is not valid with the running one (restart palm-gwd instead): %w", err)
	}
	r := ctl.ReloadReply{OK: true, Applied: diffKeys(cur, merged), NeedsRestart: diffKeys(merged, next)}
	d.cfgp.Store(merged)
	d.allow.Reconfigure(merged.Bluetooth.Allow, merged.Bluetooth.AllowAny)
	if err := d.applyFirewall(ctx, merged, "reload"); err != nil {
		r.OK = false
		r.Errors = append(r.Errors, err.Error())
	}
	changed, err := d.syncDnsmasq(ctx, merged)
	r.DnsmasqRestarted = changed
	if err != nil {
		r.OK = false
		r.Errors = append(r.Errors, "dnsmasq: "+err.Error())
	}
	d.log.Info("configuration reloaded", "event", "reload", "applied", r.Applied, "needs_restart", r.NeedsRestart,
		"dnsmasq_restarted", changed, "ok", r.OK)
	d.events.Emit("reload", "", "")
	return r, nil
}
