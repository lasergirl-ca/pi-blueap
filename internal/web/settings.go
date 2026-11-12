package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/setup"
)

// Part 3: the firewall and settings pages (PLAN.md section 8.12). Every
// change goes through config.set: the daemon validates it, writes
// config.toml and reloads, and says which keys wait for a restart.

func (s *Server) settingsRoutes(mux *http.ServeMux, side Side) {
	a := func(h http.HandlerFunc) http.Handler { return s.auth(side, h) }
	mux.Handle("GET /firewall", a(s.firewallPage))
	mux.Handle("POST /firewall/add", a(s.firewallAdd))
	mux.Handle("POST /firewall/{n}/save", a(s.firewallSave))
	mux.Handle("POST /firewall/{n}/delete", a(s.firewallDelete))
	mux.Handle("POST /firewall/test", a(s.firewallTest))
	mux.Handle("GET /settings", a(s.settingsPage))
	mux.Handle("POST /settings", a(s.settingsSave))
	mux.Handle("POST /settings/network", a(s.networkSave))
	mux.Handle("POST /settings/password", a(s.passwordChange))
	mux.Handle("POST /settings/restart", a(s.restart))
}

// setKey runs config.set for one key.
func (s *Server) setKey(ctx context.Context, path string, value any) (ctl.ConfigSetReply, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return ctl.ConfigSetReply{}, err
	}
	var r ctl.ConfigSetReply
	err = s.call(ctx, ctl.Request{Cmd: "config.set", Path: path, Value: b}, &r)
	return r, err
}

func (s *Server) allowRules(ctx context.Context) ([]config.AllowRule, error) {
	var v ctl.ConfigValue
	if err := s.call(ctx, ctl.Request{Cmd: "config.get", Path: "firewall.allow"}, &v); err != nil {
		return nil, err
	}
	var rules []config.AllowRule
	err := json.Unmarshal(v.Value, &rules)
	return rules, err
}

var portRe = regexp.MustCompile(`^(tcp|udp)/[0-9]{1,5}$|^icmp$`)

// parsePorts splits "tcp/14238, udp/53 icmp" into its entries.
func parsePorts(s string) ([]string, error) {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if len(fields) == 0 {
		return nil, fmt.Errorf("give at least one port, such as tcp/14238, udp/53 or icmp")
	}
	for _, f := range fields {
		if !portRe.MatchString(f) {
			return nil, fmt.Errorf("%q is not tcp/N, udp/N or icmp", f)
		}
	}
	return fields, nil
}

func ruleFromForm(r *http.Request) (config.AllowRule, error) {
	ports, err := parsePorts(r.PostFormValue("ports"))
	if err != nil {
		return config.AllowRule{}, err
	}
	return config.AllowRule{
		Host:    strings.TrimSpace(r.PostFormValue("host")),
		Ports:   ports,
		Comment: strings.TrimSpace(r.PostFormValue("comment")),
	}, nil
}

func (s *Server) firewallPage(w http.ResponseWriter, r *http.Request) {
	s.renderFirewall(w, r, http.StatusOK, map[string]any{})
}

func (s *Server) renderFirewall(w http.ResponseWriter, r *http.Request, code int, extra map[string]any) {
	data := s.page(r, "Firewall")
	rules, err := s.allowRules(r.Context())
	if err != nil {
		data["Error"] = err.Error()
	}
	data["Rules"] = rules
	var cs ctl.CountersReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "firewall.counters"}, &cs); err != nil {
		data["CountersError"] = err.Error()
	}
	data["Counters"] = cs.Counters
	data["PalmToPi"] = s.o.Config().Firewall.PalmToPi
	for k, v := range extra {
		data[k] = v
	}
	s.render(w, r, code, "firewall", data)
}

// saveRules writes the list and renders the page with what happened.
func (s *Server) saveRules(w http.ResponseWriter, r *http.Request, rules []config.AllowRule, done string) {
	if rules == nil {
		rules = []config.AllowRule{}
	}
	rep, err := s.setKey(r.Context(), "firewall.allow", rules)
	if err != nil {
		s.renderFirewall(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: firewall rules saved", "event", "web_firewall_saved", "user", sessionOf(r).user, "rules", len(rules))
	msg := done + " The rules apply to Palms now."
	if len(rep.Reload.Errors) > 0 {
		msg = done + " Saved, but applying them reported: " + strings.Join(rep.Reload.Errors, "; ")
	}
	s.renderFirewall(w, r, http.StatusOK, map[string]any{"Done": msg})
}

func (s *Server) firewallAdd(w http.ResponseWriter, r *http.Request) {
	rule, err := ruleFromForm(r)
	if err != nil {
		s.renderFirewall(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error(), "Form": r.PostForm})
		return
	}
	rules, err := s.allowRules(r.Context())
	if err != nil {
		s.renderFirewall(w, r, http.StatusBadGateway, map[string]any{"Error": err.Error()})
		return
	}
	s.saveRules(w, r, append(rules, rule), fmt.Sprintf("Added %s %s.", rule.Host, strings.Join(rule.Ports, " ")))
}

// ruleIndex reads {n} (1-based) against the current rules.
func (s *Server) ruleIndex(r *http.Request) ([]config.AllowRule, int, error) {
	rules, err := s.allowRules(r.Context())
	if err != nil {
		return nil, 0, err
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 || n > len(rules) {
		return nil, 0, fmt.Errorf("there is no rule %s (the list may have changed; reload the page)", r.PathValue("n"))
	}
	return rules, n - 1, nil
}

func (s *Server) firewallSave(w http.ResponseWriter, r *http.Request) {
	rules, i, err := s.ruleIndex(r)
	if err == nil {
		var rule config.AllowRule
		if rule, err = ruleFromForm(r); err == nil {
			rules[i] = rule
			s.saveRules(w, r, rules, fmt.Sprintf("Rule %d saved.", i+1))
			return
		}
	}
	s.renderFirewall(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
}

func (s *Server) firewallDelete(w http.ResponseWriter, r *http.Request) {
	rules, i, err := s.ruleIndex(r)
	if err != nil {
		s.renderFirewall(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	gone := rules[i]
	s.saveRules(w, r, append(rules[:i], rules[i+1:]...), fmt.Sprintf("Removed %s %s.", gone.Host, strings.Join(gone.Ports, " ")))
}

func (s *Server) firewallTest(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.PostFormValue("host"))
	port, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("port")))
	if host == "" || err != nil || port < 1 || port > 65535 {
		s.renderFirewall(w, r, http.StatusBadRequest, map[string]any{"Error": "Give a host and a TCP port from 1 to 65535 to test."})
		return
	}
	var t ctl.TestReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "firewall.test", Host: host, Port: port}, &t); err != nil {
		s.renderFirewall(w, r, http.StatusBadGateway, map[string]any{"Error": err.Error()})
		return
	}
	s.renderFirewall(w, r, http.StatusOK, map[string]any{"Test": t})
}

// option is one entry of a drop-down.
type option struct {
	Value, Label string
	Selected     bool
}

// adapterOptions offers "auto" and every adapter BlueZ sees. An adapter
// configured by its hciN keeps that value, so saving the page unchanged
// changes nothing; a configured adapter that is not there now is kept too.
func adapterOptions(cur string, c ctl.ChoicesReply) []option {
	opts := []option{{"auto", "Automatic: the first adapter found", cur == "auto"}}
	found := cur == "auto"
	for _, a := range c.Adapters {
		o := option{Value: a.Address, Label: a.Label()}
		if strings.EqualFold(a.Address, c.AdapterIn) {
			o.Label += ", in use now"
		}
		if strings.EqualFold(cur, a.Address) || cur == a.HCI {
			o.Value, o.Selected, found = cur, true, true
		}
		opts = append(opts, o)
	}
	if !found && cur != "" {
		opts = append(opts, option{cur, cur + " (not found now)", true})
	}
	return opts
}

// uplinkOptions offers the interfaces with a default route, and the
// configured one even when it has none now.
func uplinkOptions(cur string, c ctl.ChoicesReply) []option {
	var opts []option
	found := false
	for _, u := range c.Uplinks {
		opts = append(opts, option{u.Iface, u.Label(), u.Iface == cur})
		found = found || u.Iface == cur
	}
	if !found {
		opts = append(opts, option{cur, cur + " (no internet route now)", true})
	}
	return opts
}

var idleChoices = []struct {
	secs  int
	label string
}{{0, "Never"}, {300, "After 5 minutes"}, {600, "After 10 minutes"}, {900, "After 15 minutes"}, {1800, "After 30 minutes"}, {3600, "After an hour"}}

func idleOptions(cur int) []option {
	var opts []option
	found := false
	for _, c := range idleChoices {
		opts = append(opts, option{strconv.Itoa(c.secs), c.label, c.secs == cur})
		found = found || c.secs == cur
	}
	if !found {
		opts = append(opts, option{strconv.Itoa(cur), fmt.Sprintf("After %d seconds", cur), true})
	}
	return opts
}

func tlsOptions(cur string) []option {
	opts := []option{
		{"self-signed", "HTTPS with a self-signed certificate (recommended)", cur == "self-signed"},
		{"off", "Plain HTTP, home network only", cur == "off"},
	}
	if cur == "file" {
		opts = append(opts, option{"file", "HTTPS with your own certificate files", true})
	}
	return opts
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, http.StatusOK, map[string]any{})
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, code int, extra map[string]any) {
	data := s.page(r, "Settings")
	cfg := s.o.Config()
	data["Config"] = cfg
	var c ctl.ChoicesReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "choices"}, &c); err != nil {
		data["ChoicesError"] = err.Error()
	}
	data["Choices"] = c
	data["Adapters"] = adapterOptions(cfg.Bluetooth.Adapter, c)
	data["Uplinks"] = uplinkOptions(cfg.Network.Uplink, c)
	data["Idle"] = idleOptions(cfg.PPP.IdleSeconds)
	data["TLSModes"] = tlsOptions(cfg.Web.TLS)
	for k, v := range extra {
		data[k] = v
	}
	s.render(w, r, code, "settings", data)
}

// settingsSave applies each field whose value changed, one config.set each,
// and reports what applied and what waits for a restart.
func (s *Server) settingsSave(w http.ResponseWriter, r *http.Request) {
	cfg := s.o.Config()
	type change struct {
		key   string
		value any
	}
	var changes []change
	if v := strings.TrimSpace(r.PostFormValue("bluetooth.name")); v != cfg.Bluetooth.Name {
		changes = append(changes, change{"bluetooth.name", v})
	}
	if v := strings.TrimSpace(r.PostFormValue("bluetooth.adapter")); v != cfg.Bluetooth.Adapter {
		changes = append(changes, change{"bluetooth.adapter", v})
	}
	if v := strings.TrimSpace(r.PostFormValue("ppp.idle_seconds")); v != strconv.Itoa(cfg.PPP.IdleSeconds) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			s.renderSettings(w, r, http.StatusBadRequest, map[string]any{"Error": fmt.Sprintf("Hang up idle connections: %q is not a number of seconds.", v)})
			return
		}
		changes = append(changes, change{"ppp.idle_seconds", n})
	}
	if v := r.PostFormValue("ppp.debug") != ""; v != cfg.PPP.Debug {
		changes = append(changes, change{"ppp.debug", v})
	}
	if len(changes) == 0 {
		s.renderSettings(w, r, http.StatusOK, map[string]any{"Done": "Nothing changed."})
		return
	}
	var saved, restart []string
	for _, c := range changes {
		rep, err := s.setKey(r.Context(), c.key, c.value)
		if err != nil {
			msg := err.Error()
			if len(saved) > 0 {
				msg = "Saved " + strings.Join(saved, ", ") + ", then: " + msg
			}
			s.renderSettings(w, r, http.StatusBadRequest, map[string]any{"Error": msg})
			return
		}
		saved = append(saved, c.key)
		restart = append(restart, rep.Reload.NeedsRestart...)
		s.log.Info("web: setting changed", "event", "web_setting", "user", sessionOf(r).user, "key", c.key)
	}
	extra := map[string]any{"Done": "Saved: " + friendlyKeys(saved) + "."}
	if len(restart) > 0 {
		extra["NeedsRestart"] = dedupe(restart)
	}
	s.renderSettings(w, r, http.StatusOK, extra)
}

// keyNames are the settings as the page calls them.
var keyNames = map[string]string{
	"bluetooth.name":     "gateway name",
	"bluetooth.adapter":  "Bluetooth adapter",
	"ppp.idle_seconds":   "idle hang-up",
	"ppp.debug":          "connection logging",
	"network.uplink":     "internet connection",
	"network.subnet":     "Palm network",
	"network.gateway":    "gateway address",
	"network.ppp_pool":   "Palm addresses",
	"network.dhcp_pool":  "laptop addresses",
	"web.tls":            "web UI security",
	"web.bluetooth_side": "web UI over Bluetooth",
}

func friendlyKeys(keys []string) string {
	var out []string
	for _, k := range keys {
		if n, ok := keyNames[k]; ok {
			k = n
		}
		out = append(out, k)
	}
	return strings.Join(dedupe(out), ", ")
}

// networkSave sends the network card to network.set, which saves it and
// starts `palm-gw setup --apply`; palm-gwd restarts when that is done
// (D-054). Turning HTTPS off turns the Bluetooth side off with it.
func (s *Server) networkSave(w http.ResponseWriter, r *http.Request) {
	nc := setup.NetworkChange{
		Uplink: strings.TrimSpace(r.PostFormValue("network.uplink")),
		Subnet: strings.TrimSpace(r.PostFormValue("network.subnet")),
		TLS:    r.PostFormValue("web.tls"),
	}
	if nc.TLS == "file" {
		nc.TLS = "" // set by hand in config.toml; the page leaves it alone
	}
	newTLS := nc.TLS
	if newTLS == "" {
		newTLS = s.o.Config().Web.TLS
	}
	side := false
	if newTLS != "off" {
		side = r.PostFormValue("web.bluetooth_side") != ""
		nc.BluetoothSide = &side
	}
	var rep ctl.NetworkSetReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "network.set", Network: &nc}, &rep); err != nil {
		s.renderSettings(w, r, http.StatusBadRequest, map[string]any{"NetworkError": err.Error()})
		return
	}
	if !rep.Applying {
		s.renderSettings(w, r, http.StatusOK, map[string]any{"NetworkDone": "Nothing changed."})
		return
	}
	se := sessionOf(r)
	s.log.Info("web: network settings changed", "event", "web_setting", "user", se.user, "keys", rep.Changed)
	data := s.page(r, "Applying")
	data["Changed"] = friendlyKeys(rep.Changed)
	// Where the UI will be once palm-gwd is back.
	next := ""
	switch {
	case se.side == SideBluetooth && !side:
		data["Gone"] = true
	case se.side == SideBluetooth:
		gw := s.o.Config().Network.Gateway
		if p, err := netip.ParsePrefix(nc.Subnet); err == nil && nc.Subnet != "" {
			a := p.Addr().As4()
			gw = netip.AddrFrom4([4]byte{a[0], a[1], a[2], 1}).String()
		}
		next = "https://" + net.JoinHostPort(gw, s.o.Config().WebPort()) + "/login"
	default:
		scheme := "https"
		if newTLS == "off" {
			scheme = "http"
		}
		next = scheme + "://" + r.Host + "/login"
	}
	if next != "" {
		data["Next"] = next
		data["Refresh"] = "25;url=" + next
	}
	s.render(w, r, http.StatusOK, "applying", data)
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// passwordChange needs the current password, so a session left open on a
// shared browser cannot be used to lock the owner out.
func (s *Server) passwordChange(w http.ResponseWriter, r *http.Request) {
	se := sessionOf(r)
	ip := clientIP(r)
	if blocked, _ := s.limiter.blocked(se.side, ip); blocked {
		s.renderSettings(w, r, http.StatusTooManyRequests, map[string]any{"PasswordError": "Too many wrong passwords. Wait a minute."})
		return
	}
	if check(s.accounts(), se.user, r.PostFormValue("current")) != RoleAdmin {
		n := s.limiter.failed(se.side, ip)
		s.log.Warn("web: password change refused", "event", "web_password_refused", "user", se.user, "ip", ip, "failures", n)
		s.renderSettings(w, r, http.StatusUnauthorized, map[string]any{"PasswordError": "The current password is wrong."})
		return
	}
	pw := r.PostFormValue("new")
	if err := setup.CheckNewPassword(pw); err != nil {
		s.renderSettings(w, r, http.StatusBadRequest, map[string]any{"PasswordError": err.Error()})
		return
	}
	if pw != r.PostFormValue("again") {
		s.renderSettings(w, r, http.StatusBadRequest, map[string]any{"PasswordError": "The two new passwords do not match."})
		return
	}
	hash, err := setup.HashPassword(pw)
	if err != nil {
		s.renderSettings(w, r, http.StatusInternalServerError, map[string]any{"PasswordError": err.Error()})
		return
	}
	if _, err := s.setKey(r.Context(), "web.password_hash", hash); err != nil {
		s.renderSettings(w, r, http.StatusBadRequest, map[string]any{"PasswordError": err.Error()})
		return
	}
	s.log.Info("web: password changed", "event", "web_password_changed", "user", se.user, "side", se.side, "ip", ip)
	s.renderSettings(w, r, http.StatusOK, map[string]any{"PasswordDone": "Password changed. It is in effect now."})
}

func (s *Server) restart(w http.ResponseWriter, r *http.Request) {
	var ok ctl.OKReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "restart"}, &ok); err != nil {
		s.renderSettings(w, r, http.StatusBadGateway, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: restart requested", "event", "web_restart", "user", sessionOf(r).user)
	data := s.page(r, "Restarting")
	data["Refresh"] = "10;url=/login"
	s.render(w, r, http.StatusOK, "restarting", data)
}
