package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"palm-gw/internal/allow"
	"palm-gw/internal/ctl"
	"palm-gw/internal/event"
)

var btAddrRe = regexp.MustCompile(`^[0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){5}$`)

// routes adds the logged-in pages of part 2: dashboard, pairing, devices and
// the event stream (PLAN.md section 8.12).
func (s *Server) routes(mux *http.ServeMux, side Side) {
	a := func(h http.HandlerFunc) http.Handler { return s.auth(side, h) }
	mux.Handle("GET /{$}", a(s.dashboard))
	mux.Handle("GET /partials/dashboard", a(s.dashboardPartial))
	mux.Handle("GET /pair", a(s.pairPage))
	mux.Handle("POST /pair", a(s.pairOpen))
	mux.Handle("POST /pair/close", a(s.pairClose))
	mux.Handle("GET /events", a(s.eventStream))
	mux.Handle("GET /devices", a(s.devicesPage))
	mux.Handle("POST /devices/allow", a(s.deviceAllow))
	mux.Handle("GET /devices/{addr}/forget", a(s.forgetConfirm))
	mux.Handle("POST /devices/{addr}/forget", a(s.deviceAction("forget")))
	mux.Handle("POST /devices/{addr}/disconnect", a(s.deviceAction("disconnect")))
	mux.Handle("POST /devices/{addr}/rename", a(s.deviceAction("rename")))
}

// status fetches the daemon's status.
func (s *Server) status(ctx context.Context) (ctl.StatusReply, error) {
	var st ctl.StatusReply
	err := s.call(ctx, ctl.Request{Cmd: "status"}, &st)
	return st, err
}

// recentEvents is the newest n events from the daemon's history, newest
// first, without PINs.
func (s *Server) recentEvents(ctx context.Context, n int) []event.Event {
	var out []event.Event
	_ = s.o.Backend.Call(ctx, ctl.Request{Cmd: "events", Recent: n, NoWait: true}, func(line json.RawMessage) error {
		var e event.Event
		if json.Unmarshal(line, &e) == nil {
			delete(e.Fields, "pin")
			out = append(out, e)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	return out
}

func (s *Server) dashboardData(r *http.Request) map[string]any {
	data := s.page(r, "Dashboard")
	st, err := s.status(r.Context())
	if err != nil {
		data["Error"] = err.Error()
	}
	data["Status"] = st
	data["Events"] = s.recentEvents(r.Context(), 10)
	if sessionOf(r).side == SideBluetooth { // "This device" (docs/WEB-UI.md section 5)
		data["Me"], data["MeIP"] = meRec(st, clientIP(r)), clientIP(r)
	}
	return data
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "dashboard", s.dashboardData(r))
}

// dashboardPartial is the body htmx swaps in every 3 s.
func (s *Server) dashboardPartial(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "dash-body", s.dashboardData(r))
}

// pairRunner keeps a pairing window the web UI opened alive after the POST
// that opened it: the window belongs to a ctl "pair" command, which closes it
// when its caller goes away, so the caller here is the server, not the
// request. The window ends on timeout, after one device, or on /pair/close.
type pairRunner struct {
	mu      sync.Mutex
	running bool
}

func (s *Server) pairPage(w http.ResponseWriter, r *http.Request) {
	data := s.page(r, "Pair a device")
	st, err := s.status(r.Context())
	if err != nil {
		data["Error"] = err.Error()
	}
	data["Status"] = st
	data["Window"] = st.PairingWindow
	data["Minutes"] = []int{1, 2, 5, 10, 15, 30}
	data["Default"] = s.o.Config().Bluetooth.PairWindowDefaultMinutes
	// Class of Device: what the adapter shows now, and what the toggle
	// switches it to (the major and minor class; BlueZ adds service bits).
	bt := s.o.Config().Bluetooth
	next := bt.ClassFallback
	if st.Adapter != nil {
		data["ClassNow"] = fmt.Sprintf("0x%06X", st.Adapter.Class)
		if st.Adapter.Class&0x1FFC == bt.ClassFallback&0x1FFC {
			next = bt.Class
		}
	}
	data["ClassNext"] = fmt.Sprintf("0x%06X", next)
	data["ClassNextIsFallback"] = next == bt.ClassFallback
	if msg := r.URL.Query().Get("error"); msg != "" {
		data["Error"] = msg
	}
	s.render(w, r, http.StatusOK, "pair", data)
}

func (s *Server) pairOpen(w http.ResponseWriter, r *http.Request) {
	minutes, err := strconv.Atoi(r.PostFormValue("minutes"))
	if err != nil || minutes < 1 || minutes > 60 {
		minutes = s.o.Config().Bluetooth.PairWindowDefaultMinutes
	}
	one := r.PostFormValue("one") != ""
	if err := s.openPairWindow(minutes, one); err != nil {
		s.log.Warn("web: pairing window not opened", "err", err)
		data := s.page(r, "Pair a device")
		data["Error"] = err.Error()
		data["Minutes"] = []int{1, 2, 5, 10, 15, 30}
		data["Default"] = minutes
		s.render(w, r, http.StatusConflict, "pair", data)
		return
	}
	s.log.Info("web: pairing window opened", "event", "web_pair_open", "user", sessionOf(r).user, "side", sessionOf(r).side, "minutes", minutes, "one", one)
	http.Redirect(w, r, "/pair", http.StatusSeeOther)
}

// openPairWindow starts a ctl "pair" command owned by the server and waits
// for its first line (window_open) or its error.
func (s *Server) openPairWindow(minutes int, one bool) error {
	s.pair.mu.Lock()
	if s.pair.running {
		s.pair.mu.Unlock()
		return errors.New("a pairing window opened here is already open")
	}
	s.pair.running = true
	s.pair.mu.Unlock()

	first := make(chan error, 1)
	go func() {
		defer func() {
			s.pair.mu.Lock()
			s.pair.running = false
			s.pair.mu.Unlock()
		}()
		got := false
		err := s.o.Backend.Call(s.ctx, ctl.Request{Cmd: "pair", Minutes: minutes, One: one}, func(json.RawMessage) error {
			if !got {
				got = true
				first <- nil
			}
			return nil
		})
		if !got {
			if err == nil {
				err = errors.New("the daemon ended the pairing command without opening a window")
			}
			first <- err
		}
	}()
	select {
	case err := <-first:
		return err
	case <-time.After(15 * time.Second):
		return errors.New("timed out waiting for the pairing window to open")
	}
}

func (s *Server) pairClose(w http.ResponseWriter, r *http.Request) {
	var rep ctl.PairCloseReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "pair.close"}, &rep); err != nil {
		http.Redirect(w, r, "/pair?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/pair", http.StatusSeeOther)
}

// eventStream is the server-sent event feed of daemon events (the pairing
// page's live list). It starts with up to ?recent= past events (default 20),
// or those after Last-Event-ID on a reconnect. PINs are never sent.
func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	req := ctl.Request{Cmd: "events", Recent: 20}
	if n, err := strconv.Atoi(r.URL.Query().Get("recent")); err == nil && n >= 0 && n <= 200 {
		req.Recent = n
	}
	if id, err := strconv.ParseUint(r.Header.Get("Last-Event-ID"), 10, 64); err == nil {
		req.Recent, req.Since = 0, id
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()

	var mu sync.Mutex
	write := func(b []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if _, err := w.Write(b); err != nil {
			return err
		}
		return rc.Flush()
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() { // a comment line keeps idle connections open through proxies and NAT
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if write([]byte(": keepalive\n\n")) != nil {
					cancel()
					return
				}
			}
		}
	}()
	_ = s.o.Backend.Call(ctx, req, func(line json.RawMessage) error {
		var e event.Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil
		}
		delete(e.Fields, "pin")
		b, _ := json.Marshal(e)
		return write([]byte(fmt.Sprintf("id: %d\ndata: %s\n\n", e.Seq, b)))
	})
}

// deviceRow is one line of the devices page: an allowlist entry or a device
// BlueZ knows, with what is connected now.
type deviceRow struct {
	Address     string
	Name        string
	Label       string
	Source      string // "paired here", "config", "BlueZ only"
	Allowed     bool
	Dynamic     bool
	FirstPaired time.Time
	LastSeen    time.Time
	Trusted     bool
	Connected   string // "lap ppp0 10.42.0.10", "PAN bnep0", "" when not
}

func deviceRows(st ctl.StatusReply) []deviceRow {
	rows := map[string]*deviceRow{}
	get := func(addr string) *deviceRow {
		if r := rows[addr]; r != nil {
			return r
		}
		r := &deviceRow{Address: addr}
		rows[addr] = r
		return r
	}
	for _, e := range st.Allowlist {
		r := get(e.Address)
		r.Allowed = true
		r.Name, r.Label = e.Name, e.Label
		r.FirstPaired, r.LastSeen = e.FirstPaired, e.LastSeen
		if e.Source == allow.SourceDynamic {
			r.Dynamic, r.Source = true, "paired here"
		} else {
			r.Source = "config"
		}
	}
	for _, d := range st.Devices {
		r := get(d.Address)
		if r.Name == "" {
			r.Name = d.DisplayName()
		}
		if r.Source == "" {
			r.Source = "BlueZ only"
		}
		r.Trusted = d.Trusted
	}
	for _, se := range st.Sessions {
		r := get(se.Addr)
		r.Connected = strings.TrimSpace(fmt.Sprintf("%s %s %s", se.Profile, se.Ifname, se.Peer))
	}
	for _, c := range st.PAN.Clients {
		r := get(c.Addr)
		r.Connected = strings.TrimSpace(fmt.Sprintf("PAN %s %s", c.Ifname, c.IP))
	}
	out := make([]deviceRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Allowed != out[j].Allowed {
			return out[i].Allowed
		}
		return out[i].Address < out[j].Address
	})
	return out
}

var doneMessages = map[string]string{
	"disconnect": "Disconnected %s.",
	"forget":     "Forgot %s: removed from the allowlist and from BlueZ.",
	"rename":     "Renamed %s.",
	"allow":      "Allowlisted %s.",
}

func (s *Server) devicesPage(w http.ResponseWriter, r *http.Request) {
	s.renderDevices(w, r, http.StatusOK, "")
}

func (s *Server) renderDevices(w http.ResponseWriter, r *http.Request, code int, errMsg string) {
	data := s.page(r, "Devices")
	st, err := s.status(r.Context())
	if err != nil && errMsg == "" {
		errMsg = err.Error()
	}
	data["Rows"] = deviceRows(st)
	data["Error"] = errMsg
	q := r.URL.Query()
	if f, ok := doneMessages[q.Get("done")]; ok && btAddrRe.MatchString(q.Get("addr")) {
		data["Done"] = fmt.Sprintf(f, strings.ToUpper(q.Get("addr")))
	}
	s.render(w, r, code, "devices", data)
}

func (s *Server) deviceAllow(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimSpace(r.PostFormValue("addr"))
	if !btAddrRe.MatchString(addr) {
		s.renderDevices(w, r, http.StatusBadRequest, fmt.Sprintf("%q is not a Bluetooth address (XX:XX:XX:XX:XX:XX).", addr))
		return
	}
	var rep ctl.AllowReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "allow", Addr: addr, Name: strings.TrimSpace(r.PostFormValue("name"))}, &rep); err != nil {
		s.renderDevices(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("web: device allowlisted", "event", "web_allow", "user", sessionOf(r).user, "addr", rep.Address)
	http.Redirect(w, r, "/devices?done=allow&addr="+urlQuery(rep.Address), http.StatusSeeOther)
}

func (s *Server) forgetConfirm(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("addr")
	if !btAddrRe.MatchString(addr) {
		http.NotFound(w, r)
		return
	}
	data := s.page(r, "Forget "+addr)
	data["Addr"] = strings.ToUpper(addr)
	st, _ := s.status(r.Context())
	for _, row := range deviceRows(st) {
		if strings.EqualFold(row.Address, addr) {
			data["Row"] = row
		}
	}
	s.render(w, r, http.StatusOK, "forget", data)
}

// deviceAction runs disconnect, forget or rename for /devices/{addr}/...
func (s *Server) deviceAction(cmd string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		addr := r.PathValue("addr")
		if !btAddrRe.MatchString(addr) {
			http.NotFound(w, r)
			return
		}
		req := ctl.Request{Cmd: cmd, Addr: addr}
		if cmd == "rename" {
			req.Label = strings.TrimSpace(r.PostFormValue("label"))
		}
		var rep json.RawMessage
		if err := s.call(r.Context(), req, &rep); err != nil {
			s.renderDevices(w, r, http.StatusBadRequest, fmt.Sprintf("%s %s: %v", cmd, strings.ToUpper(addr), err))
			return
		}
		s.log.Info("web: device "+cmd, "event", "web_"+cmd, "user", sessionOf(r).user, "side", sessionOf(r).side, "addr", strings.ToUpper(addr))
		http.Redirect(w, r, "/devices?done="+cmd+"&addr="+urlQuery(strings.ToUpper(addr)), http.StatusSeeOther)
	}
}
