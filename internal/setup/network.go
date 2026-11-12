package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"palm-gw/internal/config"
)

// NetworkChange is a change to the settings that only `palm-gw setup --apply`
// can put into effect, because they re-render nftables, dnsmasq and the
// bridge (D-054). Empty strings and a nil BluetoothSide keep the current value.
type NetworkChange struct {
	Uplink        string `json:"uplink,omitempty"`
	Subnet        string `json:"subnet,omitempty"`
	TLS           string `json:"tls,omitempty"` // self-signed or off
	BluetoothSide *bool  `json:"bluetooth_side,omitempty"`
}

// NetworkEdits turns a NetworkChange into config.toml edits against cur. The
// subnet renumbers the gateway and both pools (SubnetEdits). Turning TLS off
// also turns the Bluetooth side off, which is TLS only (D-039); asking for
// both is an error rather than a silent change.
func NetworkEdits(cur *config.Config, ch NetworkChange) ([]KV, error) {
	var out []KV
	if u := strings.TrimSpace(ch.Uplink); u != "" && u != cur.Network.Uplink {
		if ownIface(u) {
			return nil, fmt.Errorf("uplink: %s is one of the gateway's own interfaces, not a way to the internet", u)
		}
		out = append(out, KV{"network", "uplink", TOMLString(u)})
	}
	if s := strings.TrimSpace(ch.Subnet); s != "" {
		edits, err := SubnetEdits(s, cur)
		if err != nil {
			return nil, err
		}
		out = append(out, edits...)
	}
	tls := cur.Web.TLS
	switch ch.TLS {
	case "":
	case "self-signed", "off":
		tls = ch.TLS
	default:
		return nil, fmt.Errorf("tls: want self-signed or off, got %q", ch.TLS)
	}
	if tls != cur.Web.TLS {
		out = append(out, KV{"web", "tls", TOMLString(tls)})
	}
	side := cur.Web.BluetoothSide
	if ch.BluetoothSide != nil {
		side = *ch.BluetoothSide
	}
	if tls == "off" && side {
		if ch.BluetoothSide != nil && *ch.BluetoothSide {
			return nil, fmt.Errorf("the Bluetooth side needs HTTPS (D-039): keep HTTPS on, or turn the Bluetooth side off")
		}
		side = false
	}
	if side != cur.Web.BluetoothSide {
		out = append(out, KV{"web", "bluetooth_side", fmt.Sprint(side)})
	}
	return out, nil
}

// ApplyRecord is what `palm-gw setup --apply --record FILE` leaves behind, so
// a run started from the web UI can say afterwards how it went.
type ApplyRecord struct {
	Finished       time.Time `json:"finished"`
	OK             bool      `json:"ok"`
	Error          string    `json:"error,omitempty"`
	Changed        []string  `json:"changed,omitempty"`
	Warnings       []string  `json:"warnings,omitempty"`
	RebootRequired bool      `json:"reboot_required,omitempty"`
	Fingerprint    string    `json:"fingerprint,omitempty"`
}

// WriteApplyRecord writes r to path, readable by the palm-gw group.
func WriteApplyRecord(path string, r ApplyRecord) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = writeIfChanged(path, append(b, '\n'), 0o644)
	return err
}

// ReadApplyRecord reads a record; a missing file is (nil, nil).
func ReadApplyRecord(path string) (*ApplyRecord, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r ApplyRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Label names an adapter for a person: what kind it is, its address and hciN.
func (a Adapter) Label() string {
	kind := "Onboard radio"
	if !a.Onboard() {
		kind = "USB dongle"
	}
	return fmt.Sprintf("%s %s (%s)", kind, a.Address, a.HCI)
}

// Label names an uplink for a person: Ethernet, Wi-Fi with its band, or the
// bare interface, and the router it goes through.
func (u Uplink) Label() string {
	kind := u.Iface
	switch {
	case u.Wireless:
		kind = "Wi-Fi " + u.Iface
		switch f := u.FreqMHz; {
		case f >= 2400 && f < 2500:
			kind += " (2.4 GHz)"
		case f >= 4900 && f < 5925:
			kind += " (5 GHz)"
		case f >= 5925:
			kind += " (6 GHz)"
		}
	case strings.HasPrefix(u.Iface, "eth") || strings.HasPrefix(u.Iface, "en"):
		kind = "Ethernet " + u.Iface
	case strings.HasPrefix(u.Iface, "usb"):
		kind = "USB network " + u.Iface
	}
	if u.Gateway != "" {
		kind += ", via router " + u.Gateway
	}
	return kind
}
