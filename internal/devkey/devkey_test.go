package devkey

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssueCheckRevoke(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 17, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	key, e, err := s.Issue("sysadmin", "admin", "  Zoë's Palm TX ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "pgk1."+e.ID+".") || len(key) != len("pgk1.")+12+1+64 || e.Name != "Zoë's Palm TX" || e.Hash != "" {
		t.Errorf("key %q entry %+v", key, e)
	}
	fi, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	if fi.Mode().Perm() != 0o600 || strings.Contains(string(b), key[len("pgk1.")+13:]) {
		t.Errorf("file mode %v, or it holds the secret", fi.Mode())
	}
	now = now.Add(time.Hour)
	got, err := s.Check(key, "10.42.0.10")
	if err != nil || got.User != "sysadmin" || got.Role != "admin" || got.LastIP != "10.42.0.10" || !got.LastUsed.Equal(now) {
		t.Errorf("check: %+v %v", got, err)
	}
	// A reopened store knows the key and its last use.
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if l := s2.List(); len(l) != 1 || l[0].LastIP != "10.42.0.10" || l[0].Hash != "" {
		t.Errorf("list %+v", l)
	}
	for _, bad := range []string{"", "pgk1.", key[:len(key)-1] + "0", "pgk1." + e.ID, "pgk2" + key[4:], strings.Replace(key, e.ID, "000000000000", 1)} {
		if _, err := s2.Check(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := s2.Revoke(e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Check(key, ""); err == nil {
		t.Error("revoked key accepted")
	}
	if _, err := s2.Revoke(e.ID); err == nil {
		t.Error("revoked twice")
	}
	if _, _, err := s2.Issue("x", "root", "p"); err == nil {
		t.Error("bad role issued")
	}
}

func TestMaxKeys(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), FileName))
	for i := 0; i < MaxKeys; i++ {
		if _, _, err := s.Issue("u", "viewer", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.Issue("u", "viewer", ""); err == nil {
		t.Error("issued past the limit")
	}
}
