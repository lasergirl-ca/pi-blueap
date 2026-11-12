package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/setup"
)

func TestFirewallPage(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "admin", "admin-pw")
	_, page, _ := get(t, c, ts.URL+"/firewall")
	for _, want := range []string{"192.168.1.20", "tcp/14238", "NetSync", "palm-&gt;rfc1918", "720", "Add a rule"} {
		if !strings.Contains(page, want) {
			t.Errorf("firewall page lacks %q", want)
		}
	}
	rules := func() []config.AllowRule {
		var r []config.AllowRule
		_ = json.Unmarshal(f.set["firewall.allow"], &r)
		return r
	}

	// Add: ports split on spaces and commas, lower-cased; the whole list is set.
	code, _, body := post(t, c, ts.URL+"/firewall/add", url.Values{"csrf": {tok}, "host": {" 10.4.0.5 "}, "ports": {"TCP/22, icmp"}, "comment": {"NAS"}})
	if got := rules(); code != 200 || len(got) != 2 || got[1].Host != "10.4.0.5" || strings.Join(got[1].Ports, " ") != "tcp/22 icmp" || !strings.Contains(body, "apply to Palms now") {
		t.Errorf("add: %d %+v", code, got)
	}
	// Bad ports never reach the daemon.
	delete(f.set, "firewall.allow")
	code, _, body = post(t, c, ts.URL+"/firewall/add", url.Values{"csrf": {tok}, "host": {"10.4.0.5"}, "ports": {"ssh"}})
	if code != http.StatusBadRequest || !strings.Contains(body, "not tcp/N") || f.set["firewall.allow"] != nil {
		t.Errorf("bad ports: %d", code)
	}
	// Edit rule 2, delete rule 1.
	post(t, c, ts.URL+"/firewall/2/save", url.Values{"csrf": {tok}, "host": {"10.4.0.6"}, "ports": {"tcp/2049"}, "comment": {"NFS"}})
	if got := rules(); len(got) != 2 || got[1].Host != "10.4.0.6" || got[1].Comment != "NFS" {
		t.Errorf("edit: %+v", got)
	}
	post(t, c, ts.URL+"/firewall/1/delete", url.Values{"csrf": {tok}})
	if got := rules(); len(got) != 1 || got[0].Host != "10.4.0.6" {
		t.Errorf("delete: %+v", got)
	}
	if code, _, _ := post(t, c, ts.URL+"/firewall/9/delete", url.Values{"csrf": {tok}}); code != http.StatusBadRequest {
		t.Errorf("delete rule 9: %d", code)
	}
	// Test reachability.
	_, _, body = post(t, c, ts.URL+"/firewall/test", url.Values{"csrf": {tok}, "host": {"192.168.1.20"}, "port": {"14238"}})
	if !strings.Contains(body, "answers from the Pi (3 ms)") || !strings.Contains(body, "A rule lets Palms reach it") {
		t.Errorf("test ok:\n%s", body)
	}
	_, _, body = post(t, c, ts.URL+"/firewall/test", url.Values{"csrf": {tok}, "host": {"192.168.1.20"}, "port": {"22"}})
	if !strings.Contains(body, "does not answer from the Pi: connection refused") || !strings.Contains(body, "No rule covers it") {
		t.Errorf("test refused:\n%s", body)
	}
	if code, _, _ := post(t, c, ts.URL+"/firewall/test", url.Values{"csrf": {tok}, "host": {"x"}, "port": {"99999"}}); code != http.StatusBadRequest {
		t.Errorf("bad port: %d", code)
	}
}

func TestNATTable(t *testing.T) {
	_, ts, c, _ := loggedIn(t, "viewer", "viewer-pw")
	_, page, _ := get(t, c, ts.URL+"/firewall")
	for _, want := range []string{
		`hx-get="/partials/nat"`,
		"<td>Zoë&#39;s Palm TX</td><td>tcp</td><td><code>10.42.0.10:1028</code></td><td><code>93.184.215.14:80</code></td>",
		"<code>10.4.1.118:1028</code>", "established", "4d23h",
		"<td>unknown</td><td>udp</td>", "not translated (the gateway itself)", "answered",
		"2 from the Palm network 10.42.0.0/24, of 70 connections",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("firewall page lacks %s", want)
		}
	}
	code, part, _ := get(t, c, ts.URL+"/partials/nat")
	if code != 200 || !strings.Contains(part, "93.184.215.14:80") || strings.Contains(part, "<html") {
		t.Errorf("partial %d:\n%s", code, part)
	}
}

func TestSettingsPage(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "admin", "admin-pw")
	cfg := config.Default()
	_, page, _ := get(t, c, ts.URL+"/settings")
	for _, want := range []string{
		`value="Palm Gateway"`,
		`<option value="auto">Automatic: the first adapter found</option>`,
		`<option value="hci0" selected>USB dongle 60:15:6F:EA:00:81 (hci0), in use now</option>`,
		`<option value="2C:CF:67:2F:29:F5">Onboard radio 2C:CF:67:2F:29:F5 (hci1)</option>`,
		`Internet connection: <strong>eth0</strong>`,
		`<a class="button" href="/internet">Change the internet connection or Wi-Fi network</a>`,
		`<option value="0" selected>Never</option>`,
		`<option value="self-signed" selected>HTTPS with a self-signed certificate (recommended)</option>`,
		`value="10.42.0.0/24"`,
		"Apply network changes",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("settings page lacks %s", want)
		}
	}
	form := func(edit func(url.Values)) url.Values {
		v := url.Values{"csrf": {tok}, "bluetooth.name": {cfg.Bluetooth.Name}, "bluetooth.adapter": {cfg.Bluetooth.Adapter}, "ppp.idle_seconds": {"0"}}
		edit(v)
		return v
	}
	// Only what changed is set.
	_, _, body := post(t, c, ts.URL+"/settings", form(func(v url.Values) { v.Set("bluetooth.name", "Lyman GW") }))
	if len(f.set) != 1 || string(f.set["bluetooth.name"]) != `"Lyman GW"` || !strings.Contains(body, "Saved: gateway name.") {
		t.Errorf("name: %v", f.set)
	}
	if strings.Contains(body, "Restart now") {
		t.Error("a live key asked for a restart")
	}
	// A restart-only key offers the Restart button.
	_, _, body = post(t, c, ts.URL+"/settings", form(func(v url.Values) { v.Set("bluetooth.adapter", "hci1") }))
	if !strings.Contains(body, "used once palm-gwd restarts") || !strings.Contains(body, "Restart now") {
		t.Errorf("adapter:\n%s", body)
	}
	// The daemon's validation error is shown.
	code, _, body := post(t, c, ts.URL+"/settings", form(func(v url.Values) { v.Set("bluetooth.adapter", "bogus") }))
	if code != http.StatusBadRequest || !strings.Contains(body, "must be hciN") {
		t.Errorf("bogus adapter: %d", code)
	}
	code, _, _ = post(t, c, ts.URL+"/settings", form(func(v url.Values) { v.Set("ppp.idle_seconds", "soon") }))
	if code != http.StatusBadRequest {
		t.Errorf("idle soon: %d", code)
	}
	post(t, c, ts.URL+"/settings", form(func(v url.Values) { v.Set("ppp.idle_seconds", "600"); v.Set("ppp.debug", "1") }))
	if string(f.set["ppp.idle_seconds"]) != "600" || string(f.set["ppp.debug"]) != "true" {
		t.Errorf("ppp: %s %s", f.set["ppp.idle_seconds"], f.set["ppp.debug"])
	}
	// Restart, then the page sends the browser to the login page.
	_, _, body = post(t, c, ts.URL+"/settings/restart", url.Values{"csrf": {tok}})
	if _, ok := f.last("restart"); !ok || !strings.Contains(body, `http-equiv="refresh" content="10;url=/login"`) {
		t.Errorf("restart:\n%s", body)
	}
}

func TestAdapterOptionsKeepConfiguredForm(t *testing.T) {
	c := ctl.ChoicesReply{Adapters: []setup.Adapter{{HCI: "hci0", Address: "60:15:6F:EA:00:81", Bus: "usb"}}}
	// Configured as hci0: the option keeps "hci0", so saving unchanged changes nothing.
	got := adapterOptions("hci0", c)
	if len(got) != 2 || got[1].Value != "hci0" || !got[1].Selected {
		t.Errorf("hci0: %+v", got)
	}
	// Configured adapter missing: still offered, and selected.
	got = adapterOptions("AA:BB:CC:DD:EE:FF", c)
	if last := got[len(got)-1]; last.Value != "AA:BB:CC:DD:EE:FF" || !last.Selected || !strings.Contains(last.Label, "not found now") {
		t.Errorf("missing: %+v", got)
	}
	if o := idleOptions(45); !o[len(o)-1].Selected || o[len(o)-1].Value != "45" {
		t.Errorf("idle: %+v", o)
	}
}

func TestNetworkSave(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "admin", "admin-pw")
	form := func(edit func(url.Values)) url.Values {
		v := url.Values{"csrf": {tok}, "network.uplink": {"eth0"}, "network.subnet": {"10.42.0.0/24"}, "web.tls": {"self-signed"}, "web.bluetooth_side": {"1"}}
		edit(v)
		return v
	}
	_, _, body := post(t, c, ts.URL+"/settings/network", form(func(url.Values) {}))
	if !strings.Contains(body, "Nothing changed.") {
		t.Errorf("unchanged:\n%s", body)
	}
	req, _ := f.last("network.set")
	if req.Network.BluetoothSide == nil || !*req.Network.BluetoothSide {
		t.Errorf("bluetooth side not sent: %+v", req.Network)
	}
	code, _, body := post(t, c, ts.URL+"/settings/network", form(func(v url.Values) { v.Set("network.subnet", "10.4.0.0/24") }))
	if code != http.StatusBadRequest || !strings.Contains(body, "10.43.0.0/24 is free") {
		t.Errorf("clash: %d", code)
	}
	_, _, body = post(t, c, ts.URL+"/settings/network", form(func(v url.Values) { v.Set("network.uplink", "wlan0") }))
	if !strings.Contains(body, "Saved: internet connection.") || !strings.Contains(body, `content="25;url=https://`) {
		t.Errorf("uplink:\n%s", body)
	}
	// Plain HTTP: the Bluetooth side is not sent (the daemon turns it off), and the browser goes to http://.
	_, _, body = post(t, c, ts.URL+"/settings/network", form(func(v url.Values) { v.Set("web.tls", "off") }))
	req, _ = f.last("network.set")
	if req.Network.TLS != "off" || req.Network.BluetoothSide != nil || !strings.Contains(body, `content="25;url=http://`) {
		t.Errorf("tls off: %+v\n%s", req.Network, body)
	}
}

func TestPasswordChange(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "admin", "admin-pw")
	pw := func(cur, nw, again string) (int, string) {
		code, _, body := post(t, c, ts.URL+"/settings/password", url.Values{"csrf": {tok}, "current": {cur}, "new": {nw}, "again": {again}})
		return code, body
	}
	if code, body := pw("wrong", "a-new-password", "a-new-password"); code != http.StatusUnauthorized || !strings.Contains(body, "current password is wrong") {
		t.Errorf("wrong current: %d", code)
	}
	if code, _ := pw("admin-pw", "short", "short"); code != http.StatusBadRequest {
		t.Errorf("short: %d", code)
	}
	if code, body := pw("admin-pw", "a-new-password", "another-one!"); code != http.StatusBadRequest || !strings.Contains(body, "do not match") {
		t.Errorf("mismatch: %d", code)
	}
	if _, ok := f.set["web.password_hash"]; ok {
		t.Fatal("a refused change was saved")
	}
	if code, body := pw("admin-pw", "a-new-password", "a-new-password"); code != 200 || !strings.Contains(body, "Password changed") {
		t.Fatalf("change: %d", code)
	}
	var hash string
	_ = json.Unmarshal(f.set["web.password_hash"], &hash)
	if ok, err := setup.VerifyPassword("a-new-password", hash); !ok || err != nil {
		t.Errorf("saved hash does not verify: %v", err)
	}
}

func TestViewerSettingsReadOnly(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "viewer", "viewer-pw")
	_, page, _ := get(t, c, ts.URL+"/firewall")
	if strings.Contains(page, "Add a rule") || strings.Contains(page, "/firewall/1/delete") {
		t.Error("viewer firewall page has admin forms")
	}
	_, page, _ = get(t, c, ts.URL+"/settings")
	if strings.Contains(page, "Change the web password") || !strings.Contains(page, "disabled") {
		t.Error("viewer settings page is editable")
	}
	for _, u := range []string{"/firewall/add", "/firewall/1/delete", "/firewall/test", "/settings", "/settings/network", "/settings/password", "/settings/restart"} {
		if code, _, _ := post(t, c, ts.URL+u, url.Values{"csrf": {tok}}); code != http.StatusForbidden {
			t.Errorf("viewer POST %s = %d", u, code)
		}
	}
	if len(f.set) != 0 {
		t.Errorf("viewer changed %v", f.set)
	}
}
