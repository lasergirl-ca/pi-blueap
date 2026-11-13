package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/event"
)

// The /api/v1/ API for the Palm app and scripts (docs/WEB-UI.md section 6,
// D-058): a thin layer over the ctl commands, as palm-gwctl is. A login
// returns a bearer token, an in-memory session marked as an API one, so a
// token is not a cookie and a cookie is not a token. Replies are JSON, or
// with ?format=text key=value lines in Windows-1252 for Palm OS.

// kv is one field of a reply record; V is a string, int64, int or bool.
type kv struct {
	K string
	V any
}

// rec is a record with its fields in order.
type rec []kv

// reply is a head record and, for list replies, the items under a name.
type reply struct {
	head  rec
	name  string // "sessions", "devices", ... ("" = no list)
	items []rec
}

func (r rec) json(b *bytes.Buffer) {
	b.WriteByte('{')
	for i, f := range r {
		if i > 0 {
			b.WriteByte(',')
		}
		// No HTML escaping: this is an API, not a page ("<token>" stays as is).
		enc := json.NewEncoder(b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(f.K)
		b.Truncate(b.Len() - 1) // Encode ends with a newline
		b.WriteByte(':')
		if raw, ok := f.V.(json.RawMessage); ok {
			b.Write(raw)
		} else {
			_ = enc.Encode(f.V)
			b.Truncate(b.Len() - 1)
		}
	}
	b.WriteByte('}')
}

func (r rec) text(b *bytes.Buffer) {
	for _, f := range r {
		var v string
		switch x := f.V.(type) {
		case bool:
			v = "0"
			if x {
				v = "1"
			}
		case []string:
			v = strings.Join(x, " ")
		default:
			v = fmt.Sprint(x)
		}
		v = strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
		b.WriteString(f.K + "=" + v + "\r\n")
	}
}

// cp1252 has the code points of Windows-1252's 0x80-0x9F row; 0xA0-0xFF
// match Latin-1, so those runes map to their own value.
var cp1252 = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87, 'ˆ': 0x88,
	'‰': 0x89, 'Š': 0x8A, '‹': 0x8B, 'Œ': 0x8C, 'Ž': 0x8E, '‘': 0x91, '’': 0x92, '“': 0x93,
	'”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97, '˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B,
	'œ': 0x9C, 'ž': 0x9E, 'Ÿ': 0x9F,
}

// Windows1252 encodes s; characters it does not have become '?'.
func Windows1252(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch b, ok := cp1252[r]; {
		case r < 0x80 || (r >= 0xA0 && r <= 0xFF):
			out = append(out, byte(r))
		case ok:
			out = append(out, b)
		default:
			out = append(out, '?')
		}
	}
	return out
}

// FromWindows1252 turns text a Palm sent (its character set is Windows-1252)
// into UTF-8; text that is valid UTF-8 already is returned as is.
func FromWindows1252(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c < 0x80 || c >= 0xA0:
			b.WriteRune(rune(c))
		default:
			r := '?'
			for k, v := range cp1252 {
				if v == c {
					r = k
				}
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

func wantText(r *http.Request) bool { return r.URL.Query().Get("format") == "text" }

func (s *Server) apiWrite(w http.ResponseWriter, r *http.Request, code int, rep reply) {
	var b bytes.Buffer
	if wantText(r) {
		rep.head.text(&b)
		b.WriteString("\r\n")
		for _, it := range rep.items {
			it.text(&b)
			b.WriteString("\r\n")
		}
		w.Header().Set("Content-Type", "text/plain; charset=windows-1252")
		w.WriteHeader(code)
		_, _ = w.Write(Windows1252(b.String()))
		return
	}
	head := rep.head
	var items bytes.Buffer
	if rep.name != "" {
		items.WriteByte('[')
		for i, it := range rep.items {
			if i > 0 {
				items.WriteByte(',')
			}
			it.json(&items)
		}
		items.WriteByte(']')
		head = append(head, kv{rep.name, json.RawMessage(items.Bytes())})
	}
	head.json(&b)
	b.WriteByte('\n')
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(b.Bytes())
}

// apiError codes: login_required, forbidden, bad_request, not_found,
// too_many_requests, failed (the daemon refused or could not do it).
func (s *Server) apiError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	name := map[int]string{
		http.StatusUnauthorized: "login_required", http.StatusForbidden: "forbidden", http.StatusBadRequest: "bad_request",
		http.StatusNotFound: "not_found", http.StatusTooManyRequests: "too_many_requests", http.StatusConflict: "failed",
		http.StatusBadGateway: "failed",
	}[code]
	if name == "" {
		name = "failed"
	}
	if code == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="palm-gw"`)
	}
	s.apiWrite(w, r, code, reply{head: rec{{"error", name}, {"message", msg}}})
}

func (s *Server) apiOK(w http.ResponseWriter, r *http.Request, fields ...kv) {
	s.apiWrite(w, r, http.StatusOK, reply{head: append(rec{{"status", "ok"}}, fields...)})
}

func (s *Server) apiRoutes(mux *http.ServeMux, side Side) {
	get := func(h http.HandlerFunc) http.Handler { return s.apiAuth(side, h) }
	mux.HandleFunc("POST /api/v1/login", s.apiLogin(side))
	mux.Handle("POST /api/v1/logout", get(s.apiLogout))
	mux.Handle("GET /api/v1/status", get(s.apiStatus))
	mux.Handle("GET /api/v1/sessions", get(s.apiSessions))
	mux.Handle("GET /api/v1/devices", get(s.apiDevices))
	mux.Handle("GET /api/v1/me", get(s.apiMe))
	mux.Handle("GET /api/v1/events", get(s.apiEvents))
	mux.Handle("POST /api/v1/pair", get(s.apiPair))
	mux.Handle("POST /api/v1/pair/close", get(s.apiPairClose))
	mux.Handle("POST /api/v1/devices/{addr}/{action}", get(s.apiDeviceAction))
	mux.Handle("GET /api/v1/firewall", get(s.apiFirewall))
	mux.Handle("POST /api/v1/firewall/allow", get(s.apiFirewallAllow))
	mux.Handle("POST /api/v1/firewall/remove", get(s.apiFirewallRemove))
	mux.Handle("POST /api/v1/settings", get(s.apiSettings))
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.apiError(w, r, http.StatusNotFound, "no such API call: "+r.Method+" "+r.URL.Path)
	}))
}

// apiAuth requires an API token from this side in Authorization: Bearer.
// Viewers may POST only to log out. No CSRF token: a bearer token is never
// sent by a browser on its own.
func (s *Server) apiAuth(side Side, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var se *session
		if ok {
			se = s.sessions.get(strings.TrimSpace(tok), side)
		}
		if se == nil || !se.api {
			s.apiError(w, r, http.StatusUnauthorized, "log in first: POST /api/v1/login, then send Authorization: Bearer <token>")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && se.role != RoleAdmin && r.URL.Path != "/api/v1/logout" {
			s.apiError(w, r, http.StatusForbidden, "read-only account")
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, se)))
	})
}

func (s *Server) apiLogin(side Side) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if blocked, wait := s.limiter.blocked(side, ip); blocked {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			s.apiError(w, r, http.StatusTooManyRequests, "too many failed logins; wait a minute")
			return
		}
		if key := r.PostFormValue("device_key"); key != "" {
			s.apiKeyLogin(w, r, side, ip, key)
			return
		}
		user := r.PostFormValue("username")
		role := check(s.accounts(), user, r.PostFormValue("password"))
		if role == "" {
			n := s.limiter.failed(side, ip)
			s.log.Warn("api login failed", "event", "web_login_failed", "side", side, "ip", ip, "failures", n, "api", true)
			s.apiError(w, r, http.StatusUnauthorized, "wrong username or password")
			return
		}
		se := s.sessions.createAPI(user, role, side, s.o.SessionTTL)
		s.log.Info("api login", "event", "web_login", "side", side, "ip", ip, "user", user, "role", role, "api", true)
		fields := []kv{{"token", se.token}, {"expires", se.expires.UTC().Format(time.RFC3339)},
			{"expires_in", int64(se.expires.Sub(s.sessions.now()).Seconds())}, {"role", string(role)}}
		if v := r.PostFormValue("remember"); v == "1" || v == "true" {
			// A device key, so this Palm need not keep the password (D-063).
			var k ctl.DevKeyReply
			err := s.call(r.Context(), ctl.Request{Cmd: "devkey.issue", DevKey: &ctl.DevKeyRequest{
				User: user, Role: string(role), Name: FromWindows1252(r.PostFormValue("device"))}}, &k)
			if err != nil {
				fields = append(fields, kv{"device_key_error", err.Error()})
			} else {
				fields = append(fields, kv{"device_key", k.Key}, kv{"device_key_id", k.Entry.ID})
			}
		}
		s.apiOK(w, r, fields...)
	}
}

// apiKeyLogin logs in with a device key (D-063). The key must be live and
// its account must still exist with the same role; a refusal counts
// towards the login limit, as a wrong password does.
func (s *Server) apiKeyLogin(w http.ResponseWriter, r *http.Request, side Side, ip, key string) {
	var k ctl.DevKeyReply
	var role Role
	err := s.call(r.Context(), ctl.Request{Cmd: "devkey.check", DevKey: &ctl.DevKeyRequest{Key: key, IP: ip}}, &k)
	if err == nil {
		for _, a := range s.accounts() {
			if a.user == k.Entry.User && a.hash != "" && string(a.role) == k.Entry.Role {
				role = a.role
			}
		}
	}
	if role == "" {
		n := s.limiter.failed(side, ip)
		s.log.Warn("api login by device key refused", "event", "web_login_failed", "side", side, "ip", ip, "failures", n, "api", true, "device_key", k.Entry.ID)
		s.apiError(w, r, http.StatusUnauthorized, "this device key is unknown or revoked: log in with the password")
		return
	}
	se := s.sessions.createAPI(k.Entry.User, role, side, s.o.SessionTTL)
	s.log.Info("api login by device key", "event", "web_login", "side", side, "ip", ip, "user", k.Entry.User, "role", role,
		"api", true, "device_key", k.Entry.ID, "device", k.Entry.Name)
	s.apiOK(w, r, kv{"token", se.token}, kv{"expires", se.expires.UTC().Format(time.RFC3339)},
		kv{"expires_in", int64(se.expires.Sub(s.sessions.now()).Seconds())}, kv{"role", string(role)}, kv{"user", k.Entry.User})
}

func (s *Server) apiLogout(w http.ResponseWriter, r *http.Request) {
	se := sessionOf(r)
	s.sessions.drop(se.token)
	s.log.Info("api logout", "event", "web_logout", "side", se.side, "ip", clientIP(r), "user", se.user, "api", true)
	s.apiOK(w, r)
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.status(r.Context())
	if err != nil {
		s.apiError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	cfg := s.o.Config()
	f := rec{{"gateway", cfg.Bluetooth.Name}, {"version", st.Version}, {"uptime", st.UptimeSeconds}}
	if a := st.Adapter; a != nil {
		f = append(f, kv{"adapter", a.Address}, kv{"powered", a.Powered}, kv{"discoverable", a.Discoverable})
	} else {
		f = append(f, kv{"adapter", "none"})
	}
	f = append(f, kv{"sessions", len(st.Sessions)}, kv{"pan_clients", len(st.PAN.Clients)},
		kv{"pool_in_use", st.PoolInUse}, kv{"uplink", cfg.Network.Uplink}, kv{"pairing", st.PairingWindow.Open})
	if pw := st.PairingWindow; pw.Open {
		f = append(f, kv{"pairing_expires_in", int64(time.Until(pw.Expires).Seconds())}, kv{"pairing_one", pw.One})
		if sessionOf(r).role == RoleAdmin {
			f = append(f, kv{"pin", pw.PIN})
		}
	}
	s.apiOK(w, r, f...)
}

// sessionRecs are the PPP sessions and PAN clients as records.
func sessionRecs(st ctl.StatusReply) []rec {
	out := []rec{}
	for _, se := range st.Sessions {
		out = append(out, rec{{"name", se.Name}, {"addr", se.Addr}, {"profile", se.Profile}, {"ifname", se.Ifname},
			{"ip", se.Peer}, {"duration", int64(time.Since(se.Started).Seconds())}, {"rx_bytes", se.RxBytes}, {"tx_bytes", se.TxBytes}})
	}
	for _, c := range st.PAN.Clients {
		out = append(out, rec{{"name", c.Name}, {"addr", c.Addr}, {"profile", "pan"}, {"ifname", c.Ifname},
			{"ip", c.IP}, {"duration", int64(time.Since(c.Since).Seconds())}})
	}
	return out
}

func (s *Server) apiSessions(w http.ResponseWriter, r *http.Request) {
	st, err := s.status(r.Context())
	if err != nil {
		s.apiError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	items := sessionRecs(st)
	s.apiWrite(w, r, http.StatusOK, reply{head: rec{{"status", "ok"}, {"count", len(items)}}, name: "sessions", items: items})
}

func (s *Server) apiDevices(w http.ResponseWriter, r *http.Request) {
	st, err := s.status(r.Context())
	if err != nil {
		s.apiError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	items := []rec{}
	for _, d := range deviceRows(st) {
		if !d.Allowed {
			continue
		}
		name := d.Name
		if d.Label != "" {
			name = d.Label
		}
		lastSeen := ""
		if !d.LastSeen.IsZero() {
			lastSeen = d.LastSeen.UTC().Format(time.RFC3339)
		}
		items = append(items, rec{{"name", name}, {"addr", d.Address}, {"connected", d.Connected != ""},
			{"via", d.Connected}, {"last_seen", lastSeen}, {"source", d.Source}})
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: rec{{"status", "ok"}, {"count", len(items)}}, name: "devices", items: items})
}

// meRec finds the requester's own session or PAN lease by source address.
func meRec(st ctl.StatusReply, ip string) rec {
	for _, it := range sessionRecs(st) {
		for _, f := range it {
			if f.K == "ip" && f.V == ip && ip != "" {
				return it
			}
		}
	}
	return nil
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	st, err := s.status(r.Context())
	if err != nil {
		s.apiError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	me := meRec(st, clientIP(r))
	if me == nil {
		s.apiOK(w, r, kv{"session", "none"}, kv{"ip", clientIP(r)})
		return
	}
	s.apiOK(w, r, append(rec{{"session", "yes"}}, me...)...)
}

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	req := ctl.Request{Cmd: "events", NoWait: true}
	if since > 0 {
		req.Since = since
	} else {
		req.Recent = 20
	}
	var evs []event.Event
	err := s.o.Backend.Call(r.Context(), req, func(line json.RawMessage) error {
		var e event.Event
		if json.Unmarshal(line, &e) == nil && e.Event != "" {
			evs = append(evs, e)
		}
		return nil
	})
	if err != nil {
		s.apiError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	if len(evs) > 50 {
		evs = evs[len(evs)-50:]
	}
	items := []rec{}
	last := since
	for _, e := range evs {
		it := rec{{"seq", e.Seq}, {"time", e.Time.UTC().Format(time.RFC3339)}, {"event", e.Event}, {"addr", e.Addr}, {"name", e.Name}}
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k != "pin" { // never in an event list (D-052)
				it = append(it, kv{k, e.Fields[k]})
			}
		}
		items = append(items, it)
		last = max(last, e.Seq)
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: rec{{"status", "ok"}, {"last", last}, {"count", len(items)}}, name: "events", items: items})
}

func (s *Server) apiPair(w http.ResponseWriter, r *http.Request) {
	minutes, err := strconv.Atoi(r.PostFormValue("minutes"))
	if err != nil || minutes < 1 || minutes > 60 {
		minutes = s.o.Config().Bluetooth.PairWindowDefaultMinutes
	}
	one := r.PostFormValue("one") == "1" || r.PostFormValue("one") == "true"
	if err := s.openPairWindow(minutes, one); err != nil {
		s.apiError(w, r, http.StatusConflict, err.Error())
		return
	}
	st, err := s.status(r.Context())
	if err != nil || !st.PairingWindow.Open {
		s.apiError(w, r, http.StatusBadGateway, "the window did not stay open")
		return
	}
	s.log.Info("api: pairing window opened", "event", "web_pair_open", "user", sessionOf(r).user, "side", sessionOf(r).side, "minutes", minutes, "one", one)
	s.apiOK(w, r, kv{"pin", st.PairingWindow.PIN}, kv{"expires_in", int64(time.Until(st.PairingWindow.Expires).Seconds())}, kv{"one", one})
}

func (s *Server) apiPairClose(w http.ResponseWriter, r *http.Request) {
	var rep ctl.PairCloseReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "pair.close"}, &rep); err != nil {
		s.apiError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.apiOK(w, r, kv{"was_open", rep.WasOpen})
}

func (s *Server) apiDeviceAction(w http.ResponseWriter, r *http.Request) {
	addr, action := strings.ToUpper(r.PathValue("addr")), r.PathValue("action")
	if !btAddrRe.MatchString(addr) || (action != "disconnect" && action != "forget" && action != "rename") {
		s.apiError(w, r, http.StatusNotFound, "use /api/v1/devices/<XX:XX:XX:XX:XX:XX>/disconnect, forget or rename")
		return
	}
	req := ctl.Request{Cmd: action, Addr: addr}
	if action == "rename" {
		req.Label = strings.TrimSpace(r.PostFormValue("name"))
	}
	var rep json.RawMessage
	if err := s.call(r.Context(), req, &rep); err != nil {
		s.apiError(w, r, http.StatusConflict, err.Error())
		return
	}
	s.log.Info("api: device "+action, "event", "web_"+action, "user", sessionOf(r).user, "side", sessionOf(r).side, "addr", addr)
	s.apiOK(w, r, kv{"addr", addr}, kv{"action", action})
}

func ruleRecs(rules []config.AllowRule) []rec {
	out := []rec{}
	for i, r := range rules {
		out = append(out, rec{{"n", i + 1}, {"host", r.Host}, {"ports", r.Ports}, {"comment", r.Comment}})
	}
	return out
}

func (s *Server) apiFirewall(w http.ResponseWriter, r *http.Request) {
	rules, err := s.allowRules(r.Context())
	if err != nil {
		s.apiError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: rec{{"status", "ok"}, {"count", len(rules)}}, name: "rules", items: ruleRecs(rules)})
}

func (s *Server) apiSaveRules(w http.ResponseWriter, r *http.Request, rules []config.AllowRule) {
	if rules == nil {
		rules = []config.AllowRule{}
	}
	if _, err := s.setKey(r.Context(), "firewall.allow", rules); err != nil {
		s.apiError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("api: firewall rules saved", "event", "web_setting", "user", sessionOf(r).user, "key", "firewall.allow", "rules", len(rules))
	s.apiWrite(w, r, http.StatusOK, reply{head: rec{{"status", "ok"}, {"count", len(rules)}}, name: "rules", items: ruleRecs(rules)})
}

func (s *Server) apiFirewallAllow(w http.ResponseWriter, r *http.Request) {
	rule, err := ruleFromForm(r)
	if err != nil {
		s.apiError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	rules, err := s.allowRules(r.Context())
	if err != nil {
		s.apiError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.apiSaveRules(w, r, append(rules, rule))
}

// apiFirewallRemove removes rule n (1-based, as listed), or every rule for host.
func (s *Server) apiFirewallRemove(w http.ResponseWriter, r *http.Request) {
	rules, err := s.allowRules(r.Context())
	if err != nil {
		s.apiError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	var keep []config.AllowRule
	if v := r.PostFormValue("n"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > len(rules) {
			s.apiError(w, r, http.StatusBadRequest, fmt.Sprintf("n: there are %d rules", len(rules)))
			return
		}
		keep = append(append(keep, rules[:n-1]...), rules[n:]...)
	} else if host := strings.TrimSpace(r.PostFormValue("host")); host != "" {
		for _, ru := range rules {
			if ru.Host != host {
				keep = append(keep, ru)
			}
		}
		if len(keep) == len(rules) {
			s.apiError(w, r, http.StatusBadRequest, "no rule for "+host)
			return
		}
	} else {
		s.apiError(w, r, http.StatusBadRequest, "give n (the rule's number) or host")
		return
	}
	s.apiSaveRules(w, r, keep)
}

// apiSettings changes what the settings page's Gateway card changes: name,
// adapter, idle_seconds, ppp_debug. Fields left out stay as they are.
func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.apiError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	type change struct {
		key   string
		value any
	}
	var changes []change
	if v, ok := r.PostForm["name"]; ok {
		changes = append(changes, change{"bluetooth.name", strings.TrimSpace(v[0])})
	}
	if v, ok := r.PostForm["adapter"]; ok {
		changes = append(changes, change{"bluetooth.adapter", strings.TrimSpace(v[0])})
	}
	if v, ok := r.PostForm["idle_seconds"]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(v[0]))
		if err != nil || n < 0 {
			s.apiError(w, r, http.StatusBadRequest, "idle_seconds: a whole number of seconds, 0 for never")
			return
		}
		changes = append(changes, change{"ppp.idle_seconds", n})
	}
	if v, ok := r.PostForm["ppp_debug"]; ok {
		changes = append(changes, change{"ppp.debug", v[0] == "1" || v[0] == "true"})
	}
	if len(changes) == 0 {
		s.apiError(w, r, http.StatusBadRequest, "nothing to change: name, adapter, idle_seconds, ppp_debug")
		return
	}
	var saved, restart []string
	for _, c := range changes {
		rep, err := s.setKey(r.Context(), c.key, c.value)
		if err != nil {
			msg := err.Error()
			if len(saved) > 0 {
				msg = "saved " + strings.Join(saved, " ") + ", then: " + msg
			}
			s.apiError(w, r, http.StatusBadRequest, msg)
			return
		}
		saved = append(saved, c.key)
		restart = append(restart, rep.Reload.NeedsRestart...)
		s.log.Info("api: setting changed", "event", "web_setting", "user", sessionOf(r).user, "key", c.key)
	}
	s.apiOK(w, r, kv{"saved", saved}, kv{"needs_restart", dedupe(restart)})
}
