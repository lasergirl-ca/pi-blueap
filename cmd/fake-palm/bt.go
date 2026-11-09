package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"palm-gw/internal/bluez"
)

const (
	uuidSPP = "00001101-0000-1000-8000-00805f9b34fb"
	uuidLAP = "00001102-0000-1000-8000-00805f9b34fb"
	uuidDUN = "00001103-0000-1000-8000-00805f9b34fb"
	uuidNAP = "00001116-0000-1000-8000-00805f9b34fb"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func client(adapter string) (*bluez.Bus, *bluez.Adapter, error) {
	bus, err := bluez.Connect(quietLog())
	if err != nil {
		return nil, nil, err
	}
	want := adapter
	if want == "" {
		want = "auto"
	}
	a, _, err := bus.FindAdapter(want)
	if err != nil {
		_ = bus.Close()
		return nil, nil, err
	}
	if !a.Props.Powered {
		if err := a.Set("Powered", true); err != nil {
			return nil, nil, err
		}
	}
	return bus, a, nil
}

func cmdDiscover(adapter string, seconds int, expect string) int {
	bus, a, err := client(adapter)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm:", err)
		return 1
	}
	defer func() { _ = bus.Close() }()
	obj := bus.Object(a.Path)
	// BR/EDR only, like a Palm would see.
	filter := map[string]dbus.Variant{"Transport": dbus.MakeVariant("bredr")}
	if err := obj.Call(bluez.IfaceAdapter+".SetDiscoveryFilter", 0, filter).Err; err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm: SetDiscoveryFilter:", err)
	}
	if err := obj.Call(bluez.IfaceAdapter+".StartDiscovery", 0).Err; err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm: StartDiscovery:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "fake-palm: inquiry on %s (%s) for %ds...\n", a.HCI, a.Props.Address, seconds)
	time.Sleep(time.Duration(seconds) * time.Second)
	_ = obj.Call(bluez.IfaceAdapter+".StopDiscovery", 0).Err

	devs, err := a.Devices()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm:", err)
		return 1
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].Address < devs[j].Address })
	found := false
	for _, d := range devs {
		fmt.Printf("%s  class 0x%06X  %q\n", d.Address, d.Class, d.DisplayName())
		if expect != "" && d.DisplayName() == expect {
			found = true
		}
	}
	if expect != "" {
		if !found {
			fmt.Fprintf(os.Stderr, "fake-palm: %q not found\n", expect)
			return 1
		}
		fmt.Fprintf(os.Stderr, "fake-palm: found %q\n", expect)
	}
	return 0
}

func findDevice(bus *bluez.Bus, a *bluez.Adapter, addr string) (*bluez.DeviceInfo, error) {
	dev, err := a.DeviceByAddress(addr)
	if err != nil {
		return nil, err
	}
	if dev != nil {
		return dev, nil
	}
	// Not known yet: run a short inquiry to create the device object.
	obj := bus.Object(a.Path)
	_ = obj.Call(bluez.IfaceAdapter+".SetDiscoveryFilter", 0, map[string]dbus.Variant{"Transport": dbus.MakeVariant("bredr")}).Err
	if err := obj.Call(bluez.IfaceAdapter+".StartDiscovery", 0).Err; err != nil {
		return nil, fmt.Errorf("StartDiscovery: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if dev, _ = a.DeviceByAddress(addr); dev != nil {
			break
		}
	}
	_ = obj.Call(bluez.IfaceAdapter+".StopDiscovery", 0).Err
	if dev == nil {
		return nil, fmt.Errorf("device %s not found by inquiry", addr)
	}
	return dev, nil
}

func cmdSDP(adapter, addr string) int {
	bus, a, err := client(adapter)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm:", err)
		return 1
	}
	defer func() { _ = bus.Close() }()
	dev, err := findDevice(bus, a, addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm:", err)
		return 1
	}
	// Connect triggers an SDP browse; it may "fail" (no usable profile on
	// the client side) and still populate UUIDs.
	if err := bus.Object(dev.Path).Call(bluez.IfaceDevice+".Connect", 0).Err; err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm: Connect:", err, "(continuing; UUIDs may still be populated)")
	}
	time.Sleep(2 * time.Second)
	fresh, err := bus.Device(dev.Path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm:", err)
		return 1
	}
	_ = bus.Object(dev.Path).Call(bluez.IfaceDevice+".Disconnect", 0).Err
	names := map[string]string{uuidSPP: "SPP", uuidLAP: "LAP", uuidDUN: "DUN", uuidNAP: "NAP"}
	have := map[string]bool{}
	for _, u := range fresh.UUIDs {
		fmt.Printf("%s  %s\n", u, names[strings.ToLower(u)])
		have[strings.ToLower(u)] = true
	}
	rc := 0
	for _, u := range []string{uuidSPP, uuidLAP, uuidDUN} {
		if !have[u] {
			fmt.Fprintf(os.Stderr, "fake-palm: missing %s\n", names[u])
			rc = 1
		}
	}
	if rc == 0 {
		fmt.Fprintln(os.Stderr, "fake-palm: SPP, LAP and DUN all present")
	}
	return rc
}

// clientAgent answers the gateway's PIN from the client side.
type clientAgent struct{ pin string }

func (c *clientAgent) Release() *dbus.Error { return nil }

// RequestPinCode answers the gateway with the PIN from --pin. godbus needs the
// *dbus.Error result to export the method, even though it is always nil here.
func (c *clientAgent) RequestPinCode(dbus.ObjectPath) (string, *dbus.Error) { //nolint:unparam // D-Bus method signature
	fmt.Fprintf(os.Stderr, "fake-palm: gateway asked for a PIN, answering %s\n", c.pin)
	return c.pin, nil
}
func (c *clientAgent) DisplayPinCode(_ dbus.ObjectPath, pin string) *dbus.Error {
	fmt.Fprintf(os.Stderr, "fake-palm: display PIN %s\n", pin)
	return nil
}
func (c *clientAgent) RequestPasskey(dbus.ObjectPath) (uint32, *dbus.Error) {
	return 0, dbus.NewError(bluez.ErrRejected, nil)
}
func (c *clientAgent) DisplayPasskey(dbus.ObjectPath, uint32, uint16) *dbus.Error { return nil }
func (c *clientAgent) RequestConfirmation(_ dbus.ObjectPath, passkey uint32) *dbus.Error {
	fmt.Fprintf(os.Stderr, "fake-palm: confirming passkey %06d (SSP)\n", passkey)
	return nil
}
func (c *clientAgent) RequestAuthorization(dbus.ObjectPath) *dbus.Error     { return nil }
func (c *clientAgent) AuthorizeService(dbus.ObjectPath, string) *dbus.Error { return nil }
func (c *clientAgent) Cancel() *dbus.Error                                  { return nil }

func cmdPair(adapter, addr, pin string) int {
	if pin == "" {
		fmt.Fprintln(os.Stderr, "fake-palm pair: --pin is required (read it from `palm-gwctl pair` on the gateway)")
		return 2
	}
	bus, a, err := client(adapter)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm:", err)
		return 1
	}
	defer func() { _ = bus.Close() }()
	const path = dbus.ObjectPath("/fakepalm/agent")
	if err := bus.Export(&clientAgent{pin: pin}, path, bluez.IfaceAgent, bluez.Agent1XML); err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm:", err)
		return 1
	}
	mgr := bus.Object(bluez.RootPath)
	if err := mgr.Call(bluez.IfaceAgentManager+".RegisterAgent", 0, path, "KeyboardOnly").Err; err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm: RegisterAgent:", err)
		return 1
	}
	defer func() { _ = mgr.Call(bluez.IfaceAgentManager+".UnregisterAgent", 0, path).Err }()
	if err := mgr.Call(bluez.IfaceAgentManager+".RequestDefaultAgent", 0, path).Err; err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm: RequestDefaultAgent:", err, "(another agent is default; continuing)")
	}
	dev, err := findDevice(bus, a, addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake-palm:", err)
		return 1
	}
	if dev.Paired {
		fmt.Fprintf(os.Stderr, "fake-palm: %s is already paired here; removing it first\n", addr)
		if err := a.RemoveDevice(dev.Path); err != nil {
			fmt.Fprintln(os.Stderr, "fake-palm:", err)
			return 1
		}
		if dev, err = findDevice(bus, a, addr); err != nil {
			fmt.Fprintln(os.Stderr, "fake-palm:", err)
			return 1
		}
	}
	fmt.Fprintf(os.Stderr, "fake-palm: pairing with %s %q...\n", dev.Address, dev.DisplayName())
	start := time.Now()
	if err := bus.Object(dev.Path).Call(bluez.IfaceDevice+".Pair", 0).Err; err != nil {
		fmt.Fprintf(os.Stderr, "fake-palm: Pair failed after %s: %v\n", time.Since(start).Round(time.Millisecond), err)
		return 1
	}
	_ = bus.SetTrusted(dev.Path, true)
	fresh, _ := bus.Device(dev.Path)
	fmt.Printf("paired %s %q in %s (paired=%v bonded=%v)\n", fresh.Address, fresh.DisplayName(), time.Since(start).Round(time.Millisecond), fresh.Paired, fresh.Bonded)
	return 0
}
