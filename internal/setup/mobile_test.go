package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"palm-gw/internal/config"
)

func TestMobileKeyfile(t *testing.T) {
	m := config.Default().Mobile
	m.Enabled = true
	gsm := MobileKeyfileText(m, "1234")
	for _, want := range []string{"type=gsm", "auto-config=true", "home-only=true", "pin=1234", "route-metric=100"} {
		if !strings.Contains(gsm, want) {
			t.Errorf("only, no APN: missing %q in\n%s", want, gsm)
		}
	}
	m.Role, m.APN, m.Roaming = "backup", "ltemobile.apn", true
	b := MobileKeyfileText(m, "")
	for _, want := range []string{"apn=ltemobile.apn", "home-only=false", "route-metric=700"} {
		if !strings.Contains(b, want) {
			t.Errorf("backup: missing %q", want)
		}
	}
	if strings.Contains(b, "pin=") || strings.Contains(b, "auto-config") {
		t.Errorf("backup with an APN and no PIN:\n%s", b)
	}
	m.Interface = "usb0"
	r := MobileKeyfileText(m, "")
	if !strings.Contains(r, "type=ethernet") || !strings.Contains(r, "interface-name=usb0") || strings.Contains(r, "[gsm]") {
		t.Errorf("router mode:\n%s", r)
	}
}

func TestStashMobilePIN(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"12", "123456789", "12a4", ""} {
		if StashMobilePIN(root, "/var/lib/palm-gw", bad) == nil {
			t.Errorf("PIN %q accepted", bad)
		}
	}
	if err := StashMobilePIN(root, "/var/lib/palm-gw", " 4321\n"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "/var/lib/palm-gw", MobilePendingPIN))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("pending PIN: %v %v", fi, err)
	}
}

func TestApplyMobile(t *testing.T) {
	root := t.TempDir()
	state := "/var/lib/palm-gw"
	if err := StashMobilePIN(root, state, "4321"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mobile.Enabled = true
	cfg.Network.Uplink = "wwan0"
	var calls []string
	up := true
	run := func(name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if strings.Contains(line, "connection up") && !up {
			return nil, os.ErrNotExist
		}
		return nil, nil
	}
	m := &Manifest{paths: map[string]bool{}}
	a := &applier{o: ApplyOptions{Root: root, StateDir: state, Run: run}}
	if err := a.applyMobile(cfg, m); err != nil {
		t.Fatal(err)
	}
	kf := filepath.Join(root, MobileKeyfile)
	fi, err := os.Stat(kf)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("keyfile: %v %v", fi, err)
	}
	text, _ := os.ReadFile(kf)
	if !strings.Contains(string(text), "pin=4321") {
		t.Fatalf("keyfile lacks the PIN:\n%s", text)
	}
	if strings.Contains(strings.Join(calls, "\n"), "4321") {
		t.Fatal("the PIN appeared on a command line")
	}
	if _, err := os.Stat(filepath.Join(root, state, MobilePendingPIN)); !os.IsNotExist(err) {
		t.Error("the pending PIN was left behind")
	}
	// A later apply keeps the PIN from the keyfile, and a modem that is not there yet is a warning.
	up = false
	cfg.Mobile.APN = "ltemobile.apn"
	if err := a.applyMobile(cfg, m); err != nil {
		t.Fatal(err)
	}
	text, _ = os.ReadFile(kf)
	if !strings.Contains(string(text), "pin=4321") || !strings.Contains(string(text), "apn=ltemobile.apn") {
		t.Fatalf("rewrite:\n%s", text)
	}
	if len(a.res.Warnings) != 1 || !strings.Contains(a.res.Warnings[0], "did not come up") {
		t.Fatalf("warnings %v", a.res.Warnings)
	}
	// Unchanged and already up: no redial.
	calls = nil
	run2 := func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if strings.Contains(strings.Join(args, " "), "--active") {
			return []byte("br-palm\n" + MobileProfile + "\n"), nil
		}
		return nil, nil
	}
	a.o.Run = run2
	if err := a.applyMobile(cfg, m); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "connection up") {
		t.Fatalf("redialled an unchanged, active modem: %v", calls)
	}
	// Off: the keyfile goes, and with it the manifest entry.
	cfg.Mobile.Enabled = false
	if err := a.applyMobile(cfg, m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kf); !os.IsNotExist(err) || m.paths[MobileKeyfile] {
		t.Fatalf("off: keyfile %v, manifest %v", err, m.paths)
	}
}

func TestMobileFirewall(t *testing.T) {
	cfg := config.Default()
	if off := renderNft(t, cfg); strings.Contains(off, "wan->") {
		t.Fatal("off: WAN rules present")
	}
	// Only: the modem is the uplink, and hostile. Rules name any wwan*, which a reset
	// can renumber; only replies are forwarded in, to the Palms, the LAN or anywhere.
	cfg.Mobile.Enabled, cfg.Network.Uplink = true, "wwan0"
	only := renderNft(t, cfg)
	for _, want := range []string{
		`iifname "wwan*" ct state != { established, related } counter drop comment "wan->in"`,
		`iifname "wwan*" counter drop comment "wan->pi"`,
		`iifname "wwan*" udp sport 67 udp dport 68 accept`,
		`nd-router-advert`,
		`ip saddr 10.42.0.0/24 oifname "wwan0" masquerade`,
		`ip saddr 10.42.0.0/24 oifname "wwan*" masquerade`,
		`oifname "wwan*" accept`,
	} {
		if !strings.Contains(only, want) {
			t.Errorf("only: missing %s", want)
		}
	}
	// Established replies come before the drop, so the Pi's own traffic works.
	if strings.Index(only, `iifname "wwan*" ct state established,related accept`) > strings.Index(only, `"wan->pi"`) {
		t.Error("the drop comes before the replies")
	}
	// Backup: both exits, after the private-address drop.
	cfg.Mobile.Role, cfg.Network.Uplink = "backup", "eth0"
	b := renderNft(t, cfg)
	for _, want := range []string{`oifname "eth0" accept`, `oifname "wwan*" accept`, `ip saddr 10.42.0.0/24 oifname "wwan*" masquerade`} {
		if !strings.Contains(b, want) {
			t.Errorf("backup: missing %s", want)
		}
	}
	if strings.Index(b, `oifname "wwan*" accept`) < strings.Index(b, "palm->rfc1918") {
		t.Error("backup: the modem's exit is before the private-address drop")
	}
	// Router mode: the exact interface, once.
	cfg.Mobile.Interface, cfg.Network.Uplink, cfg.Mobile.Role = "usb0", "usb0", "only"
	r := renderNft(t, cfg)
	if !strings.Contains(r, `iifname "usb0" counter drop comment "wan->pi"`) || strings.Count(r, `oifname "usb0" masquerade`) != 1 {
		t.Error("router mode: rules not on usb0, or the masquerade doubled")
	}
	// With a full WireGuard tunnel that fails closed, neither the uplink nor the modem is a Palm exit.
	cfg.Mobile.Interface, cfg.Mobile.Role, cfg.Network.Uplink = "wwan0", "backup", "eth0"
	cfg.WireGuard.Enabled = true
	wg := renderNft(t, cfg)
	if strings.Contains(wg, `oifname "wwan*" accept`) || strings.Contains(wg, `oifname "eth0" accept`) {
		t.Error("WireGuard fail-closed: a non-tunnel exit is open")
	}
	if !strings.Contains(wg, `"wan->pi"`) || !strings.Contains(wg, `"wg->in"`) {
		t.Error("WireGuard on: lost a guard")
	}
}

func TestDetectModem(t *testing.T) {
	root := t.TempDir()
	h := Host{Root: root, Run: func(string, ...string) ([]byte, error) { return []byte("No modems were found\n"), nil }}
	if h.DetectModem() != nil {
		t.Fatal("found a modem on an empty system")
	}
	dev := filepath.Join(root, "sys/bus/usb/devices/1-1")
	_ = os.MkdirAll(dev, 0o755)
	_ = os.WriteFile(filepath.Join(dev, "idVendor"), []byte("1e0e\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dev, "idProduct"), []byte("9001\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "sys/class/net/wwan0"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "sys/class/net/eth0"), 0o755)
	h.Run = func(string, ...string) ([]byte, error) {
		return []byte("    /org/freedesktop/ModemManager1/Modem/0 [SIMCOM INCORPORATED] SIMCOM_SIM7600G-H\n"), nil
	}
	m := h.DetectModem()
	if m == nil || m.USB != "1e0e:9001" || m.Maker != "SIMCom" || strings.Join(m.Interfaces, ",") != "wwan0" || !m.Managed {
		t.Fatalf("modem %+v", m)
	}
}

func TestDetectModemHuaweiNeedsWwan(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "sys/bus/usb/devices/1-2")
	_ = os.MkdirAll(dev, 0o755)
	_ = os.WriteFile(filepath.Join(dev, "idVendor"), []byte("12d1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dev, "idProduct"), []byte("1f01\n"), 0o644)
	h := Host{Root: root, Run: func(string, ...string) ([]byte, error) { return []byte("No modems were found\n"), nil }}
	if m := h.DetectModem(); m != nil {
		t.Fatalf("a Huawei USB device with no wwan counted as a modem: %+v", m)
	}
	_ = os.MkdirAll(filepath.Join(root, "sys/class/net/wwan0"), 0o755)
	if m := h.DetectModem(); m == nil || m.Maker != "Huawei" {
		t.Fatalf("Huawei with wwan0: %+v", m)
	}
}
