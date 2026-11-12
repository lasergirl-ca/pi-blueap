package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogsPage(t *testing.T) {
	f, ts, c, _ := loggedIn(t, "viewer", "viewer-pw")
	_, page, _ := get(t, c, ts.URL+"/logs?event=paired&q=00:07&unit=palm-gwd&unit=bogus&n=100")
	req, _ := f.last("logs")
	if req.Logs.Event != "paired" || req.Logs.Text != "00:07" || req.Logs.Lines != 100 || len(req.Logs.Units) != 1 {
		t.Errorf("request %+v", req.Logs)
	}
	for _, want := range []string{`<a href="/logs">Logs</a>`, `value="paired"`, "Clear filters",
		`<a href="/logs?event=paired">paired</a>`, `<a href="/logs?q=00%3a07%3aE0%3a3C%3a5E%3aFA">00:07:E0:3C:5E:FA</a>`,
		`<span class="muted">name=Zoë&#39;s Palm TX</span>`, `class="log lvl-ERROR"`} {
		if !strings.Contains(page, want) {
			t.Errorf("logs page lacks %s", want)
		}
	}
	if strings.Index(page, "link key missing") > strings.Index(page, ">paired</a>") {
		t.Error("not newest first")
	}
	if strings.Contains(page, "log-poller") {
		t.Error("polls without Follow")
	}
	_, page, _ = get(t, c, ts.URL+"/logs?follow=1")
	if !strings.Contains(page, `hx-get="/partials/logs?follow=1&amp;after=c2"`) {
		t.Errorf("follow poller missing")
	}
	_, part, _ := get(t, c, ts.URL+"/partials/logs?follow=1&after=c2")
	if !strings.Contains(part, `after=c3"`) || !strings.Contains(part, `hx-swap-oob="afterbegin:#log-rows"`) || !strings.Contains(part, "session up") {
		t.Errorf("follow partial:\n%s", part)
	}
}

func TestDiagnosticsPage(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "admin", "admin-pw")
	_, page, _ := get(t, c, ts.URL+"/diagnostics")
	for _, want := range []string{"Raspberry Pi 5 Model B Rev 1.0", "USB dongle 60:15:6F:EA:00:81 (hci0), powered",
		`<span class="error">palm-gw-dnsmasq: failed</span>`, "rfcomm (not loaded)", "under-voltage since boot",
		`href="/diagnostics/files/captures/capture-20261112-100000.btsnoop"`, "Start capture", "Make a support bundle"} {
		if !strings.Contains(page, want) {
			t.Errorf("diagnostics page lacks %s", want)
		}
	}
	code, _, _ := post(t, c, ts.URL+"/diagnostics/capture", url.Values{"csrf": {tok}, "seconds": {"30"}})
	if req, _ := f.last("btmon"); code != http.StatusSeeOther || req.Seconds != 30 {
		t.Errorf("capture: %d %d", code, req.Seconds)
	}
	_, page, _ = get(t, c, ts.URL+"/diagnostics")
	if !strings.Contains(page, "data-expires=") || strings.Contains(page, "Start capture") {
		t.Error("running capture not shown")
	}
	_, _, body := post(t, c, ts.URL+"/diagnostics/bundle", url.Values{"csrf": {tok}, "with_capture": {"1"}})
	if req, _ := f.last("bundle"); !req.WithCapture || !strings.Contains(body, `href="/diagnostics/files/bundles/palm-gw-support-20261112-110000.tar.gz"`) {
		t.Errorf("bundle:\n%s", body)
	}
	// Downloads: the named file only.
	_ = os.MkdirAll(filepath.Join(f.stateDir, "captures"), 0o750)
	_ = os.WriteFile(filepath.Join(f.stateDir, "captures", "capture-20261112-100000.btsnoop"), []byte("btsnoop"), 0o600)
	_ = os.WriteFile(filepath.Join(f.stateDir, "state.json"), []byte("{}"), 0o600)
	resp, err := c.Get(ts.URL + "/diagnostics/files/captures/capture-20261112-100000.btsnoop")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") || resp.ContentLength != 7 {
		t.Errorf("download %d %v", resp.StatusCode, resp.Header)
	}
	for _, bad := range []string{"/diagnostics/files/captures/..%2fstate.json", "/diagnostics/files/tls/cert.pem", "/diagnostics/files/captures/capture-20261112-100001.btsnoop"} {
		if code, _, _ := get(t, c, ts.URL+bad); code != http.StatusNotFound {
			t.Errorf("%s = %d", bad, code)
		}
	}
}

func TestDiagnosticsViewer(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "viewer", "viewer-pw")
	_, page, _ := get(t, c, ts.URL+"/diagnostics")
	if !strings.Contains(page, "Health check") || strings.Contains(page, "Start capture") || strings.Contains(page, "/diagnostics/files/") {
		t.Error("viewer sees captures or bundles")
	}
	_ = os.MkdirAll(filepath.Join(f.stateDir, "captures"), 0o750)
	_ = os.WriteFile(filepath.Join(f.stateDir, "captures", "capture-20261112-100000.btsnoop"), []byte("btsnoop"), 0o600)
	if code, _, _ := get(t, c, ts.URL+"/diagnostics/files/captures/capture-20261112-100000.btsnoop"); code != http.StatusForbidden {
		t.Errorf("viewer download = %d", code)
	}
	for _, u := range []string{"/diagnostics/capture", "/diagnostics/bundle", "/about/check", "/pair/class"} {
		if code, _, _ := post(t, c, ts.URL+u, url.Values{"csrf": {tok}}); code != http.StatusForbidden {
			t.Errorf("viewer POST %s = %d", u, code)
		}
	}
}

func TestAboutAndClass(t *testing.T) {
	f, ts, c, tok := loggedIn(t, "admin", "admin-pw")
	_, page, _ := get(t, c, ts.URL+"/about")
	if !strings.Contains(page, "BlueZ 5.82, adapter mode dongle") || !strings.Contains(page, "Check for updates") || !strings.Contains(page, `<a href="/about">About</a>`) {
		t.Errorf("about:\n%s", page)
	}
	if _, ok := f.last("update.check"); ok {
		t.Error("checked for updates without being asked")
	}
	_, _, body := post(t, c, ts.URL+"/about/check", url.Values{"csrf": {tok}})
	if !strings.Contains(body, "no release has been published yet") {
		t.Errorf("update check:\n%s", body)
	}
	_, page, _ = get(t, c, ts.URL+"/pair")
	if !strings.Contains(page, "Palm can't see the gateway?") || !strings.Contains(page, "Switch the device class to 0x000104") {
		t.Errorf("pair page class card missing")
	}
	_, _, body = post(t, c, ts.URL+"/pair/class", url.Values{"csrf": {tok}})
	if !strings.Contains(body, "Class of Device 0x000300 → 0x000104") || !strings.Contains(body, `content="25;url=/login"`) {
		t.Errorf("class toggle:\n%s", body)
	}
}
