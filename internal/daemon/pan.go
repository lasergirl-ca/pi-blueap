package daemon

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/godbus/dbus/v5"

	"palm-gw/internal/bluez"
	"palm-gw/internal/ctl"
	"palm-gw/internal/pan"
)

const (
	defaultDnsmasqLeases = "/var/lib/palm-gw/dnsmasq.leases"
	// panEvery is how often the PAN loop looks at the kernel's BNEP
	// connections. It is also how long a device that is trusted in BlueZ but
	// no longer allowlisted can stay connected (D-032).
	panEvery = time.Second
	// bridgeWait is how long the bridge may be missing at start before it is
	// reported as an error (section 8.6: NetworkManager brings it up
	// asynchronously).
	bridgeWait = 30 * time.Second
)

// panState is the daemon's view of the NAP server and its clients.
type panState struct {
	registered bool
	gen        uint64 // BlueZ generation the registration belongs to
	adapter    dbus.ObjectPath
	since      time.Time
	err        string
	clients    map[string]*ctl.PANClient // by address
}

// keepPAN registers the NAP server once BlueZ and the bridge are there (again
// after every BlueZ restart), and follows PAN clients: logs them, refuses
// devices that are not allowed, and notes their DHCP address.
func (d *Daemon) keepPAN(ctx context.Context) {
	if !d.config().PAN.Enabled {
		return
	}
	bridge := d.config().PAN.Bridge
	start := time.Now()
	waitLogged := false
	t := time.NewTicker(panEvery)
	defer t.Stop()
	for {
		d.ensureNAP(bridge, start, &waitLogged)
		d.watchPANClients()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ensureNAP registers the NAP server if this BlueZ instance does not have it yet.
func (d *Daemon) ensureNAP(bridge string, start time.Time, waitLogged *bool) {
	d.mu.Lock()
	present, regd, gen, adapter := d.present, d.regd, d.gen, d.adapter
	d.mu.Unlock()
	d.panMu.Lock()
	ok := d.pan.registered && d.pan.gen == gen
	d.panMu.Unlock()
	if ok || !present || !regd || adapter == nil {
		return
	}
	if !pan.BridgePresent(bridge) {
		switch {
		case time.Since(start) < bridgeWait:
			if !*waitLogged {
				d.panLog.Info("waiting for the bridge before registering NAP", "bridge", bridge)
				*waitLogged = true
			}
		default:
			d.setPANError("bridge " + bridge + " does not exist (is palm-gwd-nm-bridge.service active?)")
		}
		return
	}
	if err := pan.Register(d.bus, adapter.Path, bridge); err != nil {
		d.setPANError(err.Error())
		return
	}
	d.panMu.Lock()
	d.pan.registered, d.pan.gen, d.pan.adapter, d.pan.since, d.pan.err = true, gen, adapter.Path, time.Now(), ""
	d.panMu.Unlock()
	d.panLog.Info("NAP server registered", "event", "pan_registered", "hci", adapter.HCI, "bridge", bridge)
	d.events.Emit("pan_registered", "", "", "bridge", bridge)
}

// setPANError records a registration failure, logging it once per message.
func (d *Daemon) setPANError(msg string) {
	d.panMu.Lock()
	prev := d.pan.err
	d.pan.registered = false
	d.pan.err = msg
	d.panMu.Unlock()
	if prev != msg {
		d.panLog.Error("NAP server not registered", "event", "pan_error", "err", msg)
		d.events.Emit("pan_error", "", "", "err", msg)
	}
}

// watchPANClients compares the kernel's BNEP connections with what it saw
// last time. A client that is not allowed is dropped at once: BlueZ skips
// AuthorizeService for Trusted devices and PAN has no NewConnection hook to
// check in (D-032, the PAN side of D-019).
func (d *Daemon) watchPANClients() {
	conns, err := pan.Conns()
	if err != nil {
		if !errors.Is(err, pan.ErrUnsupported) {
			d.panMu.Lock()
			d.pan.err = err.Error()
			d.panMu.Unlock()
		}
		return
	}
	now := map[string]pan.Conn{}
	for _, c := range conns {
		if c.Role != pan.RoleNAP {
			continue // an outgoing PANU connection from this host
		}
		now[c.Addr] = c
	}
	var leases []pan.Lease
	d.panMu.Lock()
	needLeases := false
	for addr := range now {
		if c := d.pan.clients[addr]; c == nil || c.IP == "" {
			needLeases = true
		}
	}
	d.panMu.Unlock()
	if needLeases {
		leases, _ = pan.ReadLeases(d.opts.DnsmasqLeases)
	}

	for addr, c := range now {
		d.panMu.Lock()
		known := d.pan.clients[addr]
		d.panMu.Unlock()
		if known == nil {
			name := d.deviceName(addr)
			if ok, _ := d.allow.Check(addr); !ok {
				err := pan.Kill(addr)
				d.panLog.Warn("PAN client refused", "event", "pan_refused", "addr", addr, "name", name,
					"ifname", c.Ifname, "reason", "not_allowed", "err", errString(err))
				d.events.Emit("pan_refused", addr, name, "reason", "not_allowed")
				go d.dropLink(addr, "not_allowed")
				continue
			}
			known = &ctl.PANClient{Addr: addr, Name: name, Ifname: c.Ifname, Since: time.Now()}
			d.panMu.Lock()
			d.pan.clients[addr] = known
			d.panMu.Unlock()
			_ = d.store.Seen(addr, name)
			d.panLog.Info("PAN client connected", "event", "pan_connected", "addr", addr, "name", name, "ifname", c.Ifname)
			d.events.Emit("pan_connected", addr, name, "ifname", c.Ifname)
		}
		if known.IP == "" {
			if l := leaseFor(leases, addr); l != nil {
				d.panMu.Lock()
				known.IP, known.Hostname = l.IP, l.Hostname
				d.panMu.Unlock()
				d.panLog.Info("PAN client has an address", "event", "pan_lease", "addr", addr, "name", known.Name,
					"ifname", known.Ifname, "ip", l.IP, "hostname", l.Hostname)
				d.events.Emit("pan_lease", addr, known.Name, "ip", l.IP)
			}
		}
	}
	d.panMu.Lock()
	var gone []*ctl.PANClient
	for addr, c := range d.pan.clients {
		if _, ok := now[addr]; !ok {
			gone = append(gone, c)
			delete(d.pan.clients, addr)
		}
	}
	d.panMu.Unlock()
	for _, c := range gone {
		d.panLog.Info("PAN client disconnected", "event", "pan_disconnected", "addr", c.Addr, "name", c.Name,
			"ifname", c.Ifname, "ip", c.IP, "duration", time.Since(c.Since).Round(time.Second).String())
		d.events.Emit("pan_disconnected", c.Addr, c.Name)
	}
}

// leaseFor finds the client's lease. A PANU's Ethernet address is its
// Bluetooth address, so the lease's MAC matches.
func leaseFor(leases []pan.Lease, addr string) *pan.Lease {
	for i := range leases {
		if leases[i].MAC == addr {
			return &leases[i]
		}
	}
	return nil
}

// deviceName is BlueZ's name for addr under our adapter, else the state file's.
func (d *Daemon) deviceName(addr string) string {
	if a := d.currentAdapter(); a != nil {
		if dev, _ := a.DeviceByAddress(addr); dev != nil {
			return dev.DisplayName()
		}
	}
	if dev, ok := d.store.Get(addr); ok {
		return dev.Name
	}
	return ""
}

// disconnectPAN drops addr's PAN connection; it reports whether there was one.
// With link, the Bluetooth link is dropped too so the client notices (dropLink).
func (d *Daemon) disconnectPAN(addr, reason string, link bool) bool {
	conns, err := pan.Conns()
	if err != nil {
		return false
	}
	for _, c := range conns {
		if c.Addr != addr || c.Role != pan.RoleNAP {
			continue
		}
		err := pan.Kill(addr)
		d.panLog.Info("PAN client dropped", "event", "pan_drop", "addr", addr, "ifname", c.Ifname,
			"reason", reason, "err", errString(err))
		if link {
			d.dropLink(addr, reason)
		}
		return err == nil
	}
	return false
}

// dropLink disconnects addr's Bluetooth link (Device1.Disconnect). Deleting the
// kernel's BNEP session alone stops the traffic and removes bnepN, but
// bluetoothd keeps its own descriptor for the session's L2CAP socket, so the
// channel stays open and the client still thinks it is connected (the PAN
// side of D-028; D-032). This also ends any RFCOMM session of the device.
func (d *Daemon) dropLink(addr, reason string) {
	a := d.currentAdapter()
	if a == nil {
		return
	}
	dev, err := a.DeviceByAddress(addr)
	if err != nil || dev == nil {
		return
	}
	err = d.bus.Object(dev.Path).Call(bluez.IfaceDevice+".Disconnect", 0).Err
	d.panLog.Info("Bluetooth link dropped", "event", "link_drop", "addr", addr, "reason", reason, "err", errString(err))
}

// panStatus is the PAN part of `status`.
func (d *Daemon) panStatus() ctl.PANStatus {
	cfg := d.config()
	d.panMu.Lock()
	defer d.panMu.Unlock()
	st := ctl.PANStatus{Enabled: cfg.PAN.Enabled, Bridge: cfg.PAN.Bridge, Registered: d.pan.registered, Error: d.pan.err,
		Ports: pan.Ports(cfg.PAN.Bridge)}
	if d.pan.registered {
		st.Since = d.pan.since
	}
	for _, c := range d.pan.clients {
		st.Clients = append(st.Clients, *c)
	}
	sort.Slice(st.Clients, func(i, j int) bool { return st.Clients[i].Ifname < st.Clients[j].Ifname })
	return st
}

// unregisterNAP runs at shutdown while BlueZ is still there.
func (d *Daemon) unregisterNAP() {
	d.panMu.Lock()
	regd, adapter := d.pan.registered, d.pan.adapter
	d.pan.registered = false
	d.panMu.Unlock()
	if !regd || adapter == "" {
		return
	}
	if err := pan.Unregister(d.bus, adapter); err != nil {
		d.panLog.Warn("NAP server unregister failed", "err", err)
	}
}

// markNAPGone forgets the registration when BlueZ goes away.
func (d *Daemon) markNAPGone() {
	d.panMu.Lock()
	d.pan.registered = false
	d.panMu.Unlock()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// untrustDisallowed clears Device1.Trusted on every device under the adapter
// that the allowlist no longer accepts, so BlueZ asks AuthorizeService (and the
// agent refuses) before a PAN or RFCOMM connection is set up. That can only
// happen when the allowlist changed behind BlueZ's back: a static entry
// removed from config.toml, or the state file edited. The per-connection
// checks (D-019, and watchPANClients for PAN) stay as the backstop (D-032).
func (d *Daemon) untrustDisallowed(why string) {
	a := d.currentAdapter()
	if a == nil {
		return
	}
	devs, err := a.Devices()
	if err != nil {
		return
	}
	for _, dev := range devs {
		if !dev.Trusted {
			continue
		}
		if ok, _ := d.allow.Check(dev.Address); ok {
			continue
		}
		err := d.bus.SetTrusted(dev.Path, false)
		d.log.Warn("trusted device is not allowlisted; trust cleared", "event", "untrusted", "addr", dev.Address,
			"name", dev.DisplayName(), "why", why, "err", errString(err))
		d.events.Emit("untrusted", dev.Address, dev.DisplayName(), "why", why)
	}
}

// panClients counts the PAN clients connected now.
func (d *Daemon) panClients() int {
	d.panMu.Lock()
	defer d.panMu.Unlock()
	return len(d.pan.clients)
}
