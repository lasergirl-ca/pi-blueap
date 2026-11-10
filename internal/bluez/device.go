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

// DeviceChange is one PropertiesChanged on a Device1.
type DeviceChange struct {
	Path    dbus.ObjectPath
	Changed map[string]dbus.Variant
}

// Paired reports whether this change flipped Paired, and to what.
func (c DeviceChange) Paired() (value, changed bool) {
	v, ok := c.Changed["Paired"]
	if !ok {
		return false, false
	}
	b, _ := v.Value().(bool)
	return b, true
}

// OnDeviceChange calls h for every Device1 PropertiesChanged until the
// returned function is called.
func (b *Bus) OnDeviceChange(h func(DeviceChange)) func() {
	return b.OnSignal(func(s *dbus.Signal) {
		if s.Name != IfaceProperties+".PropertiesChanged" || len(s.Body) < 2 {
			return
		}
		iface, _ := s.Body[0].(string)
		if iface != IfaceDevice {
			return
		}
		changed, _ := s.Body[1].(map[string]dbus.Variant)
		h(DeviceChange{Path: s.Path, Changed: changed})
	})
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
