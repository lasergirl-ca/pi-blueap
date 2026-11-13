package web

import (
	"fmt"
	"net/http"
	"strings"

	"palm-gw/internal/ctl"
	"palm-gw/internal/nm"
)

// The internet connection wizard (D-056): Ethernet or Wi-Fi, and which Wi-Fi
// network. Each action is a ctl command (internet, internet.use, wifi.*).

func (s *Server) internetRoutes(mux *http.ServeMux, side Side) {
	a := func(h http.HandlerFunc) http.Handler { return s.auth(side, h) }
	mux.Handle("GET /internet", a(s.internetPage))
	mux.Handle("GET /partials/internet-job", a(s.internetJob))
	mux.Handle("POST /internet/use", a(s.internetUse))
	mux.Handle("POST /internet/join", a(s.wifiJoin))
	mux.Handle("POST /internet/forget", a(s.wifiForget))
	mux.Handle("POST /internet/radio", a(s.wifiRadio))
	s.wgRoutes(mux, a)
}

// netRow is a scanned network as the page shows it.
type netRow struct {
	nm.Network
	Bars string
	Band string
}

func bars(signal int) string {
	n := 1
	switch {
	case signal >= 75:
		n = 4
	case signal >= 50:
		n = 3
	case signal >= 25:
		n = 2
	}
	return strings.Repeat("▮", n) + strings.Repeat("▯", 4-n)
}

// describe says in words what a device is doing.
func describe(d nm.Device) string {
	switch {
	case d.Type == "ethernet" && !d.Carrier:
		return "No cable plugged in."
	case d.Connected && d.Type == "wifi":
		return fmt.Sprintf("Connected to %q, address %s, router %s.", d.Connection, d.IP, orNone(d.Gateway))
	case d.Connected:
		return fmt.Sprintf("Cable plugged in, address %s, router %s.", d.IP, orNone(d.Gateway))
	case d.Type == "ethernet":
		return "Cable plugged in, but no address yet."
	}
	return "Not connected to a network."
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func (s *Server) internetPage(w http.ResponseWriter, r *http.Request) {
	s.renderInternet(w, r, http.StatusOK, map[string]any{})
}

func (s *Server) renderInternet(w http.ResponseWriter, r *http.Request, code int, extra map[string]any) {
	data := s.page(r, "Internet")
	var st ctl.InternetReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "internet"}, &st); err != nil {
		data["Error"] = err.Error()
	}
	data["St"] = st
	if st.Error != "" {
		data["Error"] = "NetworkManager: " + st.Error
	}
	eth, hasEth := deviceOf(st.Devices, "ethernet")
	wifi, hasWifi := deviceOf(st.Devices, "wifi")
	data["Eth"], data["HasEth"], data["EthText"] = eth, hasEth, describe(eth)
	data["Wifi"], data["HasWifi"], data["WifiText"] = wifi, hasWifi, describe(wifi)
	data["EthInUse"] = hasEth && eth.Iface == st.Uplink
	data["WifiInUse"] = hasWifi && wifi.Iface == st.Uplink
	switch {
	case hasEth && eth.Iface == st.Uplink:
		data["Current"] = "Ethernet (" + eth.Iface + ")"
	case hasWifi && wifi.Iface == st.Uplink && wifi.Connected:
		data["Current"] = fmt.Sprintf("Wi-Fi, the network %q (%s)", wifi.Connection, wifi.Iface)
	default:
		data["Current"] = st.Uplink
	}
	if hasWifi && st.WifiOn {
		var sc ctl.WifiScanReply
		if err := s.call(r.Context(), ctl.Request{Cmd: "wifi.scan", WiFi: &ctl.WifiRequest{Rescan: r.URL.Query().Get("rescan") != ""}}, &sc); err != nil {
			data["ScanError"] = err.Error()
		}
		rows := make([]netRow, 0, len(sc.Networks))
		for _, n := range sc.Networks {
			rows = append(rows, netRow{Network: n, Bars: bars(n.Signal), Band: n.Band()})
		}
		data["Networks"] = rows
	}
	data["Job"] = st.Job
	var wg ctl.WGStatus
	if err := s.call(r.Context(), ctl.Request{Cmd: "wg"}, &wg); err == nil {
		data["WG"] = wg
	}
	for k, v := range extra {
		data[k] = v
	}
	s.render(w, r, code, "internet", data)
}

func deviceOf(ds []nm.Device, typ string) (nm.Device, bool) {
	for _, d := range ds {
		if d.Type == typ {
			return d, true
		}
	}
	return nm.Device{}, false
}

// internetJob is the polled join status. 286 tells htmx to stop polling.
func (s *Server) internetJob(w http.ResponseWriter, r *http.Request) {
	var st ctl.InternetReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "internet"}, &st); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	code := http.StatusOK
	if st.Job == nil || st.Job.Done {
		code = 286
	}
	data := s.page(r, "Internet")
	data["Job"] = st.Job
	s.render(w, r, code, "internet-job", data)
}

func (s *Server) internetUse(w http.ResponseWriter, r *http.Request) {
	kind := r.PostFormValue("use")
	var rep ctl.InternetUseReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "internet.use", WiFi: &ctl.WifiRequest{Use: kind}}, &rep); err != nil {
		s.renderInternet(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: internet connection chosen", "event", "web_setting", "user", sessionOf(r).user, "key", "internet.use", "iface", rep.Iface)
	if !rep.Switching {
		s.renderInternet(w, r, http.StatusOK, map[string]any{"Done": "The internet connection is " + kindLabel(kind) + "."})
		return
	}
	data := s.page(r, "Applying")
	data["Changed"] = "internet connection: " + kindLabel(kind)
	data["Next"] = "/login"
	data["Refresh"] = "25;url=/login"
	s.render(w, r, http.StatusOK, "applying", data)
}

func kindLabel(kind string) string {
	if kind == "wifi" {
		return "Wi-Fi"
	}
	return "Ethernet"
}

func (s *Server) wifiJoin(w http.ResponseWriter, r *http.Request) {
	req := &ctl.WifiRequest{SSID: r.PostFormValue("ssid"), Password: r.PostFormValue("wifi-password"), Hidden: r.PostFormValue("hidden") != ""}
	var ok ctl.OKReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "wifi.join", WiFi: req}, &ok); err != nil {
		s.renderInternet(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: Wi-Fi join", "event", "web_setting", "user", sessionOf(r).user, "key", "wifi.join", "ssid", req.SSID)
	http.Redirect(w, r, "/internet", http.StatusSeeOther)
}

func (s *Server) wifiForget(w http.ResponseWriter, r *http.Request) {
	ssid := r.PostFormValue("ssid")
	var ok ctl.OKReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "wifi.forget", WiFi: &ctl.WifiRequest{SSID: ssid}}, &ok); err != nil {
		s.renderInternet(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	s.renderInternet(w, r, http.StatusOK, map[string]any{"Done": fmt.Sprintf("Forgot %q.", ssid)})
}

func (s *Server) wifiRadio(w http.ResponseWriter, r *http.Request) {
	on := r.PostFormValue("on") == "1"
	var ok ctl.OKReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "wifi.radio", WiFi: &ctl.WifiRequest{On: &on}}, &ok); err != nil {
		s.renderInternet(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	msg := "Wi-Fi is off."
	if on {
		msg = "Wi-Fi is on. Networks appear after a few seconds: use Scan again."
	}
	s.renderInternet(w, r, http.StatusOK, map[string]any{"Done": msg})
}
