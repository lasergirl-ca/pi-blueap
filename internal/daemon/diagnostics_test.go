package daemon

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"palm-gw/internal/ctl"
	"palm-gw/internal/diag"
	"palm-gw/internal/state"
)

func newDiagFixture(t *testing.T) *netFixture {
	f := newNetFixture(t)
	st, err := state.Open(f.d.opts.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	f.d.store = st
	f.d.ctl = f.srv
	for cmd, h := range map[string]ctl.Handler{
		"btmon": f.d.handleBtmon, "diagnostics": f.d.handleDiagnostics, "bundle": f.d.handleBundle,
		"class.toggle": f.d.handleClassToggle, "about": f.d.handleAbout, "check": f.d.handleCheck,
	} {
		f.srv.Handle(cmd, h)
	}
	return f
}

func TestBtmonCapture(t *testing.T) {
	f := newDiagFixture(t)
	// A stand-in btmon: writes its -w file, then waits to be stopped.
	script := filepath.Join(t.TempDir(), "btmon")
	_ = os.WriteFile(script, []byte("#!/bin/sh\nprintf 'btsnoop' > \"$2\"\ntrap 'exit 0' INT\nwhile :; do sleep 0.05; done\n"), 0o755)
	f.d.opts.Btmon = script
	var st ctl.CaptureState
	if err := f.call(t, ctl.Request{Cmd: "btmon", Seconds: 1}, &st); err != nil || !st.Running || !diag.ValidName(diag.Captures, st.Name) {
		t.Fatalf("%+v %v", st, err)
	}
	if err := f.call(t, ctl.Request{Cmd: "btmon", Seconds: 1}, &st); err == nil || !strings.Contains(err.Error(), "running until") {
		t.Errorf("second capture: %v", err)
	}
	if err := f.call(t, ctl.Request{Cmd: "btmon", Seconds: 601}, &st); err == nil {
		t.Error("601 s accepted")
	}
	var d ctl.DiagnosticsReply
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := f.call(t, ctl.Request{Cmd: "diagnostics"}, &d); err != nil {
			t.Fatal(err)
		}
		if !d.Capture.Running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if d.Capture.Running || d.Capture.Error != "" || len(d.Captures) != 1 || d.Captures[0].Size != 7 {
		t.Errorf("%+v", d)
	}
}

func TestBundleRedacts(t *testing.T) {
	f := newDiagFixture(t)
	cfg, _ := os.ReadFile(f.cfgPath)
	cfg = []byte(strings.Replace(string(cfg), `password_hash  = ""`, `password_hash  = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"`, 1))
	if !strings.Contains(string(cfg), "$argon2id") {
		t.Fatal("test config has no hash line to fill")
	}
	_ = os.WriteFile(f.cfgPath, cfg, 0o640)
	var b ctl.BundleReply
	if err := f.call(t, ctl.Request{Cmd: "bundle"}, &b); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Open(b.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	gz, _ := gzip.NewReader(fh)
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		names[filepath.Base(h.Name)] = true
		if strings.Contains(string(data), "argon2") {
			t.Errorf("%s holds a hash", h.Name)
		}
		if filepath.Base(h.Name) == "config.toml" && !strings.Contains(string(data), `"REDACTED"`) {
			t.Errorf("config.toml not redacted:\n%s", data)
		}
	}
	for _, want := range []string{"README.txt", "config.toml", "check.json", "journal-palm-gwd.txt", "nft-ruleset.txt"} {
		if !names[want] {
			t.Errorf("bundle lacks %s: %v", want, names)
		}
	}
}

func TestClassToggle(t *testing.T) {
	f := newDiagFixture(t)
	var r ctl.ClassReply
	if err := f.call(t, ctl.Request{Cmd: "class.toggle"}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Previous != "0x000300" || r.Class != "0x000104" || f.d.store.Class() != 0x104 {
		t.Errorf("%+v, stored %#x", r, f.d.store.Class())
	}
	if last := strings.Join(f.ran[len(f.ran)-1], " "); !strings.Contains(last, "setup --apply") {
		t.Errorf("no apply: %s", last)
	}
	if err := f.call(t, ctl.Request{Cmd: "class.toggle"}, &r); err != nil || r.Class != "0x000300" || f.d.store.Class() != 0 {
		t.Errorf("back: %+v %v, stored %#x", r, err, f.d.store.Class())
	}
	// The apply cannot start: the stored class is put back.
	f.runErr = errors.New("exit status 1")
	if err := f.call(t, ctl.Request{Cmd: "class.toggle"}, &r); err == nil || f.d.store.Class() != 0 {
		t.Errorf("failed apply: %v, stored %#x", err, f.d.store.Class())
	}
}

func TestAbout(t *testing.T) {
	f := newDiagFixture(t)
	_ = os.WriteFile(filepath.Join(filepath.Dir(f.d.opts.StatePath), "install-info.json"), []byte(`{"version":"0.6.0~test23","board":"pi5","bluez":"5.82"}`), 0o644)
	f.d.opts.Version = "test"
	var a ctl.AboutReply
	if err := f.call(t, ctl.Request{Cmd: "about"}, &a); err != nil || a.Version != "test" || a.Install == nil || a.Install.BlueZ != "5.82" {
		t.Errorf("%+v %v", a, err)
	}
}
