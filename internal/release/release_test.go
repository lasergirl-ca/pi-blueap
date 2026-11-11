package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebVersion(t *testing.T) {
	for tag, want := range map[string]string{
		"v1.2.3":     "1.2.3",
		"v0.6.0-rc1": "0.6.0~rc1",
		"v1.0.0":     "1.0.0",
	} {
		if got := DebVersion(tag); got != want {
			t.Errorf("DebVersion(%q) = %q, want %q", tag, got, want)
		}
	}
	for tag, want := range map[string]bool{"v1.2.3": true, "v0.6.0-rc1": true, "1.2.3": false, "v": false, "v1;rm": false, "": false} {
		if got := ValidTag(tag); got != want {
			t.Errorf("ValidTag(%q) = %v", tag, got)
		}
	}
}

func TestParseSums(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	a, err := ParseSums(strings.NewReader(sum + "  install.sh\n" + sum + " *palm-gw_1.2.3_arm64.deb\n"))
	if err != nil || a.Name != "palm-gw_1.2.3_arm64.deb" || a.SHA256 != sum {
		t.Fatalf("got %+v, %v", a, err)
	}
	for name, in := range map[string]string{
		"none":    sum + "  install.sh\n",
		"two":     sum + "  palm-gw_1_arm64.deb\n" + sum + "  palm-gw_2_arm64.deb\n",
		"badsum":  "abc  palm-gw_1_arm64.deb\n",
		"badname": sum + "  ../palm-gw_1_arm64.deb\n",
	} {
		if _, err := ParseSums(strings.NewReader(in)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func release(t *testing.T, deb []byte, sumOf []byte) string {
	t.Helper()
	dir := t.TempDir()
	h := sha256.Sum256(sumOf)
	sums := hex.EncodeToString(h[:]) + "  palm-gw_1.2.3_arm64.deb\n"
	if err := os.WriteFile(filepath.Join(dir, SumsFile), []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "palm-gw_1.2.3_arm64.deb"), deb, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFetch(t *testing.T) {
	deb := []byte("not really a deb")
	srv := httptest.NewServer(http.FileServer(http.Dir(release(t, deb, deb))))
	defer srv.Close()

	// Over HTTP.
	out := t.TempDir()
	path, a, err := Client{}.Fetch(context.Background(), srv.URL+"/", out)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(deb) || a.Name != "palm-gw_1.2.3_arm64.deb" {
		t.Fatalf("fetched %q as %+v", got, a)
	}

	// From a directory on disk.
	dir := release(t, deb, deb)
	if _, _, err := (Client{}).Fetch(context.Background(), "file://"+dir, t.TempDir()); err != nil {
		t.Fatalf("file://: %v", err)
	}
}

func TestFetchMismatch(t *testing.T) {
	dir := release(t, []byte("tampered"), []byte("original"))
	out := t.TempDir()
	if _, _, err := (Client{}).Fetch(context.Background(), "file://"+dir, out); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Fatalf("left %v behind", ents)
	}
}

func TestLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","name":"x"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	tag, err := Client{API: srv.URL}.Latest(context.Background())
	if err != nil || tag != "v1.2.3" {
		t.Fatalf("got %q, %v", tag, err)
	}
	if _, err := (Client{API: srv.URL + "/none"}).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "no release") {
		t.Fatalf("missing release: %v", err)
	}
	if got := (Client{}).BaseURL("v1.2.3"); got != "https://github.com/"+Repo+"/releases/download/v1.2.3" {
		t.Fatalf("BaseURL = %q", got)
	}
}
