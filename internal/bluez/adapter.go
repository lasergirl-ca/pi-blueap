package bluez

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

var hciRe = regexp.MustCompile(`^hci[0-9]+$`)

// AdapterProps are the org.bluez.Adapter1 properties the daemon reads.
type AdapterProps struct {
	Address             string `json:"address"`
	Name                string `json:"name"`
	Alias               string `json:"alias"`
	Class               uint32 `json:"class"`
	Powered             bool   `json:"powered"`
	Discoverable        bool   `json:"discoverable"`
	Pairable            bool   `json:"pairable"`
	DiscoverableTimeout uint32 `json:"discoverable_timeout"`
	PairableTimeout     uint32 `json:"pairable_timeout"`
	Modalias            string `json:"modalias,omitempty"`
}

// Adapter is one BlueZ controller.
type Adapter struct {
	bus   *Bus
	Path  dbus.ObjectPath `json:"path"`
	HCI   string          `json:"hci"`
	Props AdapterProps    `json:"props"`
}

func adapterFromProps(b *Bus, p dbus.ObjectPath, m map[string]dbus.Variant) *Adapter {
	return &Adapter{bus: b, Path: p, HCI: path.Base(string(p)), Props: propsFromMap(m)}
}

func propsFromMap(m map[string]dbus.Variant) AdapterProps {
	return AdapterProps{
		Address:             vString(m, "Address"),
		Name:                vString(m, "Name"),
		Alias:               vString(m, "Alias"),
		Class:               vUint32(m, "Class"),
		Powered:             vBool(m, "Powered"),
		Discoverable:        vBool(m, "Discoverable"),
		Pairable:            vBool(m, "Pairable"),
		DiscoverableTimeout: vUint32(m, "DiscoverableTimeout"),
		PairableTimeout:     vUint32(m, "PairableTimeout"),
		Modalias:            vString(m, "Modalias"),
	}
}

// Adapters lists every controller BlueZ knows, lowest hciN first.
func (b *Bus) Adapters() ([]*Adapter, error) {
	objs, err := b.ManagedObjects()
	if err != nil {
		return nil, err
	}
	var out []*Adapter
	for p, ifaces := range objs {
		if m, ok := ifaces[IfaceAdapter]; ok {
			out = append(out, adapterFromProps(b, p, m))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HCI < out[j].HCI })
	return out, nil
}

// FindAdapter picks the controller for [bluetooth].adapter: "hciN" by name,
// or "auto": one adapter → it; several → the one whose Modalias starts with
// "usb:" (a dongle), else the lowest hciN (PLAN.md section 8.1).
func (b *Bus) FindAdapter(want string) (*Adapter, string, error) {
	adapters, err := b.Adapters()
	if err != nil {
		return nil, "", err
	}
	if len(adapters) == 0 {
		return nil, "", fmt.Errorf("bluez: no Bluetooth adapter found (is dtoverlay=disable-bt set, or the dongle unplugged?)")
	}
	if want != "auto" {
		if !hciRe.MatchString(want) {
			return nil, "", fmt.Errorf("bluez: bad adapter name %q", want)
		}
		for _, a := range adapters {
			if a.HCI == want {
				return a, "configured", nil
			}
		}
		names := make([]string, 0, len(adapters))
		for _, a := range adapters {
			names = append(names, fmt.Sprintf("%s (%s)", a.HCI, a.Props.Address))
		}
		return nil, "", fmt.Errorf("bluez: adapter %s not found; present: %s", want, strings.Join(names, ", "))
	}
	if len(adapters) == 1 {
		return adapters[0], "auto: only adapter", nil
	}
	for _, a := range adapters {
		if strings.HasPrefix(a.Props.Modalias, "usb:") {
			return a, "auto: USB dongle preferred", nil
		}
	}
	return adapters[0], "auto: lowest hciN", nil
}

// Refresh re-reads the adapter properties.
func (a *Adapter) Refresh() error {
	var m map[string]dbus.Variant
	if err := a.bus.Object(a.Path).Call(IfaceProperties+".GetAll", 0, IfaceAdapter).Store(&m); err != nil {
		return fmt.Errorf("bluez: %s GetAll: %w", a.HCI, err)
	}
	a.Props = propsFromMap(m)
	return nil
}

// Set writes one Adapter1 property.
func (a *Adapter) Set(prop string, v any) error {
	if err := a.bus.Object(a.Path).SetProperty(IfaceAdapter+"."+prop, dbus.MakeVariant(v)); err != nil {
		return fmt.Errorf("bluez: set %s.%s: %w", a.HCI, prop, err)
	}
	return nil
}

// Configure applies the runtime properties from PLAN.md section 8.1: Alias,
// Powered, Discoverable forever, Pairable off (the pairing window turns it on).
func (a *Adapter) Configure(alias string) error {
	steps := []struct {
		prop string
		v    any
	}{
		{"Powered", true},
		{"Alias", alias},
		{"DiscoverableTimeout", uint32(0)},
		{"Discoverable", true},
		{"PairableTimeout", uint32(0)},
		{"Pairable", false},
	}
	for _, s := range steps {
		if err := a.Set(s.prop, s.v); err != nil {
			return err
		}
	}
	return a.Refresh()
}

// SetPairable toggles Pairable.
func (a *Adapter) SetPairable(on bool) error { return a.Set("Pairable", on) }

// RemoveDevice forgets a device: link key, trust, everything.
func (a *Adapter) RemoveDevice(dev dbus.ObjectPath) error {
	if err := a.bus.Object(a.Path).Call(IfaceAdapter+".RemoveDevice", 0, dev).Err; err != nil {
		return fmt.Errorf("bluez: RemoveDevice %s: %w", dev, err)
	}
	return nil
}

// Devices lists the devices BlueZ knows under this adapter.
func (a *Adapter) Devices() ([]DeviceInfo, error) {
	objs, err := a.bus.ManagedObjects()
	if err != nil {
		return nil, err
	}
	prefix := string(a.Path) + "/"
	var out []DeviceInfo
	for p, ifaces := range objs {
		if !strings.HasPrefix(string(p), prefix) {
			continue
		}
		if m, ok := ifaces[IfaceDevice]; ok {
			out = append(out, deviceFromProps(p, m))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

// DeviceByAddress finds a device under this adapter by Bluetooth address.
func (a *Adapter) DeviceByAddress(addr string) (*DeviceInfo, error) {
	devs, err := a.Devices()
	if err != nil {
		return nil, err
	}
	addr = strings.ToUpper(addr)
	for i := range devs {
		if strings.ToUpper(devs[i].Address) == addr {
			return &devs[i], nil
		}
	}
	return nil, nil
}
