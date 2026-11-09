package agent

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/godbus/dbus/v5"

	"palm-gw/internal/allow"
	"palm-gw/internal/bluez"
	"palm-gw/internal/event"
)

// Path is where the agent lives on the bus.
const Path = dbus.ObjectPath("/palm/agent")

// Capability steers BlueZ to RequestPinCode for legacy peers and
// RequestConfirmation for SSP peers (PLAN.md section 8.2).
const Capability = "DisplayYesNo"

// Agent implements org.bluez.Agent1. Every method looks the device up by the
// object path BlueZ passes in so logs carry the address and name.
type Agent struct {
	bus    *bluez.Bus
	window *Window
	allow  *allow.List
	events *event.Bus
	log    *slog.Logger

	mu       sync.Mutex
	pending  map[dbus.ObjectPath]struct{} // devices with a PIN outstanding
	released func()
}

// New builds an agent. onRelease is called when BlueZ tells us the agent was
// released (it will be re-registered when BlueZ comes back).
func New(bus *bluez.Bus, window *Window, allowList *allow.List, events *event.Bus, log *slog.Logger, onRelease func()) *Agent {
	return &Agent{
		bus:      bus,
		window:   window,
		allow:    allowList,
		events:   events,
		log:      log.With("component", "agent"),
		pending:  map[dbus.ObjectPath]struct{}{},
		released: onRelease,
	}
}

// Export puts the agent on the bus (once per process).
func (a *Agent) Export() error {
	return a.bus.Export(a, Path, bluez.IfaceAgent, bluez.Agent1XML)
}

// Register registers with AgentManager1 and becomes the default agent. Call
// it every time org.bluez (re)appears.
func (a *Agent) Register() error {
	mgr := a.bus.Object(bluez.RootPath)
	if err := mgr.Call(bluez.IfaceAgentManager+".RegisterAgent", 0, Path, Capability).Err; err != nil {
		// BlueZ keeps one registration per bus connection; if it still has
		// ours, that is the state we want.
		var derr dbus.Error
		if !errors.As(err, &derr) || derr.Name != "org.bluez.Error.AlreadyExists" {
			return fmt.Errorf("agent: RegisterAgent: %w", err)
		}
		a.log.Info("agent was already registered", "path", Path)
	}
	if err := mgr.Call(bluez.IfaceAgentManager+".RequestDefaultAgent", 0, Path).Err; err != nil {
		return fmt.Errorf("agent: RequestDefaultAgent: %w", err)
	}
	a.log.Info("agent registered", "path", Path, "capability", Capability)
	return nil
}

// Unregister tells BlueZ we are going away (shutdown).
func (a *Agent) Unregister() {
	_ = a.bus.Object(bluez.RootPath).Call(bluez.IfaceAgentManager+".UnregisterAgent", 0, Path).Err
}

func (a *Agent) device(p dbus.ObjectPath) (addr, name string) {
	d, err := a.bus.Device(p)
	if err != nil {
		a.log.Warn("device lookup failed", "path", p, "err", err)
		return string(p), ""
	}
	return d.Address, d.DisplayName()
}

func (a *Agent) emit(name, addr, dev string, kv ...string) {
	a.events.Emit(name, addr, dev, kv...)
}

// Release implements Agent1.Release.
func (a *Agent) Release() *dbus.Error {
	a.log.Warn("agent released by BlueZ")
	if a.released != nil {
		a.released()
	}
	return nil
}

// RequestPinCode is the path every Palm takes: legacy PIN pairing.
func (a *Agent) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	addr, name := a.device(device)
	pin, open := a.window.PIN()
	if !open {
		a.log.Warn("pairing rejected", "event", "pairing_rejected", "reason", "no_window", "addr", addr, "name", name)
		a.emit("pairing_rejected", addr, name, "reason", "no_window")
		return "", bluez.Rejected()
	}
	a.mu.Lock()
	a.pending[device] = struct{}{}
	a.mu.Unlock()
	a.log.Info("PIN requested", "event", "pin_requested", "addr", addr, "name", name)
	a.emit("pin_requested", addr, name)
	return pin, nil
}

// DisplayPinCode: the remote generated the PIN (Palms do not).
func (a *Agent) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error {
	addr, name := a.device(device)
	a.log.Info("PIN to display", "event", "display_pin", "addr", addr, "name", name, "pin", pincode)
	a.emit("display_pin", addr, name, "pin", pincode)
	return nil
}

// RequestPasskey: SSP numeric entry. Reject unless a window is open.
func (a *Agent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	addr, name := a.device(device)
	if !a.window.IsOpen() {
		a.log.Warn("passkey rejected", "event", "pairing_rejected", "reason", "no_window", "addr", addr, "name", name)
		a.emit("pairing_rejected", addr, name, "reason", "no_window")
		return 0, bluez.Rejected()
	}
	key, err := RandomPasskey()
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	ps := fmt.Sprintf("%06d", key)
	a.log.Info("passkey generated", "event", "passkey", "addr", addr, "name", name, "passkey", ps)
	a.emit("passkey", addr, name, "passkey", ps)
	return key, nil
}

// DisplayPasskey logs SSP progress.
func (a *Agent) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	addr, name := a.device(device)
	a.log.Info("passkey display", "event", "display_passkey", "addr", addr, "name", name, "passkey", fmt.Sprintf("%06d", passkey), "entered", entered)
	return nil
}

// RequestConfirmation: SSP numeric comparison (modern PANU clients). Accept
// inside a window, reject outside; log the passkey so the operator can compare.
func (a *Agent) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	addr, name := a.device(device)
	ps := fmt.Sprintf("%06d", passkey)
	if !a.window.IsOpen() {
		a.log.Warn("confirmation rejected", "event", "pairing_rejected", "reason", "no_window", "addr", addr, "name", name, "passkey", ps)
		a.emit("pairing_rejected", addr, name, "reason", "no_window", "passkey", ps)
		return bluez.Rejected()
	}
	a.log.Info("confirmation accepted", "event", "confirm", "addr", addr, "name", name, "passkey", ps)
	a.emit("confirm", addr, name, "passkey", ps)
	return nil
}

// RequestAuthorization: accept if a window is open or the device is allowlisted.
func (a *Agent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error {
	addr, name := a.device(device)
	if a.window.IsOpen() || a.allow.Allowed(addr) {
		a.log.Info("authorization accepted", "event", "authorized", "addr", addr, "name", name)
		return nil
	}
	a.log.Warn("authorization rejected", "event", "authorization_rejected", "addr", addr, "name", name)
	a.emit("authorization_rejected", addr, name)
	return bluez.Rejected()
}

// AuthorizeService is the allowlist gate for every profile connection.
func (a *Agent) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error {
	addr, name := a.device(device)
	if ok, src := a.allow.Check(addr); ok {
		a.log.Info("service authorized", "event", "service_authorized", "addr", addr, "name", name, "uuid", uuid, "source", string(src))
		return nil
	}
	a.log.Warn("service rejected", "event", "service_rejected", "addr", addr, "name", name, "uuid", uuid)
	a.emit("service_rejected", addr, name, "uuid", uuid)
	return bluez.Rejected()
}

// Cancel clears any pending PIN display.
func (a *Agent) Cancel() *dbus.Error {
	a.mu.Lock()
	n := len(a.pending)
	a.pending = map[dbus.ObjectPath]struct{}{}
	a.mu.Unlock()
	a.log.Info("pairing cancelled by BlueZ", "event", "pairing_cancelled", "pending", n)
	a.emit("pairing_cancelled", "", "")
	return nil
}

// ClearPending forgets a device with a PIN outstanding (after it paired).
func (a *Agent) ClearPending(device dbus.ObjectPath) {
	a.mu.Lock()
	delete(a.pending, device)
	a.mu.Unlock()
}
