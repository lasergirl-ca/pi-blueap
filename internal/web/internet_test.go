package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"palm-gw/internal/ctl"
)

func TestInternetPage(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "admin", "admin-pw")
	_, page, _ := get(t, c, ts.URL+"/internet")
	for _, want := range []string{
		`<a href="/internet">Internet</a>`,
		"Palms reach the internet through <strong>Ethernet (eth0)</strong>",
		`Ethernet <span class="badge">in use</span>`,
		"Cable plugged in, address 10.4.1.118/23, router 10.4.0.1.",
		"Not connected to a network.",
		"<td>Lyman <span class=\"muted\">(saved)</span></td>",
		"▮▮▮▮ 82%", "5 GHz", "🔒 WPA2",
		`<input type="hidden" name="ssid" value="Lyman"><button type="submit" class="secondary">Connect</button>`, // saved: no password
		`aria-label="Password for Neighbour"`,                                               // new: password
		`name="ssid" value="Cafe"><button type="submit" class="secondary">Connect</button>`, // open
		"needs nmcli", // enterprise
		"Join a hidden network",
		`action="/internet/forget"`,
		"Turn Wi-Fi off",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("internet page lacks %s", want)
		}
	}
	if strings.Contains(page, "Use Ethernet for the internet") {
		t.Error("offers Ethernet while it is in use")
	}
	// Join: the password goes to the daemon; the page then shows the job polling.
	code, _, _ := post(t, c, ts.URL+"/internet/join", url.Values{"csrf": {tok}, "ssid": {"Neighbour"}, "wifi-password": {"a-wifi-pass"}})
	_, body, _ := get(t, c, ts.URL+"/internet")
	req, _ := f.last("wifi.join")
	if code != http.StatusSeeOther || req.WiFi.SSID != "Neighbour" || req.WiFi.Password != "a-wifi-pass" || !strings.Contains(body, `hx-get="/partials/internet-job"`) || !strings.Contains(body, "Joining “Neighbour”") {
		t.Errorf("join %d %+v:\n%s", code, req.WiFi, body)
	}
	code, part, _ := get(t, c, ts.URL+"/partials/internet-job")
	if code != 200 || !strings.Contains(part, "Joining") {
		t.Errorf("job partial %d", code)
	}
	f.mu.Lock()
	f.job.Done, f.job.OK, f.job.Switching = true, true, true
	f.mu.Unlock()
	code, part, _ = get(t, c, ts.URL+"/partials/internet-job")
	if code != 286 || !strings.Contains(part, "log in again") || strings.Contains(part, "hx-get") {
		t.Errorf("done partial %d:\n%s", code, part)
	}
	// A refusal is shown on the page.
	code, _, body = post(t, c, ts.URL+"/internet/use", url.Values{"csrf": {tok}, "use": {"wifi"}})
	if code != http.StatusBadRequest || !strings.Contains(body, "join one first") {
		t.Errorf("use wifi: %d", code)
	}
	_, _, body = post(t, c, ts.URL+"/internet/forget", url.Values{"csrf": {tok}, "ssid": {"Lyman"}})
	if !strings.Contains(body, "Forgot &#34;Lyman&#34;.") {
		t.Errorf("forget:\n%s", body)
	}
}

func TestInternetViewer(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "viewer", "viewer-pw")
	_, page, _ := get(t, c, ts.URL+"/internet")
	if !strings.Contains(page, "Networks in range") || strings.Contains(page, `action="/internet/join"`) || strings.Contains(page, "Turn Wi-Fi off") {
		t.Error("viewer page has admin forms")
	}
	for _, u := range []string{"/internet/use", "/internet/join", "/internet/forget", "/internet/radio"} {
		if code, _, _ := post(t, c, ts.URL+u, url.Values{"csrf": {tok}, "ssid": {"x"}}); code != http.StatusForbidden {
			t.Errorf("viewer POST %s = %d", u, code)
		}
	}
	if _, ok := f.last("wifi.join"); ok {
		t.Error("viewer joined")
	}
}

func TestInternetWireGuard(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "admin", "admin-pw")
	_, page, _ := get(t, c, ts.URL+"/internet")
	for _, want := range []string{"<h2>WireGuard", "It is off.", `action="/internet/wg/import"`, `action="/internet/wg/generate"`} {
		if !strings.Contains(page, want) {
			t.Errorf("off: page lacks %s", want)
		}
	}
	f.mu.Lock()
	f.wg = ctl.WGStatus{Enabled: true, State: "up", Interface: "wg0", Mode: "full", OnDown: "fail_closed", Address: "10.17.200.5/32",
		Endpoint: "vpn.example.net:51820", AllowedIPs: []string{"0.0.0.0/0"}, DNS: []string{"10.17.200.1"}, RxBytes: 4096, LastHeardSeconds: 12,
		PublicKey: "gateway-pub"}
	f.mu.Unlock()
	_, page, _ = get(t, c, ts.URL+"/internet")
	for _, want := range []string{
		`WireGuard <span class="badge">up</span>`,
		"All Palm traffic goes through the tunnel. When the tunnel is down, Palm traffic stops",
		"same guards as your LAN", "<code>10.17.200.1</code> (through the tunnel)", "gateway-pub",
		"Bring the tunnel down now", "Turn WireGuard off",
		`name="on_down" value="fail_closed" checked`, `name="mode" value="full" checked`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("up: page lacks %s", want)
		}
	}
	// Saving goes to wg.set and ends on the applying page.
	code, _, body := post(t, c, ts.URL+"/internet/wg/set", url.Values{"csrf": {tok}, "mode": {"split"}, "networks": {"10.17.0.0/16, 192.168.50.0/24"}, "on_down": {"fallback"}})
	req, _ := f.last("wg.set")
	if code != 200 || req.WG.Mode != "split" || strings.Join(req.WG.AllowedIPs, ",") != "10.17.0.0/16,192.168.50.0/24" || req.WG.OnDown != "fallback" || !strings.Contains(body, "WireGuard: wireguard.mode") {
		t.Errorf("set %d %+v", code, req.WG)
	}
	// A bad .conf is shown, not applied.
	code, _, body = post(t, c, ts.URL+"/internet/wg/import", url.Values{"csrf": {tok}, "conf": {"a bad conf"}})
	if code != http.StatusBadRequest || !strings.Contains(body, "PublicKey is missing") {
		t.Errorf("bad import %d", code)
	}
}

func TestInternetWireGuardViewer(t *testing.T) {
	_, ts, c, _ := loggedIn(t, "viewer", "viewer-pw")
	_, page, _ := get(t, c, ts.URL+"/internet")
	if !strings.Contains(page, "<h2>WireGuard") || strings.Contains(page, "/internet/wg/") {
		t.Error("a viewer sees the WireGuard forms")
	}
}
