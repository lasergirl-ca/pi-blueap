package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"palm-gw/internal/ctl"
	"palm-gw/internal/nm"
)

type fakeNM struct {
	mu      sync.Mutex
	devs    []nm.Device
	wifiOn  bool
	joinErr error
	joined  []string // ssid/password
	prefer  []string
	forgot  []string
}

func (f *fakeNM) Devices() ([]nm.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nm.Device(nil), f.devs...), nil
}
func (f *fakeNM) WifiEnabled() (bool, bool, error) { return f.wifiOn, true, nil }
func (f *fakeNM) SetWifiEnabled(on bool) error     { f.wifiOn = on; return nil }
func (f *fakeNM) Scan(context.Context, bool) ([]nm.Network, error) {
	return []nm.Network{{SSID: "Lyman", Signal: 80, Security: "wpa"}}, nil
}
func (f *fakeNM) Saved() ([]string, error) { return []string{"Lyman"}, nil }
func (f *fakeNM) Join(_ context.Context, ssid, pw string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joined = append(f.joined, ssid+"/"+pw)
	if f.joinErr != nil {
		return f.joinErr
	}
	f.devs[1].Connected, f.devs[1].Connection = true, ssid
	return nil
}
func (f *fakeNM) Prefer(kind string) error { f.prefer = append(f.prefer, kind); return nil }
func (f *fakeNM) Forget(ssid string) error { f.forgot = append(f.forgot, ssid); return nil }
func (f *fakeNM) Close()                   {}

func newInternetFixture(t *testing.T) (*netFixture, *fakeNM) {
	f := newNetFixture(t)
	n := &fakeNM{wifiOn: true, devs: []nm.Device{
		{Iface: "eth0", Type: "ethernet", Connected: true, Carrier: true, IP: "10.4.1.118/23"},
		{Iface: "wlan0", Type: "wifi", State: "disconnected"},
	}}
	f.d.opts.NetManager = func() (NetManager, error) { return n, nil }
	for _, c := range []string{"internet", "internet.use", "wifi.scan", "wifi.join", "wifi.forget", "wifi.radio"} {
		switch c {
		case "internet":
			f.srv.Handle(c, f.d.handleInternet)
		case "internet.use":
			f.srv.Handle(c, f.d.handleInternetUse)
		case "wifi.scan":
			f.srv.Handle(c, f.d.handleWifiScan)
		case "wifi.join":
			f.srv.Handle(c, f.d.handleWifiJoin)
		case "wifi.forget":
			f.srv.Handle(c, f.d.handleWifiForget)
		case "wifi.radio":
			f.srv.Handle(c, f.d.handleWifiRadio)
		}
	}
	return f, n
}

func (f *netFixture) call(t *testing.T, req ctl.Request, out any) error {
	t.Helper()
	return f.srv.Call(context.Background(), req, func(line json.RawMessage) error {
		var e ctl.ErrorReply
		if json.Unmarshal(line, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return json.Unmarshal(line, out)
	})
}

func (f *netFixture) waitJob(t *testing.T) *ctl.WifiJob {
	t.Helper()
	for i := 0; i < 200; i++ {
		if j := f.d.wifiJobCopy(); j != nil && j.Done {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("join did not finish")
	return nil
}

func TestWifiJoinSwitchesUplink(t *testing.T) {
	f, n := newInternetFixture(t)
	var ok ctl.OKReply
	if err := f.call(t, ctl.Request{Cmd: "wifi.join", WiFi: &ctl.WifiRequest{SSID: " Lyman ", Password: "a-wifi-pass"}}, &ok); err != nil {
		t.Fatal(err)
	}
	j := f.waitJob(t)
	if !j.OK || !j.Switching || j.SSID != "Lyman" || n.joined[0] != "Lyman/a-wifi-pass" {
		t.Errorf("job %+v, joined %v", j, n.joined)
	}
	last := strings.Join(f.ran[len(f.ran)-1], " ")
	if !strings.HasPrefix(last, "systemd-run --unit=palm-gw-apply") {
		t.Errorf("no apply: %s", last)
	}
	var st ctl.InternetReply
	if err := f.call(t, ctl.Request{Cmd: "internet"}, &st); err != nil || st.Job == nil || !st.WifiOn || len(st.Devices) != 2 || st.Saved[0] != "Lyman" {
		t.Errorf("status %+v %v", st, err)
	}
}

func TestWifiJoinFails(t *testing.T) {
	f, n := newInternetFixture(t)
	n.joinErr = errors.New(`could not join "Lyman": the password is wrong or missing`)
	var ok ctl.OKReply
	if err := f.call(t, ctl.Request{Cmd: "wifi.join", WiFi: &ctl.WifiRequest{SSID: "Lyman", Password: "wrong-pass"}}, &ok); err != nil {
		t.Fatal(err)
	}
	if j := f.waitJob(t); j.OK || !strings.Contains(j.Error, "password is wrong") {
		t.Errorf("%+v", j)
	}
	for _, r := range f.ran {
		if r[0] == "systemd-run" {
			t.Error("a failed join switched the uplink")
		}
	}
	if err := f.call(t, ctl.Request{Cmd: "wifi.join", WiFi: &ctl.WifiRequest{SSID: ""}}, &ok); err == nil {
		t.Error("empty ssid accepted")
	}
}

func TestInternetUse(t *testing.T) {
	f, n := newInternetFixture(t)
	var r ctl.InternetUseReply
	// Ethernet is already the uplink: preference only, no apply.
	if err := f.call(t, ctl.Request{Cmd: "internet.use", WiFi: &ctl.WifiRequest{Use: "ethernet"}}, &r); err != nil || r.Switching || n.prefer[0] != "ethernet" {
		t.Errorf("%+v %v %v", r, err, n.prefer)
	}
	if err := f.call(t, ctl.Request{Cmd: "internet.use", WiFi: &ctl.WifiRequest{Use: "wifi"}}, &r); err == nil || !strings.Contains(err.Error(), "join one first") {
		t.Errorf("wifi not connected: %v", err)
	}
	n.devs[1].Connected, n.devs[1].Connection = true, "Lyman"
	if err := f.call(t, ctl.Request{Cmd: "internet.use", WiFi: &ctl.WifiRequest{Use: "wifi"}}, &r); err != nil || !r.Switching || r.Iface != "wlan0" {
		t.Errorf("use wifi: %+v %v", r, err)
	}
	n.devs[0].Carrier = false
	f.active = true // the apply just started is still running
	if err := f.call(t, ctl.Request{Cmd: "internet.use", WiFi: &ctl.WifiRequest{Use: "ethernet"}}, &r); err == nil || !strings.Contains(err.Error(), "being applied") {
		t.Errorf("while applying: %v", err)
	}
	f.active = false
	if err := f.call(t, ctl.Request{Cmd: "internet.use", WiFi: &ctl.WifiRequest{Use: "ethernet"}}, &r); err == nil || !strings.Contains(err.Error(), "no cable") {
		t.Errorf("no cable: %v", err)
	}
}

func TestWifiForgetAndRadioGuardTheUplink(t *testing.T) {
	f, n := newInternetFixture(t)
	n.devs[1].Connected, n.devs[1].Connection = true, "Lyman"
	cfg := *f.d.config()
	cfg.Network.Uplink = "wlan0"
	f.d.cfgp.Store(&cfg)
	var ok ctl.OKReply
	if err := f.call(t, ctl.Request{Cmd: "wifi.forget", WiFi: &ctl.WifiRequest{SSID: "Lyman"}}, &ok); err == nil {
		t.Error("forgot the network in use")
	}
	off := false
	if err := f.call(t, ctl.Request{Cmd: "wifi.radio", WiFi: &ctl.WifiRequest{On: &off}}, &ok); err == nil {
		t.Error("turned off the Wi-Fi in use")
	}
	if err := f.call(t, ctl.Request{Cmd: "wifi.forget", WiFi: &ctl.WifiRequest{SSID: "Old"}}, &ok); err != nil || n.forgot[0] != "Old" {
		t.Errorf("forget other: %v", err)
	}
}
