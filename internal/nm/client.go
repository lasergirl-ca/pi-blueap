package nm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	service      = "org.freedesktop.NetworkManager"
	rootPath     = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	settingsPath = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings")
	ifaceDevice  = service + ".Device"
	ifaceWired   = service + ".Device.Wired"
	ifaceWifi    = service + ".Device.Wireless"
	ifaceAP      = service + ".AccessPoint"
	ifaceActive  = service + ".Connection.Active"
	ifaceIP4     = service + ".IP4Config"
	ifaceSetConn = service + ".Settings.Connection"
)

// ErrNoWifi means NetworkManager manages no Wi-Fi interface.
var ErrNoWifi = errors.New("this Pi has no Wi-Fi interface that NetworkManager manages")

// JoinTimeout is how long a join may take before it counts as failed and
// the previous network comes back.
const JoinTimeout = 45 * time.Second

// Client talks to NetworkManager on the system bus.
type Client struct {
	conn *dbus.Conn
}

// Connect opens a private system bus connection and checks that
// NetworkManager is running.
func Connect() (*Client, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("NetworkManager: system bus: %w", err)
	}
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, service).Store(&has); err != nil || !has {
		_ = conn.Close()
		return nil, errors.New("NetworkManager is not running")
	}
	return &Client{conn: conn}, nil
}

// Close closes the bus connection.
func (c *Client) Close() { _ = c.conn.Close() }

func (c *Client) obj(p dbus.ObjectPath) dbus.BusObject { return c.conn.Object(service, p) }

func (c *Client) prop(p dbus.ObjectPath, iface, name string) (dbus.Variant, error) {
	return c.obj(p).GetProperty(iface + "." + name)
}

func propAs[T any](c *Client, p dbus.ObjectPath, iface, name string) (T, error) {
	var zero T
	v, err := c.prop(p, iface, name)
	if err != nil {
		return zero, err
	}
	t, ok := v.Value().(T)
	if !ok {
		return zero, fmt.Errorf("NetworkManager: %s.%s is %s", iface, name, v.Signature())
	}
	return t, nil
}

// Devices lists the Ethernet and Wi-Fi interfaces.
func (c *Client) Devices() ([]Device, error) {
	var paths []dbus.ObjectPath
	if err := c.obj(rootPath).Call(service+".GetDevices", 0).Store(&paths); err != nil {
		return nil, fmt.Errorf("NetworkManager: %w", err)
	}
	var out []Device
	for _, p := range paths {
		typ, err := propAs[uint32](c, p, ifaceDevice, "DeviceType")
		if err != nil || (typ != 1 && typ != 2) {
			continue
		}
		d := Device{Type: "ethernet", path: p, Metric: -1}
		if typ == 2 {
			d.Type = "wifi"
		}
		d.Iface, _ = propAs[string](c, p, ifaceDevice, "Interface")
		st, _ := propAs[uint32](c, p, ifaceDevice, "State")
		d.State, d.Connected = deviceStates[st], st == 100
		if typ == 1 {
			d.Carrier, _ = propAs[bool](c, p, ifaceWired, "Carrier")
		}
		if ac, err := propAs[dbus.ObjectPath](c, p, ifaceDevice, "ActiveConnection"); err == nil && ac != "/" {
			d.Connection, _ = propAs[string](c, ac, ifaceActive, "Id")
			if sp, err := propAs[dbus.ObjectPath](c, ac, ifaceActive, "Connection"); err == nil {
				if s, err := c.settings(sp); err == nil {
					d.Metric = metricOf(s)
				}
			}
		}
		if ip4, err := propAs[dbus.ObjectPath](c, p, ifaceDevice, "Ip4Config"); err == nil && ip4 != "/" {
			if ad, err := propAs[[]map[string]dbus.Variant](c, ip4, ifaceIP4, "AddressData"); err == nil && len(ad) > 0 {
				addr, _ := ad[0]["address"].Value().(string)
				prefix, _ := ad[0]["prefix"].Value().(uint32)
				if addr != "" {
					d.IP = fmt.Sprintf("%s/%d", addr, prefix)
				}
			}
			d.Gateway, _ = propAs[string](c, ip4, ifaceIP4, "Gateway")
		}
		out = append(out, d)
	}
	return out, nil
}

func metricOf(s map[string]map[string]dbus.Variant) int64 {
	if m, ok := s["ipv4"]["route-metric"].Value().(int64); ok {
		return m
	}
	return -1
}

func (c *Client) wifiDevice() (Device, error) {
	ds, err := c.Devices()
	if err != nil {
		return Device{}, err
	}
	for _, d := range ds {
		if d.Type == "wifi" {
			return d, nil
		}
	}
	return Device{}, ErrNoWifi
}

// WifiEnabled reports the Wi-Fi radio switch and the hardware (rfkill) switch.
func (c *Client) WifiEnabled() (on, hardware bool, err error) {
	if on, err = propAs[bool](c, rootPath, service, "WirelessEnabled"); err != nil {
		return false, false, err
	}
	hardware, err = propAs[bool](c, rootPath, service, "WirelessHardwareEnabled")
	return on, hardware, err
}

// SetWifiEnabled turns the Wi-Fi radio on or off.
func (c *Client) SetWifiEnabled(on bool) error {
	return c.obj(rootPath).SetProperty(service+".WirelessEnabled", dbus.MakeVariant(on))
}

func (c *Client) accessPoints(dev dbus.ObjectPath) ([]apInfo, error) {
	var paths []dbus.ObjectPath
	if err := c.obj(dev).Call(ifaceWifi+".GetAllAccessPoints", 0).Store(&paths); err != nil {
		return nil, fmt.Errorf("NetworkManager: %w", err)
	}
	var out []apInfo
	for _, p := range paths {
		a := apInfo{path: p}
		ssid, err := propAs[[]byte](c, p, ifaceAP, "Ssid")
		if err != nil {
			continue // gone between the list and the read
		}
		a.ssid = string(ssid)
		s, _ := propAs[byte](c, p, ifaceAP, "Strength")
		a.strength = int(s)
		f, _ := propAs[uint32](c, p, ifaceAP, "Frequency")
		a.freq = int(f)
		a.flags, _ = propAs[uint32](c, p, ifaceAP, "Flags")
		a.wpa, _ = propAs[uint32](c, p, ifaceAP, "WpaFlags")
		a.rsn, _ = propAs[uint32](c, p, ifaceAP, "RsnFlags")
		out = append(out, a)
	}
	return out, nil
}

// Scan lists the Wi-Fi networks in range. With rescan it asks for a fresh
// scan first and waits up to 8 s for it; otherwise NetworkManager's last
// results are used.
func (c *Client) Scan(ctx context.Context, rescan bool) ([]Network, error) {
	dev, err := c.wifiDevice()
	if err != nil {
		return nil, err
	}
	if rescan {
		before, _ := propAs[int64](c, dev.path, ifaceWifi, "LastScan")
		if err := c.obj(dev.path).Call(ifaceWifi+".RequestScan", 0, map[string]dbus.Variant{}).Err; err == nil {
			ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			for ctx.Err() == nil {
				if now, _ := propAs[int64](c, dev.path, ifaceWifi, "LastScan"); now != before {
					break
				}
				select {
				case <-ctx.Done():
				case <-time.After(300 * time.Millisecond):
				}
			}
		} // a scan refused (one ran a moment ago) still leaves results
	}
	aps, err := c.accessPoints(dev.path)
	if err != nil {
		return nil, err
	}
	saved := map[string]bool{}
	ws, err := c.savedWifi()
	if err != nil {
		return nil, err
	}
	for _, w := range ws {
		saved[w.ssid] = true
	}
	active := ""
	if dev.Connected {
		if ap, err := propAs[dbus.ObjectPath](c, dev.path, ifaceWifi, "ActiveAccessPoint"); err == nil && ap != "/" {
			if ssid, err := propAs[[]byte](c, ap, ifaceAP, "Ssid"); err == nil {
				active = string(ssid)
			}
		}
	}
	return networks(aps, saved, active), nil
}

type savedConn struct {
	path dbus.ObjectPath
	ssid string
	s    map[string]map[string]dbus.Variant
}

func (c *Client) settings(p dbus.ObjectPath) (map[string]map[string]dbus.Variant, error) {
	var s map[string]map[string]dbus.Variant
	err := c.obj(p).Call(ifaceSetConn+".GetSettings", 0).Store(&s)
	return s, err
}

func (c *Client) savedWifi() ([]savedConn, error) {
	var paths []dbus.ObjectPath
	if err := c.obj(settingsPath).Call(service+".Settings.ListConnections", 0).Store(&paths); err != nil {
		return nil, fmt.Errorf("NetworkManager: %w", err)
	}
	var out []savedConn
	for _, p := range paths {
		s, err := c.settings(p)
		if err != nil {
			continue
		}
		if t, _ := s["connection"]["type"].Value().(string); t != "802-11-wireless" {
			continue
		}
		ssid, _ := s["802-11-wireless"]["ssid"].Value().([]byte)
		out = append(out, savedConn{path: p, ssid: string(ssid), s: s})
	}
	return out, nil
}

// Saved lists the SSIDs of the saved Wi-Fi networks.
func (c *Client) Saved() ([]string, error) {
	ws, err := c.savedWifi()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, w := range ws {
		if !seen[w.ssid] {
			seen[w.ssid] = true
			out = append(out, w.ssid)
		}
	}
	return out, nil
}

// Join connects the Wi-Fi interface to ssid and makes it the preferred
// route (PreferredMetric). A saved network is used as saved when password
// is empty; otherwise a new connection is made, and the old ones for the
// same SSID are removed once it works. If the network is not up within
// JoinTimeout, a new connection is deleted again and the Wi-Fi network that
// was active before is brought back, so a wrong password cannot leave the
// Pi without the network it had.
func (c *Client) Join(ctx context.Context, ssid, password string, hidden bool) error {
	dev, err := c.wifiDevice()
	if err != nil {
		return err
	}
	ap := dbus.ObjectPath("/")
	sec := "open"
	if password != "" {
		sec = "wpa"
	}
	if !hidden {
		aps, err := c.accessPoints(dev.path)
		if err != nil {
			return err
		}
		found := false
		for _, n := range networks(aps, nil, "") {
			if n.SSID == ssid {
				ap, sec, found = n.ap, n.Security, true
			}
		}
		if !found {
			return fmt.Errorf("%q is not in range now; scan again", ssid)
		}
		if n := (Network{Security: sec}); !n.Supported() {
			return fmt.Errorf("%q uses %s security, which needs nmcli", ssid, sec)
		}
	}
	prev := dbus.ObjectPath("")
	if ac, err := propAs[dbus.ObjectPath](c, dev.path, ifaceDevice, "ActiveConnection"); err == nil && ac != "/" {
		prev, _ = propAs[dbus.ObjectPath](c, ac, ifaceActive, "Connection")
	}
	saved, err := c.savedWifi()
	if err != nil {
		return err
	}
	var same []savedConn
	for _, w := range saved {
		if w.ssid == ssid {
			same = append(same, w)
		}
	}

	var conn, active dbus.ObjectPath
	created := false
	switch {
	case password == "" && len(same) > 0:
		conn = same[0].path
		if err := c.setMetric(conn, PreferredMetric); err != nil {
			return err
		}
		if err := c.obj(rootPath).Call(service+".ActivateConnection", 0, conn, dev.path, ap).Store(&active); err != nil {
			return fmt.Errorf("NetworkManager: %w", err)
		}
	case password == "" && sec != "open":
		return fmt.Errorf("%q needs a password", ssid)
	default:
		if sec != "open" && (len(password) < 8 || len(password) > 63) {
			return errors.New("a Wi-Fi password is 8 to 63 characters")
		}
		if err := c.obj(rootPath).Call(service+".AddAndActivateConnection", 0, wifiSettings(ssid, sec, password, hidden), dev.path, ap).Store(&conn, &active); err != nil {
			return fmt.Errorf("NetworkManager: %w", err)
		}
		created = true
	}

	if err := c.waitActive(ctx, active); err != nil {
		reason := err.Error()
		if r, err := propAs[[]any](c, dev.path, ifaceDevice, "StateReason"); err == nil && len(r) == 2 {
			if code, ok := r[1].(uint32); ok && code != 0 {
				reason = Reason(code)
			}
		}
		if created {
			_ = c.obj(conn).Call(ifaceSetConn+".Delete", 0).Err
		}
		if prev != "" && prev != conn {
			_ = c.obj(rootPath).Call(service+".ActivateConnection", 0, prev, dev.path, dbus.ObjectPath("/")).Err
		}
		return fmt.Errorf("could not join %q: %s", ssid, reason)
	}
	if created {
		for _, w := range same {
			_ = c.obj(w.path).Call(ifaceSetConn+".Delete", 0).Err
		}
	}
	return nil
}

// waitActive polls an active connection until it is activated (2), goes
// away or deactivates (4), or JoinTimeout passes.
func (c *Client) waitActive(ctx context.Context, active dbus.ObjectPath) error {
	ctx, cancel := context.WithTimeout(ctx, JoinTimeout)
	defer cancel()
	for {
		st, err := propAs[uint32](c, active, ifaceActive, "State")
		switch {
		case err != nil:
			return errors.New("the connection attempt ended")
		case st == 2:
			return nil
		case st == 4:
			return errors.New("the connection attempt failed")
		}
		select {
		case <-ctx.Done():
			_ = c.obj(rootPath).Call(service+".DeactivateConnection", 0, active).Err
			return errors.New("it did not connect within 45 seconds")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Prefer makes Wi-Fi ("wifi") or Ethernet ("ethernet") the default route.
// Wi-Fi: the active Wi-Fi connection gets PreferredMetric. Ethernet: every
// saved Wi-Fi connection with PreferredMetric goes back to the default.
// Active connections are reapplied, so the route changes without a
// reconnect.
func (c *Client) Prefer(kind string) error {
	dev, err := c.wifiDevice()
	if err != nil {
		if kind == "ethernet" && errors.Is(err, ErrNoWifi) {
			return nil
		}
		return err
	}
	activeConn := dbus.ObjectPath("")
	if ac, err := propAs[dbus.ObjectPath](c, dev.path, ifaceDevice, "ActiveConnection"); err == nil && ac != "/" {
		activeConn, _ = propAs[dbus.ObjectPath](c, ac, ifaceActive, "Connection")
	}
	switch kind {
	case "wifi":
		if activeConn == "" {
			return errors.New("Wi-Fi is not connected to a network")
		}
		if err := c.setMetric(activeConn, PreferredMetric); err != nil {
			return err
		}
	case "ethernet":
		ws, err := c.savedWifi()
		if err != nil {
			return err
		}
		for _, w := range ws {
			if metricOf(w.s) == PreferredMetric {
				if err := c.setMetric(w.path, -1); err != nil {
					return err
				}
			}
		}
	default:
		return fmt.Errorf("prefer %q", kind)
	}
	if activeConn != "" {
		// An empty connection reapplies the saved settings.
		return c.obj(dev.path).Call(ifaceDevice+".Reapply", 0, map[string]map[string]dbus.Variant{}, uint64(0), uint32(0)).Err
	}
	return nil
}

// setMetric sets ipv4 and ipv6 route-metric on a saved connection. Update
// replaces every setting, so the Wi-Fi secret is read and sent back with it.
func (c *Client) setMetric(p dbus.ObjectPath, metric int64) error {
	s, err := c.settings(p)
	if err != nil {
		return fmt.Errorf("NetworkManager: %w", err)
	}
	if metricOf(s) == metric {
		return nil
	}
	if _, ok := s["802-11-wireless-security"]; ok {
		var sec map[string]map[string]dbus.Variant
		if err := c.obj(p).Call(ifaceSetConn+".GetSecrets", 0, "802-11-wireless-security").Store(&sec); err == nil {
			for k, v := range sec["802-11-wireless-security"] {
				s["802-11-wireless-security"][k] = v
			}
		}
	}
	for _, ip := range []string{"ipv4", "ipv6"} {
		if s[ip] == nil {
			s[ip] = map[string]dbus.Variant{}
		}
		// The deprecated forms clash with address-data and route-data.
		delete(s[ip], "addresses")
		delete(s[ip], "routes")
		s[ip]["route-metric"] = dbus.MakeVariant(metric)
	}
	if err := c.obj(p).Call(ifaceSetConn+".Update", 0, s).Err; err != nil {
		return fmt.Errorf("NetworkManager: %w", err)
	}
	return nil
}

// Forget deletes every saved connection for ssid.
func (c *Client) Forget(ssid string) error {
	ws, err := c.savedWifi()
	if err != nil {
		return err
	}
	n := 0
	for _, w := range ws {
		if w.ssid == ssid {
			if err := c.obj(w.path).Call(ifaceSetConn+".Delete", 0).Err; err != nil {
				return fmt.Errorf("NetworkManager: %w", err)
			}
			n++
		}
	}
	if n == 0 {
		return fmt.Errorf("%q is not saved", ssid)
	}
	return nil
}

// Trim is for SSIDs typed by a person: NetworkManager allows up to 32 bytes.
func Trim(ssid string) (string, error) {
	ssid = strings.TrimSpace(ssid)
	if ssid == "" || len(ssid) > 32 {
		return "", errors.New("a Wi-Fi network name is 1 to 32 bytes")
	}
	return ssid, nil
}
