package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/setup"
)

// Made-up keys, never a real tunnel's.
var (
	wgTestPriv = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	wgTestPeer = base64.StdEncoding.EncodeToString(make([]byte, 32))
)

func wgFixture(t *testing.T) (*netFixture, *fakeNM) {
	t.Helper()
	f := newNetFixture(t)
	fake := &fakeNM{}
	f.d.opts.NetManager = func() (NetManager, error) { return fake, nil }
	for _, cmd := range []string{"wg", "wg.set", "wg.import", "wg.generate"} {
		h := map[string]func(context.Context, ctl.Request, *ctl.Writer) error{
			"wg": f.d.handleWG, "wg.set": f.d.handleWGSet, "wg.import": f.d.handleWGImport, "wg.generate": f.d.handleWGGenerate,
		}[cmd]
		f.srv.Handle(cmd, h)
	}
	f.srv.Handle("wg.up", f.d.handleWGUpDown(true))
	f.srv.Handle("wg.down", f.d.handleWGUpDown(false))
	return f, fake
}

func wgCall[T any](t *testing.T, f *netFixture, req ctl.Request) (T, error) {
	t.Helper()
	var r T
	err := f.srv.Call(context.Background(), req, func(line json.RawMessage) error {
		var e ctl.ErrorReply
		if json.Unmarshal(line, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return json.Unmarshal(line, &r)
	})
	return r, err
}

const wgConfText = `[Interface]
PrivateKey = %PRIV%
Address = 10.17.200.5/32
DNS = 10.17.200.1

[Peer]
PublicKey = %PEER%
AllowedIPs = 0.0.0.0/0
Endpoint = vpn.example.net:51820
`

func TestWGImportSavesAndApplies(t *testing.T) {
	f, _ := wgFixture(t)
	conf := strings.NewReplacer("%PRIV%", wgTestPriv, "%PEER%", wgTestPeer).Replace(wgConfText)
	r, err := wgCall[ctl.WGSetReply](t, f, ctl.Request{Cmd: "wg.import", WG: &ctl.WGRequest{Conf: conf}})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Applying || r.Mode != "full" || r.PublicKey == "" {
		t.Fatalf("reply %+v", r)
	}
	cfg, err := config.Load(f.cfgPath)
	if err != nil || !cfg.WireGuard.Enabled || cfg.WireGuard.Address != "10.17.200.5/32" || cfg.WireGuard.PublicKey != r.PublicKey {
		t.Fatalf("config: %v %+v", err, cfg.WireGuard)
	}
	raw, _ := os.ReadFile(f.cfgPath)
	if strings.Contains(string(raw), wgTestPriv) {
		t.Fatal("the private key reached config.toml")
	}
	pending := filepath.Join(filepath.Dir(f.d.opts.StatePath), setup.WGPendingKey)
	if fi, err := os.Stat(pending); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("pending key: %v %v", fi, err)
	}
	if last := strings.Join(f.ran[len(f.ran)-1], " "); !strings.HasPrefix(last, "systemd-run --unit=palm-gw-apply") {
		t.Errorf("ran: %s", last)
	}
}

func TestWGSetModesAndRefusals(t *testing.T) {
	f, _ := wgFixture(t)
	// Fall back when down: allowed on its own while the tunnel is off.
	r, err := wgCall[ctl.WGSetReply](t, f, ctl.Request{Cmd: "wg.set", WG: &ctl.WGRequest{OnDown: "fallback"}})
	if err != nil || !r.Applying || strings.Join(r.Changed, ",") != "wireguard.on_down" {
		t.Fatalf("on_down: %v %+v", err, r)
	}
	// Turning it on without an address or peer is refused, and nothing is saved.
	before, _ := os.ReadFile(f.cfgPath)
	on := true
	if _, err := wgCall[ctl.WGSetReply](t, f, ctl.Request{Cmd: "wg.set", WG: &ctl.WGRequest{Enabled: &on}}); err == nil || !strings.Contains(err.Error(), "wireguard.address") {
		t.Fatalf("enabled without a peer: %v", err)
	}
	if after, _ := os.ReadFile(f.cfgPath); string(after) != string(before) {
		t.Fatal("a refused change was saved")
	}
	// Split mode needs its networks; with them it saves.
	if _, err := wgCall[ctl.WGSetReply](t, f, ctl.Request{Cmd: "wg.set", WG: &ctl.WGRequest{Mode: "bogus"}}); err == nil {
		t.Fatal("mode bogus accepted")
	}
	// While an apply runs, nothing is changed.
	f.active = true
	if _, err := wgCall[ctl.WGSetReply](t, f, ctl.Request{Cmd: "wg.set", WG: &ctl.WGRequest{OnDown: "fail_closed"}}); err == nil || !strings.Contains(err.Error(), "being applied") {
		t.Fatalf("during an apply: %v", err)
	}
}

func TestWGGenerateWhileOff(t *testing.T) {
	f, _ := wgFixture(t)
	runs := len(f.ran)
	r, err := wgCall[ctl.WGSetReply](t, f, ctl.Request{Cmd: "wg.generate"})
	if err != nil || r.PublicKey == "" || r.Applying {
		t.Fatalf("generate: %v %+v", err, r)
	}
	for _, c := range f.ran[runs:] {
		if c[0] == "systemd-run" {
			t.Fatal("generating a key with the tunnel off started an apply")
		}
	}
	cfg, _ := config.Load(f.cfgPath)
	if cfg.WireGuard.PublicKey != r.PublicKey {
		t.Fatalf("public_key not recorded: %+v", cfg.WireGuard)
	}
}

func TestWGUpDownAndStatus(t *testing.T) {
	f, fake := wgFixture(t)
	if _, err := wgCall[ctl.WGStatus](t, f, ctl.Request{Cmd: "wg.up"}); err == nil {
		t.Fatal("wg.up with WireGuard off")
	}
	cfg := config.Default()
	cfg.WireGuard.Enabled = true
	f.d.cfgp.Store(cfg)
	up := true
	fake.wgUp = &up
	f.d.opts.IfBytes = func(string) (uint64, uint64) { return 4096, 1024 }
	if _, err := wgCall[ctl.WGStatus](t, f, ctl.Request{Cmd: "wg.down"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fake.wgCalls, ",") != "down" { // checked before wg.up below
		t.Fatalf("calls %v", fake.wgCalls)
	}
	if st, _ := wgCall[ctl.WGStatus](t, f, ctl.Request{Cmd: "wg"}); st.State != "down" {
		t.Fatalf("after wg.down: %+v", st)
	}
	if st, err := wgCall[ctl.WGStatus](t, f, ctl.Request{Cmd: "wg.up"}); err != nil || st.State != "up" {
		t.Fatalf("wg.up: %v %+v", err, st)
	}
	f.d.wg().observe("wg0", true)
	st, err := wgCall[ctl.WGStatus](t, f, ctl.Request{Cmd: "wg"})
	if err != nil || st.State != "up" || st.RxBytes != 4096 || st.LastHeardSeconds < 0 {
		t.Fatalf("status: %v %+v", err, st)
	}
	fake.wgUp = nil
	if st, _ := wgCall[ctl.WGStatus](t, f, ctl.Request{Cmd: "wg"}); st.State != "missing" {
		t.Fatalf("no profile: %+v", st)
	}
}

func TestWGWatchGoesStale(t *testing.T) {
	now := time.Date(2026, 11, 12, 23, 0, 0, 0, time.UTC)
	rx := uint64(100)
	w := &wgWatch{ifbytes: func(string) (uint64, uint64) { return rx, 0 }, now: func() time.Time { return now }}
	if s, b := w.observe("wg0", true); s || b {
		t.Fatal("first sight")
	}
	now = now.Add(2 * time.Minute)
	rx = 200 // a keepalive answered
	if s, _ := w.observe("wg0", true); s {
		t.Fatal("stale while receiving")
	}
	now = now.Add(3*time.Minute + time.Second) // nothing since
	if s, _ := w.observe("wg0", true); !s {
		t.Fatal("not stale after 3 minutes of nothing")
	}
	if s, _ := w.observe("wg0", true); s {
		t.Fatal("wg_stale twice for one silence")
	}
	rx = 300
	if _, back := w.observe("wg0", true); !back {
		t.Fatal("not back after receiving again")
	}
	if secs, stale := w.lastHeard(); secs != 0 || stale {
		t.Fatalf("lastHeard %d %v", secs, stale)
	}
}
