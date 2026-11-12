package web

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/setup"
)

var (
	hashOnce            sync.Once
	adminHash, viewHash string
)

func hashes(t *testing.T) (string, string) {
	t.Helper()
	hashOnce.Do(func() {
		var err error
		if adminHash, err = setup.HashPassword("admin-pw"); err != nil {
			t.Fatal(err)
		}
		if viewHash, err = setup.HashPassword("viewer-pw"); err != nil {
			t.Fatal(err)
		}
	})
	return adminHash, viewHash
}

// fakeBackend answers status like the daemon would, with an empty gateway.
type fakeBackend struct {
	mu   sync.Mutex
	reqs []ctl.Request
}

func (f *fakeBackend) Call(_ context.Context, req ctl.Request, emit func(json.RawMessage) error) error {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if req.Cmd == "status" {
		b, _ := json.Marshal(ctl.StatusReply{Version: "test"})
		return emit(b)
	}
	return ctl.ErrUnknownCommand
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newServer(t *testing.T, edit func(*config.Config)) (*Server, *clock) {
	t.Helper()
	ah, vh := hashes(t)
	cfg := config.Default()
	cfg.Web.Username = "admin"
	cfg.Web.PasswordHash = ah
	cfg.Web.ViewerUsername = "viewer"
	cfg.Web.ViewerPasswordHash = vh
	if edit != nil {
		edit(cfg)
	}
	// The real time, not a fixed date: the cookie jar expires cookies by the
	// real clock, so a fixed start turns into a failure once it has passed.
	clk := &clock{t: time.Now().UTC().Truncate(time.Second)}
	s, err := New(Options{Config: func() *config.Config { return cfg }, Backend: &fakeBackend{}, Now: clk.now, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return s, clk
}

// client is a browser with a cookie jar that does not follow redirects.
func client(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func login(t *testing.T, c *http.Client, base, user, pw string) *http.Response {
	t.Helper()
	resp, err := c.PostForm(base+"/login", url.Values{"username": {user}, "password": {pw}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func get(t *testing.T, c *http.Client, u string) (int, string, http.Header) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// csrfFrom pulls the hidden CSRF token out of a page.
func csrfFrom(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `name="csrf" value="`)
	if i < 0 {
		t.Fatalf("no CSRF token in page:\n%s", page)
	}
	rest := page[i+len(`name="csrf" value="`):]
	return rest[:strings.IndexByte(rest, '"')]
}

// Every page needs a login, on both sides (docs/WEB-UI.md section 1).
func TestRoutesNeedLogin(t *testing.T) {
	s, _ := newServer(t, nil)
	for _, side := range []Side{SideLAN, SideBluetooth} {
		ts := httptest.NewServer(s.Handler(side, false))
		c := client(t)
		code, _, h := get(t, c, ts.URL+"/")
		if code != http.StatusSeeOther || h.Get("Location") != "/login" {
			t.Errorf("%s: GET / = %d %q", side, code, h.Get("Location"))
		}
		resp, err := c.PostForm(ts.URL+"/logout", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: POST /logout without a session = %d", side, resp.StatusCode)
		}
		if code, _, h := get(t, c, ts.URL+"/login"); code != 200 || h.Get("Content-Security-Policy") == "" {
			t.Errorf("%s: GET /login = %d, CSP %q", side, code, h.Get("Content-Security-Policy"))
		}
		ts.Close()
	}
}

func TestLoginLogout(t *testing.T) {
	s, _ := newServer(t, nil)
	ts := httptest.NewServer(s.Handler(SideLAN, false))
	defer ts.Close()

	// A wrong username and a wrong password look the same.
	c := client(t)
	ru := login(t, c, ts.URL, "nobody", "admin-pw")
	rp := login(t, c, ts.URL, "admin", "wrong")
	if ru.StatusCode != http.StatusUnauthorized || rp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad logins: %d %d", ru.StatusCode, rp.StatusCode)
	}

	resp := login(t, c, ts.URL, "admin", "admin-pw")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("login: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	ck := resp.Cookies()[0]
	if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Secure {
		t.Errorf("cookie %+v (Secure must follow the listener: plain here)", ck)
	}
	code, page, _ := get(t, c, ts.URL+"/")
	if code != 200 || !strings.Contains(page, "Adapter") {
		t.Fatalf("dashboard %d:\n%s", code, page)
	}
	tok := csrfFrom(t, page)

	// Logout needs the CSRF token.
	r, _ := c.PostForm(ts.URL+"/logout", url.Values{"csrf": {"wrong"}})
	_ = r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("logout with a bad token = %d", r.StatusCode)
	}
	r, _ = c.PostForm(ts.URL+"/logout", url.Values{"csrf": {tok}})
	_ = r.Body.Close()
	if r.StatusCode != http.StatusSeeOther {
		t.Errorf("logout = %d", r.StatusCode)
	}
	if code, _, _ := get(t, c, ts.URL+"/"); code != http.StatusSeeOther {
		t.Errorf("after logout GET / = %d", code)
	}
}

// A session is bound to the side it logged in on.
func TestSessionStaysOnItsSide(t *testing.T) {
	s, _ := newServer(t, nil)
	lan := httptest.NewServer(s.Handler(SideLAN, false))
	defer lan.Close()
	bt := httptest.NewServer(s.Handler(SideBluetooth, false))
	defer bt.Close()
	c := client(t)
	resp := login(t, c, lan.URL, "admin", "admin-pw")
	req, _ := http.NewRequest("GET", bt.URL+"/", nil)
	req.AddCookie(resp.Cookies()[0])
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusSeeOther {
		t.Errorf("LAN cookie on the Bluetooth side: %d", r.StatusCode)
	}
}

func TestSessionExpires(t *testing.T) {
	s, clk := newServer(t, func(c *config.Config) { c.Web.SessionHours = 1 })
	ts := httptest.NewServer(s.Handler(SideLAN, false))
	defer ts.Close()
	c := client(t)
	login(t, c, ts.URL, "admin", "admin-pw")
	if code, _, _ := get(t, c, ts.URL+"/"); code != 200 {
		t.Fatalf("GET / = %d", code)
	}
	clk.add(61 * time.Minute)
	if code, _, _ := get(t, c, ts.URL+"/"); code != http.StatusSeeOther {
		t.Errorf("expired session: GET / = %d", code)
	}
}

func TestViewerIsReadOnly(t *testing.T) {
	s, _ := newServer(t, nil)
	ts := httptest.NewServer(s.Handler(SideBluetooth, false))
	defer ts.Close()
	c := client(t)
	if r := login(t, c, ts.URL, "viewer", "viewer-pw"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("viewer login %d", r.StatusCode)
	}
	_, page, _ := get(t, c, ts.URL+"/")
	if !strings.Contains(page, "(read-only)") {
		t.Error("the page does not say read-only")
	}
	if se := s.sessions.byID; len(se) != 1 {
		t.Fatalf("sessions %d", len(se))
	}
	for _, se := range s.sessions.byID {
		if se.role != RoleViewer {
			t.Errorf("role %q", se.role)
		}
	}
	// Mutating routes arrive in later parts; the 403 is checked in auth for any POST
	// other than /logout, exercised here with a route that does not exist yet.
	h := s.auth(SideBluetooth, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	var tok, cookie string
	for _, se := range s.sessions.byID {
		tok, cookie = se.csrf, se.token
	}
	req := httptest.NewRequest("POST", "/pair", strings.NewReader("csrf="+tok))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer POST = %d", rec.Code)
	}
}

// Five failures per minute per side and source, then 429 even for the right
// password; the other side is not affected; a minute later it works again.
func TestRateLimit(t *testing.T) {
	s, clk := newServer(t, nil)
	lan := httptest.NewServer(s.Handler(SideLAN, false))
	defer lan.Close()
	bt := httptest.NewServer(s.Handler(SideBluetooth, false))
	defer bt.Close()
	c := client(t)
	for i := 0; i < maxFailures; i++ {
		if r := login(t, c, lan.URL, "admin", "wrong"); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i+1, r.StatusCode)
		}
	}
	r := login(t, c, lan.URL, "admin", "admin-pw")
	if r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" {
		t.Errorf("sixth try: %d, Retry-After %q", r.StatusCode, r.Header.Get("Retry-After"))
	}
	if r := login(t, client(t), bt.URL, "admin", "admin-pw"); r.StatusCode != http.StatusSeeOther {
		t.Errorf("Bluetooth side blocked too: %d", r.StatusCode)
	}
	clk.add(61 * time.Second)
	if r := login(t, c, lan.URL, "admin", "admin-pw"); r.StatusCode != http.StatusSeeOther {
		t.Errorf("after a minute: %d", r.StatusCode)
	}
}

// allow_cidrs applies to the TCP peer only; headers cannot claim another
// address (docs/WEB-UI.md section 1).
func TestAllowCIDRsAndSpoofing(t *testing.T) {
	s, _ := newServer(t, func(c *config.Config) { c.Web.AllowCIDRs = []string{"192.168.1.0/24"} })
	ts := httptest.NewServer(s.Handler(SideLAN, false))
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/login", nil)
	req.Header.Set("X-Forwarded-For", "192.168.1.5")
	req.Header.Set("X-Real-IP", "192.168.1.5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("127.0.0.1 outside allow_cidrs: %d", resp.StatusCode)
	}
	// The Bluetooth side ignores allow_cidrs.
	bt := httptest.NewServer(s.Handler(SideBluetooth, false))
	defer bt.Close()
	if code, _, _ := get(t, client(t), bt.URL+"/login"); code != 200 {
		t.Errorf("Bluetooth side with allow_cidrs: %d", code)
	}
}

func TestNoPasswordSet(t *testing.T) {
	s, _ := newServer(t, func(c *config.Config) {
		c.Web.PasswordHash = ""
		c.Web.ViewerUsername = ""
		c.Web.ViewerPasswordHash = ""
	})
	ts := httptest.NewServer(s.Handler(SideLAN, false))
	defer ts.Close()
	_, page, _ := get(t, client(t), ts.URL+"/login")
	if !strings.Contains(page, "palm-gw web set-password") {
		t.Errorf("login page:\n%s", page)
	}
	if r := login(t, client(t), ts.URL, "admin", ""); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("empty password with no hash: %d", r.StatusCode)
	}
}

func TestConnectedPage(t *testing.T) {
	s, _ := newServer(t, nil)
	s.o.TLS = &tls.Config{} // the admin link needs the TLS-only Bluetooth side
	ts := httptest.NewServer(s.Handler(SideConnected, false))
	defer ts.Close()
	code, page, _ := get(t, client(t), ts.URL+"/")
	if code != 200 || !strings.Contains(page, "You are connected") || !strings.Contains(page, "127.0.0.1") ||
		!strings.Contains(page, "https://10.42.0.1:8042/") {
		t.Errorf("connected page %d:\n%s", code, page)
	}
	if code, _, _ := get(t, client(t), ts.URL+"/login"); code != http.StatusNotFound {
		t.Errorf("port 80 serves /login: %d", code)
	}
}

// The Bluetooth listener is TLS 1.3 only, and plain HTTP gets no answer at
// all, not even net/http's "client sent an HTTP request to an HTTPS server".
func TestBluetoothListenerTLSOnly(t *testing.T) {
	s, _ := newServer(t, nil)
	dir := t.TempDir()
	if _, _, err := setup.EnsureCert(dir, setup.CertNames{IPs: []net.IP{net.IPv4(127, 0, 0, 1)}}); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	s.o.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	b, err := s.bind(bindSpec{Addr: "127.0.0.1:0", Side: SideBluetooth, TLS: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.srv.Close() }()
	addr := waitAddr(t, b)

	// Plain HTTP: zero bytes back, then the connection closes.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: 10.42.0.1\r\n\r\n"))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, _ := io.ReadAll(conn)
	_ = conn.Close()
	if len(got) != 0 {
		t.Errorf("plain HTTP got an answer: %q", got)
	}

	// TLS 1.2 is refused; TLS 1.3 serves the login page.
	c12 := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}}}
	if resp, err := c12.Get("https://" + addr + "/login"); err == nil {
		_ = resp.Body.Close()
		t.Error("TLS 1.2 accepted on the Bluetooth side")
	}
	c13 := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := c13.Get("https://" + addr + "/login")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.TLS == nil || resp.TLS.Version != tls.VersionTLS13 {
		t.Errorf("TLS 1.3: %d %+v", resp.StatusCode, resp.TLS)
	}
}

// waitAddr finds the port bind chose for ":0" by asking the server.
func waitAddr(t *testing.T, b *binding) string {
	t.Helper()
	for i := 0; i < 50; i++ {
		if a := b.addr(); a != "" {
			return a
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("listener has no address")
	return ""
}

func TestWanted(t *testing.T) {
	addrs := []IfAddr{
		{"lo", net.IPv4(127, 0, 0, 1)},
		{"eth0", net.IPv4(10, 4, 1, 118)},
		{"wlan0", net.IPv4(10, 4, 0, 243)},
		{"br-palm", net.IPv4(10, 42, 0, 1)},
		{"ppp0", net.IPv4(10, 42, 0, 1)},
		{"bnep0", net.IPv4(169, 254, 3, 3)},
	}
	o := Options{Listen: []string{"0.0.0.0:8042"}, Gateway: "10.42.0.1", Bridge: "br-palm", BluetoothSide: true, TLS: &tls.Config{}, ConnectedPort: 80}
	got := wanted(o, addrs)
	want := []bindSpec{
		{"10.4.0.243:8042", SideLAN, true},
		{"10.4.1.118:8042", SideLAN, true},
		{"10.42.0.1:80", SideConnected, false},
		{"10.42.0.1:8042", SideBluetooth, true},
		{"127.0.0.1:8042", SideLAN, true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] %v, want %v", i, got[i], want[i])
		}
	}

	// A specific listen address; TLS off drops the Bluetooth side; no
	// gateway address yet means no gateway listeners.
	o = Options{Listen: []string{"10.4.1.118:8042"}, Gateway: "10.42.0.1", Bridge: "br-palm", BluetoothSide: true, ConnectedPort: 80}
	got = wanted(o, addrs)
	if len(got) != 2 || got[0] != (bindSpec{"10.4.1.118:8042", SideLAN, false}) || got[1] != (bindSpec{"10.42.0.1:80", SideConnected, false}) {
		t.Errorf("tls off: %v", got)
	}
	got = wanted(o, addrs[:3])
	if len(got) != 1 || got[0].Side != SideLAN {
		t.Errorf("bridge down: %v", got)
	}
}

// After a restart (or expiry) an htmx refresh must send the browser to the
// login page, not swap the login page into the dashboard (Zoe's screenshot,
// 2026-11-12).
func TestHtmxRefreshWithoutSession(t *testing.T) {
	s, _ := newServer(t, nil)
	ts := httptest.NewServer(s.Handler(SideLAN, false))
	defer ts.Close()
	for _, p := range []string{"/partials/dashboard", "/partials/nat", "/partials/logs", "/partials/internet-job"} {
		req, _ := http.NewRequest("GET", ts.URL+p, nil)
		req.Header.Set("HX-Request", "true")
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("HX-Redirect") != "/login" {
			t.Errorf("%s: %d HX-Redirect=%q", p, resp.StatusCode, resp.Header.Get("HX-Redirect"))
		}
	}
}
