package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/setup"
)

// netFixture is a daemon with only what network.set needs: a config file, a
// fake host root with eth0 and wlan0, and a recorded command runner.
type netFixture struct {
	d       *Daemon
	srv     *ctl.Server
	cfgPath string
	ran     [][]string
	active  bool  // systemctl is-active palm-gw-apply
	runErr  error // systemd-run's result
}

func newNetFixture(t *testing.T) *netFixture {
	t.Helper()
	dir := t.TempDir()
	example, err := os.ReadFile("../../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	f := &netFixture{cfgPath: filepath.Join(dir, "config.toml")}
	if err := os.WriteFile(f.cfgPath, example, 0o640); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	for _, i := range []string{"eth0", "wlan0"} {
		if err := os.MkdirAll(filepath.Join(root, "sys/class/net", i), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Parse(string(example))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.d = &Daemon{log: log, opts: Options{
		ConfigPath: f.cfgPath,
		StatePath:  filepath.Join(dir, "state.json"),
		SetupBin:   "/usr/bin/palm-gw",
		HostRoot:   root,
		Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			f.ran = append(f.ran, append([]string{name}, args...))
			switch {
			case name == "systemctl":
				if f.active {
					return nil, nil
				}
				return nil, errors.New("exit status 3")
			case name == "systemd-run":
				return []byte("boom"), f.runErr
			case name == "ip" && strings.Contains(strings.Join(args, " "), "addr"):
				return []byte(`[{"ifname":"eth0","addr_info":[{"family":"inet","local":"10.4.0.241","prefixlen":23}]}]`), nil
			case name == "ip":
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected command " + name)
		},
	}}
	f.d.cfgp.Store(cfg)
	f.srv = ctl.NewServer("", "", log)
	f.srv.Handle("network.set", f.d.handleNetworkSet)
	f.srv.Handle("choices", f.d.handleChoices)
	return f
}

func (f *netFixture) set(t *testing.T, nc setup.NetworkChange) (ctl.NetworkSetReply, error) {
	t.Helper()
	var r ctl.NetworkSetReply
	err := f.srv.Call(context.Background(), ctl.Request{Cmd: "network.set", Network: &nc}, func(line json.RawMessage) error {
		var e ctl.ErrorReply
		if json.Unmarshal(line, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return json.Unmarshal(line, &r)
	})
	return r, err
}

func TestNetworkSetStartsApply(t *testing.T) {
	f := newNetFixture(t)
	before, _ := os.ReadFile(f.cfgPath)
	r, err := f.set(t, setup.NetworkChange{Uplink: "wlan0", Subnet: "10.43.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Applying || strings.Join(r.Changed, " ") != "network.uplink network.subnet network.gateway network.ppp_pool network.dhcp_pool" {
		t.Errorf("reply: %+v", r)
	}
	after, _ := os.ReadFile(f.cfgPath)
	cfg, err := config.Parse(string(after))
	if err != nil || cfg.Network.Uplink != "wlan0" || cfg.Network.Gateway != "10.43.0.1" {
		t.Fatalf("config: %v %+v", err, cfg.Network)
	}
	if len(strings.Split(string(after), "\n")) != len(strings.Split(string(before), "\n")) {
		t.Error("the edit added or removed lines")
	}
	last := strings.Join(f.ran[len(f.ran)-1], " ")
	if !strings.HasPrefix(last, "systemd-run --unit=palm-gw-apply --collect") ||
		!strings.Contains(last, "/usr/bin/palm-gw setup --apply --config "+f.cfgPath+" --record ") {
		t.Errorf("ran: %s", last)
	}
}

func TestNetworkSetRefuses(t *testing.T) {
	f := newNetFixture(t)
	before, _ := os.ReadFile(f.cfgPath)
	for _, c := range []struct {
		nc   setup.NetworkChange
		want string
	}{
		{setup.NetworkChange{Uplink: "eth9"}, "no interface called"},
		{setup.NetworkChange{Uplink: "br-palm"}, "gateway's own interfaces"},
		{setup.NetworkChange{Subnet: "10.4.0.0/24"}, "overlaps a network this Pi is on (eth0 10.4.0.0/23); 10.43.0.0/24 is free"},
		{setup.NetworkChange{Subnet: "10.43.0.0/16"}, "want an IPv4 /24"},
		{setup.NetworkChange{TLS: "off", BluetoothSide: ptr(true)}, "needs HTTPS"},
	} {
		if _, err := f.set(t, c.nc); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c.nc, err)
		}
	}
	// An apply already running.
	f.active = true
	if _, err := f.set(t, setup.NetworkChange{Uplink: "wlan0"}); err == nil || !strings.Contains(err.Error(), "being applied now") {
		t.Errorf("running: %v", err)
	}
	// systemd-run fails: the file is put back.
	f.active, f.runErr = false, errors.New("exit status 1")
	if _, err := f.set(t, setup.NetworkChange{Uplink: "wlan0"}); err == nil || !strings.Contains(err.Error(), "not saved") {
		t.Errorf("run failed: %v", err)
	}
	if after, _ := os.ReadFile(f.cfgPath); string(after) != string(before) {
		t.Error("config.toml changed by refused or failed changes")
	}
}

func TestNetworkSetTLSOffTurnsBluetoothSideOff(t *testing.T) {
	f := newNetFixture(t)
	r, err := f.set(t, setup.NetworkChange{TLS: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.Changed, " ") != "web.tls web.bluetooth_side" {
		t.Errorf("changed: %v", r.Changed)
	}
	if r, err := f.set(t, setup.NetworkChange{TLS: "off"}); err != nil || r.Applying {
		t.Errorf("same again: %+v %v", r, err)
	}
}

func TestChoicesReadsLastApply(t *testing.T) {
	f := newNetFixture(t)
	if err := setup.WriteApplyRecord(f.d.applyRecordPath(), setup.ApplyRecord{OK: false, Error: "the firewall does not load"}); err != nil {
		t.Fatal(err)
	}
	var r ctl.ChoicesReply
	if err := f.srv.Call(context.Background(), ctl.Request{Cmd: "choices"}, func(line json.RawMessage) error { return json.Unmarshal(line, &r) }); err != nil {
		t.Fatal(err)
	}
	if r.LastApply == nil || r.LastApply.Error != "the firewall does not load" || r.Applying || r.Subnet == nil || !r.Subnet.Free {
		t.Errorf("%+v", r)
	}
}

func ptr[T any](v T) *T { return &v }
