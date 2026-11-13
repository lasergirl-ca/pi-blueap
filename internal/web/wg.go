package web

import (
	"net/http"
	"strings"

	"palm-gw/internal/ctl"
)

// The WireGuard card on the Internet page and its /api/v1 calls (D-069): the
// tunnel's state, importing a .conf or making a key pair, full or split, what
// happens when it is down, and up or down now. Changes that need the tunnel
// rebuilt go through palm-gw setup --apply, like the network settings, and end
// with the "applying" page: palm-gwd restarts.

// wgRoutes adds the Internet page's WireGuard forms (a is the page login).
func (s *Server) wgRoutes(mux *http.ServeMux, a func(http.HandlerFunc) http.Handler) {
	mux.Handle("POST /internet/wg/set", a(s.wgSet))
	mux.Handle("POST /internet/wg/import", a(s.wgImport))
	mux.Handle("POST /internet/wg/generate", a(s.wgGenerate))
	mux.Handle("POST /internet/wg/updown", a(s.wgUpDown))
}

// wgAPIRoutes adds the /api/v1 calls (a is the API's bearer login).
func (s *Server) wgAPIRoutes(mux *http.ServeMux, a func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/v1/wireguard", a(s.apiWG))
	mux.Handle("POST /api/v1/wireguard/set", a(s.apiWGSet))
	mux.Handle("POST /api/v1/wireguard/import", a(s.apiWGImport))
	mux.Handle("POST /api/v1/wireguard/generate", a(s.apiWGGenerate))
	mux.Handle("POST /api/v1/wireguard/up", a(s.apiWGUpDown(true)))
	mux.Handle("POST /api/v1/wireguard/down", a(s.apiWGUpDown(false)))
}

// wgRequest reads the settings a form or API call changes; empty fields stay.
func wgRequest(r *http.Request) *ctl.WGRequest {
	req := &ctl.WGRequest{
		Mode:          r.PostFormValue("mode"),
		OnDown:        r.PostFormValue("on_down"),
		Address:       r.PostFormValue("address"),
		Endpoint:      r.PostFormValue("endpoint"),
		PeerPublicKey: r.PostFormValue("peer_public_key"),
	}
	if v := r.PostFormValue("enabled"); v != "" {
		on := v == "1" || v == "true"
		req.Enabled = &on
	}
	if v := strings.TrimSpace(r.PostFormValue("networks")); v != "" {
		req.AllowedIPs = strings.FieldsFunc(v, func(c rune) bool { return c == ',' || c == ' ' || c == '\n' || c == '\r' })
	}
	return req
}

// wgApplying shows the applying page after a change that rebuilds the tunnel.
func (s *Server) wgApplying(w http.ResponseWriter, r *http.Request, rep ctl.WGSetReply) {
	if !rep.Applying {
		msg := "Nothing changed."
		if rep.PublicKey != "" {
			msg = "The gateway's new public key: " + rep.PublicKey + ". Give it to the peer's admin, then fill in the tunnel and turn it on."
		}
		s.renderInternet(w, r, http.StatusOK, map[string]any{"Done": msg})
		return
	}
	data := s.page(r, "Applying")
	data["Changed"] = "WireGuard: " + strings.Join(rep.Changed, ", ")
	data["Next"] = "/login"
	data["Refresh"] = "25;url=/login"
	s.render(w, r, http.StatusOK, "applying", data)
}

func (s *Server) wgSet(w http.ResponseWriter, r *http.Request) {
	var rep ctl.WGSetReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "wg.set", WG: wgRequest(r)}, &rep); err != nil {
		s.renderInternet(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: WireGuard settings", "event", "web_setting", "user", sessionOf(r).user, "key", "wireguard", "changed", rep.Changed)
	s.wgApplying(w, r, rep)
}

func (s *Server) wgImport(w http.ResponseWriter, r *http.Request) {
	var rep ctl.WGSetReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "wg.import", WG: &ctl.WGRequest{Conf: r.PostFormValue("conf")}}, &rep); err != nil {
		s.renderInternet(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: WireGuard .conf imported", "event", "web_setting", "user", sessionOf(r).user, "key", "wireguard.import", "mode", rep.Mode)
	s.wgApplying(w, r, rep)
}

func (s *Server) wgGenerate(w http.ResponseWriter, r *http.Request) {
	var rep ctl.WGSetReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "wg.generate"}, &rep); err != nil {
		s.renderInternet(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: WireGuard key pair made", "event", "web_setting", "user", sessionOf(r).user, "key", "wireguard.generate")
	s.wgApplying(w, r, rep)
}

func (s *Server) wgUpDown(w http.ResponseWriter, r *http.Request) {
	cmd := "wg.down"
	if r.PostFormValue("up") == "1" {
		cmd = "wg.up"
	}
	var st ctl.WGStatus
	if err := s.call(r.Context(), ctl.Request{Cmd: cmd}, &st); err != nil {
		s.renderInternet(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: WireGuard "+strings.TrimPrefix(cmd, "wg."), "event", "web_setting", "user", sessionOf(r).user, "key", cmd)
	s.renderInternet(w, r, http.StatusOK, map[string]any{"Done": "The tunnel is " + st.State + "."})
}

// ---- /api/v1 ----

func wgRec(g ctl.WGStatus) rec {
	return rec{{"status", "ok"}, {"enabled", g.Enabled}, {"state", g.State}, {"interface", g.Interface},
		{"mode", g.Mode}, {"on_down", g.OnDown}, {"address", g.Address}, {"endpoint", g.Endpoint},
		{"public_key", g.PublicKey}, {"peer_public_key", g.PeerPublicKey},
		{"allowed_ips", strings.Join(g.AllowedIPs, ",")}, {"dns", strings.Join(g.DNS, ",")},
		{"rx_bytes", g.RxBytes}, {"tx_bytes", g.TxBytes}, {"last_heard", g.LastHeardSeconds},
		{"stale", g.Stale}, {"applying", g.Applying}}
}

func (s *Server) apiWG(w http.ResponseWriter, r *http.Request) {
	var g ctl.WGStatus
	if !s.ctlOr(w, r, ctl.Request{Cmd: "wg"}, &g) {
		return
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: wgRec(g)})
}

func (s *Server) apiWGReply(w http.ResponseWriter, r *http.Request, what string, rep ctl.WGSetReply) {
	s.logAPI(r, what, "changed", rep.Changed)
	s.apiOK(w, r, kv{"changed", strings.Join(rep.Changed, ",")}, kv{"applying", rep.Applying},
		kv{"public_key", rep.PublicKey}, kv{"mode", rep.Mode})
}

func (s *Server) apiWGSet(w http.ResponseWriter, r *http.Request) {
	var rep ctl.WGSetReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "wg.set", WG: wgRequest(r)}, &rep) {
		return
	}
	s.apiWGReply(w, r, "wireguard.set", rep)
}

func (s *Server) apiWGImport(w http.ResponseWriter, r *http.Request) {
	var rep ctl.WGSetReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "wg.import", WG: &ctl.WGRequest{Conf: r.PostFormValue("conf")}}, &rep) {
		return
	}
	s.apiWGReply(w, r, "wireguard.import", rep)
}

func (s *Server) apiWGGenerate(w http.ResponseWriter, r *http.Request) {
	var rep ctl.WGSetReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "wg.generate"}, &rep) {
		return
	}
	s.apiWGReply(w, r, "wireguard.generate", rep)
}

func (s *Server) apiWGUpDown(up bool) http.HandlerFunc {
	cmd := map[bool]string{true: "wg.up", false: "wg.down"}[up]
	return func(w http.ResponseWriter, r *http.Request) {
		var g ctl.WGStatus
		if !s.ctlOr(w, r, ctl.Request{Cmd: cmd}, &g) {
			return
		}
		s.logAPI(r, cmd)
		s.apiWrite(w, r, http.StatusOK, reply{head: wgRec(g)})
	}
}
