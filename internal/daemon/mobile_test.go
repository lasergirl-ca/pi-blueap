package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/event"
	"palm-gw/internal/mm"
	"palm-gw/internal/setup"
)

// mobileFixture is the network fixture with the 4G commands and a recorder for
// nmcli's route changes.
func mobileFixture(t *testing.T) (*netFixture, *[]string) {
	t.Helper()
	f := newNetFixture(t)
	f.d.events = event.New()
	var nmcli []string
	inner := f.d.opts.Command
	f.d.opts.Command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "nmcli" {
			nmcli = append(nmcli, strings.Join(args, " "))
			return nil, nil
		}
		return inner(ctx, name, args...)
	}
	f.srv.Handle("mobile", f.d.handleMobile)
	f.srv.Handle("mobile.set", f.d.handleMobileSet)
	f.srv.Handle("mobile.pin", f.d.handleMobilePIN)
	return f, &nmcli
}

func TestMobileFailover(t *testing.T) {
	f, nmcli := mobileFixture(t)
	cfg := config.Default()
	cfg.Mobile.Enabled, cfg.Mobile.Role = true, "backup"
	f.d.cfgp.Store(cfg)
	up := false
	f.d.opts.Probe = func(_ context.Context, iface, check string) error {
		if iface != "eth0" || check != "1.1.1.1" {
			t.Errorf("probe %s %s", iface, check)
		}
		if up {
			return nil
		}
		return errors.New("no route to host")
	}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		f.d.mobileStep(ctx, cfg, now)
	}
	if len(*nmcli) != 0 || f.d.mob().failedOver {
		t.Fatal("failed over after 2 misses")
	}
	f.d.mobileStep(ctx, cfg, now)
	if !f.d.mob().failedOver || (*nmcli)[0] != "device modify wwan0 ipv4.route-metric 50 ipv6.route-metric 50" {
		t.Fatalf("no failover after 3 misses: %v", *nmcli)
	}
	up = true
	f.d.mobileStep(ctx, cfg, now.Add(30*time.Second))
	f.d.mobileStep(ctx, cfg, now.Add(2*time.Minute))
	if !f.d.mob().failedOver {
		t.Fatal("handed back before 2 minutes of answers")
	}
	f.d.mobileStep(ctx, cfg, now.Add(2*time.Minute+30*time.Second))
	if f.d.mob().failedOver || (*nmcli)[len(*nmcli)-1] != "device modify wwan0 ipv4.route-metric 700 ipv6.route-metric 700" {
		t.Fatalf("no hand-back: %v", *nmcli)
	}
	// One miss after a hand-back does not fail over again.
	up = false
	f.d.mobileStep(ctx, cfg, now.Add(3*time.Minute))
	if f.d.mob().failedOver {
		t.Fatal("failed over after one miss")
	}
}

func TestMobileDataCount(t *testing.T) {
	f, _ := mobileFixture(t)
	cfg := config.Default()
	cfg.Mobile.Enabled, cfg.Network.Uplink, cfg.Mobile.DataWarningMB = true, "wwan0", 2
	rx, tx := uint64(1<<20), uint64(0)
	f.d.opts.IfBytes = func(string) (uint64, uint64) { return rx, tx }
	nov := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	f.d.countMobile(cfg, nov, false) // first reading: counts from the counters' start
	rx, tx = 2<<20, 1<<20
	f.d.countMobile(cfg, nov, true)
	w := f.d.mob()
	if w.data.RX != 2<<20 || w.data.TX != 1<<20 || !w.data.Warned {
		t.Fatalf("november %+v", w.data)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(f.d.opts.StatePath), mobileDataFile))
	var saved mobileData
	if err != nil || json.Unmarshal(b, &saved) != nil || saved.RX != 2<<20 {
		t.Fatalf("saved %s %v", b, err)
	}
	// The interface came back with fresh counters: what it shows is new traffic.
	rx, tx = 100, 50
	f.d.countMobile(cfg, nov, false)
	if w.data.RX != 2<<20+100 || w.data.TX != 1<<20+50 {
		t.Fatalf("after a reset %+v", w.data)
	}
	// A new month starts from zero.
	rx, tx = 300, 50
	f.d.countMobile(cfg, time.Date(2026, 12, 1, 0, 0, 30, 0, time.UTC), false)
	if w.data.Month != "2026-12" || w.data.RX != 200 || w.data.TX != 0 || w.data.Warned {
		t.Fatalf("december %+v", w.data)
	}
}

func TestMobileSetFollowsTheUplink(t *testing.T) {
	f, _ := mobileFixture(t)
	on := true
	r, err := wgCall[ctl.MobileSetReply](t, f, ctl.Request{Cmd: "mobile.set", Mobile: &ctl.MobileRequest{Enabled: &on}})
	if err != nil || !r.Applying {
		t.Fatalf("on: %v %+v", err, r)
	}
	cfg, _ := config.Load(f.cfgPath)
	if !cfg.Mobile.Enabled || cfg.Network.Uplink != "wwan0" {
		t.Fatalf("only: uplink %s", cfg.Network.Uplink)
	}
	if _, err := wgCall[ctl.MobileSetReply](t, f, ctl.Request{Cmd: "mobile.set", Mobile: &ctl.MobileRequest{Role: "backup"}}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load(f.cfgPath)
	if cfg.Mobile.Role != "backup" || cfg.Network.Uplink != "eth0" {
		t.Fatalf("backup: role %s uplink %s", cfg.Mobile.Role, cfg.Network.Uplink)
	}
	apn := "ltemobile.apn"
	if _, err := wgCall[ctl.MobileSetReply](t, f, ctl.Request{Cmd: "mobile.set", Mobile: &ctl.MobileRequest{Role: "only", APN: &apn}}); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := wgCall[ctl.MobileSetReply](t, f, ctl.Request{Cmd: "mobile.set", Mobile: &ctl.MobileRequest{Enabled: &off}}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load(f.cfgPath)
	if cfg.Mobile.Enabled || cfg.Network.Uplink != "eth0" || cfg.Mobile.APN != apn {
		t.Fatalf("off from only: %+v uplink %s", cfg.Mobile, cfg.Network.Uplink)
	}
	if _, err := wgCall[ctl.MobileSetReply](t, f, ctl.Request{Cmd: "mobile.set", Mobile: &ctl.MobileRequest{Role: "sometimes"}}); err == nil {
		t.Fatal("role sometimes accepted")
	}
}

func TestMobileStatusAndPIN(t *testing.T) {
	f, _ := mobileFixture(t)
	cfg := config.Default()
	cfg.Mobile.Enabled, cfg.Network.Uplink = true, "wwan0"
	f.d.cfgp.Store(cfg)
	f.d.opts.IfaceIP = func(string) string { return "100.72.9.14" }
	f.d.opts.ReadModem = func() (mm.Modem, error) {
		return mm.Modem{Model: "SIMCOM_SIM7600G-H", State: "connected", Operator: "Rogers", Tech: "LTE", Signal: 72}, nil
	}
	st, err := wgCall[ctl.MobileStatus](t, f, ctl.Request{Cmd: "mobile"})
	if err != nil || !st.Up || !st.Active || !st.Modem || st.Operator != "Rogers" || st.Signal != 72 || st.Tech != "LTE" {
		t.Fatalf("status: %v %+v", err, st)
	}
	f.d.opts.ReadModem = func() (mm.Modem, error) { return mm.Modem{}, mm.ErrNoModem }
	if st, err := wgCall[ctl.MobileStatus](t, f, ctl.Request{Cmd: "mobile"}); err != nil || st.Modem || !strings.Contains(st.Problem, "plugged in") {
		t.Fatalf("no modem: %v %+v", err, st)
	}
	if _, err := wgCall[ctl.MobileSetReply](t, f, ctl.Request{Cmd: "mobile.pin", Mobile: &ctl.MobileRequest{PIN: "12a4"}}); err == nil {
		t.Fatal("PIN 12a4 accepted")
	}
	r, err := wgCall[ctl.MobileSetReply](t, f, ctl.Request{Cmd: "mobile.pin", Mobile: &ctl.MobileRequest{PIN: "4321"}})
	if err != nil || !r.Applying {
		t.Fatalf("pin: %v %+v", err, r)
	}
	if fi, err := os.Stat(filepath.Join(filepath.Dir(f.d.opts.StatePath), setup.MobilePendingPIN)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("pending PIN: %v %v", fi, err)
	}
}
