package bluez

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestDeviceChangeFromSignal(t *testing.T) {
	const dev = dbus.ObjectPath("/org/bluez/hci0/dev_00_07_E0_3C_5E_FA")
	on, off := dbus.MakeVariant(true), dbus.MakeVariant(false)
	changedSig := func(iface string, props map[string]dbus.Variant) *dbus.Signal {
		return &dbus.Signal{Path: dev, Name: IfaceProperties + ".PropertiesChanged",
			Body: []any{iface, props, []string{}}}
	}
	added := func(props map[string]dbus.Variant) *dbus.Signal {
		return &dbus.Signal{Path: "/", Name: IfaceObjectManager + ".InterfacesAdded",
			Body: []any{dev, map[string]map[string]dbus.Variant{IfaceDevice: props}}}
	}
	cases := []struct {
		name       string
		sig        *dbus.Signal
		ok         bool
		removed    bool
		paired     bool
		pairedSeen bool
		connected  bool
	}{
		{"properties changed: paired", changedSig(IfaceDevice, map[string]dbus.Variant{"Paired": on}), true, false, true, true, false},
		// D-040: a Palm's pairing with ReverseServiceDiscovery off shows up only as Bonded.
		{"properties changed: bonded only", changedSig(IfaceDevice, map[string]dbus.Variant{"Bonded": on}), true, false, true, true, false},
		{"properties changed: unpaired", changedSig(IfaceDevice, map[string]dbus.Variant{"Paired": off, "Bonded": off}), true, false, false, true, false},
		{"properties changed: connected", changedSig(IfaceDevice, map[string]dbus.Variant{"Connected": on}), true, false, false, false, true},
		{"properties changed: other interface", changedSig("org.bluez.Network1", map[string]dbus.Variant{"Connected": on}), false, false, false, false, false},
		{"added connected and bonded", added(map[string]dbus.Variant{"Paired": off, "Bonded": on, "Connected": on}), true, false, true, true, true},
		{"added connected only", added(map[string]dbus.Variant{"Paired": off, "Bonded": off, "Connected": on}), true, false, false, false, true},
		{"added by an inquiry", added(map[string]dbus.Variant{"Paired": off, "Connected": off}), false, false, false, false, false},
		// After a bluetoothd restart every stored device reappears paired but not connected.
		{"added after a BlueZ restart", added(map[string]dbus.Variant{"Paired": on, "Bonded": on, "Connected": off}), false, false, false, false, false},
		{"added adapter", &dbus.Signal{Path: "/", Name: IfaceObjectManager + ".InterfacesAdded",
			Body: []any{dbus.ObjectPath("/org/bluez/hci0"), map[string]map[string]dbus.Variant{IfaceAdapter: {"Powered": on}}}}, false, false, false, false, false},
		{"device removed", &dbus.Signal{Path: "/", Name: IfaceObjectManager + ".InterfacesRemoved",
			Body: []any{dev, []string{IfaceDevice, IfaceProperties}}}, true, true, false, false, false},
		{"network interface removed", &dbus.Signal{Path: "/", Name: IfaceObjectManager + ".InterfacesRemoved",
			Body: []any{dev, []string{"org.bluez.Network1"}}}, false, false, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := deviceChangeFromSignal(c.sig)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if got.Path != dev {
				t.Errorf("path = %s", got.Path)
			}
			if got.Removed != c.removed {
				t.Errorf("Removed = %v, want %v", got.Removed, c.removed)
			}
			paired, seen := got.Paired()
			if paired != c.paired || seen != c.pairedSeen {
				t.Errorf("Paired() = %v, %v; want %v, %v", paired, seen, c.paired, c.pairedSeen)
			}
			_, conn := got.Changed["Connected"]
			if conn != c.connected {
				t.Errorf("Connected present = %v, want %v", conn, c.connected)
			}
		})
	}
}
