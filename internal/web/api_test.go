package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"palm-gw/internal/config"
	"palm-gw/internal/devkey"
)

type apiFixture struct {
	f   *daemonFake
	lan *httptest.Server
	bt  *httptest.Server
	clk *clock
	t   *testing.T
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	ah, vh := hashes(t)
	cfg := config.Default()
	cfg.Web.PasswordHash = ah
	cfg.Web.ViewerUsername, cfg.Web.ViewerPasswordHash = "viewer", vh
	cfg.Web.SessionHours = 1
	clk := &clock{t: time.Now().UTC().Truncate(time.Second)}
	f := newDaemonFake()
	f.keys, _ = devkey.Open(filepath.Join(t.TempDir(), devkey.FileName))
	s, err := New(Options{Config: func() *config.Config { return cfg }, Backend: f, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	a := &apiFixture{f: f, clk: clk, t: t, lan: httptest.NewServer(s.Handler(SideLAN, false)), bt: httptest.NewServer(s.Handler(SideBluetooth, true))}
	t.Cleanup(a.lan.Close)
	t.Cleanup(a.bt.Close)
	return a
}

// do sends a request with an optional bearer token and form; it returns
// the status, the body and the headers.
func (a *apiFixture) do(base, method, path, token string, form url.Values) (int, string, http.Header) {
	a.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, base+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (a *apiFixture) login(base, user, pw string) string {
	a.t.Helper()
	code, body, _ := a.do(base, "POST", "/api/v1/login", "", url.Values{"username": {user}, "password": {pw}})
	var r struct {
		Status, Token, Role string
		ExpiresIn           int64 `json:"expires_in"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil || code != 200 || r.Status != "ok" || len(r.Token) != 64 || r.ExpiresIn != 3600 {
		a.t.Fatalf("login %s: %d %s", user, code, body)
	}
	return r.Token
}

func TestAPILogin(t *testing.T) {
	a := newAPIFixture(t)
	// Wrong username and wrong password: the same reply.
	c1, b1, h := a.do(a.lan.URL, "POST", "/api/v1/login", "", url.Values{"username": {"nobody"}, "password": {"admin-pw"}})
	c2, b2, _ := a.do(a.lan.URL, "POST", "/api/v1/login", "", url.Values{"username": {"admin"}, "password": {"wrong"}})
	if c1 != 401 || c1 != c2 || b1 != b2 || !strings.Contains(b1, `"error":"login_required"`) || h.Get("WWW-Authenticate") == "" {
		t.Errorf("%d %s / %d %s", c1, b1, c2, b2)
	}
	if _, b, _ := a.do(a.lan.URL, "GET", "/api/v1/status", "", nil); !strings.Contains(b, "Bearer <token>") {
		t.Errorf("%d %s / %d %s", c1, b1, c2, b2)
	}
	tok := a.login(a.lan.URL, "admin", "admin-pw")
	if code, body, _ := a.do(a.lan.URL, "GET", "/api/v1/status", tok, nil); code != 200 || !strings.Contains(body, `"gateway":"Palm Gateway"`) {
		t.Errorf("status %d %s", code, body)
	}
	// No token, a bad token, a token from the other side.
	for _, c := range []struct{ base, tok string }{{a.lan.URL, ""}, {a.lan.URL, "nope"}, {a.bt.URL, tok}} {
		if code, _, _ := a.do(c.base, "GET", "/api/v1/status", c.tok, nil); code != 401 {
			t.Errorf("%q on %s = %d", c.tok, c.base, code)
		}
	}
	// A token is not a cookie.
	jar := client(t)
	u, _ := url.Parse(a.lan.URL)
	jar.Jar.SetCookies(u, []*http.Cookie{{Name: cookieName, Value: tok}})
	if code, _, _ := get(t, jar, a.lan.URL+"/"); code != http.StatusSeeOther {
		t.Errorf("token as cookie = %d", code)
	}
	// Expiry, then logout.
	a.clk.add(61 * time.Minute)
	if code, _, _ := a.do(a.lan.URL, "GET", "/api/v1/status", tok, nil); code != 401 {
		t.Errorf("expired token = %d", code)
	}
	tok = a.login(a.lan.URL, "admin", "admin-pw")
	if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/logout", tok, url.Values{}); code != 200 {
		t.Errorf("logout = %d", code)
	}
	if code, _, _ := a.do(a.lan.URL, "GET", "/api/v1/status", tok, nil); code != 401 {
		t.Errorf("after logout = %d", code)
	}
}

func TestAPICookieIsNotAToken(t *testing.T) {
	_, ts, c, _ := loggedIn(t, "admin", "admin-pw")
	u, _ := url.Parse(ts.URL)
	var cookie string
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == cookieName {
			cookie = ck.Value
		}
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if cookie == "" || resp.StatusCode != 401 {
		t.Errorf("browser cookie as bearer = %d", resp.StatusCode)
	}
}

func TestAPIRateLimit(t *testing.T) {
	a := newAPIFixture(t)
	for i := 0; i < maxFailures; i++ {
		a.do(a.lan.URL, "POST", "/api/v1/login", "", url.Values{"username": {"admin"}, "password": {"wrong"}})
	}
	code, body, h := a.do(a.lan.URL, "POST", "/api/v1/login", "", url.Values{"username": {"admin"}, "password": {"admin-pw"}})
	if code != 429 || h.Get("Retry-After") == "" || !strings.Contains(body, "too_many_requests") {
		t.Errorf("%d %s", code, body)
	}
	// The other listener has its own count.
	a.login(a.bt.URL, "admin", "admin-pw")
}

func TestAPIViewer(t *testing.T) {
	a := newAPIFixture(t)
	tok := a.login(a.lan.URL, "viewer", "viewer-pw")
	for _, p := range []string{"/api/v1/pair", "/api/v1/pair/close", "/api/v1/devices/00:07:E0:3C:5E:FA/forget", "/api/v1/firewall/allow", "/api/v1/firewall/remove", "/api/v1/settings"} {
		if code, body, _ := a.do(a.lan.URL, "POST", p, tok, url.Values{}); code != 403 || !strings.Contains(body, "read-only") {
			t.Errorf("viewer POST %s = %d", p, code)
		}
	}
	a.f.mu.Lock()
	a.f.window.Open, a.f.window.PIN, a.f.window.Expires = true, "4821", time.Now().Add(time.Minute)
	a.f.mu.Unlock()
	if _, body, _ := a.do(a.lan.URL, "GET", "/api/v1/status", tok, nil); !strings.Contains(body, `"pairing":true`) || strings.Contains(body, "4821") {
		t.Errorf("viewer status shows the PIN: %s", body)
	}
	if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/logout", tok, url.Values{}); code != 200 {
		t.Errorf("viewer logout = %d", code)
	}
}

func TestAPITextFormat(t *testing.T) {
	a := newAPIFixture(t)
	tok := a.login(a.lan.URL, "admin", "admin-pw")
	code, body, h := a.do(a.lan.URL, "GET", "/api/v1/sessions?format=text", tok, nil)
	if code != 200 || h.Get("Content-Type") != "text/plain; charset=windows-1252" {
		t.Fatalf("%d %v", code, h)
	}
	want := "status=ok\r\ncount=1\r\n\r\nname=Zo\xebs Palm TX\r\naddr=00:07:E0:3C:5E:FA\r\nprofile=lap\r\nifname=ppp0\r\nip=10.42.0.10\r\n"
	want = strings.Replace(want, "Zo\xebs", "Zo\xeb's", 1)
	if !strings.HasPrefix(body, want) || !strings.HasSuffix(body, "\r\n\r\n") {
		t.Errorf("text:\n%q", body)
	}
	if got := Windows1252("Zoë’s “Palm” – 5 € 日本"); !bytes.Equal(got, []byte("Zo\xeb\x92s \x93Palm\x94 \x96 5 \x80 ??")) {
		t.Errorf("%q", got)
	}
	if got := FromWindows1252("Zo\xeb\x92s \x93Palm\x94 \x80"); got != "Zoë’s “Palm” €" {
		t.Errorf("from: %q", got)
	}
	if got := FromWindows1252("Zoë"); got != "Zoë" {
		t.Errorf("UTF-8 kept: %q", got)
	}
	_, body, _ = a.do(a.lan.URL, "GET", "/api/v1/nothing?format=text", tok, nil)
	if !strings.HasPrefix(body, "error=not_found\r\nmessage=") {
		t.Errorf("text error %q", body)
	}
	// Values cannot break lines.
	var b bytes.Buffer
	rec{{"name", "two\r\nlines"}, {"on", true}, {"ports", []string{"tcp/22", "icmp"}}}.text(&b)
	if b.String() != "name=two  lines\r\non=1\r\nports=tcp/22 icmp\r\n" {
		t.Errorf("%q", b.String())
	}
}

func TestAPIActions(t *testing.T) {
	a := newAPIFixture(t)
	tok := a.login(a.lan.URL, "admin", "admin-pw")
	// Pairing: the PIN comes back.
	code, body, _ := a.do(a.lan.URL, "POST", "/api/v1/pair", tok, url.Values{"minutes": {"2"}, "one": {"1"}})
	if code != 200 || !strings.Contains(body, `"pin":"4821"`) || !strings.Contains(body, `"one":true`) {
		t.Errorf("pair %d %s", code, body)
	}
	if _, body, _ := a.do(a.lan.URL, "GET", "/api/v1/status", tok, nil); !strings.Contains(body, `"pin":"4821"`) {
		t.Errorf("admin status lacks the PIN: %s", body)
	}
	if _, body, _ := a.do(a.lan.URL, "POST", "/api/v1/pair/close", tok, url.Values{}); !strings.Contains(body, `"was_open":true`) {
		t.Errorf("close %s", body)
	}
	// Devices.
	if _, body, _ := a.do(a.lan.URL, "GET", "/api/v1/devices", tok, nil); !strings.Contains(body, `{"name":"Zoë's Palm TX","addr":"00:07:E0:3C:5E:FA","connected":true,"via":"lap ppp0 10.42.0.10"`) {
		t.Errorf("devices %s", body)
	}
	if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/devices/00:07:e0:3c:5e:fa/rename", tok, url.Values{"name": {"TX"}}); code != 200 {
		t.Errorf("rename %d", code)
	}
	if req, _ := a.f.last("rename"); req.Addr != "00:07:E0:3C:5E:FA" || req.Label != "TX" {
		t.Errorf("rename req %+v", req)
	}
	if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/devices/11:22:33:44:55:66/rename", tok, url.Values{"name": {"x"}}); code != http.StatusConflict {
		t.Errorf("rename refused = %d", code)
	}
	if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/devices/zz/forget", tok, url.Values{}); code != 404 {
		t.Errorf("bad addr = %d", code)
	}
	// Firewall: add, then remove by number and by host.
	code, body, _ = a.do(a.lan.URL, "POST", "/api/v1/firewall/allow", tok, url.Values{"host": {"192.168.1.30"}, "ports": {"tcp/22 icmp"}, "comment": {"NAS"}})
	if code != 200 || !strings.Contains(body, `"count":2`) || !strings.Contains(body, `"ports":["tcp/22","icmp"]`) {
		t.Errorf("allow %d %s", code, body)
	}
	if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/firewall/remove", tok, url.Values{"n": {"9"}}); code != 400 {
		t.Errorf("remove 9 = %d", code)
	}
	if _, body, _ := a.do(a.lan.URL, "POST", "/api/v1/firewall/remove", tok, url.Values{"host": {"192.168.1.30"}}); !strings.Contains(body, `"count":1`) {
		t.Errorf("remove host %s", body)
	}
	if _, body, _ := a.do(a.lan.URL, "POST", "/api/v1/firewall/remove", tok, url.Values{"n": {"1"}}); !strings.Contains(body, `"count":0`) || !strings.Contains(body, `"rules":[]`) {
		t.Errorf("remove 1 %s", body)
	}
	// Settings.
	code, body, _ = a.do(a.lan.URL, "POST", "/api/v1/settings", tok, url.Values{"name": {"Lyman GW"}, "adapter": {"hci1"}})
	if code != 200 || !strings.Contains(body, `"saved":["bluetooth.name","bluetooth.adapter"]`) || !strings.Contains(body, `"needs_restart":["bluetooth.adapter"]`) {
		t.Errorf("settings %d %s", code, body)
	}
	if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/settings", tok, url.Values{}); code != 400 {
		t.Errorf("empty settings = %d", code)
	}
	// Events: the PIN never appears; since filters.
	if _, body, _ := a.do(a.lan.URL, "GET", "/api/v1/events", tok, nil); !strings.Contains(body, `"event":"paired"`) || !strings.Contains(body, `"last":`) {
		t.Errorf("events %s", body)
	}
	// Me: 127.0.0.1 has no session.
	if _, body, _ := a.do(a.lan.URL, "GET", "/api/v1/me?format=text", tok, nil); !strings.Contains(body, "session=none\r\nip=127.0.0.1") {
		t.Errorf("me %q", body)
	}
}

// D-063: Remember gives the Palm a device key; the key logs in without the
// password; a wrong or revoked key, or one whose account is gone, is refused.
func TestAPIDeviceKey(t *testing.T) {
	a := newAPIFixture(t)
	// The Palm sends its name in Windows-1252: "Zo\xeb's" becomes "Zoë's".
	code, body, _ := a.do(a.bt.URL, "POST", "/api/v1/login?format=text", "", url.Values{"username": {"admin"}, "password": {"admin-pw"}, "remember": {"1"}, "device": {"Zo\xeb\x92s Palm TX"}})
	if code != 200 || !strings.Contains(body, "device_key=pgk1.") || !strings.Contains(body, "device_key_id=") {
		t.Fatalf("remember: %d %q", code, body)
	}
	key := ""
	for _, l := range strings.Split(body, "\r\n") {
		if v, ok := strings.CutPrefix(l, "device_key="); ok {
			key = v
		}
	}
	// The key alone logs in, on either side.
	for _, base := range []string{a.bt.URL, a.lan.URL} {
		code, body, _ = a.do(base, "POST", "/api/v1/login", "", url.Values{"device_key": {key}})
		if code != 200 || !strings.Contains(body, `"role":"admin"`) || !strings.Contains(body, `"user":"admin"`) {
			t.Errorf("key login on %s: %d %s", base, code, body)
		}
	}
	list := a.f.keys.List()
	if len(list) != 1 || list[0].Name != "Zo\u00eb\u2019s Palm TX" || list[0].LastIP != "127.0.0.1" {
		t.Errorf("keys %+v", list)
	}
	// A wrong key is refused and counts towards the limit.
	for i := 0; i < maxFailures; i++ {
		if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/login", "", url.Values{"device_key": {key + "0"}}); code != 401 {
			t.Errorf("wrong key = %d", code)
		}
	}
	if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/login", "", url.Values{"device_key": {key}}); code != 429 {
		t.Errorf("after the limit = %d", code)
	}
	// A key whose account is gone is refused.
	ghost, _, _ := a.f.keys.Issue("ghost", "admin", "old Palm")
	if code, _, _ := a.do(a.bt.URL, "POST", "/api/v1/login", "", url.Values{"device_key": {ghost}}); code != 401 {
		t.Errorf("ghost key = %d", code)
	}
	// A viewer key does not become admin.
	vkey, _, _ := a.f.keys.Issue("viewer", "admin", "forged role")
	if code, _, _ := a.do(a.bt.URL, "POST", "/api/v1/login", "", url.Values{"device_key": {vkey}}); code != 401 {
		t.Errorf("viewer key claiming admin = %d", code)
	}
	// Revoked: refused.
	if _, err := a.f.keys.Revoke(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := a.do(a.bt.URL, "POST", "/api/v1/login", "", url.Values{"device_key": {key}}); code != 401 || !strings.Contains(body, "revoked") {
		t.Errorf("revoked key = %d %s", code, body)
	}
}

// D-064: every web action is in the API too.
func TestAPIParity(t *testing.T) {
	a := newAPIFixture(t)
	tok := a.login(a.lan.URL, "admin", "admin-pw")
	for _, c := range []struct {
		method, path string
		form         url.Values
		want         []string
	}{
		{"POST", "/api/v1/devices/allow", url.Values{"addr": {"00:07:e0:90:a0:c6"}, "name": {"LifeDrive"}}, []string{"addr=00:07:E0:90:A0:C6", "added=1"}},
		{"POST", "/api/v1/firewall/save", url.Values{"n": {"1"}, "host": {"192.168.1.21"}, "ports": {"tcp/14238"}, "comment": {"NetSync"}}, []string{"host=192.168.1.21"}},
		{"POST", "/api/v1/firewall/test", url.Values{"host": {"192.168.1.20"}, "port": {"14238"}}, []string{"reached=1", "allowed=1"}},
		{"GET", "/api/v1/firewall/counters", nil, []string{"rule=palm->rfc1918", "packets=12"}},
		{"GET", "/api/v1/nat", nil, []string{"subnet=10.42.0.0/24", "to=93.184.215.14:80", "out=10.4.1.118:1028", "state=established"}},
		{"GET", "/api/v1/internet", nil, []string{"uplink=eth0", "wifi_on=1", "kind=ethernet", "in_use=1", "kind=saved", "network=Lyman"}},
		{"POST", "/api/v1/internet/use", url.Values{"use": {"ethernet"}}, []string{"iface=eth0", "switching=0"}},
		{"GET", "/api/v1/wifi/scan?rescan=1", nil, []string{"ssid=Lyman", "band=5 GHz", "security=wpa", "saved=1", "supported=0"}},
		{"POST", "/api/v1/wifi/join", url.Values{"ssid": {"Lyman"}, "password": {"a-wifi-pass"}}, []string{"joining=Lyman"}},
		{"POST", "/api/v1/wifi/forget", url.Values{"ssid": {"Old"}}, []string{"forgot=Old"}},
		{"POST", "/api/v1/wifi/radio", url.Values{"on": {"1"}}, []string{"wifi_on=1"}},
		{"GET", "/api/v1/settings", nil, []string{"name=Palm Gateway", "adapter=hci0", "subnet=10.42.0.0/24", "tls=self-signed", "label=USB dongle 60:15:6F:EA:00:81 (hci0), in use now"}},
		{"POST", "/api/v1/network", url.Values{"uplink": {"wlan0"}}, []string{"changed=network.uplink", "applying=1"}},
		{"POST", "/api/v1/restart", url.Values{}, []string{"status=ok"}},
		{"GET", "/api/v1/devkeys", nil, []string{"count=0"}},
		{"GET", "/api/v1/logs?n=5&event=paired", nil, []string{"unit=bluetooth", "event=paired", "fields=name=Zoë's Palm TX"}},
		{"GET", "/api/v1/check", nil, []string{"board=Raspberry Pi 5 Model B Rev 1.0", "kind=service", "text=palm-gw-dnsmasq: failed", "ok=0", "kind=warning"}},
		{"POST", "/api/v1/capture", url.Values{"seconds": {"30"}}, []string{"capture=capture-20261112-110000.btsnoop"}},
		{"GET", "/api/v1/diagnostics", nil, []string{"capturing=1", "captures=1"}},
		{"POST", "/api/v1/bundle", url.Values{}, []string{"bundle=palm-gw-support-20261112-110000.tar.gz", "size=40960"}},
		{"GET", "/api/v1/about", nil, []string{"version=test", "bluez=5.82", "adapter_mode=dongle"}},
		{"POST", "/api/v1/update-check", url.Values{}, []string{"checked=0", "result=palm-gw upgrade: no release has been published yet"}},
		{"POST", "/api/v1/class-toggle", url.Values{}, []string{"class=0x000104", "previous=0x000300"}},
	} {
		path := c.path
		if strings.Contains(path, "?") {
			path += "&format=text"
		} else {
			path += "?format=text"
		}
		code, body, _ := a.do(a.lan.URL, c.method, path, tok, c.form)
		body = string([]rune(FromWindows1252(body)))
		if code != 200 || !strings.HasPrefix(body, "status=ok\r\n") {
			t.Errorf("%s %s: %d %q", c.method, c.path, code, body)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(body, w+"\r\n") {
				t.Errorf("%s %s lacks %q:\n%s", c.method, c.path, w, body)
			}
		}
	}
	// The password: the current one must be right.
	if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/password", tok, url.Values{"current": {"wrong"}, "new": {"another-pass-1"}}); code != 401 {
		t.Errorf("wrong current password = %d", code)
	}
	if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/password", tok, url.Values{"current": {"admin-pw"}, "new": {"short"}}); code != 400 {
		t.Errorf("short new password = %d", code)
	}
	if code, _, _ := a.do(a.lan.URL, "POST", "/api/v1/password", tok, url.Values{"current": {"admin-pw"}, "new": {"another-pass-1"}}); code != 200 {
		t.Errorf("password change = %d", code)
	}
	// A viewer reads but changes nothing.
	vt := a.login(a.lan.URL, "viewer", "viewer-pw")
	if code, _, _ := a.do(a.lan.URL, "GET", "/api/v1/nat", vt, nil); code != 200 {
		t.Errorf("viewer nat = %d", code)
	}
	for _, p := range []string{"/api/v1/devices/allow", "/api/v1/firewall/save", "/api/v1/firewall/test", "/api/v1/internet/use", "/api/v1/wifi/join",
		"/api/v1/wifi/forget", "/api/v1/wifi/radio", "/api/v1/network", "/api/v1/password", "/api/v1/restart", "/api/v1/devkeys/revoke",
		"/api/v1/capture", "/api/v1/bundle", "/api/v1/update-check", "/api/v1/class-toggle"} {
		if code, _, _ := a.do(a.lan.URL, "POST", p, vt, url.Values{}); code != 403 {
			t.Errorf("viewer POST %s = %d", p, code)
		}
	}
}
