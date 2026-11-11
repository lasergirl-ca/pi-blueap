package setup

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"palm-gw/internal/config"
)

// Detection for `palm-gw setup` (PLAN.md section 8.11): everything the wizard
// can find out by looking, so it never asks a question it can answer itself.
// Each probe reads files under Host.Root and runs commands through Host.Run,
// so tests drive them with fixture trees and canned command output.

// Host is the machine being inspected.
type Host struct {
	Root string // filesystem root; "" means "/"
	// Run runs a command and returns its standard output.
	Run func(name string, args ...string) ([]byte, error)
	// Adapters lists the Bluetooth controllers (from BlueZ). Nil means
	// BlueZ is not reachable.
	Adapters func() ([]Adapter, error)
}

// LocalHost inspects the machine this runs on. adapters may be nil.
func LocalHost(adapters func() ([]Adapter, error)) Host {
	return Host{Root: "/", Run: func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}, Adapters: adapters}
}

func (h Host) path(p string) string {
	root := h.Root
	if root == "" {
		root = "/"
	}
	return filepath.Join(root, p)
}

func (h Host) read(p string) ([]byte, error) { return os.ReadFile(h.path(p)) }

func (h Host) run(name string, args ...string) ([]byte, error) {
	if h.Run == nil {
		return nil, errors.New("no command runner")
	}
	return h.Run(name, args...)
}

// Board is the Raspberry Pi model.
type Board struct {
	ID    string `json:"id"` // pi4, pi5 or other
	Model string `json:"model"`
}

// DetectBoard reads /proc/device-tree/model.
func (h Host) DetectBoard() Board {
	b, err := h.read("proc/device-tree/model")
	if err != nil {
		return Board{ID: "other"}
	}
	model := strings.TrimRight(string(b), "\x00\n")
	switch {
	case strings.Contains(model, "Pi 4"):
		return Board{ID: "pi4", Model: model}
	case strings.Contains(model, "Pi 5"):
		return Board{ID: "pi5", Model: model}
	}
	return Board{ID: "other", Model: model}
}

// Adapter is one Bluetooth controller as the wizard sees it.
type Adapter struct {
	HCI     string `json:"hci"`
	Address string `json:"address"`
	Bus     string `json:"bus"` // usb (a dongle), serial (the onboard radio), ...
	Powered bool   `json:"powered"`
}

// Onboard reports whether this is the Pi's built-in radio.
func (a Adapter) Onboard() bool { return a.Bus != "usb" }

// DisableBTSet reports whether config.txt turns the onboard radio off.
func (h Host) DisableBTSet() bool {
	b, err := h.read("boot/firmware/config.txt")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == "dtoverlay=disable-bt" {
			return true
		}
	}
	return false
}

var btFailRe = regexp.MustCompile(`(?i)bluetooth: hci\d+: .*(fail|error|timed? ?out|not found)`)

// BluetoothFirmwareErrors returns the kernel log lines that report a
// Bluetooth controller failing to come up.
func (h Host) BluetoothFirmwareErrors() []string {
	out, err := h.run("dmesg")
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if btFailRe.MatchString(l) {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	return lines
}

// Uplink is a candidate interface for [network].uplink.
type Uplink struct {
	Iface    string `json:"iface"`
	Gateway  string `json:"gateway"`
	Metric   int    `json:"metric"`
	Wireless bool   `json:"wireless"`
	FreqMHz  int    `json:"freq_mhz,omitempty"` // 0 if unknown or not Wi-Fi
}

type ipRoute struct {
	Dst     string `json:"dst"`
	Gateway string `json:"gateway"`
	Dev     string `json:"dev"`
	Metric  int    `json:"metric"`
}

// ownIface reports interfaces that are palm-gw's own or never an uplink.
func ownIface(name string) bool {
	if name == "lo" || name == "br-palm" {
		return true
	}
	for _, p := range []string{"bnep", "ppp", "docker", "veth"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ParseDefaultRoutes turns `ip -j route show default` into uplink candidates,
// lowest metric first, one per interface.
func ParseDefaultRoutes(js []byte) ([]Uplink, error) {
	var routes []ipRoute
	if err := json.Unmarshal(js, &routes); err != nil {
		return nil, fmt.Errorf("ip route: %w", err)
	}
	seen := map[string]bool{}
	var out []Uplink
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].Metric < routes[j].Metric })
	for _, r := range routes {
		if r.Dst != "default" || r.Dev == "" || ownIface(r.Dev) || seen[r.Dev] {
			continue
		}
		seen[r.Dev] = true
		out = append(out, Uplink{Iface: r.Dev, Gateway: r.Gateway, Metric: r.Metric})
	}
	return out, nil
}

// DetectUplinks lists interfaces with a default route, with Wi-Fi bands.
func (h Host) DetectUplinks() ([]Uplink, error) {
	out, err := h.run("ip", "-j", "route", "show", "default")
	if err != nil {
		return nil, fmt.Errorf("ip route show default: %w", err)
	}
	ups, err := ParseDefaultRoutes(out)
	if err != nil {
		return nil, err
	}
	for i := range ups {
		if _, err := os.Stat(h.path("sys/class/net/" + ups[i].Iface + "/wireless")); err == nil {
			ups[i].Wireless = true
			ups[i].FreqMHz = h.wifiFreq(ups[i].Iface)
		}
	}
	return ups, nil
}

var (
	iwFreqRe    = regexp.MustCompile(`freq:\s*([0-9]+)`)
	nmcliFreqRe = regexp.MustCompile(`(?m)^\*:([0-9]+)`)
)

// wifiFreq reads the associated frequency: iw if installed (it is not on Pi
// OS Lite), else NetworkManager's scan list.
func (h Host) wifiFreq(iface string) int {
	if out, err := h.run("iw", "dev", iface, "link"); err == nil {
		if m := iwFreqRe.FindSubmatch(out); m != nil {
			n, _ := strconv.Atoi(string(m[1]))
			return n
		}
	}
	if out, err := h.run("nmcli", "-t", "-f", "IN-USE,FREQ", "dev", "wifi", "list", "ifname", iface, "--rescan", "no"); err == nil {
		if m := nmcliFreqRe.FindSubmatch(out); m != nil {
			n, _ := strconv.Atoi(string(m[1]))
			return n
		}
	}
	return 0
}

// UARTConsoleConflict reports a Pi 4 serial console on the UART the onboard
// Bluetooth radio uses (PLAN.md section 3). On a Pi 5 serial0 is the separate
// debug UART, so there is no conflict.
func (h Host) UARTConsoleConflict(board Board) bool {
	if board.ID != "pi4" {
		return false
	}
	b, err := h.read("boot/firmware/cmdline.txt")
	if err != nil {
		return false
	}
	for _, f := range strings.Fields(string(b)) {
		if strings.HasPrefix(f, "console=serial0") || strings.HasPrefix(f, "console=ttyAMA0") {
			return true
		}
	}
	return false
}

// Power is the Pi's supply state.
type Power struct {
	// MaxCurrentMA is what the Pi 5 negotiated with its supply (0 if unknown,
	// as on a Pi 4). Below 5000 the Pi 5 limits USB ports to 600 mA in total.
	MaxCurrentMA int `json:"max_current_ma"`
	// Throttled is `vcgencmd get_throttled` (bit 0 under-voltage now, bit 16
	// under-voltage has occurred); -1 if unknown.
	Throttled int64 `json:"throttled"`
}

// UnderVoltageNow and UnderVoltageSeen decode Throttled.
func (p Power) UnderVoltageNow() bool  { return p.Throttled > 0 && p.Throttled&0x1 != 0 }
func (p Power) UnderVoltageSeen() bool { return p.Throttled > 0 && p.Throttled&0x10000 != 0 }

var throttledRe = regexp.MustCompile(`throttled=0x([0-9a-fA-F]+)`)

// ParseThrottled reads `vcgencmd get_throttled` output; -1 if unreadable.
func ParseThrottled(out string) int64 {
	m := throttledRe.FindStringSubmatch(out)
	if m == nil {
		return -1
	}
	n, err := strconv.ParseInt(m[1], 16, 64)
	if err != nil {
		return -1
	}
	return n
}

// DetectPower reads the supply's negotiated current and the throttle flags.
func (h Host) DetectPower() Power {
	p := Power{Throttled: -1}
	if b, err := h.read("proc/device-tree/chosen/power/max_current"); err == nil && len(b) == 4 {
		p.MaxCurrentMA = int(binary.BigEndian.Uint32(b))
	}
	if out, err := h.run("vcgencmd", "get_throttled"); err == nil {
		p.Throttled = ParseThrottled(string(out))
	}
	return p
}

// Subnet is the Palm subnet check.
type Subnet struct {
	Want      string   `json:"want"`
	Free      bool     `json:"free"`
	Conflicts []string `json:"conflicts,omitempty"` // "eth0 10.42.0.0/16"
	Suggest   string   `json:"suggest,omitempty"`   // first free 10.N.0.0/24 when Want is taken
}

type ipAddr struct {
	Ifname   string `json:"ifname"`
	AddrInfo []struct {
		Family    string `json:"family"`
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
	} `json:"addr_info"`
}

// UsedPrefixes collects the IPv4 networks on the host's addresses and routes
// (`ip -j -4 addr`, `ip -j route show table main`), leaving out palm-gw's own
// interfaces, so a re-run of setup does not collide with itself.
func UsedPrefixes(addrJSON, routeJSON []byte) (map[netip.Prefix]string, error) {
	used := map[netip.Prefix]string{}
	var addrs []ipAddr
	if err := json.Unmarshal(addrJSON, &addrs); err != nil {
		return nil, fmt.Errorf("ip addr: %w", err)
	}
	for _, a := range addrs {
		if ownIface(a.Ifname) && a.Ifname != "lo" {
			continue
		}
		for _, ai := range a.AddrInfo {
			if ai.Family != "inet" {
				continue
			}
			ip, err := netip.ParseAddr(ai.Local)
			if err != nil {
				continue
			}
			used[netip.PrefixFrom(ip, ai.PrefixLen).Masked()] = a.Ifname
		}
	}
	var routes []ipRoute
	if err := json.Unmarshal(routeJSON, &routes); err != nil {
		return nil, fmt.Errorf("ip route: %w", err)
	}
	for _, r := range routes {
		if r.Dst == "default" || ownIface(r.Dev) {
			continue
		}
		p, err := netip.ParsePrefix(r.Dst)
		if err != nil {
			ip, err2 := netip.ParseAddr(r.Dst)
			if err2 != nil {
				continue
			}
			p = netip.PrefixFrom(ip, ip.BitLen())
		}
		used[p.Masked()] = r.Dev
	}
	return used, nil
}

// CheckSubnet reports whether want overlaps any used prefix and, if so,
// suggests the first free 10.N.0.0/24 after it.
func CheckSubnet(want string, used map[netip.Prefix]string) (Subnet, error) {
	p, err := netip.ParsePrefix(want)
	if err != nil {
		return Subnet{}, fmt.Errorf("subnet %q: %w", want, err)
	}
	s := Subnet{Want: want, Free: true}
	overlaps := func(q netip.Prefix) []string {
		var c []string
		for u, dev := range used {
			if u.Overlaps(q) {
				c = append(c, dev+" "+u.String())
			}
		}
		sort.Strings(c)
		return c
	}
	if s.Conflicts = overlaps(p); len(s.Conflicts) == 0 {
		return s, nil
	}
	s.Free = false
	start := 43
	if a := p.Addr().As4(); a[0] == 10 && int(a[1]) >= 42 {
		start = int(a[1]) + 1
	}
	for n := start; n < 256; n++ {
		q := netip.MustParsePrefix(fmt.Sprintf("10.%d.0.0/24", n))
		if len(overlaps(q)) == 0 {
			s.Suggest = q.String()
			break
		}
	}
	return s, nil
}

// DetectSubnet runs CheckSubnet against the host's addresses and routes.
func (h Host) DetectSubnet(want string) (Subnet, error) {
	addrs, err := h.run("ip", "-j", "-4", "addr")
	if err != nil {
		return Subnet{}, fmt.Errorf("ip addr: %w", err)
	}
	routes, err := h.run("ip", "-j", "route", "show", "table", "main")
	if err != nil {
		return Subnet{}, fmt.Errorf("ip route: %w", err)
	}
	used, err := UsedPrefixes(addrs, routes)
	if err != nil {
		return Subnet{}, err
	}
	return CheckSubnet(want, used)
}

// Report is everything detection found, plus warnings for the user.
type Report struct {
	Board        Board          `json:"board"`
	Adapters     []Adapter      `json:"adapters"`
	AdapterError string         `json:"adapter_error,omitempty"`
	DisableBT    bool           `json:"disable_bt"`
	BTFirmware   []string       `json:"bt_firmware_errors,omitempty"`
	Uplinks      []Uplink       `json:"uplinks"`
	UARTConflict bool           `json:"uart_conflict"`
	Power        Power          `json:"power"`
	ConfigPath   string         `json:"config_path"`
	ConfigFound  bool           `json:"config_found"`
	ConfigError  string         `json:"config_error,omitempty"`
	Config       *config.Config `json:"-"`
	Subnet       Subnet         `json:"subnet"`
	Warnings     []string       `json:"warnings"`
}

// Detect runs every probe. cfgPath is the config to pre-fill from; a missing
// file is not an error.
func (h Host) Detect(cfgPath string) Report {
	r := Report{Board: h.DetectBoard(), ConfigPath: cfgPath, DisableBT: h.DisableBTSet()}
	warn := func(f string, a ...any) { r.Warnings = append(r.Warnings, fmt.Sprintf(f, a...)) }

	if r.Board.ID == "other" {
		warn("not a Raspberry Pi 4 or 5 (%q); continuing, but this is untested", r.Board.Model)
	}

	cfg := config.Default()
	if _, err := os.Stat(h.path(cfgPath)); err == nil {
		r.ConfigFound = true
		c, err := config.Load(h.path(cfgPath))
		if err != nil {
			r.ConfigError = err.Error()
			warn("existing %s does not load: %v", cfgPath, err)
		} else {
			cfg = c
			r.Config = c
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		r.ConfigError = err.Error()
	}

	if h.Adapters == nil {
		r.AdapterError = "BlueZ is not reachable"
	} else if as, err := h.Adapters(); err != nil {
		r.AdapterError = err.Error()
	} else {
		r.Adapters = as
	}
	if len(r.Adapters) == 0 {
		r.BTFirmware = h.BluetoothFirmwareErrors()
		switch {
		case r.DisableBT:
			warn("no Bluetooth adapter: dtoverlay=disable-bt is set in /boot/firmware/config.txt and no USB dongle is plugged in")
		case len(r.BTFirmware) > 0:
			warn("no Bluetooth adapter: the kernel reports a controller failure: %s", r.BTFirmware[0])
		default:
			warn("no Bluetooth adapter found; check that bluetooth.service is running and the firmware is installed")
		}
	}

	if ups, err := h.DetectUplinks(); err != nil {
		warn("uplink detection failed: %v", err)
	} else {
		r.Uplinks = ups
		if len(ups) == 0 {
			warn("no interface has a default route; connect Ethernet or Wi-Fi before setup")
		}
	}

	dongle := r.DisableBT
	for _, a := range r.Adapters {
		if !a.Onboard() {
			dongle = true
		}
	}
	for _, u := range r.Uplinks {
		if u.Wireless && u.FreqMHz > 0 && u.FreqMHz < 3000 && !dongle {
			warn("uplink %s is on 2.4 GHz (%d MHz) and shares the onboard radio with Bluetooth; Bluetooth 1.1 Palms will suffer. Prefer Ethernet, 5 GHz, or a USB dongle", u.Iface, u.FreqMHz)
		}
	}

	r.UARTConflict = h.UARTConsoleConflict(r.Board)
	if r.UARTConflict {
		warn("Pi 4: a serial console on serial0/ttyAMA0 in cmdline.txt can conflict with the onboard Bluetooth UART")
	}

	r.Power = h.DetectPower()
	if r.Power.UnderVoltageNow() {
		warn("the Pi is under-voltage right now (get_throttled=%#x); use the official power supply", r.Power.Throttled)
	} else if r.Power.UnderVoltageSeen() {
		warn("the Pi has been under-voltage since boot (get_throttled=%#x)", r.Power.Throttled)
	}
	if r.Board.ID == "pi5" && dongle && r.Power.MaxCurrentMA > 0 && r.Power.MaxCurrentMA < 5000 {
		warn("Pi 5 supply negotiated %d mA (under 5 A), so USB ports share 600 mA; a Bluetooth dongle needs about 50 mA, which is fine unless other USB devices draw a lot", r.Power.MaxCurrentMA)
	}

	if s, err := h.DetectSubnet(cfg.Network.Subnet); err != nil {
		warn("subnet check failed: %v", err)
	} else {
		r.Subnet = s
		if !s.Free {
			warn("Palm subnet %s overlaps %s; suggest %s", s.Want, strings.Join(s.Conflicts, ", "), s.Suggest)
		}
	}
	return r
}
