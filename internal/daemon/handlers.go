package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"palm-gw/internal/agent"
	"palm-gw/internal/ctl"
	"palm-gw/internal/event"
	"palm-gw/internal/state"
)

func (d *Daemon) registerHandlers() {
	d.ctl.Handle("status", d.handleStatus)
	d.ctl.Handle("pair", d.handlePair)
	d.ctl.Handle("forget", d.handleForget)
	d.ctl.Handle("allow", d.handleAllow)
	d.ctl.Handle("disconnect", d.handleDisconnect)
	d.ctl.Handle("reload", d.handleReload)
	d.ctl.Handle("events", d.handleEvents)
	d.ctl.Handle("rename", d.handleRename)
	d.ctl.Handle("pair.close", d.handlePairClose)
	d.ctl.Handle("config.get", d.handleConfigGet)
	d.ctl.Handle("config.set", d.handleConfigSet)
	d.ctl.Handle("firewall.counters", d.handleFirewallCounters)
	d.ctl.Handle("firewall.test", d.handleFirewallTest)
	d.ctl.Handle("restart", d.handleRestart)
	d.ctl.Handle("choices", d.handleChoices)
	d.ctl.Handle("network.set", d.handleNetworkSet)
	d.ctl.Handle("nat", d.handleNAT)
}

// handleEvents streams every daemon event until the client disconnects
// (PLAN.md section 8.8). With recent or since it first sends past events from
// the bus's history, without gaps or repeats at the join.
func (d *Daemon) handleEvents(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	evCh, unsub := d.events.Subscribe()
	defer unsub()
	var last uint64
	if req.Recent > 0 || req.Since > 0 {
		for _, e := range d.events.Recent(req.Since, req.Recent) {
			if err := w.Send(e); err != nil {
				return nil
			}
			last = e.Seq
		}
	}
	if req.NoWait {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-evCh:
			if !ok {
				return nil
			}
			if e.Seq <= last {
				continue // already sent from the history
			}
			if err := w.Send(e); err != nil {
				return nil
			}
		}
	}
}

// handleRename sets the operator label of an allowlisted device (stored in
// state.json; an empty label clears it).
func (d *Daemon) handleRename(_ context.Context, req ctl.Request, w *ctl.Writer) error {
	addr := state.Norm(req.Addr)
	if addr == "" {
		return errors.New("rename: addr is required")
	}
	label := strings.TrimSpace(req.Label)
	if len(label) > 64 {
		return errors.New("rename: the label is longer than 64 characters")
	}
	if err := d.store.SetLabel(addr, label); err != nil {
		return err
	}
	d.log.Info("device renamed", "event", "renamed", "addr", addr, "label", label)
	d.events.Emit("renamed", addr, label)
	return w.Send(ctl.RenameReply{OK: true, Address: addr, Label: label})
}

// handlePairClose closes the pairing window, whoever opened it.
func (d *Daemon) handlePairClose(_ context.Context, _ ctl.Request, w *ctl.Writer) error {
	was := d.window.Close(agent.ReasonCancelled)
	if was {
		d.log.Info("pairing window closed by request", "event", "window_close_requested")
	}
	return w.Send(ctl.PairCloseReply{OK: true, WasOpen: was})
}

// handleReload re-reads config.toml and applies what can change live (D-030).
func (d *Daemon) handleReload(ctx context.Context, _ ctl.Request, w *ctl.Writer) error {
	r, err := d.reload(ctx)
	if err != nil {
		d.log.Warn("reload refused", "event", "reload_failed", "err", err)
		return err
	}
	return w.Send(r)
}

// handleDisconnect terminates the device's PPP sessions (SIGTERM to pppd's
// process group, SIGKILL after 2 s) and its PAN connection, and answers once
// they have ended.
func (d *Daemon) handleDisconnect(_ context.Context, req ctl.Request, w *ctl.Writer) error {
	addr := state.Norm(req.Addr)
	if addr == "" {
		return errors.New("disconnect: addr is required")
	}
	n := d.ppp.Disconnect(addr, "disconnect")
	panDropped := d.disconnectPAN(addr, "disconnect", true)
	d.log.Info("disconnect requested", "event", "disconnect", "addr", addr, "sessions", n, "pan", panDropped)
	return w.Send(ctl.DisconnectReply{OK: true, Address: addr, Sessions: n, PAN: panDropped})
}

func (d *Daemon) handleStatus(_ context.Context, _ ctl.Request, w *ctl.Writer) error {
	d.mu.Lock()
	st := ctl.StatusReply{
		Version:       d.opts.Version,
		Phase:         d.opts.Phase,
		StartedAt:     d.started,
		UptimeSeconds: int64(time.Since(d.started).Seconds()),
		ConfigPath:    d.opts.ConfigPath,
		StatePath:     d.opts.StatePath,
		Warnings:      d.opts.Warnings,
		BlueZ:         ctl.BlueZStatus{Present: d.present, AgentRegistered: d.regd, Since: d.since, Losses: d.losses},
		Config:        d.config().Redacted(),
	}
	adapter := d.adapter
	selection := d.selection
	d.mu.Unlock()
	d.fwMu.Lock()
	st.Firewall = d.fw
	d.fwMu.Unlock()
	st.PAN = d.panStatus()

	st.PairingWindow = d.window.State()
	st.Profiles = d.profiles.Status()
	st.Sessions = d.ppp.Sessions()
	for i := range st.Sessions {
		st.Sessions[i].RxBytes, st.Sessions[i].TxBytes = ifBytes(st.Sessions[i].Ifname)
	}
	st.PoolInUse = d.ppp.PoolInUse()
	st.Allowlist = d.allow.Entries()
	st.AllowAny = d.allow.AllowAny()
	if adapter != nil {
		if err := adapter.Refresh(); err == nil {
			st.Adapter = &ctl.AdapterStatus{HCI: adapter.HCI, Path: string(adapter.Path), Selection: selection, HWBus: adapter.HWBus, AdapterProps: adapter.Props}
		}
		if devs, err := adapter.Devices(); err == nil {
			st.Devices = devs
		}
	}
	return w.Send(st)
}

func toPairEvent(e event.Event) ctl.PairEvent {
	pe := ctl.PairEvent{Event: e.Event, Time: e.Time, Addr: e.Addr, Name: e.Name}
	pe.Reason = e.Fields["reason"]
	pe.Passkey = e.Fields["passkey"]
	pe.PIN = e.Fields["pin"]
	pe.UUID = e.Fields["uuid"]
	return pe
}

// handlePair opens a window and streams its events until it closes or the
// client disconnects (which cancels it).
func (d *Daemon) handlePair(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	minutes := req.Minutes
	if minutes <= 0 {
		minutes = d.config().Bluetooth.PairWindowDefaultMinutes
	}
	if d.currentAdapter() == nil {
		return errors.New("BlueZ is not running; cannot open a pairing window")
	}
	evCh, unsub := d.events.Subscribe()
	defer unsub()

	stateNow, err := d.window.Open(time.Duration(minutes)*time.Minute, req.One, d.config().Bluetooth.PINLength, d.config().Bluetooth.PIN)
	if err != nil {
		if errors.Is(err, agent.ErrAlreadyOpen) {
			return fmt.Errorf("a pairing window is already open (closes %s); cancel it first", stateNow.Expires.Format(time.Kitchen))
		}
		return err
	}
	d.log.Info("pairing window opened", "event", "window_open", "minutes", minutes, "one", req.One, "expires", stateNow.Expires)
	if err := w.Send(ctl.PairEvent{Event: "window_open", Time: time.Now(), PIN: stateNow.PIN, Expires: stateNow.Expires}); err != nil {
		d.window.Close(agent.ReasonCancelled)
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			d.window.Close(agent.ReasonCancelled)
			return nil
		case e, ok := <-evCh:
			if !ok {
				return nil
			}
			switch e.Event {
			case "pin_requested", "paired", "pairing_rejected", "display_pin", "passkey", "confirm", "window_closed", "pairing_cancelled":
			default:
				continue
			}
			if err := w.Send(toPairEvent(e)); err != nil {
				d.window.Close(agent.ReasonCancelled)
				return nil
			}
			if e.Event == "window_closed" {
				return nil
			}
		}
	}
}

// handleForget removes a device everywhere: allowlist, BlueZ (link key and
// trust), and its live session (PLAN.md section 8.2, "Trusted vs
// allowlist").
func (d *Daemon) handleForget(_ context.Context, req ctl.Request, w *ctl.Writer) error {
	addr := state.Norm(req.Addr)
	if addr == "" {
		return errors.New("forget: addr is required")
	}
	r := ctl.ForgetReply{OK: true, Address: addr}
	removed, isStatic, err := d.allow.Remove(addr)
	if err != nil {
		return err
	}
	r.RemovedAllow = removed
	r.StaticEntry = isStatic
	r.KilledSession = d.ppp.Disconnect(addr, "forgotten") > 0
	r.DroppedPAN = d.disconnectPAN(addr, "forgotten", false)
	if a := d.currentAdapter(); a != nil {
		dev, err := a.DeviceByAddress(addr)
		if err != nil {
			return err
		}
		if dev != nil {
			if err := a.RemoveDevice(dev.Path); err != nil {
				return err
			}
			r.RemovedBlueZ = true
		}
	}
	d.log.Info("device forgotten", "event", "forgotten", "addr", addr, "allowlist", removed, "bluez", r.RemovedBlueZ, "static_entry", isStatic)
	d.events.Emit("forgotten", addr, "")
	return w.Send(r)
}

// handleAllow adds an address to the dynamic allowlist without pairing.
func (d *Daemon) handleAllow(_ context.Context, req ctl.Request, w *ctl.Writer) error {
	addr := state.Norm(req.Addr)
	if addr == "" {
		return errors.New("allow: addr is required")
	}
	already := d.store.Has(addr)
	name := req.Name
	if name == "" {
		if a := d.currentAdapter(); a != nil {
			if dev, _ := a.DeviceByAddress(addr); dev != nil {
				name = dev.DisplayName()
			}
		}
	}
	if err := d.allow.Add(addr, name); err != nil {
		return err
	}
	d.log.Info("device allowlisted", "event", "allowed", "addr", addr, "name", name, "already", already)
	d.events.Emit("allowed", addr, name)
	return w.Send(ctl.AllowReply{OK: true, Address: addr, Added: !already})
}

// ifBytes reads an interface's byte counters (0, 0 if it has none yet).
func ifBytes(ifname string) (rx, tx uint64) {
	if ifname == "" || strings.ContainsAny(ifname, "/.") {
		return 0, 0
	}
	read := func(f string) uint64 {
		b, err := os.ReadFile("/sys/class/net/" + ifname + "/statistics/" + f)
		if err != nil {
			return 0
		}
		n, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		return n
	}
	return read("rx_bytes"), read("tx_bytes")
}
