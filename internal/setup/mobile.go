package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"palm-gw/internal/config"
)

// An LTE modem as the uplink (D-074, docs/MOBILE.md). setup writes the modem's
// NetworkManager connection as a keyfile, root only, so a SIM PIN never appears on
// a command line or in config.toml: a "gsm" connection that ModemManager brings up
// on wwan*, or, for a modem in router mode (usb0), a plain wired one. The route
// metric follows the role: the modem is the default route in "only", and behind
// network.uplink in "backup", where palm-gwd moves it in front when the uplink
// loses the internet.

const (
	// MobileProfile is the NetworkManager connection for the modem.
	MobileProfile = "palm-gw-mobile"
	// MobileKeyfile is where it lives.
	MobileKeyfile = "/etc/NetworkManager/system-connections/" + MobileProfile + ".nmconnection"
	// MobilePendingPIN holds a SIM PIN between StashMobilePIN and the apply that
	// writes it into the keyfile: mode 0600, root only, deleted after.
	MobilePendingPIN = "mobile-pin.pending"

	// MobileMetricOnly and MobileMetricBackup are the modem's route metrics: first
	// as the only uplink, and behind Ethernet (100) and Wi-Fi (600) as a backup.
	MobileMetricOnly   = 100
	MobileMetricBackup = 700
)

// RouterMode says whether the modem appears as a USB network card that routes by
// itself (usb0) rather than through ModemManager (wwan*).
func RouterMode(m config.Mobile) bool { return m.Match() != "wwan*" }

// MobileMetric is the modem's route metric for its role.
func MobileMetric(m config.Mobile) int {
	if m.Role == "backup" {
		return MobileMetricBackup
	}
	return MobileMetricOnly
}

// MobileKeyfileText is the modem's NetworkManager connection. pin may be empty.
func MobileKeyfileText(m config.Mobile, pin string) string {
	var b strings.Builder
	b.WriteString("# Written by palm-gw setup from config.toml's [mobile] (D-074); edits are overwritten.\n")
	metric := MobileMetric(m)
	if RouterMode(m) {
		fmt.Fprintf(&b, "[connection]\nid=%s\ntype=ethernet\ninterface-name=%s\nautoconnect=true\nautoconnect-priority=-10\n\n", MobileProfile, m.Interface)
	} else {
		fmt.Fprintf(&b, "[connection]\nid=%s\ntype=gsm\nautoconnect=true\n\n[gsm]\n", MobileProfile)
		if m.APN != "" {
			fmt.Fprintf(&b, "apn=%s\n", m.APN)
		} else {
			b.WriteString("auto-config=true\n")
		}
		fmt.Fprintf(&b, "home-only=%t\n", !m.Roaming)
		if pin != "" {
			fmt.Fprintf(&b, "pin=%s\n", pin)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "[ipv4]\nmethod=auto\nroute-metric=%d\n\n[ipv6]\nmethod=auto\naddr-gen-mode=default\nroute-metric=%d\n", metric, metric)
	return b.String()
}

// StashMobilePIN keeps a SIM PIN, root only, for the next apply.
func StashMobilePIN(root, stateDir, pin string) error {
	pin = strings.TrimSpace(pin)
	if len(pin) < 4 || len(pin) > 8 || strings.Trim(pin, "0123456789") != "" {
		return errors.New("a SIM PIN is 4 to 8 digits")
	}
	dir := filepath.Join(root, stateDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	p := filepath.Join(dir, MobilePendingPIN)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(pin+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// mobilePIN is the PIN to write: a pending one, else the one the current keyfile has.
func (a *applier) mobilePIN() (pin string, pending bool, err error) {
	b, err := os.ReadFile(a.path(filepath.Join(a.o.StateDir, MobilePendingPIN)))
	if err == nil {
		return strings.TrimSpace(string(b)), true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	cur, err := os.ReadFile(a.path(MobileKeyfile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	for _, l := range strings.Split(string(cur), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "pin="); ok {
			return v, false, nil
		}
	}
	return "", false, nil
}

// applyMobile makes NetworkManager's modem connection match [mobile]: removed when
// it is off, written and brought up otherwise. A modem that is not plugged in yet
// is a warning, not a failure: NetworkManager brings the connection up when it is.
func (a *applier) applyMobile(cfg *config.Config, m *Manifest) error {
	g := cfg.Mobile
	if !g.Enabled {
		if _, err := os.Stat(a.path(MobileKeyfile)); err == nil {
			if err := os.Remove(a.path(MobileKeyfile)); err != nil {
				return err
			}
			m.Drop(MobileKeyfile)
			a.res.Changed = append(a.res.Changed, MobileKeyfile)
			if _, err := a.run("nmcli", "connection", "reload"); err != nil {
				return err
			}
			a.note("4G off: %s removed", MobileProfile)
		} else {
			a.note("4G off")
		}
		_ = os.Remove(a.path(filepath.Join(a.o.StateDir, MobilePendingPIN)))
		return nil
	}
	pin, pending, err := a.mobilePIN()
	if err != nil {
		return err
	}
	if RouterMode(g) && pin != "" {
		a.warn("4G: a SIM PIN is not used in router mode (%s); unlock the SIM in the modem's own settings", g.Interface)
		pin = ""
	}
	ch, err := writeIfChanged(a.path(MobileKeyfile), []byte(MobileKeyfileText(g, pin)), 0o600)
	if err != nil {
		return err
	}
	m.Add(MobileKeyfile)
	if ch {
		a.res.Changed = append(a.res.Changed, MobileKeyfile)
	}
	if pending {
		_ = os.Remove(a.path(filepath.Join(a.o.StateDir, MobilePendingPIN)))
	}
	if _, err := a.run("nmcli", "connection", "reload"); err != nil {
		return err
	}
	how := "through ModemManager"
	if RouterMode(g) {
		how = "router mode"
	}
	// Bring it up only when it changed or is down: every "up" redials the modem, which
	// gets a new address and drops a WireGuard tunnel over it.
	if out, err := a.run("nmcli", "-t", "-f", "NAME", "connection", "show", "--active"); !ch && err == nil &&
		strings.Contains("\n"+string(out), "\n"+MobileProfile+"\n") {
		a.note("4G: %s unchanged and up on %s (%s, role %s)", MobileProfile, g.Interface, how, g.Role)
		return nil
	}
	if _, err := a.run("nmcli", "connection", "up", MobileProfile); err != nil {
		a.warn("4G: %s did not come up (is the modem plugged in, with a SIM?); NetworkManager brings it up when it can", MobileProfile)
	} else {
		a.note("4G: %s up on %s (%s, role %s)", MobileProfile, g.Interface, how, g.Role)
	}
	return nil
}

// lteVendors are USB vendors whose devices are LTE modems: SIMCom, Quectel,
// Sierra, Huawei and ZTE (the last two make other things too, so only their
// modem-looking products count: the interface check below decides).
var lteVendors = map[string]string{"1e0e": "SIMCom", "2c7c": "Quectel", "1199": "Sierra Wireless", "12d1": "Huawei", "19d2": "ZTE"}

// Modem is what setup --check found of a 4G modem.
type Modem struct {
	USB        string   `json:"usb,omitempty"`        // vendor:product, e.g. 1e0e:9001
	Maker      string   `json:"maker,omitempty"`      // from the vendor ID
	Interfaces []string `json:"interfaces,omitempty"` // wwan* and, in router mode, usb*
	Managed    bool     `json:"managed"`              // ModemManager lists a modem
}

// DetectModem looks for an LTE modem: a known vendor on the USB bus, a wwan*
// interface, or one ModemManager lists. Nil when there is none.
func (h Host) DetectModem() *Modem {
	var m Modem
	found := false
	if ds, err := os.ReadDir(h.path("sys/bus/usb/devices")); err == nil {
		for _, d := range ds {
			v, err := h.read(filepath.Join("sys/bus/usb/devices", d.Name(), "idVendor"))
			if err != nil {
				continue
			}
			vid := strings.TrimSpace(string(v))
			if maker, ok := lteVendors[vid]; ok {
				p, _ := h.read(filepath.Join("sys/bus/usb/devices", d.Name(), "idProduct"))
				m.USB, m.Maker = vid+":"+strings.TrimSpace(string(p)), maker
				// Huawei and ZTE make other USB things too: theirs count only with a
				// wwan interface or ModemManager's word (below).
				found = vid != "12d1" && vid != "19d2"
				break
			}
		}
	}
	if ifs, err := os.ReadDir(h.path("sys/class/net")); err == nil {
		for _, i := range ifs {
			n := i.Name()
			if strings.HasPrefix(n, "wwan") || (m.USB != "" && strings.HasPrefix(n, "usb")) {
				m.Interfaces = append(m.Interfaces, n)
				found = true
			}
		}
	}
	if out, err := h.run("mmcli", "-L"); err == nil && strings.Contains(string(out), "/Modem/") {
		m.Managed, found = true, true
	}
	if !found || (m.USB != "" && len(m.Interfaces) == 0 && !m.Managed && (strings.HasPrefix(m.USB, "12d1:") || strings.HasPrefix(m.USB, "19d2:"))) {
		return nil
	}
	return &m
}
