// Package mm reads an LTE modem's state from ModemManager on the system bus
// (D-074, docs/MOBILE.md): what the gateway shows about a 4G uplink. It only
// reads; NetworkManager brings the modem's connection up and down.
package mm

import (
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	service     = "org.freedesktop.ModemManager1"
	rootPath    = dbus.ObjectPath("/org/freedesktop/ModemManager1")
	ifaceModem  = service + ".Modem"
	iface3gpp   = service + ".Modem.Modem3gpp"
	objectsCall = "org.freedesktop.DBus.ObjectManager.GetManagedObjects"
)

// ErrNoModem means ModemManager runs but lists no modem.
var ErrNoModem = errors.New("ModemManager lists no modem")

// Modem is what the gateway shows about the modem.
type Modem struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	State        string `json:"state"`  // failed, locked, disabled, enabled, searching, registered, connecting, connected, ...
	Locked       string `json:"locked"` // "" or what the SIM needs: sim-pin, sim-puk, ...
	Signal       int    `json:"signal"` // percent, 0 when unknown
	Tech         string `json:"tech"`   // 5G, LTE, 3G, 2G or ""
	Operator     string `json:"operator"`
	Roaming      bool   `json:"roaming"`
	Registration string `json:"registration"` // home, roaming, searching, denied, idle, unknown
}

var states = map[int32]string{-1: "failed", 0: "unknown", 1: "initialising", 2: "locked", 3: "disabled", 4: "disabling",
	5: "enabling", 6: "enabled", 7: "searching", 8: "registered", 9: "disconnecting", 10: "connecting", 11: "connected"}

var locks = map[uint32]string{0: "", 1: "", 2: "sim-pin", 3: "sim-pin2", 4: "sim-puk", 5: "sim-puk2"}

var regs = map[uint32]string{0: "idle", 1: "home", 2: "searching", 3: "denied", 4: "unknown", 5: "roaming",
	6: "home", 7: "roaming", 8: "emergency only", 9: "home", 10: "roaming"}

// tech names the best access technology in ModemManager's bitmask.
func tech(mask uint32) string {
	switch {
	case mask&(1<<15) != 0:
		return "5G"
	case mask&(1<<14) != 0:
		return "LTE"
	case mask&(1<<5|1<<6|1<<7|1<<8|1<<9|1<<10|1<<11|1<<12|1<<13) != 0:
		return "3G"
	case mask&(1<<1|1<<2|1<<3|1<<4) != 0:
		return "2G"
	}
	return ""
}

// Parse reads the first modem out of GetManagedObjects' answer.
func Parse(objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant) (Modem, error) {
	for _, ifaces := range objs {
		p, ok := ifaces[ifaceModem]
		if !ok {
			continue
		}
		var m Modem
		m.Manufacturer, _ = p["Manufacturer"].Value().(string)
		m.Model, _ = p["Model"].Value().(string)
		st, _ := p["State"].Value().(int32)
		m.State = states[st]
		if m.State == "" {
			m.State = fmt.Sprint(st)
		}
		lock, _ := p["UnlockRequired"].Value().(uint32)
		m.Locked = locks[lock]
		if _, known := locks[lock]; !known {
			m.Locked = fmt.Sprintf("lock %d", lock)
		}
		if sq, ok := p["SignalQuality"].Value().([]any); ok && len(sq) == 2 {
			if v, ok := sq[0].(uint32); ok {
				m.Signal = int(v)
			}
		}
		at, _ := p["AccessTechnologies"].Value().(uint32)
		m.Tech = tech(at)
		if g, ok := ifaces[iface3gpp]; ok {
			m.Operator, _ = g["OperatorName"].Value().(string)
			reg, _ := g["RegistrationState"].Value().(uint32)
			m.Registration = regs[reg]
			m.Roaming = m.Registration == "roaming"
		}
		return m, nil
	}
	return Modem{}, ErrNoModem
}

// Read asks ModemManager for the first modem.
func Read() (Modem, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return Modem{}, fmt.Errorf("ModemManager: system bus: %w", err)
	}
	defer func() { _ = conn.Close() }()
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := conn.Object(service, rootPath).Call(objectsCall, 0).Store(&objs); err != nil {
		return Modem{}, fmt.Errorf("ModemManager: %w", err)
	}
	return Parse(objs)
}
