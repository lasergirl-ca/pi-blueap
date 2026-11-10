// Package pan serves PAN NAP (PLAN.md section 8.6). BlueZ does the protocol:
// the daemon registers the NAP server on the adapter with
// NetworkServer1.Register("nap", bridge) and BlueZ then accepts PANU clients,
// creates a bnepN interface for each and enslaves it to the bridge. This
// package also reads the kernel's list of BNEP connections (which client is
// on which bnepN), can drop one, and reads dnsmasq's lease file so a client's
// address can be shown next to its Bluetooth address.
package pan

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"palm-gw/internal/bluez"
)

// UUID is the NAP service class (0x1116) the SDP record advertises.
const UUID = "00001116-0000-1000-8000-00805f9b34fb"

// AuthUUID is what BlueZ's network server passes to Agent1.AuthorizeService for
// an incoming PAN connection: the BNEP protocol UUID (0x000F), not NAP's.
const AuthUUID = "0000000f-0000-1000-8000-00805f9b34fb"

// Role is the name NetworkServer1 takes for the NAP role.
const Role = "nap"

// RoleNAP is the BNEP role BlueZ gives the connections its NAP server
// accepts. A PANU connection made from this host (fake-palm) has role PANU
// (0x1115) and is not a client of ours.
const RoleNAP = 0x1116

// SysClassNet is where bridges and their ports appear; a variable for tests.
var SysClassNet = "/sys/class/net"

// BridgePresent reports whether the bridge interface exists.
func BridgePresent(bridge string) bool {
	_, err := os.Stat(filepath.Join(SysClassNet, bridge))
	return err == nil
}

// Ports lists the interfaces enslaved to the bridge (bnepN for PAN clients).
func Ports(bridge string) []string {
	ents, err := os.ReadDir(filepath.Join(SysClassNet, bridge, "brif"))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// Register registers the NAP server on the adapter. A registration this
// connection already holds (BlueZ released our agent but stayed up) counts as
// success.
func Register(bus *bluez.Bus, adapter dbus.ObjectPath, bridge string) error {
	err := bus.Object(adapter).Call(bluez.IfaceNetworkServer+".Register", 0, Role, bridge).Err
	var de dbus.Error
	if errors.As(err, &de) && de.Name == "org.bluez.Error.AlreadyExists" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("NetworkServer1.Register(%q, %q) on %s: %w", Role, bridge, adapter, err)
	}
	return nil
}

// Unregister removes the NAP server; BlueZ drops its clients.
func Unregister(bus *bluez.Bus, adapter dbus.ObjectPath) error {
	return bus.Object(adapter).Call(bluez.IfaceNetworkServer+".Unregister", 0, Role).Err
}

// Conn is one BNEP connection as the kernel reports it.
type Conn struct {
	Addr   string `json:"addr"`   // the client's Bluetooth address
	Ifname string `json:"ifname"` // bnepN
	Role   uint16 `json:"role"`
	State  uint16 `json:"state"`
}

// ErrUnsupported is returned by Conns and Kill off Linux.
var ErrUnsupported = errors.New("BNEP is only available on Linux")

// Lease is one line of dnsmasq's lease file.
type Lease struct {
	Expires  time.Time
	MAC      string // upper case, colon separated, like a Bluetooth address
	IP       string
	Hostname string // "" when the client sent none ("*" in the file)
}

// ReadLeases parses a dnsmasq lease file; a missing file is no leases.
func ReadLeases(path string) ([]Lease, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseLeases(f)
}

// ParseLeases reads "expiry mac ip hostname client-id" lines; others are skipped.
func ParseLeases(r io.Reader) ([]Lease, error) {
	var out []Lease
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue // "duid ..." and blank lines
		}
		exp, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		l := Lease{MAC: strings.ToUpper(f[1]), IP: f[2], Hostname: f[3]}
		if exp > 0 {
			l.Expires = time.Unix(exp, 0)
		}
		if l.Hostname == "*" {
			l.Hostname = ""
		}
		out = append(out, l)
	}
	return out, sc.Err()
}
