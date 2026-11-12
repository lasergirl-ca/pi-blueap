package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"palm-gw/internal/ctl"
	"palm-gw/internal/diag"
	"palm-gw/internal/setup"
)

// Part 4 of the web UI (PLAN.md section 8.12, D-057): logs, the health
// check, btmon captures, support bundles, the Class of Device toggle, and
// the about page.

func (d *Daemon) stateDir() string { return filepath.Dir(d.opts.StatePath) }

func (d *Daemon) filesDir(kind string) string { return filepath.Join(d.stateDir(), kind) }

func (d *Daemon) handleLogs(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	o := diag.ReadOptions{}
	if l := req.Logs; l != nil {
		o = diag.ReadOptions{Units: l.Units, Lines: l.Lines, After: l.After, Filter: diag.Filter{Event: l.Event, Text: l.Text}}
	}
	if o.Lines > 5000 {
		o.Lines = 5000
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	es, cursor, err := diag.Read(ctx, o)
	if err != nil {
		return err
	}
	if es == nil {
		es = []diag.Entry{}
	}
	return w.Send(ctl.LogsReply{Entries: es, Cursor: cursor})
}

var checkUnits = []string{"bluetooth", "nftables", "palm-gw-dnsmasq", "palm-gwd-nm-bridge", "palm-gwd", "NetworkManager"}
var checkModules = []string{"bnep", "rfcomm", "ppp_generic", "ppp_async"}

func (d *Daemon) check(ctx context.Context) ctl.CheckReply {
	h := d.host()
	if d.bus != nil {
		h.Adapters = func() ([]setup.Adapter, error) {
			as, err := d.bus.Adapters()
			if err != nil {
				return nil, err
			}
			out := make([]setup.Adapter, 0, len(as))
			for _, a := range as {
				out = append(out, setup.Adapter{HCI: a.HCI, Address: a.Props.Address, Bus: a.HWBus, Powered: a.Props.Powered})
			}
			return out, nil
		}
	}
	r := ctl.CheckReply{Report: h.Detect(d.opts.ConfigPath)}
	for _, u := range checkUnits {
		out, _ := d.command(ctx, "systemctl", "is-active", u)
		st := strings.TrimSpace(string(out))
		if st == "" {
			st = "unknown"
		}
		r.Units = append(r.Units, ctl.UnitState{Name: u, Active: st})
	}
	root := d.opts.HostRoot
	if root == "" {
		root = "/"
	}
	for _, m := range checkModules {
		_, err := os.Stat(filepath.Join(root, "sys/module", m))
		r.Modules = append(r.Modules, ctl.ModuleState{Name: m, Loaded: err == nil})
	}
	return r
}

func (d *Daemon) handleCheck(ctx context.Context, _ ctl.Request, w *ctl.Writer) error {
	return w.Send(d.check(ctx))
}

func (d *Daemon) captureState() ctl.CaptureState {
	d.capMu.Lock()
	defer d.capMu.Unlock()
	return d.capture
}

// handleBtmon starts `btmon -w` for req.Seconds (default 60, at most 600)
// and answers at once; diagnostics reports when it is done.
func (d *Daemon) handleBtmon(_ context.Context, req ctl.Request, w *ctl.Writer) error {
	secs := req.Seconds
	if secs == 0 {
		secs = 60
	}
	if secs < 1 || secs > 600 {
		return errors.New("btmon: 1 to 600 seconds")
	}
	d.capMu.Lock()
	defer d.capMu.Unlock()
	if d.capture.Running {
		return fmt.Errorf("a capture is running until %s", d.capture.Until.Local().Format("15:04:05"))
	}
	dir := d.filesDir(diag.Captures)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	now := time.Now()
	name := diag.NewName(diag.Captures, now)
	bin := d.opts.Btmon
	if bin == "" {
		bin = "btmon"
	}
	cmd := exec.Command(bin, "-w", filepath.Join(dir, name))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("btmon: %w", err)
	}
	dur := time.Duration(secs) * time.Second
	d.capture = ctl.CaptureState{Running: true, Name: name, Started: now, Until: now.Add(dur)}
	d.log.Info("btmon capture started", "event", "btmon_started", "file", name, "seconds", secs)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	go func() {
		var err error
		select {
		case err = <-exited:
		case <-time.After(dur):
			_ = cmd.Process.Signal(syscall.SIGINT)
			select {
			case werr := <-exited:
				_ = werr // btmon exits non-zero on SIGINT: stopped as asked
				err = nil
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				err = <-exited
			}
		}
		st := ctl.CaptureState{Name: name}
		// A capture taken during a pairing holds the link key.
		_ = os.Chmod(filepath.Join(dir, name), 0o640)
		fi, statErr := os.Stat(filepath.Join(dir, name))
		switch {
		case err != nil:
			st.Error = fmt.Sprintf("btmon stopped early: %v %s", err, strings.TrimSpace(stderr.String()))
		case statErr != nil || fi.Size() == 0:
			st.Error = "btmon wrote nothing"
		}
		diag.Prune(dir, diag.Captures)
		d.log.Info("btmon capture ended", "event", "btmon_done", "file", name, "err", st.Error)
		d.capMu.Lock()
		d.capture = st
		d.capMu.Unlock()
	}()
	return w.Send(d.capture)
}

func (d *Daemon) handleDiagnostics(_ context.Context, _ ctl.Request, w *ctl.Writer) error {
	r := ctl.DiagnosticsReply{Capture: d.captureState()}
	var err error
	if r.Captures, err = diag.List(d.filesDir(diag.Captures), diag.Captures); err != nil {
		return err
	}
	if r.Bundles, err = diag.List(d.filesDir(diag.Bundles), diag.Bundles); err != nil {
		return err
	}
	return w.Send(r)
}

// call runs one of the daemon's own commands and returns its reply.
func (d *Daemon) call(ctx context.Context, req ctl.Request) string {
	var b strings.Builder
	err := d.ctl.Call(ctx, req, func(line json.RawMessage) error {
		var pretty bytes.Buffer
		if json.Indent(&pretty, line, "", "  ") == nil {
			b.Write(pretty.Bytes())
		} else {
			b.Write(line)
		}
		b.WriteByte('\n')
		return nil
	})
	if err != nil {
		fmt.Fprintf(&b, "(error: %v)\n", err)
	}
	return b.String()
}

// handleBundle writes a support bundle (PLAN.md section 8.12): the daemon's
// view, journals, the files setup renders, the network state, with
// password hashes and Wi-Fi keys redacted; the newest btmon capture only
// when asked, since one taken during a pairing holds the link key.
func (d *Daemon) handleBundle(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	run := func(name string, args ...string) diag.Item {
		cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
		defer ccancel()
		out, err := d.command(cctx, name, args...)
		text := "$ " + name + " " + strings.Join(args, " ") + "\n" + string(out)
		if err != nil {
			text += fmt.Sprintf("\n(%v)\n", err)
		}
		return diag.Item{Text: text}
	}
	named := func(n string, it diag.Item) diag.Item { it.Name = n; return it }
	file := func(n, p string) diag.Item {
		b, err := os.ReadFile(p)
		if err != nil {
			return diag.Item{Name: n, Text: fmt.Sprintf("(could not read %s: %v)\n", p, err)}
		}
		return diag.Item{Name: n, Text: string(b)}
	}
	items := []diag.Item{
		{Name: "README.txt", Text: "palm-gw support bundle, " + time.Now().Format(time.RFC3339) + ", version " + d.opts.Version + ".\n" +
			"Password hashes and Wi-Fi keys are replaced with REDACTED. The TLS key, BlueZ's link keys\n" +
			"and NetworkManager's saved networks are not included.\n"},
		{Name: "status.json", Text: d.call(ctx, ctl.Request{Cmd: "status"})},
		{Name: "check.json", Text: d.call(ctx, ctl.Request{Cmd: "check"})},
		{Name: "internet.json", Text: d.call(ctx, ctl.Request{Cmd: "internet"})},
		{Name: "nat.json", Text: d.call(ctx, ctl.Request{Cmd: "nat"})},
		{Name: "events.json", Text: d.call(ctx, ctl.Request{Cmd: "events", Recent: 200, NoWait: true})},
		file("config.toml", d.opts.ConfigPath),
		file("install-info.json", filepath.Join(d.stateDir(), "install-info.json")),
		file("last-apply.json", d.applyRecordPath()),
		file("dnsmasq.conf", "/etc/palm-gw/dnsmasq.conf"),
		file("bridge.env", "/etc/palm-gw/bridge.env"),
		file("palm-gw.nft", "/etc/nftables.d/palm-gw.nft"),
		file("bluetooth-main.conf", "/etc/bluetooth/main.conf"),
		named("nft-ruleset.txt", run("nft", "list", "table", "inet", "palm_gw")),
		named("ip.txt", run("ip", "-br", "addr")),
		named("routes.txt", run("ip", "route")),
		named("packages.txt", run("dpkg-query", "-W", "palm-gw", "bluez", "ppp", "dnsmasq-base", "dnsmasq", "nftables", "network-manager")),
		named("units.txt", run("systemctl", "status", "--no-pager", "-n", "0", "palm-gwd", "bluetooth", "palm-gw-dnsmasq", "palm-gwd-nm-bridge", "nftables")),
		named("kernel.txt", run("journalctl", "-k", "-b", "-n", "1000", "--no-pager", "-o", "short-iso")),
	}
	for _, u := range diag.UnitNames {
		items = append(items, named("journal-"+u+".txt", run("journalctl", "-b", "-u", diag.Units[u], "-n", "5000", "--no-pager", "-o", "short-iso")))
	}
	if req.WithCapture {
		if cs, err := diag.List(d.filesDir(diag.Captures), diag.Captures); err == nil && len(cs) > 0 {
			items = append(items, diag.Item{Name: cs[0].Name, File: filepath.Join(d.filesDir(diag.Captures), cs[0].Name)})
		}
	}
	name, err := diag.WriteBundle(ctx, d.filesDir(diag.Bundles), time.Now(), items)
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	p := filepath.Join(d.filesDir(diag.Bundles), name)
	fi, _ := os.Stat(p)
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	d.log.Info("support bundle written", "event", "bundle", "file", name, "capture", req.WithCapture)
	return w.Send(ctl.BundleReply{Name: name, Path: p, Size: size})
}

// handleClassToggle swaps the Class of Device between bluetooth.class and
// class_fallback (PLAN.md section 6, "Class fallback"). The choice is kept
// in state.json; setup --apply writes it into main.conf's managed block and
// restarts bluetooth.service and palm-gwd, since palm-gwd cannot write
// /etc/bluetooth (D-054's transient unit).
func (d *Daemon) handleClassToggle(ctx context.Context, _ ctl.Request, w *ctl.Writer) error {
	if d.applyRunning(ctx) {
		return errors.New("settings are being applied now; try again when palm-gwd is back")
	}
	cfg := d.config()
	old := d.store.Class()
	cur := old
	if cur == 0 {
		cur = cfg.Bluetooth.Class
	}
	next := cfg.Bluetooth.ClassFallback
	if cur == cfg.Bluetooth.ClassFallback {
		next = cfg.Bluetooth.Class
	}
	store := next
	if next == cfg.Bluetooth.Class {
		store = 0 // back to the configured class: no override
	}
	if err := d.store.SetClass(store); err != nil {
		return err
	}
	if out, err := d.startApply(ctx); err != nil {
		_ = d.store.SetClass(old)
		return fmt.Errorf("could not start palm-gw setup --apply: %w: %s", err, strings.TrimSpace(string(out)))
	}
	r := ctl.ClassReply{Class: fmt.Sprintf("0x%06X", next), Previous: fmt.Sprintf("0x%06X", cur), Applying: true}
	d.log.Info("class of device toggled", "event", "class_toggle", "class", r.Class, "previous", r.Previous)
	return w.Send(r)
}

func (d *Daemon) handleAbout(_ context.Context, _ ctl.Request, w *ctl.Writer) error {
	r := ctl.AboutReply{Version: d.opts.Version, GoVersion: runtime.Version(), Started: d.started}
	if b, err := os.ReadFile(filepath.Join(d.stateDir(), "install-info.json")); err == nil {
		var info setup.InstallInfo
		if json.Unmarshal(b, &info) == nil {
			r.Install = &info
		}
	}
	return w.Send(r)
}

// handleUpdateCheck runs `palm-gw upgrade --check`, which asks GitHub for
// the latest release. Only when someone presses the button: never on its
// own (PLAN.md section 8.12).
func (d *Daemon) handleUpdateCheck(ctx context.Context, _ ctl.Request, w *ctl.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := d.command(ctx, d.setupBin(), "upgrade", "--check")
	r := ctl.UpdateReply{OK: err == nil, Output: strings.TrimSpace(string(out))}
	if r.Output == "" && err != nil {
		r.Output = err.Error()
	}
	d.log.Info("update check", "event", "update_check", "ok", r.OK)
	return w.Send(r)
}
