package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"palm-gw/internal/ctl"
	"palm-gw/internal/nm"
	"palm-gw/internal/setup"
)

// The internet connection wizard (D-056): Ethernet or Wi-Fi as the uplink,
// and which Wi-Fi network, through NetworkManager. Switching the uplink goes
// through setNetwork, so the firewall's masquerade follows it.

// NetManager is what the wizard needs from NetworkManager; nm.Client is
// the real one, tests use a fake.
type NetManager interface {
	Devices() ([]nm.Device, error)
	WifiEnabled() (on, hardware bool, err error)
	SetWifiEnabled(on bool) error
	Scan(ctx context.Context, rescan bool) ([]nm.Network, error)
	Saved() ([]string, error)
	Join(ctx context.Context, ssid, password string, hidden bool) error
	Prefer(kind string) error
	Forget(ssid string) error
	Close()
}

func (d *Daemon) netManager() (NetManager, error) {
	if d.opts.NetManager != nil {
		return d.opts.NetManager()
	}
	c, err := nm.Connect()
	if err != nil {
		return nil, err
	}
	return c, nil
}

func firstDevice(ds []nm.Device, typ string) (nm.Device, bool) {
	for _, d := range ds {
		if d.Type == typ {
			return d, true
		}
	}
	return nm.Device{}, false
}

func (d *Daemon) wifiJobCopy() *ctl.WifiJob {
	d.wifiMu.Lock()
	defer d.wifiMu.Unlock()
	if d.wifiJob == nil {
		return nil
	}
	j := *d.wifiJob
	return &j
}

func (d *Daemon) joining() bool {
	j := d.wifiJobCopy()
	return j != nil && !j.Done
}

// handleInternet answers internet: the devices, Wi-Fi radio, saved networks
// and any join in progress.
func (d *Daemon) handleInternet(ctx context.Context, _ ctl.Request, w *ctl.Writer) error {
	r := ctl.InternetReply{Uplink: d.config().Network.Uplink, Devices: []nm.Device{}, Saved: []string{}, Job: d.wifiJobCopy(), Applying: d.applyRunning(ctx)}
	d.mu.Lock()
	r.OnboardBT = d.adapter != nil && d.adapter.HWBus != "usb"
	d.mu.Unlock()
	c, err := d.netManager()
	if err != nil {
		r.Error = err.Error()
		return w.Send(r)
	}
	defer c.Close()
	if ds, err := c.Devices(); err == nil {
		r.Devices = ds
	} else {
		r.Error = err.Error()
	}
	r.WifiOn, r.WifiHW, _ = c.WifiEnabled()
	if s, err := c.Saved(); err == nil && s != nil {
		r.Saved = s
	}
	return w.Send(r)
}

func (d *Daemon) handleWifiScan(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	c, err := d.netManager()
	if err != nil {
		return err
	}
	defer c.Close()
	ns, err := c.Scan(ctx, req.WiFi != nil && req.WiFi.Rescan)
	if err != nil {
		return err
	}
	if ns == nil {
		ns = []nm.Network{}
	}
	return w.Send(ctl.WifiScanReply{Networks: ns})
}

// handleWifiJoin starts a join and answers at once; internet reports how it
// goes. The join runs on its own context, so it finishes (or rolls back)
// even when the browser that asked loses the Pi.
func (d *Daemon) handleWifiJoin(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	if req.WiFi == nil {
		return errors.New("wifi.join: which network?")
	}
	ssid, err := nm.Trim(req.WiFi.SSID)
	if err != nil {
		return err
	}
	if d.applyRunning(ctx) {
		return errors.New("network settings are being applied now; try again when palm-gwd is back")
	}
	d.wifiMu.Lock()
	if d.wifiJob != nil && !d.wifiJob.Done {
		busy := d.wifiJob.SSID
		d.wifiMu.Unlock()
		return fmt.Errorf("already joining %q", busy)
	}
	d.wifiJob = &ctl.WifiJob{SSID: ssid, Started: time.Now()}
	d.wifiMu.Unlock()
	d.log.Info("joining Wi-Fi", "event", "wifi_joining", "ssid", ssid, "hidden", req.WiFi.Hidden)
	go d.runJoin(ssid, req.WiFi.Password, req.WiFi.Hidden)
	return w.Send(ctl.OKReply{OK: true})
}

func (d *Daemon) runJoin(ssid, password string, hidden bool) {
	ctx, cancel := context.WithTimeout(context.Background(), nm.JoinTimeout+30*time.Second)
	defer cancel()
	finish := func(ok, switching bool, err error) {
		d.wifiMu.Lock()
		defer d.wifiMu.Unlock()
		d.wifiJob.Done, d.wifiJob.OK, d.wifiJob.Switching = true, ok, switching
		if err != nil {
			d.wifiJob.Error = err.Error()
		}
	}
	c, err := d.netManager()
	if err != nil {
		finish(false, false, err)
		return
	}
	defer c.Close()
	if err := c.Join(ctx, ssid, password, hidden); err != nil {
		d.log.Warn("Wi-Fi join failed", "event", "wifi_join_failed", "ssid", ssid, "err", err)
		finish(false, false, err)
		return
	}
	d.log.Info("Wi-Fi joined", "event", "wifi_joined", "ssid", ssid)
	ds, err := c.Devices()
	if err != nil {
		finish(true, false, fmt.Errorf("joined, but: %w", err))
		return
	}
	wifi, _ := firstDevice(ds, "wifi")
	if wifi.Iface == "" || wifi.Iface == d.config().Network.Uplink {
		finish(true, false, nil)
		return
	}
	rep, err := d.setNetwork(ctx, setup.NetworkChange{Uplink: wifi.Iface})
	if err != nil {
		err = fmt.Errorf("joined %q, but the internet connection could not be switched to Wi-Fi: %w", ssid, err)
	}
	finish(err == nil, rep.Applying, err)
}

// handleInternetUse makes Ethernet or the connected Wi-Fi network the
// internet connection: the route preference first, then the uplink.
func (d *Daemon) handleInternetUse(ctx context.Context, req ctl.Request, w *ctl.Writer) error {
	if req.WiFi == nil || (req.WiFi.Use != "ethernet" && req.WiFi.Use != "wifi") {
		return errors.New("internet.use: ethernet or wifi")
	}
	if d.joining() {
		return errors.New("a Wi-Fi network is being joined; wait for it")
	}
	if d.applyRunning(ctx) {
		return errors.New("network settings are being applied now; try again when palm-gwd is back")
	}
	kind := req.WiFi.Use
	c, err := d.netManager()
	if err != nil {
		return err
	}
	defer c.Close()
	ds, err := c.Devices()
	if err != nil {
		return err
	}
	dev, ok := firstDevice(ds, kind)
	switch {
	case !ok && kind == "ethernet":
		return errors.New("this Pi has no Ethernet port that NetworkManager manages")
	case !ok:
		return nm.ErrNoWifi
	case kind == "ethernet" && !dev.Carrier:
		return fmt.Errorf("no cable is plugged into %s, or the other end is off", dev.Iface)
	case kind == "ethernet" && !dev.Connected:
		return fmt.Errorf("%s has a cable but no network address yet (is there a DHCP server?)", dev.Iface)
	case kind == "wifi" && !dev.Connected:
		return errors.New("Wi-Fi is not connected to a network: join one first")
	}
	if err := c.Prefer(kind); err != nil {
		return err
	}
	r := ctl.InternetUseReply{OK: true, Iface: dev.Iface}
	if dev.Iface != d.config().Network.Uplink {
		rep, err := d.setNetwork(ctx, setup.NetworkChange{Uplink: dev.Iface})
		if err != nil {
			return err
		}
		r.Switching, r.Changed = rep.Applying, rep.Changed
	}
	d.log.Info("internet connection chosen", "event", "internet_use", "iface", dev.Iface, "switching", r.Switching)
	return w.Send(r)
}

// usingWifi reports whether the uplink is the Wi-Fi interface, and which.
func usingWifi(ds []nm.Device, uplink string) (nm.Device, bool) {
	wifi, ok := firstDevice(ds, "wifi")
	return wifi, ok && wifi.Iface == uplink
}

func (d *Daemon) handleWifiForget(_ context.Context, req ctl.Request, w *ctl.Writer) error {
	if req.WiFi == nil || strings.TrimSpace(req.WiFi.SSID) == "" {
		return errors.New("wifi.forget: which network?")
	}
	c, err := d.netManager()
	if err != nil {
		return err
	}
	defer c.Close()
	ds, err := c.Devices()
	if err != nil {
		return err
	}
	if wifi, in := usingWifi(ds, d.config().Network.Uplink); in && wifi.Connected && wifi.Connection == req.WiFi.SSID {
		return errors.New("the Pi reaches the internet through this network: switch to Ethernet or another network first")
	}
	if err := c.Forget(req.WiFi.SSID); err != nil {
		return err
	}
	d.log.Info("Wi-Fi network forgotten", "event", "wifi_forgotten", "ssid", req.WiFi.SSID)
	return w.Send(ctl.OKReply{OK: true})
}

func (d *Daemon) handleWifiRadio(_ context.Context, req ctl.Request, w *ctl.Writer) error {
	if req.WiFi == nil || req.WiFi.On == nil {
		return errors.New("wifi.radio: on or off")
	}
	c, err := d.netManager()
	if err != nil {
		return err
	}
	defer c.Close()
	if !*req.WiFi.On {
		ds, err := c.Devices()
		if err != nil {
			return err
		}
		if _, in := usingWifi(ds, d.config().Network.Uplink); in {
			return errors.New("the Pi reaches the internet through Wi-Fi: switch to Ethernet first")
		}
	}
	if err := c.SetWifiEnabled(*req.WiFi.On); err != nil {
		return err
	}
	d.log.Info("Wi-Fi radio switched", "event", "wifi_radio", "on", *req.WiFi.On)
	return w.Send(ctl.OKReply{OK: true})
}
