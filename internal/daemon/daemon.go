// Package daemon is palm-gwd's run loop (PLAN.md section 5, "Lifecycle"):
// control socket first, then wait for org.bluez, pick the adapter, set its
// properties, register the agent, serve until BlueZ disappears or we are told
// to stop, and start over when BlueZ comes back. The RFCOMM profiles are
// registered alongside the agent; PPP sessions outlive a BlueZ restart
// because their sockets belong to the kernel. PAN and the firewall join this
// loop in later phases.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"

	"palm-gw/internal/agent"
	"palm-gw/internal/allow"
	"palm-gw/internal/bluez"
	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/event"
	"palm-gw/internal/ppp"
	"palm-gw/internal/profile"
	"palm-gw/internal/state"
)

// Options configure a Daemon.
type Options struct {
	Config     *config.Config
	ConfigPath string
	StatePath  string
	Version    string
	Phase      string
	Warnings   []string
	Log        *slog.Logger
	// DnsmasqConf and DnsmasqUnit are the file the daemon renders and the
	// unit it restarts when that file changes ("" = the installed defaults).
	DnsmasqConf string
	DnsmasqUnit string
	// OnReady is called once, the first time the agent is registered
	// (sd_notify READY=1 lives in main).
	OnReady func()
}

// Daemon is the running gateway.
type Daemon struct {
	cfgp    atomic.Pointer[config.Config] // replaced, never mutated, by reload
	opts    Options
	log     *slog.Logger
	events  *event.Bus
	store   *state.Store
	allow   *allow.List
	ctl     *ctl.Server
	started time.Time

	bus      *bluez.Bus
	agent    *agent.Agent
	window   *agent.Window
	adapter  *bluez.Adapter // nil while BlueZ is absent
	ppp      *ppp.Manager
	profiles *profile.Server

	mu        sync.Mutex
	present   bool
	regd      bool
	since     time.Time
	losses    int
	selection string
	gen       uint64 // bumped every time BlueZ (re)appears
	readyOnce sync.Once

	fwMu sync.Mutex
	fw   ctl.FirewallStatus

	// reattach asks the main loop to redo adapter and agent setup without
	// BlueZ having gone away (BlueZ released our agent while staying up).
	reattach chan struct{}
}

// config is the configuration in effect; reload swaps it (D-030).
func (d *Daemon) config() *config.Config { return d.cfgp.Load() }

// New wires everything up without touching the system.
func New(opts Options) (*Daemon, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	st, err := state.Open(opts.StatePath)
	if err != nil {
		return nil, err
	}
	if opts.DnsmasqConf == "" {
		opts.DnsmasqConf = defaultDnsmasqConf
	}
	if opts.DnsmasqUnit == "" {
		opts.DnsmasqUnit = defaultDnsmasqUnit
	}
	d := &Daemon{
		opts:     opts,
		log:      opts.Log.With("component", "daemon"),
		events:   event.New(),
		store:    st,
		started:  time.Now(),
		reattach: make(chan struct{}, 1),
	}
	d.cfgp.Store(opts.Config)
	d.allow = allow.New(opts.Config.Bluetooth.Allow, st, opts.Config.Bluetooth.AllowAny)
	c := opts.Config
	d.ppp, err = ppp.NewManager(ppp.Config{
		OptionsFile:  c.PPP.OptionsFile,
		Gateway:      c.Network.Gateway,
		PoolFirst:    c.Network.PPPPool[0],
		PoolLast:     c.Network.PPPPool[1],
		Debug:        c.PPP.Debug,
		IdleSeconds:  c.PPP.IdleSeconds,
		MaxSessions:  c.PPP.MaxSessions,
		PerDeviceMax: c.PPP.PerDeviceMax,
	}, opts.Log, d.events)
	if err != nil {
		return nil, err
	}
	d.window = agent.NewWindow(d.setPairable, func(reason string) {
		d.log.Info("pairing window closed", "event", "window_closed", "reason", reason)
		d.events.Emit("window_closed", "", "", "reason", reason)
	})
	d.ctl = ctl.NewServer(opts.Config.Ctl.Socket, opts.Config.Ctl.Group, opts.Log)
	d.registerHandlers()
	return d, nil
}

// Run serves until ctx is cancelled. The control socket comes up first so
// `palm-gwctl status` works even when BlueZ is missing.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.ctl.Listen(); err != nil {
		return err
	}
	go d.ctl.Serve(ctx)
	defer d.ctl.Close()

	bus, err := bluez.Connect(d.opts.Log)
	if err != nil {
		return err
	}
	d.bus = bus
	defer func() { _ = bus.Close() }()
	d.agent = agent.New(bus, d.window, d.allow, d.events, d.opts.Log, d.onAgentReleased)
	if err := d.agent.Export(); err != nil {
		return err
	}
	d.profiles = profile.New(bus, d.config(), d.ppp, d.allow, d.events, d.opts.Log, d.adapterPath)
	d.profiles.OnReleased = d.onProfileReleased
	if err := d.profiles.Export(); err != nil {
		return err
	}
	// Runs on every way out, including a stop while BlueZ is absent:
	// unregister from BlueZ if we can, and end every PPP session.
	defer d.shutdown()
	go d.ppp.RunReaper(ctx, 10*time.Second)
	// Network plumbing (section 8.7) does not depend on BlueZ.
	go d.keepFirewall(ctx)
	if _, err := d.syncDnsmasq(ctx, d.config()); err != nil {
		d.log.Error("dnsmasq configuration not written", "err", err)
	}

	// Log every event once, centrally, with its stable name.
	evCh, unsub := d.events.Subscribe()
	defer unsub()
	go func() {
		for e := range evCh {
			d.log.Debug("event", e.Attrs()...)
		}
	}()

	watch := bus.WatchBlueZ(ctx)
	for {
		if err := d.bus.WaitForBlueZ(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		d.setPresent(true)
		d.log.Info("BlueZ is present", "event", "bluez_found")
		d.events.Emit("bluez_found", "", "")

		lost := d.serveBlueZ(ctx, watch)
		if ctx.Err() != nil {
			return nil
		}
		if lost {
			d.setPresent(false)
			d.log.Warn("BlueZ went away", "event", "bluez_lost")
			d.events.Emit("bluez_lost", "", "")
			d.window.Close(agent.ReasonBlueZLost)
		}
	}
}

// serveBlueZ runs one BlueZ session: attach (retrying while BlueZ stays up),
// then wait until BlueZ goes away (returns true) or ctx ends (false).
func (d *Daemon) serveBlueZ(ctx context.Context, watch <-chan bool) bool {
	var stopDevWatch func()
	defer func() {
		if stopDevWatch != nil {
			stopDevWatch()
		}
	}()
	attached := false
	retry := time.NewTimer(0)
	defer retry.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case up, ok := <-watch:
			if !ok {
				return false
			}
			if !up {
				return true
			}
		case <-d.reattach:
			d.log.Warn("re-attaching to BlueZ after it released the agent or a profile")
			attached = false
			if stopDevWatch != nil {
				stopDevWatch()
				stopDevWatch = nil
			}
			retry.Reset(0)
		case <-retry.C:
			if attached {
				continue
			}
			stop, err := d.attach(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return false
				}
				// BlueZ answered the bus but refused us (adapter missing,
				// registration failed). Back off; status shows it.
				d.log.Error("attach to BlueZ failed; retrying in 3 s", "err", err)
				retry.Reset(3 * time.Second)
				continue
			}
			attached = true
			stopDevWatch = stop
		}
	}
}

// attach does lifecycle steps 3 and 4: adapter, properties, agent.
func (d *Daemon) attach(ctx context.Context) (func(), error) {
	var adapter *bluez.Adapter
	var how string
	// bluetoothd registers its adapters a moment after taking the bus name.
	deadline := time.Now().Add(30 * time.Second)
	for {
		a, h, err := d.bus.FindAdapter(d.config().Bluetooth.Adapter)
		if err == nil {
			adapter, how = a, h
			break
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		d.log.Debug("adapter not ready", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err := adapter.Configure(d.config().Bluetooth.Name); err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.adapter = adapter
	d.selection = how
	d.mu.Unlock()
	d.log.Info("adapter configured",
		"hci", adapter.HCI, "address", adapter.Props.Address, "alias", adapter.Props.Alias,
		"class", fmt.Sprintf("0x%06X", adapter.Props.Class), "hw_bus", adapter.HWBus,
		"modalias", adapter.Props.Modalias,
		"powered", adapter.Props.Powered, "discoverable", adapter.Props.Discoverable,
		"pairable", adapter.Props.Pairable, "selection", how)
	if adapter.Props.Class&0x001F00 != d.config().Bluetooth.Class&0x001F00 {
		d.log.Warn("adapter Class major/minor differs from config; is the main.conf block applied and bluetooth restarted?",
			"adapter_class", fmt.Sprintf("0x%06X", adapter.Props.Class), "config_class", fmt.Sprintf("0x%06X", d.config().Bluetooth.Class))
	}

	if err := d.agent.Register(); err != nil {
		return nil, err
	}
	if err := d.reassertPairable(adapter); err != nil {
		return nil, err
	}
	if err := d.profiles.Register(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.regd = true
	d.since = time.Now()
	d.mu.Unlock()

	stop := d.bus.OnDeviceChange(d.onDeviceChange)
	d.readyOnce.Do(func() {
		if d.opts.OnReady != nil {
			d.opts.OnReady()
		}
	})
	return stop, nil
}

// reassertPairable puts Pairable back to what the pairing window says.
// BlueZ turns bondable mode on whenever an agent registers
// (adapter_set_io_capability), which would leave the gateway pairable with no
// window open; observed on the Pi 5 with BlueZ 5.82 (DECISIONS.md D-013).
func (d *Daemon) reassertPairable(a *bluez.Adapter) error {
	want := d.window.IsOpen()
	if err := a.SetPairable(want); err != nil {
		return err
	}
	if err := a.Refresh(); err != nil {
		return err
	}
	if a.Props.Pairable != want {
		return fmt.Errorf("adapter %s: Pairable is %v after setting it to %v", a.HCI, a.Props.Pairable, want)
	}
	return nil
}

func (d *Daemon) shutdown() {
	d.window.Close(agent.ReasonCancelled)
	d.mu.Lock()
	present := d.present
	d.mu.Unlock()
	if present {
		d.profiles.Unregister()
		d.agent.Unregister()
	}
	d.ppp.Shutdown()
	d.log.Info("palm-gwd stopping")
}

func (d *Daemon) setPresent(p bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.present && !p {
		d.losses++
	}
	d.present = p
	if p {
		d.gen++
	} else {
		d.regd = false
		d.adapter = nil
		if d.profiles != nil {
			d.profiles.MarkUnregistered()
		}
	}
}

// adapterPath is the gateway adapter's object path, or "" without BlueZ.
func (d *Daemon) adapterPath() dbus.ObjectPath {
	if a := d.currentAdapter(); a != nil {
		return a.Path
	}
	return ""
}

func (d *Daemon) currentAdapter() *bluez.Adapter {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.adapter
}

func (d *Daemon) setPairable(on bool) error {
	a := d.currentAdapter()
	if a == nil {
		return errors.New("BlueZ is not running")
	}
	return a.SetPairable(on)
}

// onAgentReleased runs when BlueZ calls Agent1.Release. Normally that is
// bluetoothd shutting down and the main loop sees bluez_lost right after. If
// BlueZ is still there 3 s later in the same session, ask the loop to attach
// again. The generation check stops this from racing a restart.
func (d *Daemon) onAgentReleased() {
	d.mu.Lock()
	d.regd = false
	d.mu.Unlock()
	d.scheduleReattach(func() bool { return !d.regd })
}

// onProfileReleased is the same for Profile1.Release.
func (d *Daemon) onProfileReleased() {
	d.scheduleReattach(func() bool { return !d.profiles.Registered() })
}

// scheduleReattach asks the main loop to attach again in 3 s if BlueZ is
// still the same instance and stillNeeded (called with d.mu held) says so.
func (d *Daemon) scheduleReattach(stillNeeded func() bool) {
	d.mu.Lock()
	gen := d.gen
	d.mu.Unlock()
	go func() {
		time.Sleep(3 * time.Second)
		d.mu.Lock()
		same := d.present && d.gen == gen && stillNeeded()
		d.mu.Unlock()
		if !same {
			return
		}
		select {
		case d.reattach <- struct{}{}:
		default:
		}
	}()
}

// onDeviceChange watches Device1.Paired and Connected (PLAN.md section 8.2,
// "Pairing window").
func (d *Daemon) onDeviceChange(c bluez.DeviceChange) {
	// Other adapters on the host (a test dongle) are none of our business.
	if !bluez.UnderAdapter(c.Path, d.adapterPath()) {
		return
	}
	paired, flipped := c.Paired()
	connV, connChanged := c.Changed["Connected"]
	if !flipped && !connChanged {
		return
	}
	dev, err := d.bus.Device(c.Path)
	if err != nil {
		return
	}
	if flipped && paired {
		if d.window.IsOpen() {
			if err := d.allow.Add(dev.Address, dev.DisplayName()); err != nil {
				d.log.Error("allowlist update failed", "err", err)
			}
			if d.config().Bluetooth.TrustOnPair {
				if err := d.bus.SetTrusted(c.Path, true); err != nil {
					d.log.Warn("could not set Trusted", "addr", dev.Address, "err", err)
				}
			}
			d.agent.ClearPending(c.Path)
			d.log.Info("device paired", "event", "paired", "addr", dev.Address, "name", dev.DisplayName(), "trusted", d.config().Bluetooth.TrustOnPair)
			d.events.Emit("paired", dev.Address, dev.DisplayName())
			d.window.Paired(dev.Address)
		} else {
			// A re-pair of an already known device (JustWorksRepairing), or a
			// pairing BlueZ accepted without asking us. Not allowlisted.
			d.log.Warn("device paired outside a pairing window", "event", "paired_outside_window", "addr", dev.Address, "name", dev.DisplayName(), "allowed", d.allow.Allowed(dev.Address))
			d.events.Emit("paired_outside_window", dev.Address, dev.DisplayName())
			_ = d.store.Seen(dev.Address, dev.DisplayName())
		}
	}
	if connChanged {
		if connected, _ := connV.Value().(bool); connected {
			_ = d.store.Seen(dev.Address, dev.DisplayName())
			d.log.Info("device connected", "event", "device_connected", "addr", dev.Address, "name", dev.DisplayName())
		} else {
			d.log.Info("device disconnected", "event", "device_disconnected", "addr", dev.Address, "name", dev.DisplayName())
		}
	}
}
