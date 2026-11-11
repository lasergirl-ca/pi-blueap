package bluez

import (
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// DeviceInfo is the org.bluez.Device1 state the daemon cares about.
type DeviceInfo struct {
	Path      dbus.ObjectPath `json:"path"`
	Address   string          `json:"address"`
	Name      string          `json:"name,omitempty"`
	Alias     string          `json:"alias,omitempty"`
	Class     uint32          `json:"class,omitempty"`
	Paired    bool            `json:"paired"`
	Bonded    bool            `json:"bonded"`
	Trusted   bool            `json:"trusted"`
	Connected bool            `json:"connected"`
	UUIDs     []string        `json:"uuids,omitempty"`
}

// DisplayName is the best human name we have.
func (d DeviceInfo) DisplayName() string {
	if d.Name != "" {
		return d.Name
	}
	return d.Alias
}

func deviceFromProps(p dbus.ObjectPath, m map[string]dbus.Variant) DeviceInfo {
	return DeviceInfo{
		Path:      p,
		Address:   vString(m, "Address"),
		Name:      vString(m, "Name"),
		Alias:     vString(m, "Alias"),
		Class:     vUint32(m, "Class"),
		Paired:    vBool(m, "Paired"),
		Bonded:    vBool(m, "Bonded"),
		Trusted:   vBool(m, "Trusted"),
		Connected: vBool(m, "Connected"),
		UUIDs:     vStrings(m, "UUIDs"),
	}
}

// Device reads a device by object path (what BlueZ hands the agent).
func (b *Bus) Device(p dbus.ObjectPath) (DeviceInfo, error) {
	var m map[string]dbus.Variant
	if err := b.Object(p).Call(IfaceProperties+".GetAll", 0, IfaceDevice).Store(&m); err != nil {
		return DeviceInfo{Path: p}, fmt.Errorf("bluez: %s GetAll: %w", p, err)
	}
	return deviceFromProps(p, m), nil
}

// SetTrusted writes Device1.Trusted.
func (b *Bus) SetTrusted(p dbus.ObjectPath, trusted bool) error {
	if err := b.Object(p).SetProperty(IfaceDevice+".Trusted", dbus.MakeVariant(trusted)); err != nil {
		return fmt.Errorf("bluez: set Trusted on %s: %w", p, err)
	}
	return nil
}

// DeviceChange is one PropertiesChanged on a Device1, a Device1 announced
// with properties already set, or (Removed) a Device1 that went away.
type DeviceChange struct {
	Path    dbus.ObjectPath
	Changed map[string]dbus.Variant
	Removed bool
}

// Paired reports whether this change flipped Paired or Bonded, and to what:
// true if either became true. BlueZ (5.66 and 5.82) holds the Paired signal
// back until the device's services are resolved (device_set_paired,
// pending_paired), and with ReverseServiceDiscovery off (D-037) a Palm's never
// are, so a Palm's pairing shows up only as Bonded (D-040).
func (c DeviceChange) Paired() (value, changed bool) {
	for _, k := range []string{"Paired", "Bonded"} {
		v, ok := c.Changed[k]
		if !ok {
			continue
		}
		if b, _ := v.Value().(bool); b {
			return true, true
		}
		changed = true
	}
	return false, changed
}

// OnDeviceChange calls h for every Device1 PropertiesChanged until the
// returned function is called. It also turns a new Device1 that is announced
// already paired, bonded or connected into a change, because BlueZ suppresses
// PropertiesChanged for an object it has not published yet (gdbus object.c,
// "don't emit property changed if interface is not yet published"), so a
// change made in that window would otherwise be lost.
func (b *Bus) OnDeviceChange(h func(DeviceChange)) func() {
	return b.OnSignal(func(s *dbus.Signal) {
		if c, ok := deviceChangeFromSignal(s); ok {
			h(c)
		}
	})
}

// deviceChangeFromSignal is OnDeviceChange's filter, split out for tests.
func deviceChangeFromSignal(s *dbus.Signal) (DeviceChange, bool) {
	switch s.Name {
	case IfaceProperties + ".PropertiesChanged":
		if len(s.Body) < 2 {
			return DeviceChange{}, false
		}
		if iface, _ := s.Body[0].(string); iface != IfaceDevice {
			return DeviceChange{}, false
		}
		changed, _ := s.Body[1].(map[string]dbus.Variant)
		return DeviceChange{Path: s.Path, Changed: changed}, true
	case IfaceObjectManager + ".InterfacesAdded":
		if len(s.Body) < 2 {
			return DeviceChange{}, false
		}
		p, _ := s.Body[0].(dbus.ObjectPath)
		ifaces, _ := s.Body[1].(map[string]map[string]dbus.Variant)
		props, ok := ifaces[IfaceDevice]
		if !ok {
			return DeviceChange{}, false
		}
		// Only a device that is already connected counts: one found by an
		// inquiry is announced unconnected, and after a bluetoothd restart
		// every stored device is announced paired but not connected, which is
		// not a new pairing. Of its properties, only true values are changes.
		if on, _ := props["Connected"].Value().(bool); !on {
			return DeviceChange{}, false
		}
		changed := map[string]dbus.Variant{}
		for _, k := range []string{"Paired", "Bonded", "Connected"} {
			if v, ok := props[k]; ok {
				if on, _ := v.Value().(bool); on {
					changed[k] = v
				}
			}
		}
		return DeviceChange{Path: p, Changed: changed}, true
	case IfaceObjectManager + ".InterfacesRemoved":
		if len(s.Body) < 2 {
			return DeviceChange{}, false
		}
		p, _ := s.Body[0].(dbus.ObjectPath)
		ifaces, _ := s.Body[1].([]string)
		for _, i := range ifaces {
			if i == IfaceDevice {
				return DeviceChange{Path: p, Removed: true}, true
			}
		}
		return DeviceChange{}, false
	}
	return DeviceChange{}, false
}

// AddrFromPath turns /org/bluez/hciN/dev_00_07_E0_12_34_56 into
// 00:07:E0:12:34:56, or "" if the path is not a device path.
func AddrFromPath(p dbus.ObjectPath) string {
	s := string(p)
	i := strings.LastIndex(s, "/dev_")
	if i < 0 {
		return ""
	}
	a := strings.ReplaceAll(s[i+len("/dev_"):], "_", ":")
	if len(a) != 17 {
		return ""
	}
	return strings.ToUpper(a)
}

// UnderAdapter reports whether device path p belongs to adapter path a.
func UnderAdapter(p, a dbus.ObjectPath) bool {
	return a != "" && strings.HasPrefix(string(p), string(a)+"/")
}
