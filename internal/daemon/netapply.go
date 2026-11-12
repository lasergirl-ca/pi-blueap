package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/setup"
)

// The uplink, the Palm subnet and the web TLS mode re-render nftables,
// dnsmasq, the bridge and the certificate, which palm-gwd's sandbox cannot
// write (ProtectSystem=strict). The settings page saves them to config.toml
// and starts `palm-gw setup --apply` as a transient unit, outside the
// sandbox and outside palm-gwd's cgroup, which restarts palm-gwd when it is
// done (D-054).

// applyUnit is the transient unit's name; one apply runs at a time.
const applyUnit = "palm-gw-apply"

func (d *Daemon) applyRecordPath() string {
	return filepath.Join(filepath.Dir(d.opts.StatePath), "last-apply.json")
}

// command runs a helper; tests replace it.
func (d *Daemon) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	if d.opts.Command != nil {
		return d.opts.Command(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// setupBin is palm-gw, installed next to palm-gwd in both layouts.
func (d *Daemon) setupBin() string {
	if d.opts.SetupBin != "" {
		return d.opts.SetupBin
	}
	exe, err := os.Executable()
	if err != nil {
		return "palm-gw"
	}
	return filepath.Join(filepath.Dir(exe), "palm-gw")
}

func (d *Daemon) host() setup.Host {
	h := setup.LocalHost(nil)
	if d.opts.HostRoot != "" {
		h.Root = d.opts.HostRoot
	}
	if d.opts.Command != nil {
		h.Run = func(name string, args ...string) ([]byte, error) {
			return d.opts.Command(context.Background(), name, args...)
		}
	}
	return h
}

// applyRunning reports whether an apply started earlier is still going.
func (d *Daemon) applyRunning(ctx context.Context) bool {
	_, err := d.command(ctx, "systemctl", "is-active", "--quiet", applyUnit)
	return err == nil
}

// handleChoices answers choices: the adapters, uplinks and subnet check the
// settings page's drop-downs are built from, and the last apply's outcome.
func (d *Daemon) handleChoices(ctx context.Context, _ ctl.Request, w *ctl.Writer) error {
	r := ctl.ChoicesReply{Adapters: []setup.Adapter{}, Uplinks: []setup.Uplink{}}
	if d.bus != nil {
		if as, err := d.bus.Adapters(); err == nil {
			for _, a := range as {
				r.Adapters = append(r.Adapters, setup.Adapter{HCI: a.HCI, Address: a.Props.Address, Bus: a.HWBus, Powered: a.Props.Powered})
			}
		}
	}
	d.mu.Lock()
	if d.adapter != nil {
		r.AdapterIn = d.adapter.Props.Address
	}
	d.mu.Unlock()
	h := d.host()
	if ups, err := h.DetectUplinks(); err == nil && ups != nil {
		r.Uplinks = ups
	}
	if s, err := h.DetectSubnet(d.config().Network.Subnet); err == nil {
		r.Subnet = &s
	}
	r.Applying = d.applyRunning(ctx)
	if rec, err := setup.ReadApplyRecord(d.applyRecordPath()); err == nil {
		r.LastApply = rec
	}
	return w.Send(r)
}

// handleNetworkSet answers network.set: checks the change, writes it to
// config.toml and starts the apply. If the apply cannot be started, the file
// is put back, so the next restart does not pick up half a change.
func (d *Daemon) handleNetworkSet(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	if req.Network == nil {
		return errors.New("network.set: nothing to change")
	}
	r, err := d.setNetwork(ctx, *req.Network)
	if err != nil {
		return err
	}
	return w.Send(r)
}

// setNetwork is network.set's work, also used by the internet wizard.
func (d *Daemon) setNetwork(ctx context.Context, ch setup.NetworkChange) (ctl.NetworkSetReply, error) {
	no := ctl.NetworkSetReply{}
	d.cfgEditMu.Lock()
	defer d.cfgEditMu.Unlock()
	if d.applyRunning(ctx) {
		return no, errors.New("network settings are being applied now; palm-gwd restarts when that is done")
	}
	path := d.opts.ConfigPath
	old, err := os.ReadFile(path)
	if err != nil {
		return no, err
	}
	cur, err := config.Parse(string(old))
	if err != nil {
		return no, err
	}
	edits, err := setup.NetworkEdits(cur, ch)
	if err != nil {
		return no, err
	}
	if u := strings.TrimSpace(ch.Uplink); u != "" && u != cur.Network.Uplink && !d.host().HasIface(u) {
		return no, fmt.Errorf("uplink: this Pi has no interface called %q", u)
	}
	var changed []string
	for _, e := range edits {
		changed = append(changed, e.Section+"."+e.Key)
		if e.Section == "network" && e.Key == "subnet" {
			want := netip.MustParsePrefix(strings.TrimSpace(ch.Subnet)).String()
			if s, err := d.host().DetectSubnet(want); err == nil && !s.Free {
				msg := fmt.Sprintf("%s overlaps a network this Pi is on (%s)", want, strings.Join(s.Conflicts, ", "))
				if s.Suggest != "" {
					msg += "; " + s.Suggest + " is free"
				}
				return no, errors.New(msg)
			}
		}
	}
	if len(edits) == 0 {
		return ctl.NetworkSetReply{OK: true, Changed: []string{}}, nil
	}
	text := setup.SetKeys(string(old), edits)
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
	_ = os.Remove(d.applyRecordPath())
	out, err := d.command(ctx, "systemd-run", "--unit="+applyUnit, "--collect", "--quiet",
		"--description=palm-gw: apply network settings",
		d.setupBin(), "setup", "--apply", "--config", path, "--record", d.applyRecordPath())
	if err != nil {
		if rerr := writeAtomic(path, old); rerr != nil {
			return no, fmt.Errorf("could not start palm-gw setup --apply (%w: %s), and could not put config.toml back: %w", err, strings.TrimSpace(string(out)), rerr)
		}
		return no, fmt.Errorf("not saved: could not start palm-gw setup --apply: %w: %s", err, strings.TrimSpace(string(out)))
	}
	d.log.Info("network settings saved; applying", "event", "network_set", "keys", changed, "unit", applyUnit)
	return ctl.NetworkSetReply{OK: true, Changed: changed, Applying: true, Unit: applyUnit}, nil
}
