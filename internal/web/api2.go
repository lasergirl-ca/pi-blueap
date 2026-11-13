package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"palm-gw/internal/ctl"
	"palm-gw/internal/setup"
)

// The rest of /api/v1: everything the web pages do, so the Palm app can do
// it too (Zoe: "1:1 feature parity", D-064). Each call is the ctl command
// its page uses. Downloads (btmon captures, support bundles) stay in the
// browser: the app can start them, not fetch them (WEB-UI section 5).

func (s *Server) apiRoutes2(mux *http.ServeMux, side Side) {
	a := func(h http.HandlerFunc) http.Handler { return s.apiAuth(side, h) }
	mux.Handle("POST /api/v1/devices/allow", a(s.apiDeviceAllow))
	mux.Handle("POST /api/v1/firewall/save", a(s.apiFirewallSave))
	mux.Handle("POST /api/v1/firewall/test", a(s.apiFirewallTest))
	mux.Handle("GET /api/v1/firewall/counters", a(s.apiCounters))
	mux.Handle("GET /api/v1/nat", a(s.apiNAT))
	mux.Handle("GET /api/v1/internet", a(s.apiInternet))
	mux.Handle("POST /api/v1/internet/use", a(s.apiInternetUse))
	mux.Handle("GET /api/v1/wifi/scan", a(s.apiWifiScan))
	mux.Handle("POST /api/v1/wifi/join", a(s.apiWifiJoin))
	mux.Handle("POST /api/v1/wifi/forget", a(s.apiWifiForget))
	mux.Handle("POST /api/v1/wifi/radio", a(s.apiWifiRadio))
	mux.Handle("GET /api/v1/settings", a(s.apiSettingsGet))
	mux.Handle("POST /api/v1/network", a(s.apiNetwork))
	mux.Handle("POST /api/v1/password", a(s.apiPassword))
	mux.Handle("POST /api/v1/restart", a(s.apiRestart))
	mux.Handle("GET /api/v1/devkeys", a(s.apiDevKeys))
	mux.Handle("POST /api/v1/devkeys/revoke", a(s.apiDevKeyRevoke))
	mux.Handle("GET /api/v1/logs", a(s.apiLogs))
	mux.Handle("GET /api/v1/check", a(s.apiCheck))
	mux.Handle("GET /api/v1/diagnostics", a(s.apiDiagnostics))
	mux.Handle("POST /api/v1/capture", a(s.apiCapture))
	mux.Handle("POST /api/v1/bundle", a(s.apiBundle))
	mux.Handle("GET /api/v1/about", a(s.apiAbout))
	mux.Handle("POST /api/v1/update-check", a(s.apiUpdateCheck))
	mux.Handle("POST /api/v1/class-toggle", a(s.apiClassToggle))
}

// ctlOr runs a ctl command into out, or writes the API error and returns
// false. A refusal by the daemon is "failed" (409).
func (s *Server) ctlOr(w http.ResponseWriter, r *http.Request, req ctl.Request, out any) bool {
	if err := s.call(r.Context(), req, out); err != nil {
		s.apiError(w, r, http.StatusConflict, err.Error())
		return false
	}
	return true
}

func (s *Server) logAPI(r *http.Request, key string, args ...any) {
	s.log.Info("api: "+key, append([]any{"event", "web_setting", "user", sessionOf(r).user, "side", sessionOf(r).side, "key", key}, args...)...)
}

func (s *Server) apiDeviceAllow(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToUpper(strings.TrimSpace(r.PostFormValue("addr")))
	if !btAddrRe.MatchString(addr) {
		s.apiError(w, r, http.StatusBadRequest, "addr: a Bluetooth address, XX:XX:XX:XX:XX:XX")
		return
	}
	var rep ctl.AllowReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "allow", Addr: addr, Name: strings.TrimSpace(r.PostFormValue("name"))}, &rep) {
		return
	}
	s.logAPI(r, "allow", "addr", addr)
	s.apiOK(w, r, kv{"addr", rep.Address}, kv{"added", rep.Added})
}

func (s *Server) apiFirewallSave(w http.ResponseWriter, r *http.Request) {
	rules, err := s.allowRules(r.Context())
	if err != nil {
		s.apiError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	n, err := strconv.Atoi(r.PostFormValue("n"))
	if err != nil || n < 1 || n > len(rules) {
		s.apiError(w, r, http.StatusBadRequest, fmt.Sprintf("n: there are %d rules", len(rules)))
		return
	}
	rule, err := ruleFromForm(r)
	if err != nil {
		s.apiError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	rules[n-1] = rule
	s.apiSaveRules(w, r, rules)
}

func (s *Server) apiFirewallTest(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.PostFormValue("host"))
	port, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("port")))
	if host == "" || err != nil || port < 1 || port > 65535 {
		s.apiError(w, r, http.StatusBadRequest, "give a host and a TCP port from 1 to 65535")
		return
	}
	var t ctl.TestReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "firewall.test", Host: host, Port: port}, &t) {
		return
	}
	s.apiOK(w, r, kv{"reached", t.OK}, kv{"ms", t.Millis}, kv{"allowed", t.Allowed}, kv{"problem", t.Problem})
}

func (s *Server) apiCounters(w http.ResponseWriter, r *http.Request) {
	var cs ctl.CountersReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "firewall.counters"}, &cs) {
		return
	}
	items := []rec{}
	for _, c := range cs.Counters {
		items = append(items, rec{{"rule", c.Comment}, {"chain", c.Chain}, {"packets", c.Packets}, {"bytes", c.Bytes}})
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: rec{{"status", "ok"}, {"count", len(items)}}, name: "counters", items: items})
}

func hostPort(a fmt.Stringer, p uint16) string {
	if p == 0 {
		return a.String()
	}
	return a.String() + ":" + strconv.Itoa(int(p))
}

func (s *Server) apiNAT(w http.ResponseWriter, r *http.Request) {
	var n ctl.NATReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "nat"}, &n) {
		return
	}
	items := []rec{}
	for _, e := range n.Entries {
		out := ""
		if e.NATAddr.IsValid() {
			out = hostPort(e.NATAddr, e.NATPort)
		}
		items = append(items, rec{{"device", e.Device}, {"proto", e.Proto}, {"from", hostPort(e.Src, e.SrcPort)},
			{"to", hostPort(e.Dst, e.DstPort)}, {"out", out}, {"state", natStateText(e.State, e.Assured, e.Replied)}, {"timeout", e.Timeout}})
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: rec{{"status", "ok"}, {"subnet", n.Subnet}, {"total", n.Total}, {"count", len(items)}},
		name: "connections", items: items})
}

func natStateText(state string, assured, replied bool) string {
	switch {
	case state != "":
		return state
	case assured:
		return "two-way"
	case replied:
		return "answered"
	}
	return "no answer yet"
}

func (s *Server) apiInternet(w http.ResponseWriter, r *http.Request) {
	var st ctl.InternetReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "internet"}, &st) {
		return
	}
	head := rec{{"status", "ok"}, {"uplink", st.Uplink}, {"wifi_on", st.WifiOn}, {"wifi_hw", st.WifiHW},
		{"applying", st.Applying}, {"onboard_bt", st.OnboardBT}, {"error", st.Error}}
	if j := st.Job; j != nil {
		head = append(head, kv{"join_ssid", j.SSID}, kv{"join_done", j.Done}, kv{"join_ok", j.OK}, kv{"join_error", j.Error},
			kv{"join_switching", j.Switching})
	}
	items := []rec{}
	for _, d := range st.Devices {
		items = append(items, rec{{"kind", d.Type}, {"iface", d.Iface}, {"state", d.State}, {"connected", d.Connected},
			{"carrier", d.Carrier}, {"network", d.Connection}, {"ip", d.IP}, {"router", d.Gateway}, {"in_use", d.Iface == st.Uplink}})
	}
	for _, n := range st.Saved {
		items = append(items, rec{{"kind", "saved"}, {"network", n}})
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: head, name: "items", items: items})
}

func (s *Server) apiInternetUse(w http.ResponseWriter, r *http.Request) {
	var rep ctl.InternetUseReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "internet.use", WiFi: &ctl.WifiRequest{Use: r.PostFormValue("use")}}, &rep) {
		return
	}
	s.logAPI(r, "internet.use", "iface", rep.Iface)
	s.apiOK(w, r, kv{"iface", rep.Iface}, kv{"switching", rep.Switching})
}

func (s *Server) apiWifiScan(w http.ResponseWriter, r *http.Request) {
	var sc ctl.WifiScanReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "wifi.scan", WiFi: &ctl.WifiRequest{Rescan: r.URL.Query().Get("rescan") != ""}}, &sc) {
		return
	}
	items := []rec{}
	for _, n := range sc.Networks {
		items = append(items, rec{{"ssid", n.SSID}, {"signal", n.Signal}, {"band", n.Band()}, {"security", n.Security},
			{"saved", n.Saved}, {"active", n.Active}, {"supported", n.Supported()}})
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: rec{{"status", "ok"}, {"count", len(items)}}, name: "networks", items: items})
}

func (s *Server) apiWifiJoin(w http.ResponseWriter, r *http.Request) {
	req := &ctl.WifiRequest{SSID: FromWindows1252(r.PostFormValue("ssid")), Password: r.PostFormValue("password"), Hidden: r.PostFormValue("hidden") == "1"}
	var ok ctl.OKReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "wifi.join", WiFi: req}, &ok) {
		return
	}
	s.logAPI(r, "wifi.join", "ssid", req.SSID)
	s.apiOK(w, r, kv{"joining", req.SSID})
}

func (s *Server) apiWifiForget(w http.ResponseWriter, r *http.Request) {
	ssid := FromWindows1252(r.PostFormValue("ssid"))
	var ok ctl.OKReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "wifi.forget", WiFi: &ctl.WifiRequest{SSID: ssid}}, &ok) {
		return
	}
	s.apiOK(w, r, kv{"forgot", ssid})
}

func (s *Server) apiWifiRadio(w http.ResponseWriter, r *http.Request) {
	on := r.PostFormValue("on") == "1"
	var ok ctl.OKReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "wifi.radio", WiFi: &ctl.WifiRequest{On: &on}}, &ok) {
		return
	}
	s.apiOK(w, r, kv{"wifi_on", on})
}

// apiSettingsGet is what the settings page shows: the values, and the
// adapters to choose from.
func (s *Server) apiSettingsGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.o.Config()
	var c ctl.ChoicesReply
	_ = s.call(r.Context(), ctl.Request{Cmd: "choices"}, &c)
	head := rec{{"status", "ok"}, {"name", cfg.Bluetooth.Name}, {"adapter", cfg.Bluetooth.Adapter},
		{"idle_seconds", cfg.PPP.IdleSeconds}, {"ppp_debug", cfg.PPP.Debug}, {"uplink", cfg.Network.Uplink},
		{"subnet", cfg.Network.Subnet}, {"gateway", cfg.Network.Gateway}, {"tls", cfg.Web.TLS},
		{"bluetooth_side", cfg.Web.BluetoothSide}, {"applying", c.Applying}}
	if a := c.LastApply; a != nil {
		head = append(head, kv{"last_apply", a.Finished.UTC().Format(time.RFC3339)}, kv{"last_apply_ok", a.OK}, kv{"last_apply_error", a.Error})
	}
	if c.Subnet != nil && !c.Subnet.Free {
		head = append(head, kv{"subnet_clash", strings.Join(c.Subnet.Conflicts, ", ")}, kv{"subnet_free", c.Subnet.Suggest})
	}
	items := []rec{}
	for _, o := range adapterOptions(cfg.Bluetooth.Adapter, c) {
		items = append(items, rec{{"value", o.Value}, {"label", o.Label}, {"selected", o.Selected}})
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: head, name: "adapters", items: items})
}

// apiNetwork changes the settings setup applies (D-054): palm-gwd restarts
// when it is done, which ends the Palm's own link.
func (s *Server) apiNetwork(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.apiError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	nc := setup.NetworkChange{Uplink: strings.TrimSpace(r.PostFormValue("uplink")), Subnet: strings.TrimSpace(r.PostFormValue("subnet")),
		TLS: r.PostFormValue("tls")}
	if v, ok := r.PostForm["bluetooth_side"]; ok {
		on := v[0] == "1" || v[0] == "true"
		nc.BluetoothSide = &on
	}
	var rep ctl.NetworkSetReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "network.set", Network: &nc}, &rep) {
		return
	}
	s.logAPI(r, "network.set", "keys", rep.Changed)
	s.apiOK(w, r, kv{"changed", rep.Changed}, kv{"applying", rep.Applying})
}

func (s *Server) apiPassword(w http.ResponseWriter, r *http.Request) {
	se := sessionOf(r)
	ip := clientIP(r)
	if blocked, wait := s.limiter.blocked(se.side, ip); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		s.apiError(w, r, http.StatusTooManyRequests, "too many wrong passwords; wait a minute")
		return
	}
	if check(s.accounts(), se.user, r.PostFormValue("current")) != RoleAdmin {
		n := s.limiter.failed(se.side, ip)
		s.log.Warn("api: password change refused", "event", "web_password_refused", "user", se.user, "ip", ip, "failures", n)
		s.apiError(w, r, http.StatusUnauthorized, "the current password is wrong")
		return
	}
	pw := r.PostFormValue("new")
	if err := setup.CheckNewPassword(pw); err != nil {
		s.apiError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := setup.HashPassword(pw)
	if err == nil {
		_, err = s.setKey(r.Context(), "web.password_hash", hash)
	}
	if err != nil {
		s.apiError(w, r, http.StatusConflict, err.Error())
		return
	}
	s.log.Info("api: password changed", "event", "web_password_changed", "user", se.user, "side", se.side, "ip", ip)
	s.apiOK(w, r)
}

func (s *Server) apiRestart(w http.ResponseWriter, r *http.Request) {
	var ok ctl.OKReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "restart"}, &ok) {
		return
	}
	s.logAPI(r, "restart")
	s.apiOK(w, r)
}

func (s *Server) apiDevKeys(w http.ResponseWriter, r *http.Request) {
	var k ctl.DevKeysReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "devkey.list"}, &k) {
		return
	}
	items := []rec{}
	for _, e := range k.Keys {
		last := ""
		if !e.LastUsed.IsZero() {
			last = e.LastUsed.UTC().Format(time.RFC3339)
		}
		items = append(items, rec{{"id", e.ID}, {"name", e.Name}, {"user", e.User}, {"role", e.Role},
			{"created", e.Created.UTC().Format(time.RFC3339)}, {"last_used", last}, {"last_ip", e.LastIP}})
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: rec{{"status", "ok"}, {"count", len(items)}}, name: "keys", items: items})
}

func (s *Server) apiDevKeyRevoke(w http.ResponseWriter, r *http.Request) {
	var k ctl.DevKeyReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "devkey.revoke", DevKey: &ctl.DevKeyRequest{ID: r.PostFormValue("id")}}, &k) {
		return
	}
	s.logAPI(r, "devkey.revoke", "id", k.Entry.ID)
	s.apiOK(w, r, kv{"revoked", k.Entry.Name})
}

// apiLogs is the logs page, newest first, at most 100 lines (a Palm's
// reply buffer is small).
func (s *Server) apiLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("n"))
	if n <= 0 || n > 100 {
		n = 30
	}
	req := ctl.LogsRequest{Lines: n, Event: strings.TrimSpace(q.Get("event")), Text: strings.TrimSpace(q.Get("q"))}
	if u := q.Get("unit"); u != "" {
		req.Units = []string{u}
	}
	var rep ctl.LogsReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "logs", Logs: &req}, &rep) {
		return
	}
	items := []rec{}
	for i := len(rep.Entries) - 1; i >= 0; i-- {
		e := rep.Entries[i]
		items = append(items, rec{{"time", e.Time.Local().Format("Jan 02 15:04:05")}, {"unit", e.Unit}, {"level", e.Level},
			{"event", e.Event}, {"addr", e.Addr}, {"msg", e.Msg}, {"fields", strings.Join(e.FieldList(), " ")}})
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: rec{{"status", "ok"}, {"count", len(items)}}, name: "lines", items: items})
}

func (s *Server) apiCheck(w http.ResponseWriter, r *http.Request) {
	var c ctl.CheckReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "check"}, &c) {
		return
	}
	power := "OK"
	switch {
	case c.Power.UnderVoltageNow():
		power = "under-voltage now"
	case c.Power.UnderVoltageSeen():
		power = "under-voltage since boot"
	}
	items := []rec{}
	for _, a := range c.Adapters {
		items = append(items, rec{{"kind", "adapter"}, {"text", a.Label()}, {"ok", a.Powered}})
	}
	for _, u := range c.Uplinks {
		items = append(items, rec{{"kind", "uplink"}, {"text", u.Label()}, {"ok", true}})
	}
	for _, u := range c.Units {
		items = append(items, rec{{"kind", "service"}, {"text", u.Name + ": " + u.Active}, {"ok", u.Active == "active"}})
	}
	for _, m := range c.Modules {
		items = append(items, rec{{"kind", "module"}, {"text", m.Name}, {"ok", m.Loaded}})
	}
	for _, wn := range c.Warnings {
		items = append(items, rec{{"kind", "warning"}, {"text", wn}, {"ok", false}})
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: rec{{"status", "ok"}, {"board", c.Board.Model}, {"power", power},
		{"warnings", len(c.Warnings)}}, name: "items", items: items})
}

func (s *Server) apiDiagnostics(w http.ResponseWriter, r *http.Request) {
	var d ctl.DiagnosticsReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "diagnostics"}, &d) {
		return
	}
	head := rec{{"status", "ok"}, {"capturing", d.Capture.Running}, {"capture", d.Capture.Name}, {"capture_error", d.Capture.Error},
		{"captures", len(d.Captures)}, {"bundles", len(d.Bundles)}}
	if d.Capture.Running {
		head = append(head, kv{"capture_left", int64(time.Until(d.Capture.Until).Seconds())})
	}
	if len(d.Bundles) > 0 {
		head = append(head, kv{"last_bundle", d.Bundles[0].Name})
	}
	s.apiOK(w, r, head[1:]...)
}

func (s *Server) apiCapture(w http.ResponseWriter, r *http.Request) {
	secs, _ := strconv.Atoi(r.PostFormValue("seconds"))
	var st ctl.CaptureState
	if !s.ctlOr(w, r, ctl.Request{Cmd: "btmon", Seconds: secs}, &st) {
		return
	}
	s.logAPI(r, "capture", "seconds", secs)
	s.apiOK(w, r, kv{"capture", st.Name}, kv{"until", st.Until.UTC().Format(time.RFC3339)})
}

func (s *Server) apiBundle(w http.ResponseWriter, r *http.Request) {
	var b ctl.BundleReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "bundle", WithCapture: r.PostFormValue("with_capture") == "1"}, &b) {
		return
	}
	s.logAPI(r, "bundle", "file", b.Name)
	s.apiOK(w, r, kv{"bundle", b.Name}, kv{"size", b.Size})
}

func (s *Server) apiAbout(w http.ResponseWriter, r *http.Request) {
	var a ctl.AboutReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "about"}, &a) {
		return
	}
	head := rec{{"version", a.Version}, {"go", a.GoVersion}, {"started", a.Started.UTC().Format(time.RFC3339)},
		{"up", int64(time.Since(a.Started).Seconds())}}
	if in := a.Install; in != nil {
		head = append(head, kv{"board", in.Model}, kv{"os", in.OS}, kv{"kernel", in.Kernel}, kv{"bluez", in.BlueZ},
			kv{"pppd", in.PPPD}, kv{"adapter_mode", in.AdapterMode}, kv{"installed", in.InstalledAt})
	}
	s.apiOK(w, r, head...)
}

func (s *Server) apiUpdateCheck(w http.ResponseWriter, r *http.Request) {
	var u ctl.UpdateReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "update.check"}, &u) {
		return
	}
	s.apiOK(w, r, kv{"checked", u.OK}, kv{"result", u.Output})
}

func (s *Server) apiClassToggle(w http.ResponseWriter, r *http.Request) {
	var c ctl.ClassReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "class.toggle"}, &c) {
		return
	}
	s.logAPI(r, "class.toggle", "class", c.Class)
	s.apiOK(w, r, kv{"class", c.Class}, kv{"previous", c.Previous})
}
