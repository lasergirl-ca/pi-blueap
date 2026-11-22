package web

import (
	"net/http"
	"strings"

	"palm-gw/internal/ctl"
)

// The Mobile card on the Internet page and its /api/v1 calls (D-074): the 4G
// modem's state, on or off, its role, APN, roaming and SIM PIN. Changes go
// through palm-gw setup --apply and end with the "applying" page.

// mobileRoutes adds the Internet page's 4G forms (a is the page login).
func (s *Server) mobileRoutes(mux *http.ServeMux, a func(http.HandlerFunc) http.Handler) {
	mux.Handle("POST /internet/mobile/set", a(s.mobileSet))
	mux.Handle("POST /internet/mobile/pin", a(s.mobilePIN))
}

// mobileAPIRoutes adds the /api/v1 calls (a is the API's bearer login).
func (s *Server) mobileAPIRoutes(mux *http.ServeMux, a func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/v1/mobile", a(s.apiMobile))
	mux.Handle("POST /api/v1/mobile/set", a(s.apiMobileSet))
	mux.Handle("POST /api/v1/mobile/pin", a(s.apiMobilePIN))
}

// mobileRequest reads the settings a form or API call changes; absent fields stay.
func mobileRequest(r *http.Request) *ctl.MobileRequest {
	req := &ctl.MobileRequest{Role: r.PostFormValue("role")}
	if v := r.PostFormValue("enabled"); v != "" {
		on := v == "1" || v == "true"
		req.Enabled = &on
	}
	if _, ok := r.PostForm["apn"]; ok {
		apn := strings.TrimSpace(r.PostFormValue("apn"))
		req.APN = &apn
	}
	// A checkbox follows a hidden "0" of the same name: the last value is the box's.
	if vs := r.PostForm["roaming"]; len(vs) > 0 {
		on := vs[len(vs)-1] == "1" || vs[len(vs)-1] == "true"
		req.Roaming = &on
	}
	return req
}

func (s *Server) mobileApplying(w http.ResponseWriter, r *http.Request, rep ctl.MobileSetReply) {
	if !rep.Applying {
		s.renderInternet(w, r, http.StatusOK, map[string]any{"Done": "Nothing changed."})
		return
	}
	data := s.page(r, "Applying")
	data["Changed"] = "4G: " + strings.Join(rep.Changed, ", ")
	data["Next"] = "/login"
	data["Refresh"] = "25;url=/login"
	s.render(w, r, http.StatusOK, "applying", data)
}

func (s *Server) mobileSet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderInternet(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	var rep ctl.MobileSetReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "mobile.set", Mobile: mobileRequest(r)}, &rep); err != nil {
		s.renderInternet(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: 4G settings", "event", "web_setting", "user", sessionOf(r).user, "key", "mobile", "changed", rep.Changed)
	s.mobileApplying(w, r, rep)
}

func (s *Server) mobilePIN(w http.ResponseWriter, r *http.Request) {
	var rep ctl.MobileSetReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "mobile.pin", Mobile: &ctl.MobileRequest{PIN: r.PostFormValue("pin")}}, &rep); err != nil {
		s.renderInternet(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: SIM PIN set", "event", "web_setting", "user", sessionOf(r).user, "key", "mobile.pin")
	s.mobileApplying(w, r, rep)
}

// ---- /api/v1 ----

func mobileRec(m ctl.MobileStatus) rec {
	return rec{{"status", "ok"}, {"enabled", m.Enabled}, {"role", m.Role}, {"interface", m.Interface},
		{"router_mode", m.RouterMode}, {"up", m.Up}, {"ip", m.IP}, {"active", m.Active},
		{"failed_over", m.FailedOver}, {"uplink_ok", m.UplinkOK}, {"modem", m.Modem}, {"state", m.State},
		{"locked", m.Locked}, {"operator", m.Operator}, {"tech", m.Tech}, {"signal", m.Signal},
		{"roaming", m.Roaming}, {"apn", m.APN}, {"roaming_ok", m.RoamingOK},
		{"month_bytes", m.MonthRxBytes + m.MonthTxBytes}, {"warning_mb", m.WarningMB},
		{"problem", m.Problem}, {"applying", m.Applying}}
}

func (s *Server) apiMobile(w http.ResponseWriter, r *http.Request) {
	var m ctl.MobileStatus
	if !s.ctlOr(w, r, ctl.Request{Cmd: "mobile"}, &m) {
		return
	}
	s.apiWrite(w, r, http.StatusOK, reply{head: mobileRec(m)})
}

func (s *Server) apiMobileSet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.apiError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	var rep ctl.MobileSetReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "mobile.set", Mobile: mobileRequest(r)}, &rep) {
		return
	}
	s.logAPI(r, "mobile.set", "changed", rep.Changed)
	s.apiOK(w, r, kv{"changed", strings.Join(rep.Changed, ",")}, kv{"applying", rep.Applying})
}

func (s *Server) apiMobilePIN(w http.ResponseWriter, r *http.Request) {
	var rep ctl.MobileSetReply
	if !s.ctlOr(w, r, ctl.Request{Cmd: "mobile.pin", Mobile: &ctl.MobileRequest{PIN: r.PostFormValue("pin")}}, &rep) {
		return
	}
	s.logAPI(r, "mobile.pin")
	s.apiOK(w, r, kv{"applying", rep.Applying})
}
