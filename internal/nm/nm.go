// Package nm is the little of NetworkManager's D-Bus API the internet
// connection wizard needs (D-056): the Ethernet and Wi-Fi devices, a Wi-Fi
// scan, the saved Wi-Fi networks, joining one, preferring Wi-Fi or Ethernet
// for the default route, and forgetting a network. Passwords go to
// NetworkManager over D-Bus and are stored by it; they are never put on a
// command line or logged.
package nm

import (
	"sort"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

// PreferredMetric is the route metric of the Wi-Fi connection chosen as the
// internet connection: below NetworkManager's 100 for Ethernet, so its
// default route wins while both are up. Choosing Ethernet puts it back to
// the default (-1, which NetworkManager makes 600 for Wi-Fi).
const PreferredMetric = 50

// Device is an Ethernet or Wi-Fi interface NetworkManager manages.
type Device struct {
	Iface      string `json:"iface"`
	Type       string `json:"type"` // ethernet or wifi
	State      string `json:"state"`
	Connected  bool   `json:"connected"`
	Carrier    bool   `json:"carrier,omitempty"`    // Ethernet: a cable with a link
	Connection string `json:"connection,omitempty"` // the active connection's name (the SSID for Wi-Fi)
	IP         string `json:"ip,omitempty"`         // first IPv4 address, with prefix
	Gateway    string `json:"gateway,omitempty"`
	Metric     int64  `json:"metric,omitempty"` // the active connection's ipv4.route-metric, -1 = default
	path       dbus.ObjectPath
}

// Network is one Wi-Fi network from a scan, strongest access point first.
type Network struct {
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"` // percent
	FreqMHz  int    `json:"freq_mhz"`
	Security string `json:"security"` // open, wpa, wpa3, enterprise, wep
	Saved    bool   `json:"saved"`
	Active   bool   `json:"active"`
	ap       dbus.ObjectPath
}

// Band is "2.4 GHz", "5 GHz" or "6 GHz".
func (n Network) Band() string {
	switch f := n.FreqMHz; {
	case f >= 5925:
		return "6 GHz"
	case f >= 4900:
		return "5 GHz"
	case f > 0:
		return "2.4 GHz"
	}
	return ""
}

// NeedsPassword reports whether joining asks for a password.
func (n Network) NeedsPassword() bool { return n.Security == "wpa" || n.Security == "wpa3" }

// Supported reports whether the wizard can join it: open, WPA/WPA2
// personal and WPA3 personal. Enterprise needs a username and certificates,
// WEP is broken; both are left to nmcli.
func (n Network) Supported() bool { return n.Security == "open" || n.NeedsPassword() }

// NM_802_11_AP_FLAGS and NM_802_11_AP_SEC key management bits.
const (
	apFlagPrivacy   = 0x1
	secKeyMgmtPSK   = 0x100
	secKeyMgmt8021X = 0x200
	secKeyMgmtSAE   = 0x400
	secKeyMgmtOWE   = 0x800
)

// security names an access point's security from its Flags, WpaFlags and
// RsnFlags. A WPA2/WPA3 transition network is "wpa": NetworkManager's
// wpa-psk works with both.
func security(flags, wpa, rsn uint32) string {
	km := wpa | rsn
	switch {
	case km&secKeyMgmt8021X != 0:
		return "enterprise"
	case km&secKeyMgmtPSK != 0:
		return "wpa"
	case km&secKeyMgmtSAE != 0:
		return "wpa3"
	case km&secKeyMgmtOWE != 0:
		return "open" // enhanced open: encrypted, no password
	case flags&apFlagPrivacy != 0:
		return "wep"
	}
	return "open"
}

// keyMgmt is the 802-11-wireless-security.key-mgmt for a security name.
func keyMgmt(sec string) string {
	if sec == "wpa3" {
		return "sae"
	}
	return "wpa-psk"
}

type apInfo struct {
	path            dbus.ObjectPath
	ssid            string
	strength        int
	freq            int
	flags, wpa, rsn uint32
}

// networks folds access points into one entry per SSID (the strongest),
// drops hidden ones (empty SSID), marks saved and active, and sorts the
// active one first, then by signal.
func networks(aps []apInfo, saved map[string]bool, active string) []Network {
	best := map[string]Network{}
	for _, a := range aps {
		if strings.TrimSpace(a.ssid) == "" {
			continue
		}
		n := Network{SSID: a.ssid, Signal: a.strength, FreqMHz: a.freq, Security: security(a.flags, a.wpa, a.rsn), ap: a.path}
		if old, ok := best[a.ssid]; ok && old.Signal >= n.Signal {
			continue
		}
		best[a.ssid] = n
	}
	out := make([]Network, 0, len(best))
	for _, n := range best {
		n.Saved, n.Active = saved[n.SSID], n.SSID == active && active != ""
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		if out[i].Signal != out[j].Signal {
			return out[i].Signal > out[j].Signal
		}
		return out[i].SSID < out[j].SSID
	})
	return out
}

// wifiSettings is a new Wi-Fi connection for AddAndActivateConnection.
func wifiSettings(ssid, sec, password string, hidden bool) map[string]map[string]dbus.Variant {
	s := map[string]map[string]dbus.Variant{
		"connection": {
			"id":          dbus.MakeVariant(ssid),
			"type":        dbus.MakeVariant("802-11-wireless"),
			"autoconnect": dbus.MakeVariant(true),
		},
		"802-11-wireless": {
			"ssid": dbus.MakeVariant([]byte(ssid)),
			"mode": dbus.MakeVariant("infrastructure"),
		},
		"ipv4": {"method": dbus.MakeVariant("auto"), "route-metric": dbus.MakeVariant(int64(PreferredMetric))},
		"ipv6": {"method": dbus.MakeVariant("auto"), "route-metric": dbus.MakeVariant(int64(PreferredMetric))},
	}
	if hidden {
		s["802-11-wireless"]["hidden"] = dbus.MakeVariant(true)
	}
	if sec != "open" {
		s["802-11-wireless"]["security"] = dbus.MakeVariant("802-11-wireless-security")
		s["802-11-wireless-security"] = map[string]dbus.Variant{
			"key-mgmt": dbus.MakeVariant(keyMgmt(sec)),
			"psk":      dbus.MakeVariant(password),
		}
	}
	return s
}

// Device states (NMDeviceState).
var deviceStates = map[uint32]string{
	0: "unknown", 10: "unmanaged", 20: "unavailable", 30: "disconnected", 40: "preparing",
	50: "configuring", 60: "authenticating", 70: "getting an address", 80: "checking",
	90: "waiting", 100: "connected", 110: "disconnecting", 120: "failed",
}

// reasons explains the NMDeviceStateReason values a failed join ends with.
var reasons = map[uint32]string{
	5:  "it gave no address (DHCP did not answer)",
	7:  "the password is wrong or missing",
	8:  "the network refused the connection; the password is probably wrong",
	9:  "Wi-Fi could not be set up for this network",
	10: "Wi-Fi failed",
	11: "the network did not answer in time; it may be out of range, or the password is wrong",
	53: "the network was not found; it may be out of range",
	36: "the connection was replaced by another one",
	39: "the connection was taken down",
}

// Reason explains a device state reason.
func Reason(code uint32) string {
	if r, ok := reasons[code]; ok {
		return r
	}
	return "NetworkManager reason " + strconv.Itoa(int(code))
}
