package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/mm"
	"palm-gw/internal/setup"
)

// The 4G uplink from the daemon's side (D-074, docs/MOBILE.md): the modem's state
// for status, mobile.set and mobile.pin (which save [mobile] and start the same
// apply as the network settings), the backup role's failover, and the month's
// data. NetworkManager owns the modem's connection and ModemManager the modem;
// the daemon reads them, and in the backup role moves the modem's route in front
// of the uplink's while the uplink has no internet.

const (
	mobileTick        = 30 * time.Second
	mobileFailAfter   = 3               // failed checks in a row before the modem takes over
	mobileRestoreHold = 2 * time.Minute // the uplink answering this long hands back
	mobileMetricFront = 50              // the modem's route metric while it carries the internet in backup
	mobileDataFile    = "mobile-data.json"
)

// mobileWatch is the failover's and the data count's memory between checks.
type mobileWatch struct {
	mu         sync.Mutex
	fails      int
	okSince    time.Time
	failedOver bool
	uplinkOK   bool
	data       mobileData
	loaded     bool
}

// mobileData is the month's 4G traffic, kept across restarts.
type mobileData struct {
	Month  string `json:"month"` // 2026-11
	RX     uint64 `json:"rx"`
	TX     uint64 `json:"tx"`
	LastRX uint64 `json:"last_rx"` // the interface's counters at the last reading
	LastTX uint64 `json:"last_tx"`
	Warned bool   `json:"warned"`
}

func (d *Daemon) mob() *mobileWatch {
	d.mobOnce.Do(func() { d.mobw = &mobileWatch{} })
	return d.mobw
}

func (d *Daemon) ifbytes(iface string) (uint64, uint64) {
	if d.opts.IfBytes != nil {
		return d.opts.IfBytes(iface)
	}
	return ifBytes(iface)
}

func (d *Daemon) readModem() (mm.Modem, error) {
	if d.opts.ReadModem != nil {
		return d.opts.ReadModem()
	}
	return mm.Read()
}

// probeUplink asks whether the internet answers through iface: a TCP connection to
// check on port 53, then 443, bound to the interface.
func (d *Daemon) probeUplink(ctx context.Context, iface, check string) error {
	if d.opts.Probe != nil {
		return d.opts.Probe(ctx, iface, check)
	}
	dialer := net.Dialer{Timeout: 4 * time.Second, Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = bindToDevice(int(fd), iface) }); err != nil {
			return err
		}
		return serr
	}}
	var last error
	for _, port := range []string{"53", "443"} {
		conn, err := dialer.DialContext(ctx, "tcp4", net.JoinHostPort(check, port))
		if err == nil {
			_ = conn.Close()
			return nil
		}
		last = err
	}
	return last
}

// setModemMetric moves the modem's routes, for now only (NetworkManager's runtime
// change; the profile keeps its own metric for the next boot).
func (d *Daemon) setModemMetric(ctx context.Context, iface string, metric int) error {
	m := strconv.Itoa(metric)
	out, err := d.command(ctx, "nmcli", "device", "modify", iface, "ipv4.route-metric", m, "ipv6.route-metric", m)
	if err != nil {
		return fmt.Errorf("nmcli device modify %s: %w: %s", iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// mobileStep is one check of the backup role: probe the uplink, fail over after
// mobileFailAfter failures, hand back after mobileRestoreHold of answers.
func (d *Daemon) mobileStep(ctx context.Context, cfg *config.Config, now time.Time) {
	w := d.mob()
	m := cfg.Mobile
	if !m.Enabled || m.Role != "backup" {
		w.mu.Lock()
		was := w.failedOver
		w.failedOver, w.fails, w.okSince, w.uplinkOK = false, 0, time.Time{}, false
		w.mu.Unlock()
		if was && m.Enabled {
			_ = d.setModemMetric(ctx, m.Interface, setup.MobileMetric(m))
		}
		return
	}
	err := d.probeUplink(ctx, cfg.Network.Uplink, m.Check)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.uplinkOK = err == nil
	if err != nil {
		w.fails++
		w.okSince = time.Time{}
		if !w.failedOver && w.fails >= mobileFailAfter {
			if merr := d.setModemMetric(ctx, m.Interface, mobileMetricFront); merr != nil {
				d.log.Error("4G failover failed", "event", "uplink_failover_error", "err", merr)
				return
			}
			w.failedOver = true
			d.log.Warn("the uplink lost the internet; 4G takes over", "event", "uplink_failover",
				"uplink", cfg.Network.Uplink, "modem", m.Interface, "check", m.Check, "err", err.Error())
			d.events.Emit("uplink_failover", "", "")
		}
		return
	}
	w.fails = 0
	if w.okSince.IsZero() {
		w.okSince = now
	}
	if w.failedOver && now.Sub(w.okSince) >= mobileRestoreHold {
		if merr := d.setModemMetric(ctx, m.Interface, setup.MobileMetric(m)); merr != nil {
			d.log.Error("4G hand-back failed", "event", "uplink_restore_error", "err", merr)
			return
		}
		w.failedOver = false
		d.log.Info("the uplink has the internet again; 4G hands back", "event", "uplink_restored",
			"uplink", cfg.Network.Uplink, "modem", m.Interface)
		d.events.Emit("uplink_restored", "", "")
	}
}

func (d *Daemon) mobileDataPath() string {
	return filepath.Join(filepath.Dir(d.opts.StatePath), mobileDataFile)
}

// countMobile adds what the modem's interface moved since the last reading to the
// month, and warns once a month passes data_warning_mb. save is whether to write
// the count to disk now.
func (d *Daemon) countMobile(cfg *config.Config, now time.Time, save bool) {
	w := d.mob()
	m := cfg.Mobile
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.loaded {
		if b, err := os.ReadFile(d.mobileDataPath()); err == nil {
			_ = json.Unmarshal(b, &w.data)
		}
		w.loaded = true
	}
	month := now.Format("2006-01")
	if w.data.Month != month {
		w.data = mobileData{Month: month, LastRX: w.data.LastRX, LastTX: w.data.LastTX}
	}
	rx, tx := d.ifbytes(m.Interface)
	add := func(cur uint64, last *uint64, total *uint64) {
		if cur >= *last {
			*total += cur - *last
		} else { // the interface came back with fresh counters
			*total += cur
		}
		*last = cur
	}
	add(rx, &w.data.LastRX, &w.data.RX)
	add(tx, &w.data.LastTX, &w.data.TX)
	if m.DataWarningMB > 0 && !w.data.Warned && (w.data.RX+w.data.TX)/(1<<20) >= uint64(m.DataWarningMB) {
		w.data.Warned = true
		d.log.Warn("this month's 4G data passed the warning", "event", "wan_data_warning",
			"mb", (w.data.RX+w.data.TX)/(1<<20), "warning_mb", m.DataWarningMB)
		d.events.Emit("wan_data_warning", "", "")
	}
	if save {
		if b, err := json.Marshal(w.data); err == nil {
			_ = os.WriteFile(d.mobileDataPath(), b, 0o640)
		}
	}
}

// keepMobile runs the checks every mobileTick while [mobile] is on; the data count
// is saved every 5 minutes.
func (d *Daemon) keepMobile(ctx context.Context) {
	t := time.NewTicker(mobileTick)
	defer t.Stop()
	for n := 0; ; n++ {
		cfg := d.config()
		if cfg.Mobile.Enabled {
			d.mobileStep(ctx, cfg, time.Now())
			d.countMobile(cfg, time.Now(), n%10 == 0)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func ifaceIPv4(iface string) string {
	i, err := net.InterfaceByName(iface)
	if err != nil {
		return ""
	}
	addrs, _ := i.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	return ""
}

// mobileStatus is the 4G uplink for status and mobile.show.
func (d *Daemon) mobileStatus(ctx context.Context) *ctl.MobileStatus {
	cfg := d.config()
	m := cfg.Mobile
	st := &ctl.MobileStatus{Enabled: m.Enabled, Role: m.Role, Interface: m.Interface, RouterMode: setup.RouterMode(m),
		APN: m.APN, RoamingOK: m.Roaming, WarningMB: m.DataWarningMB, Applying: d.applyRunning(ctx)}
	if d.opts.IfaceIP != nil {
		st.IP = d.opts.IfaceIP(m.Interface)
	} else {
		st.IP = ifaceIPv4(m.Interface)
	}
	st.Up = st.IP != ""
	w := d.mob()
	w.mu.Lock()
	st.FailedOver, st.UplinkOK = w.failedOver, w.uplinkOK
	st.MonthRxBytes, st.MonthTxBytes = w.data.RX, w.data.TX
	w.mu.Unlock()
	st.Active = m.Enabled && st.Up && (m.Role == "only" || st.FailedOver)
	if !st.RouterMode {
		mo, err := d.readModem()
		switch {
		case errors.Is(err, mm.ErrNoModem):
			st.Problem = "no modem: is it plugged in?"
		case err != nil:
			st.Problem = err.Error()
		default:
			st.Modem = true
			st.State, st.Locked, st.Operator, st.Tech, st.Signal, st.Roaming = mo.State, mo.Locked, mo.Operator, mo.Tech, mo.Signal, mo.Roaming
		}
	}
	return st
}

func (d *Daemon) handleMobile(ctx context.Context, _ ctl.Request, w *ctl.Writer) error {
	return w.Send(d.mobileStatus(ctx))
}

// otherUplink is the uplink to use when the modem stops being it: the best
// default route that is not the modem's, else eth0.
func (d *Daemon) otherUplink(modem string) string {
	if ups, err := d.host().DetectUplinks(); err == nil {
		for _, u := range ups {
			if u.Iface != modem && !strings.HasPrefix(u.Iface, "ppp") && !strings.HasPrefix(u.Iface, "wg") {
				return u.Iface
			}
		}
	}
	if d.host().HasIface("eth0") {
		return "eth0"
	}
	return "wlan0"
}

// handleMobileSet turns 4G on or off, sets its role, APN or roaming, and applies.
// "only" makes the modem network.uplink; leaving "only" (or turning 4G off from
// it) puts the uplink back on the Pi's other connection.
func (d *Daemon) handleMobileSet(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	r := req.Mobile
	if r == nil {
		return errors.New("mobile.set: nothing to change")
	}
	if err := d.lockForApply(ctx); err != nil {
		return err
	}
	defer d.cfgEditMu.Unlock()
	path := d.opts.ConfigPath
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cur, err := config.Parse(string(old))
	if err != nil {
		return err
	}
	m := cur.Mobile
	var edits []setup.KV
	kv := func(key, lit string) { edits = append(edits, setup.KV{Section: "mobile", Key: key, Value: lit}) }
	if r.Role != "" {
		kv("role", setup.TOMLString(r.Role))
		m.Role = r.Role
	}
	if r.APN != nil {
		kv("apn", setup.TOMLString(strings.TrimSpace(*r.APN)))
	}
	if r.Roaming != nil {
		kv("roaming", fmt.Sprint(*r.Roaming))
	}
	if r.Enabled != nil {
		kv("enabled", fmt.Sprint(*r.Enabled))
		m.Enabled = *r.Enabled
	}
	if len(edits) == 0 {
		return errors.New("mobile.set: nothing to change")
	}
	// The uplink follows the role.
	switch {
	case m.Enabled && m.Role == "only" && cur.Network.Uplink != m.Interface:
		edits = append(edits, setup.KV{Section: "network", Key: "uplink", Value: setup.TOMLString(m.Interface)})
	case (!m.Enabled || m.Role == "backup") && cur.Network.Uplink == m.Interface:
		edits = append(edits, setup.KV{Section: "network", Key: "uplink", Value: setup.TOMLString(d.otherUplink(m.Interface))})
	}
	text := setup.SetKeys(string(old), edits)
	if text == string(old) {
		return w.Send(ctl.MobileSetReply{OK: true, Changed: []string{}})
	}
	cfg, err := config.Parse(text)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		return fmt.Errorf("not saved: %w", err)
	}
	if err := writeAtomic(path, []byte(text)); err != nil {
		return err
	}
	var changed []string
	for _, e := range edits {
		changed = append(changed, e.Section+"."+e.Key)
	}
	if out, err := d.startApply(ctx); err != nil {
		if rerr := writeAtomic(path, old); rerr != nil {
			return fmt.Errorf("could not start palm-gw setup --apply (%w: %s), and could not put config.toml back: %w", err, strings.TrimSpace(string(out)), rerr)
		}
		return fmt.Errorf("not saved: could not start palm-gw setup --apply: %w: %s", err, strings.TrimSpace(string(out)))
	}
	d.log.Info("4G settings saved; applying", "event", "mobile_set", "keys", changed, "unit", applyUnit)
	return w.Send(ctl.MobileSetReply{OK: true, Changed: changed, Applying: true, Unit: applyUnit})
}

// handleMobilePIN keeps a SIM PIN for the apply, which writes it into the modem's
// connection, and starts the apply.
func (d *Daemon) handleMobilePIN(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	if req.Mobile == nil || req.Mobile.PIN == "" {
		return errors.New("mobile.pin: no PIN")
	}
	if err := d.lockForApply(ctx); err != nil {
		return err
	}
	defer d.cfgEditMu.Unlock()
	if err := setup.StashMobilePIN("", filepath.Dir(d.opts.StatePath), req.Mobile.PIN); err != nil {
		return err
	}
	if out, err := d.startApply(ctx); err != nil {
		return fmt.Errorf("the PIN is kept, but palm-gw setup --apply did not start: %w: %s", err, strings.TrimSpace(string(out)))
	}
	d.log.Info("SIM PIN set; applying", "event", "mobile_pin", "unit", applyUnit)
	return w.Send(ctl.MobileSetReply{OK: true, Changed: []string{"mobile.pin"}, Applying: true, Unit: applyUnit})
}
