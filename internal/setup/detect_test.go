package setup

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures are real outputs from the Pi 5 test rig on 2026-11-10, with
// palm-gw installed, a T|X on ppp0, Ethernet and Wi-Fi both up.
const (
	routeDefaultPi5 = `[{"dst":"default","gateway":"10.4.0.1","dev":"eth0","protocol":"dhcp","prefsrc":"10.4.0.241","metric":100,"flags":[]},{"dst":"default","gateway":"10.4.0.1","dev":"wlan0","protocol":"dhcp","prefsrc":"10.4.0.243","metric":600,"flags":[]}]`
	routeMainPi5    = `[{"dst":"default","gateway":"10.4.0.1","dev":"eth0","protocol":"dhcp","prefsrc":"10.4.0.241","metric":100,"flags":[]},{"dst":"default","gateway":"10.4.0.1","dev":"wlan0","protocol":"dhcp","prefsrc":"10.4.0.243","metric":600,"flags":[]},{"dst":"10.4.0.0/23","dev":"eth0","protocol":"kernel","scope":"link","prefsrc":"10.4.0.241","metric":100,"flags":[]},{"dst":"10.4.0.0/23","dev":"wlan0","protocol":"kernel","scope":"link","prefsrc":"10.4.0.243","metric":600,"flags":[]},{"dst":"10.42.0.0/24","dev":"br-palm","protocol":"kernel","scope":"link","prefsrc":"10.42.0.1","metric":425,"flags":["linkdown"]},{"dst":"10.42.0.10","dev":"ppp0","protocol":"kernel","scope":"link","prefsrc":"10.42.0.1","flags":[]}]`
	addrPi5         = `[{"ifindex":1,"ifname":"lo","addr_info":[{"family":"inet","local":"127.0.0.1","prefixlen":8}]},{"ifindex":2,"ifname":"eth0","addr_info":[{"family":"inet","local":"10.4.0.241","prefixlen":23}]},{"ifindex":3,"ifname":"wlan0","addr_info":[{"family":"inet","local":"10.4.0.243","prefixlen":23}]},{"ifindex":4,"ifname":"br-palm","addr_info":[{"family":"inet","local":"10.42.0.1","prefixlen":24}]},{"ifindex":8,"ifname":"ppp0","addr_info":[{"family":"inet","local":"10.42.0.1","address":"10.42.0.10","prefixlen":32}]}]`
)

// fakeHost builds a root tree from files and answers commands from outputs
// (key: the command line joined by spaces). A missing key fails the command.
func fakeHost(t *testing.T, files map[string]string, outputs map[string]string) Host {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Host{Root: root, Run: func(name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		if out, ok := outputs[key]; ok {
			return []byte(out), nil
		}
		return nil, errors.New("not found: " + key)
	}}
}

func TestDetectBoard(t *testing.T) {
	for model, want := range map[string]string{
		"Raspberry Pi 5 Model B Rev 1.0\x00":        "pi5",
		"Raspberry Pi 4 Model B Rev 1.5\x00":        "pi4",
		"Raspberry Pi Compute Module 4 Rev 1.1\x00": "other",
	} {
		h := fakeHost(t, map[string]string{"proc/device-tree/model": model}, nil)
		if got := h.DetectBoard(); got.ID != want || strings.Contains(got.Model, "\x00") {
			t.Errorf("%q: got %+v, want %s", model, got, want)
		}
	}
	if got := fakeHost(t, nil, nil).DetectBoard(); got.ID != "other" {
		t.Errorf("no device tree: got %+v", got)
	}
}

func TestParseDefaultRoutes(t *testing.T) {
	ups, err := ParseDefaultRoutes([]byte(routeDefaultPi5))
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 2 || ups[0].Iface != "eth0" || ups[1].Iface != "wlan0" || ups[0].Gateway != "10.4.0.1" {
		t.Fatalf("got %+v", ups)
	}
	// Own and container interfaces never count; one entry per interface,
	// lowest metric first.
	js := `[{"dst":"default","dev":"wlan0","metric":600},{"dst":"default","dev":"ppp0","metric":0},{"dst":"default","dev":"docker0","metric":1},{"dst":"default","dev":"eth0","metric":100},{"dst":"default","dev":"eth0","metric":700}]`
	ups, err = ParseDefaultRoutes([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 2 || ups[0].Iface != "eth0" || ups[0].Metric != 100 || ups[1].Iface != "wlan0" {
		t.Fatalf("got %+v", ups)
	}
}

func TestDetectUplinksWifiBand(t *testing.T) {
	files := map[string]string{"sys/class/net/wlan0/wireless/.keep": ""}
	h := fakeHost(t, files, map[string]string{
		"ip -j route show default": routeDefaultPi5,
		// Pi OS Lite has no iw; NetworkManager answers.
		"nmcli -t -f IN-USE,FREQ dev wifi list ifname wlan0 --rescan no": "*:5240 MHz\n :2412 MHz\n",
	})
	ups, err := h.DetectUplinks()
	if err != nil {
		t.Fatal(err)
	}
	if ups[0].Wireless || !ups[1].Wireless || ups[1].FreqMHz != 5240 {
		t.Fatalf("got %+v", ups)
	}
	h = fakeHost(t, files, map[string]string{
		"ip -j route show default": routeDefaultPi5,
		"iw dev wlan0 link":        "Connected to aa:bb:cc:dd:ee:ff (on wlan0)\n\tSSID: x\n\tfreq: 2437.0\n",
	})
	ups, _ = h.DetectUplinks()
	if ups[1].FreqMHz != 2437 {
		t.Fatalf("iw: got %+v", ups[1])
	}
}

func TestUARTConsoleConflict(t *testing.T) {
	cmdline := map[string]string{"boot/firmware/cmdline.txt": "console=serial0,115200 console=tty1 root=PARTUUID=f6d7b2a3-02 rootwait\n"}
	h := fakeHost(t, cmdline, nil)
	if !h.UARTConsoleConflict(Board{ID: "pi4"}) {
		t.Error("pi4 with console=serial0 should conflict")
	}
	if h.UARTConsoleConflict(Board{ID: "pi5"}) {
		t.Error("pi5's serial0 is the debug UART: no conflict")
	}
	h = fakeHost(t, map[string]string{"boot/firmware/cmdline.txt": "console=tty1 root=x\n"}, nil)
	if h.UARTConsoleConflict(Board{ID: "pi4"}) {
		t.Error("no serial console: no conflict")
	}
}

func TestPower(t *testing.T) {
	for out, want := range map[string]int64{"throttled=0x0\n": 0, "throttled=0x50005\n": 0x50005, "VCHI init failed\n": -1} {
		if got := ParseThrottled(out); got != want {
			t.Errorf("ParseThrottled(%q) = %#x, want %#x", out, got, want)
		}
	}
	p := Power{Throttled: 0x50005}
	if !p.UnderVoltageNow() || !p.UnderVoltageSeen() {
		t.Error("0x50005 is under-voltage now and seen")
	}
	if (Power{Throttled: -1}).UnderVoltageSeen() {
		t.Error("unknown is not under-voltage")
	}
	// The rig's supply: 0x05dc = 1500 mA.
	h := fakeHost(t, map[string]string{"proc/device-tree/chosen/power/max_current": "\x00\x00\x05\xdc"},
		map[string]string{"vcgencmd get_throttled": "throttled=0x0\n"})
	if got := h.DetectPower(); got.MaxCurrentMA != 1500 || got.Throttled != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestSubnet(t *testing.T) {
	// palm-gw's own br-palm and ppp0 must not count against 10.42.0.0/24.
	used, err := UsedPrefixes([]byte(addrPi5), []byte(routeMainPi5))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := used[netip.MustParsePrefix("10.42.0.0/24")]; ok {
		t.Fatal("own br-palm counted as used")
	}
	s, err := CheckSubnet("10.42.0.0/24", used)
	if err != nil || !s.Free {
		t.Fatalf("got %+v, %v", s, err)
	}
	// A LAN on 10.42.0.0/16 and a VPN on 10.43.0.0/24: suggest 10.44.
	used[netip.MustParsePrefix("10.42.0.0/16")] = "eth0"
	used[netip.MustParsePrefix("10.43.0.0/24")] = "wg0"
	s, _ = CheckSubnet("10.42.0.0/24", used)
	if s.Free || s.Suggest != "10.44.0.0/24" || len(s.Conflicts) != 1 || s.Conflicts[0] != "eth0 10.42.0.0/16" {
		t.Fatalf("got %+v", s)
	}
}

func TestDetectZeroAdapters(t *testing.T) {
	h := fakeHost(t, map[string]string{
		"proc/device-tree/model":   "Raspberry Pi 5 Model B Rev 1.0\x00",
		"boot/firmware/config.txt": "[all]\ndtoverlay=disable-bt\n",
	}, map[string]string{
		"ip -j route show default":    routeDefaultPi5,
		"ip -j -4 addr":               addrPi5,
		"ip -j route show table main": routeMainPi5,
		"dmesg":                       "[ 3.2] Bluetooth: HCI socket layer up\n",
	})
	h.Adapters = func() ([]Adapter, error) { return nil, nil }
	r := h.Detect("/etc/palm-gw/config.toml")
	if !r.DisableBT || len(r.Adapters) != 0 || !hasWarning(r, "dtoverlay=disable-bt") {
		t.Fatalf("got %+v", r)
	}
	if r.ConfigFound || !r.Subnet.Free || len(r.Uplinks) != 2 {
		t.Fatalf("got %+v", r)
	}

	h.Run = func(name string, args ...string) ([]byte, error) {
		if name == "dmesg" {
			return []byte("[ 4.1] Bluetooth: hci0: BCM: firmware Patch file not found, tried: brcm/BCM4345C0.hcd\n"), nil
		}
		return nil, errors.New("no")
	}
	if err := os.WriteFile(filepath.Join(h.Root, "boot/firmware/config.txt"), []byte("[all]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = h.Detect("/etc/palm-gw/config.toml")
	if len(r.BTFirmware) != 1 || !hasWarning(r, "controller failure") {
		t.Fatalf("got %+v", r)
	}
}

func TestDetectDonglePower(t *testing.T) {
	h := fakeHost(t, map[string]string{
		"proc/device-tree/model":                    "Raspberry Pi 5 Model B Rev 1.0\x00",
		"proc/device-tree/chosen/power/max_current": "\x00\x00\x05\xdc",
		"etc/palm-gw/config.toml":                   "[bluetooth]\nadapter = \"60:15:6F:E9:EB:03\"\n",
	}, map[string]string{
		"ip -j route show default":    routeDefaultPi5,
		"ip -j -4 addr":               addrPi5,
		"ip -j route show table main": routeMainPi5,
		"vcgencmd get_throttled":      "throttled=0x0\n",
	})
	h.Adapters = func() ([]Adapter, error) {
		return []Adapter{{HCI: "hci0", Address: "60:15:6F:E9:EB:03", Bus: "usb", Powered: true}}, nil
	}
	r := h.Detect("/etc/palm-gw/config.toml")
	if !r.ConfigFound || r.Config == nil || r.Config.Bluetooth.Adapter != "60:15:6F:E9:EB:03" {
		t.Fatalf("config not pre-filled: %+v", r)
	}
	if !hasWarning(r, "1500 mA") {
		t.Fatalf("expected the Pi 5 supply warning: %v", r.Warnings)
	}
}

func hasWarning(r Report, sub string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}
