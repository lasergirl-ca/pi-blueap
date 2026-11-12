package diag

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const gwdLine = `{"__CURSOR":"s=1;i=2","__REALTIME_TIMESTAMP":"1794505782603038","_SYSTEMD_UNIT":"palm-gwd.service","PRIORITY":"6","MESSAGE":"{\"time\":\"2026-11-12T10:49:42-07:00\",\"level\":\"WARN\",\"msg\":\"pairing rejected\",\"component\":\"agent\",\"event\":\"pairing_rejected\",\"addr\":\"00:07:E0:3C:5E:FA\",\"reason\":\"no_window\",\"n\":3}"}`
const btLine = `{"__CURSOR":"s=1;i=3","__REALTIME_TIMESTAMP":"1794505783000000","_SYSTEMD_UNIT":"bluetooth.service","PRIORITY":"3","MESSAGE":"src/adapter.c:new_link_key() 00:07:E0:3C:5E:FA type 0"}`
const bytesLine = `{"__CURSOR":"s=1;i=4","__REALTIME_TIMESTAMP":"1794505784000000","_SYSTEMD_UNIT":"palm-gw-dnsmasq.service","PRIORITY":"6","MESSAGE":[104,105,255]}`

func TestParseEntry(t *testing.T) {
	e, ok := ParseEntry([]byte(gwdLine))
	if !ok || e.Unit != "palm-gwd" || e.Level != "WARN" || e.Msg != "pairing rejected" || e.Event != "pairing_rejected" ||
		e.Addr != "00:07:E0:3C:5E:FA" || e.Cursor != "s=1;i=2" || e.Time.UnixMicro() != 1794505782603038 {
		t.Fatalf("%+v", e)
	}
	if got := strings.Join(e.FieldList(), " "); got != "component=agent n=3 reason=no_window" {
		t.Errorf("fields %q", got)
	}
	b, _ := ParseEntry([]byte(btLine))
	if b.Unit != "bluetooth" || b.Level != "ERROR" || b.Event != "" || !strings.Contains(b.Msg, "new_link_key") {
		t.Errorf("%+v", b)
	}
	started := `{"__CURSOR":"c","_SYSTEMD_UNIT":"init.scope","UNIT":"palm-gwd.service","PRIORITY":"6","MESSAGE":"Started palm-gwd.service"}`
	if s, _ := ParseEntry([]byte(started)); s.Unit != "palm-gwd" {
		t.Errorf("systemd line about palm-gwd: %q", s.Unit)
	}
	d, _ := ParseEntry([]byte(bytesLine))
	if d.Unit != "dnsmasq" || d.Msg != "hi?" {
		t.Errorf("%+v", d)
	}
	if _, ok := ParseEntry([]byte("not json")); ok {
		t.Error("parsed junk")
	}
}

func TestFilterAndCollect(t *testing.T) {
	in := strings.Join([]string{gwdLine, btLine, bytesLine, "junk"}, "\n")
	for _, c := range []struct {
		f    Filter
		want int
	}{
		{Filter{}, 3},
		{Filter{Event: "pairing_rejected"}, 1},
		{Filter{Event: "pairing_*"}, 1},
		{Filter{Event: "paired"}, 0},
		{Filter{Text: "00:07:e0:3c:5e:fa"}, 2}, // case-insensitive, message or addr
		{Filter{Text: "no_window"}, 1},         // a field
	} {
		es, cursor := Collect(strings.NewReader(in), ReadOptions{Filter: c.f})
		if len(es) != c.want || cursor != "s=1;i=4" {
			t.Errorf("%+v: %d entries, cursor %q", c.f, len(es), cursor)
		}
	}
	es, _ := Collect(strings.NewReader(in), ReadOptions{Lines: 2})
	if len(es) != 2 || es[1].Unit != "dnsmasq" {
		t.Errorf("newest 2: %+v", es)
	}
}

func TestReadArgs(t *testing.T) {
	a, _ := ReadOptions{}.args()
	if got := strings.Join(a, " "); got != "-o json --no-pager -q -u palm-gwd.service -u palm-gw-dnsmasq.service -u bluetooth.service -n 500" {
		t.Errorf("%s", got)
	}
	a, _ = ReadOptions{Units: []string{"apply"}, After: "s=1", Filter: Filter{Event: "x"}}.args()
	if got := strings.Join(a, " "); got != "-o json --no-pager -q -u palm-gw-apply.service --after-cursor=s=1" {
		t.Errorf("%s", got)
	}
	a, _ = ReadOptions{Lines: 2000, Filter: Filter{Text: "x"}}.args()
	if a[len(a)-1] != "20000" {
		t.Errorf("filtered read goes further back: %v", a)
	}
	a, _ = ReadOptions{Lines: 5, Filter: Filter{Text: "x"}}.args()
	if a[len(a)-1] != "5000" {
		t.Errorf("filtered read goes further back: %v", a)
	}
	if _, err := (ReadOptions{Units: []string{"sshd"}}).args(); err == nil {
		t.Error("unknown unit accepted")
	}
}

func TestRedact(t *testing.T) {
	in := "[web]\nusername      = \"admin\"\npassword_hash = \"$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA\"  # set by setup\n" +
		"viewer_password_hash = \"$argon2id$v=19$x$y\"\n" +
		`{"msg":"x","hash":"$argon2id$v=19$m=1$abc$def"}` + "\npsk=hunter2hunter2\n"
	out := Redact(in)
	if strings.Contains(out, "argon2") || strings.Contains(out, "hunter2") || !strings.Contains(out, `password_hash = "REDACTED"`) ||
		!strings.Contains(out, `viewer_password_hash = "REDACTED"`) || !strings.Contains(out, `username      = "admin"`) {
		t.Errorf("%s", out)
	}
}

func TestValidName(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 4, 5, 0, time.UTC)
	if n := NewName(Captures, now); n != "capture-20261112-100405.btsnoop" || !ValidName(Captures, n) {
		t.Errorf("%s", n)
	}
	for _, bad := range []string{"../state.json", "capture-20261112-100405.btsnoop/../../x", "palm-gw-support-x.tar.gz", ""} {
		if ValidName(Captures, bad) || ValidName(Bundles, bad) {
			t.Errorf("%q valid", bad)
		}
	}
	if ValidName("tls", "cert.pem") {
		t.Error("unknown kind")
	}
}

func TestWriteBundle(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "c.btsnoop")
	_ = os.WriteFile(capture, []byte("btsnoop\x00raw $argon2id$keep"), 0o600)
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	var name string
	for i := 0; i < 7; i++ {
		var err error
		name, err = WriteBundle(context.Background(), dir, start.Add(time.Duration(i)*time.Second), []Item{
			{Name: "config.toml", Text: "password_hash = \"$argon2id$v=19$a$b\"\n"},
			{Name: "c.btsnoop", File: capture},
			{Name: "gone", File: filepath.Join(dir, "missing")},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	files, _ := List(dir, Bundles)
	if len(files) != keep || files[0].Name != name || name != "palm-gw-support-20261112-100006.tar.gz" {
		t.Errorf("kept %v", files)
	}
	f, _ := os.Open(filepath.Join(dir, name))
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		got[h.Name] = string(b)
	}
	top := "palm-gw-support-20261112-100006/"
	if got[top+"config.toml"] != "password_hash = \"REDACTED\"\n" {
		t.Errorf("config: %q", got[top+"config.toml"])
	}
	if got[top+"c.btsnoop"] != "btsnoop\x00raw $argon2id$keep" { // binary copied as is
		t.Errorf("capture: %q", got[top+"c.btsnoop"])
	}
	if !strings.Contains(got[top+"gone.error.txt"], "could not read") {
		t.Errorf("missing file: %v", got)
	}
	if fi, _ := os.Stat(filepath.Join(dir, name)); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", fi.Mode())
	}
}
