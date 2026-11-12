package web

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"palm-gw/internal/ctl"
	"palm-gw/internal/diag"
)

// Part 4 (PLAN.md section 8.12, D-057): logs, diagnostics, about, and the
// Class of Device toggle on the pairing page.

func (s *Server) diagRoutes(mux *http.ServeMux, side Side) {
	a := func(h http.HandlerFunc) http.Handler { return s.auth(side, h) }
	mux.Handle("GET /logs", a(s.logsPage))
	mux.Handle("GET /partials/logs", a(s.logsMore))
	mux.Handle("GET /diagnostics", a(s.diagPage))
	mux.Handle("POST /diagnostics/capture", a(s.captureStart))
	mux.Handle("POST /diagnostics/bundle", a(s.bundleCreate))
	mux.Handle("GET /diagnostics/files/{kind}/{name}", a(s.diagDownload))
	mux.Handle("GET /about", a(s.aboutPage))
	mux.Handle("POST /about/check", a(s.updateCheck))
	mux.Handle("POST /pair/class", a(s.classToggle))
}

var logLineChoices = []int{100, 500, 2000}

// logsQuery reads the logs page's filters from the URL.
func logsQuery(q url.Values) (ctl.LogsRequest, url.Values) {
	req := ctl.LogsRequest{Event: strings.TrimSpace(q.Get("event")), Text: strings.TrimSpace(q.Get("q")), Lines: 500}
	if n, err := strconv.Atoi(q.Get("n")); err == nil {
		for _, c := range logLineChoices {
			if n == c {
				req.Lines = n
			}
		}
	}
	for _, u := range q["unit"] {
		if _, ok := diag.Units[u]; ok {
			req.Units = append(req.Units, u)
		}
	}
	keep := url.Values{}
	for _, k := range []string{"event", "q", "n", "follow"} {
		if v := q.Get(k); v != "" {
			keep.Set(k, v)
		}
	}
	for _, u := range req.Units {
		keep.Add("unit", u)
	}
	return req, keep
}

func reversed(es []diag.Entry) []diag.Entry {
	out := make([]diag.Entry, len(es))
	for i, e := range es {
		out[len(es)-1-i] = e
	}
	return out
}

func (s *Server) logsPage(w http.ResponseWriter, r *http.Request) {
	data := s.page(r, "Logs")
	req, keep := logsQuery(r.URL.Query())
	var rep ctl.LogsReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "logs", Logs: &req}, &rep); err != nil {
		data["Error"] = err.Error()
	}
	data["Entries"] = reversed(rep.Entries) // newest first
	data["Cursor"] = rep.Cursor
	data["Req"] = req
	data["Query"] = keep.Encode()
	data["Follow"] = r.URL.Query().Get("follow") != ""
	data["LineChoices"] = logLineChoices
	data["UnitNames"] = diag.UnitNames
	sel := map[string]bool{}
	for _, u := range req.Units {
		sel[u] = true
	}
	if len(req.Units) == 0 {
		sel["palm-gwd"], sel["dnsmasq"], sel["bluetooth"] = true, true, true
	}
	data["UnitSel"] = sel
	s.render(w, r, http.StatusOK, "logs", data)
}

// logsMore is Follow: the lines after the cursor, inserted at the top, and
// the poller again with the new cursor.
func (s *Server) logsMore(w http.ResponseWriter, r *http.Request) {
	req, keep := logsQuery(r.URL.Query())
	req.After, req.Lines = r.URL.Query().Get("after"), 1000
	var rep ctl.LogsReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "logs", Logs: &req}, &rep); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	data := s.page(r, "Logs")
	data["Entries"] = reversed(rep.Entries)
	data["Cursor"] = rep.Cursor
	data["Query"] = keep.Encode()
	s.render(w, r, http.StatusOK, "logs-more", data)
}

func (s *Server) diagPage(w http.ResponseWriter, r *http.Request) {
	s.renderDiag(w, r, http.StatusOK, map[string]any{})
}

func (s *Server) renderDiag(w http.ResponseWriter, r *http.Request, code int, extra map[string]any) {
	data := s.page(r, "Diagnostics")
	var chk ctl.CheckReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "check"}, &chk); err != nil {
		data["CheckError"] = err.Error()
	}
	data["Check"] = chk
	var d ctl.DiagnosticsReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "diagnostics"}, &d); err != nil {
		data["Error"] = err.Error()
	}
	data["D"] = d
	data["CaptureSeconds"] = []int64{30, 60, 120, 300}
	for k, v := range extra {
		data[k] = v
	}
	s.render(w, r, code, "diagnostics", data)
}

func (s *Server) captureStart(w http.ResponseWriter, r *http.Request) {
	secs, _ := strconv.Atoi(r.PostFormValue("seconds"))
	var st ctl.CaptureState
	if err := s.call(r.Context(), ctl.Request{Cmd: "btmon", Seconds: secs}, &st); err != nil {
		s.renderDiag(w, r, http.StatusBadRequest, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: btmon capture started", "event", "web_capture", "user", sessionOf(r).user, "seconds", secs)
	http.Redirect(w, r, "/diagnostics#capture", http.StatusSeeOther)
}

func (s *Server) bundleCreate(w http.ResponseWriter, r *http.Request) {
	var b ctl.BundleReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "bundle", WithCapture: r.PostFormValue("with_capture") != ""}, &b); err != nil {
		s.renderDiag(w, r, http.StatusBadGateway, map[string]any{"Error": err.Error()})
		return
	}
	s.log.Info("web: support bundle made", "event", "web_bundle", "user", sessionOf(r).user, "file", b.Name)
	s.renderDiag(w, r, http.StatusOK, map[string]any{"Bundle": b})
}

// diagDownload serves a capture or bundle. Admin only: a capture taken
// during a pairing holds the link key.
func (s *Server) diagDownload(w http.ResponseWriter, r *http.Request) {
	if sessionOf(r).role != RoleAdmin {
		http.Error(w, "Only the admin account can download captures and bundles.", http.StatusForbidden)
		return
	}
	kind, name := r.PathValue("kind"), r.PathValue("name")
	if !diag.ValidName(kind, name) || s.o.StateDir == "" {
		http.NotFound(w, r)
		return
	}
	if kind == diag.Captures {
		var d ctl.DiagnosticsReply
		if err := s.call(r.Context(), ctl.Request{Cmd: "diagnostics"}, &d); err == nil && d.Capture.Running && d.Capture.Name == name {
			http.Error(w, "This capture is still recording. Download it when it has finished.", http.StatusConflict)
			return
		}
	}
	f, err := os.Open(filepath.Join(s.o.StateDir, kind, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := "application/gzip"
	if kind == diag.Captures {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	s.log.Info("web: file downloaded", "event", "web_download", "user", sessionOf(r).user, "file", name)
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (s *Server) aboutPage(w http.ResponseWriter, r *http.Request) {
	s.renderAbout(w, r, map[string]any{})
}

func (s *Server) renderAbout(w http.ResponseWriter, r *http.Request, extra map[string]any) {
	data := s.page(r, "About")
	var a ctl.AboutReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "about"}, &a); err != nil {
		data["Error"] = err.Error()
	}
	data["About"] = a
	for k, v := range extra {
		data[k] = v
	}
	s.render(w, r, http.StatusOK, "about", data)
}

func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	var u ctl.UpdateReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "update.check"}, &u); err != nil {
		u.Output = err.Error()
	}
	s.renderAbout(w, r, map[string]any{"Update": u})
}

func (s *Server) classToggle(w http.ResponseWriter, r *http.Request) {
	var c ctl.ClassReply
	if err := s.call(r.Context(), ctl.Request{Cmd: "class.toggle"}, &c); err != nil {
		data := s.page(r, "Pair a device")
		data["Error"], data["Back"] = err.Error(), "/pair"
		s.render(w, r, http.StatusBadRequest, "message", data)
		return
	}
	s.log.Info("web: class toggled", "event", "web_setting", "user", sessionOf(r).user, "key", "class", "class", c.Class)
	data := s.page(r, "Applying")
	data["Changed"] = "Class of Device " + c.Previous + " → " + c.Class
	data["Next"] = "/login"
	data["Refresh"] = "25;url=/login"
	s.render(w, r, http.StatusOK, "applying", data)
}
