package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"palm-gw/internal/agent"
	"palm-gw/internal/allow"
	"palm-gw/internal/bluez"
	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/event"
	"palm-gw/internal/ppp"
)

// daemonFake stands in for palm-gwd's control handlers: a status with one
// paired Palm in a session, a pairing window that the pair command opens and
// pair.close closes, and an event history.
type daemonFake struct {
	mu     sync.Mutex
	reqs   []ctl.Request
	window agent.State
	closed chan struct{}
	events []event.Event
}

func newDaemonFake() *daemonFake {
	return &daemonFake{events: []event.Event{
		{Seq: 1, Time: time.Now(), Event: "paired", Addr: "00:07:E0:3C:5E:FA", Name: "Zoë's Palm TX"},
		{Seq: 2, Time: time.Now(), Event: "display_pin", Fields: map[string]string{"pin": "4821"}},
	}}
}

func (f *daemonFake) last(cmd string) (ctl.Request, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if f.reqs[i].Cmd == cmd {
			return f.reqs[i], true
		}
	}
	return ctl.Request{}, false
}

func (f *daemonFake) Call(ctx context.Context, req ctl.Request, emit func(json.RawMessage) error) error {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	send := func(v any) error {
		b, _ := json.Marshal(v)
		return emit(b)
	}
	switch req.Cmd {
	case "status":
		f.mu.Lock()
		w := f.window
		f.mu.Unlock()
		return send(ctl.StatusReply{
			Version:       "test",
			PairingWindow: w,
			Adapter:       &ctl.AdapterStatus{HCI: "hci0", HWBus: "usb", AdapterProps: bluez.AdapterProps{Address: "60:15:6F:EA:00:81", Alias: "Palm Gateway", Powered: true}},
			Allowlist: []allow.Entry{
				{Address: "00:07:E0:3C:5E:FA", Source: allow.SourceDynamic, Name: "Zoë's Palm TX", FirstPaired: time.Now().Add(-time.Hour)},
				{Address: "11:22:33:44:55:66", Source: allow.SourceStatic},
			},
			Devices:  []bluez.DeviceInfo{{Address: "AA:BB:CC:DD:EE:FF", Name: "Laptop", Paired: true}},
			Sessions: []ppp.Info{{Addr: "00:07:E0:3C:5E:FA", Name: "Zoë's Palm TX", Profile: "lap", Ifname: "ppp0", Peer: "10.42.0.10", Started: time.Now().Add(-90 * time.Second), TxBytes: 580020, RxBytes: 4096}},
		})
	case "pair":
		f.mu.Lock()
		if f.window.Open {
			f.mu.Unlock()
			return errors.New("a pairing window is already open")
		}
		f.window = agent.State{Open: true, PIN: "4821", Expires: time.Now().Add(time.Duration(req.Minutes) * time.Minute), One: req.One}
		f.closed = make(chan struct{})
		closed := f.closed
		f.mu.Unlock()
		if err := send(ctl.PairEvent{Event: "window_open", PIN: "4821"}); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-closed:
		}
		return send(ctl.PairEvent{Event: "window_closed", Reason: "cancelled"})
	case "pair.close":
		f.mu.Lock()
		was := f.window.Open
		f.window = agent.State{}
		if f.closed != nil {
			close(f.closed)
			f.closed = nil
		}
		f.mu.Unlock()
		return send(ctl.PairCloseReply{OK: true, WasOpen: was})
	case "events":
		for _, e := range f.events {
			if err := send(e); err != nil {
				return nil
			}
		}
		if req.NoWait {
			return nil
		}
		<-ctx.Done()
		return nil
	case "disconnect":
		return send(ctl.DisconnectReply{OK: true, Address: req.Addr, Sessions: 1})
	case "forget":
		return send(ctl.ForgetReply{OK: true, Address: req.Addr})
	case "rename":
		if req.Addr == "11:22:33:44:55:66" {
			return errors.New("state: 11:22:33:44:55:66 is not in the allowlist")
		}
		return send(ctl.RenameReply{OK: true, Address: req.Addr, Label: req.Label})
	case "allow":
		return send(ctl.AllowReply{OK: true, Address: strings.ToUpper(req.Addr), Added: true})
	}
	return ctl.ErrUnknownCommand
}

// loggedIn starts a LAN server on the fake daemon and logs in as user.
func loggedIn(t *testing.T, user, pw string) (*daemonFake, *httptest.Server, *http.Client, string) {
	t.Helper()
	ah, vh := hashes(t)
	cfg := config.Default()
	cfg.Web.PasswordHash = ah
	cfg.Web.ViewerUsername, cfg.Web.ViewerPasswordHash = "viewer", vh
	f := newDaemonFake()
	s, err := New(Options{Config: func() *config.Config { return cfg }, Backend: f})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler(SideLAN, false))
	t.Cleanup(ts.Close)
	c := client(t)
	if r := login(t, c, ts.URL, user, pw); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("login %s: %d", user, r.StatusCode)
	}
	_, page, _ := get(t, c, ts.URL+"/")
	return f, ts, c, csrfFrom(t, page)
}

func post(t *testing.T, c *http.Client, u string, v url.Values) (int, string, string) {
	t.Helper()
	resp, err := c.PostForm(u, v)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var b strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		b.WriteString(sc.Text() + "\n")
	}
	return resp.StatusCode, resp.Header.Get("Location"), b.String()
}

func TestDashboard(t *testing.T) {
	f, ts, c, _ := loggedIn(t, "admin", "admin-pw")
	code, page, _ := get(t, c, ts.URL+"/")
	for _, want := range []string{"60:15:6F:EA:00:81", "Zoë&#39;s Palm TX", "ppp0", "10.42.0.10", "566.4 KB", `hx-get="/partials/dashboard"`, "paired", "/static/htmx.min.js"} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard (%d) lacks %q", code, want)
		}
	}
	if strings.Contains(page, "4821") {
		t.Error("a PIN from the event history reached the dashboard")
	}
	if r, ok := f.last("events"); !ok || r.Recent != 10 || !r.NoWait {
		t.Errorf("events request %+v", r)
	}
	code, part, _ := get(t, c, ts.URL+"/partials/dashboard")
	if code != 200 || strings.Contains(part, "<html") || !strings.Contains(part, "ppp0") {
		t.Errorf("partial %d:\n%s", code, part)
	}
}

func TestPairOpenAndClose(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "admin", "admin-pw")
	_, page, _ := get(t, c, ts.URL+"/pair")
	if !strings.Contains(page, "Open pairing window") {
		t.Fatalf("closed pair page:\n%s", page)
	}
	code, loc, _ := post(t, c, ts.URL+"/pair", url.Values{"csrf": {tok}, "minutes": {"2"}, "one": {"1"}})
	if code != http.StatusSeeOther || loc != "/pair" {
		t.Fatalf("open: %d %q", code, loc)
	}
	if r, _ := f.last("pair"); r.Minutes != 2 || !r.One {
		t.Errorf("pair request %+v", r)
	}
	_, page, _ = get(t, c, ts.URL+"/pair")
	if !strings.Contains(page, "4 8 2 1") || !strings.Contains(page, "data-expires=") || !strings.Contains(page, "or after one device pairs") {
		t.Errorf("open pair page:\n%s", page)
	}
	// The window stays open after the request that opened it has ended.
	time.Sleep(50 * time.Millisecond)
	if f.window.Open != true {
		t.Fatal("the window closed with the request")
	}
	// A second window is refused with a message.
	code, _, body := post(t, c, ts.URL+"/pair", url.Values{"csrf": {tok}, "minutes": {"5"}})
	if code != http.StatusConflict || !strings.Contains(body, "already open") {
		t.Errorf("second window: %d", code)
	}
	code, loc, _ = post(t, c, ts.URL+"/pair/close", url.Values{"csrf": {tok}})
	if code != http.StatusSeeOther || loc != "/pair" {
		t.Errorf("close: %d %q", code, loc)
	}
	if f.window.Open {
		t.Error("window still open")
	}
	// Bad minutes fall back to the configured default.
	time.Sleep(50 * time.Millisecond)
	post(t, c, ts.URL+"/pair", url.Values{"csrf": {tok}, "minutes": {"999"}})
	if r, _ := f.last("pair"); r.Minutes != 5 {
		t.Errorf("minutes 999 became %d", r.Minutes)
	}
}

func TestViewerSeesNoPINAndCannotAct(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "viewer", "viewer-pw")
	f.window = agent.State{Open: true, PIN: "4821", Expires: time.Now().Add(time.Minute)}
	_, page, _ := get(t, c, ts.URL+"/pair")
	if strings.Contains(page, "4 8 2 1") || !strings.Contains(page, "admin account only") {
		t.Errorf("viewer pair page:\n%s", page)
	}
	_, page, _ = get(t, c, ts.URL+"/devices")
	if strings.Contains(page, "Forget") || strings.Contains(page, "Add by address") {
		t.Error("viewer devices page has admin actions")
	}
	for _, u := range []string{"/pair", "/pair/close", "/devices/00:07:E0:3C:5E:FA/forget", "/devices/allow"} {
		if code, _, _ := post(t, c, ts.URL+u, url.Values{"csrf": {tok}, "addr": {"00:07:E0:3C:5E:FA"}}); code != http.StatusForbidden {
			t.Errorf("viewer POST %s = %d", u, code)
		}
	}
	if _, ok := f.last("forget"); ok {
		t.Error("a viewer's forget reached the daemon")
	}
}

func TestDevices(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "admin", "admin-pw")
	_, page, _ := get(t, c, ts.URL+"/devices")
	for _, want := range []string{"00:07:E0:3C:5E:FA", "paired here", "11:22:33:44:55:66", "config", "AA:BB:CC:DD:EE:FF", "BlueZ only", "lap ppp0 10.42.0.10", "Disconnect", "Rename"} {
		if !strings.Contains(page, want) {
			t.Errorf("devices page lacks %q", want)
		}
	}

	// Forget asks first, then acts.
	code, page, _ := get(t, c, ts.URL+"/devices/00:07:E0:3C:5E:FA/forget")
	if code != 200 || !strings.Contains(page, "Forget 00:07:E0:3C:5E:FA?") {
		t.Errorf("confirm page %d", code)
	}
	if _, ok := f.last("forget"); ok {
		t.Error("GET forgot the device")
	}
	code, loc, _ := post(t, c, ts.URL+"/devices/00:07:E0:3C:5E:FA/forget", url.Values{"csrf": {tok}})
	if code != http.StatusSeeOther || !strings.Contains(loc, "done=forget") {
		t.Errorf("forget: %d %q", code, loc)
	}
	if r, _ := f.last("forget"); r.Addr != "00:07:E0:3C:5E:FA" {
		t.Errorf("forget request %+v", r)
	}
	_, page, _ = get(t, c, ts.URL+loc)
	if !strings.Contains(page, "Forgot 00:07:E0:3C:5E:FA") {
		t.Error("no confirmation after forget")
	}

	post(t, c, ts.URL+"/devices/00:07:E0:3C:5E:FA/disconnect", url.Values{"csrf": {tok}})
	if r, _ := f.last("disconnect"); r.Addr != "00:07:E0:3C:5E:FA" {
		t.Errorf("disconnect request %+v", r)
	}
	post(t, c, ts.URL+"/devices/00:07:E0:3C:5E:FA/rename", url.Values{"csrf": {tok}, "label": {"  T|X  "}})
	if r, _ := f.last("rename"); r.Label != "T|X" {
		t.Errorf("rename request %+v", r)
	}
	// A daemon error comes back on the page.
	code, _, body := post(t, c, ts.URL+"/devices/11:22:33:44:55:66/rename", url.Values{"csrf": {tok}, "label": {"x"}})
	if code != http.StatusBadRequest || !strings.Contains(body, "not in the allowlist") {
		t.Errorf("rename error: %d", code)
	}
	// Add by address checks the address before asking the daemon.
	code, _, body = post(t, c, ts.URL+"/devices/allow", url.Values{"csrf": {tok}, "addr": {"nonsense"}})
	if code != http.StatusBadRequest || !strings.Contains(body, "not a Bluetooth address") {
		t.Errorf("bad address: %d", code)
	}
	code, loc, _ = post(t, c, ts.URL+"/devices/allow", url.Values{"csrf": {tok}, "addr": {"de:ad:be:ef:00:01"}, "name": {"Treo"}})
	if r, _ := f.last("allow"); code != http.StatusSeeOther || r.Addr != "de:ad:be:ef:00:01" || r.Name != "Treo" {
		t.Errorf("allow: %d %+v", code, r)
	}
	if code, _ := getCode(t, c, ts.URL+"/devices/not-an-address/forget"); code != http.StatusNotFound {
		t.Errorf("bad path address: %d", code)
	}
	// Mutations need the CSRF token.
	if code, _, _ := post(t, c, ts.URL+"/devices/00:07:E0:3C:5E:FA/disconnect", nil); code != http.StatusForbidden {
		t.Errorf("disconnect without CSRF: %d", code)
	}
	_ = loc
}

func getCode(t *testing.T, c *http.Client, u string) (int, string) {
	code, body, _ := get(t, c, u)
	return code, body
}

// The event stream sends the history as SSE with ids and never a PIN.
func TestEventStream(t *testing.T) {
	_, ts, c, _ := loggedIn(t, "admin", "admin-pw")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events?recent=5", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	var lines []string
	for sc.Scan() && len(lines) < 6 {
		if sc.Text() != "" {
			lines = append(lines, sc.Text())
		}
	}
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "id: 1\ndata: {") || !strings.Contains(got, `"event":"paired"`) || !strings.Contains(got, "id: 2") {
		t.Errorf("stream:\n%s", got)
	}
	if strings.Contains(got, "4821") {
		t.Error("PIN in the event stream")
	}
}
