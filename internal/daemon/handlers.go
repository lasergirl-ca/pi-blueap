package daemon

import (
	"context"
	"errors"
	"fmt"
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
	d.ctl.Handle("disconnect", func(context.Context, ctl.Request, *ctl.Writer) error {
		return errors.New("disconnect: not implemented until Phase 2")
	})
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
		Config:        d.cfg.Redacted(),
	}
	adapter := d.adapter
	selection := d.selection
	d.mu.Unlock()

	st.PairingWindow = d.window.State()
	st.Allowlist = d.allow.Entries()
	st.AllowAny = d.allow.AllowAny()
	if adapter != nil {
		if err := adapter.Refresh(); err == nil {
			st.Adapter = &ctl.AdapterStatus{HCI: adapter.HCI, Path: string(adapter.Path), Selection: selection, AdapterProps: adapter.Props}
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
		minutes = d.cfg.Bluetooth.PairWindowDefaultMinutes
	}
	if d.currentAdapter() == nil {
		return errors.New("BlueZ is not running; cannot open a pairing window")
	}
	evCh, unsub := d.events.Subscribe()
	defer unsub()

	stateNow, err := d.window.Open(time.Duration(minutes)*time.Minute, req.One, d.cfg.Bluetooth.PINLength, d.cfg.Bluetooth.PIN)
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
// trust), and later its live session (PLAN.md section 8.2, "Trusted vs
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
