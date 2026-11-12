package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"palm-gw/internal/config"
)

func TestAllowed(t *testing.T) {
	rules := []config.AllowRule{
		{Host: "192.168.1.20", Ports: []string{"tcp/14238", "udp/53"}},
		{Host: "10.4.0.0/23", Ports: []string{"tcp/22", "icmp"}},
	}
	for _, c := range []struct {
		host string
		port int
		want bool
	}{
		{"192.168.1.20", 14238, true},
		{"192.168.1.20", 53, false}, // udp only
		{"192.168.1.21", 14238, false},
		{"10.4.1.200", 22, true},
		{"10.4.2.1", 22, false},
		{"nas.local", 22, false},
	} {
		if got := allowed(rules, c.host, c.port); got != c.want {
			t.Errorf("%s:%d = %v", c.host, c.port, got)
		}
	}
}

func TestWriteAtomicKeepsMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	if string(b) != "new" || fi.Mode().Perm() != 0o640 {
		t.Errorf("%q %v", b, fi.Mode())
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".config.toml-*")); len(m) != 0 {
		t.Errorf("temp files left: %v", m)
	}
}
