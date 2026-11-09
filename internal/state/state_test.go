package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenMissingAddRemoveReload(t *testing.T) {
	p := Path(t.TempDir())
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Devices()) != 0 || s.Has("00:11:22:33:44:55") {
		t.Fatal("new store must be empty")
	}
	if err := s.Add("00:11:22:33:44:55", "Palm T|X"); err != nil {
		t.Fatal(err)
	}
	if !s.Has("00:11:22:33:44:55") || !s.Has(strings.ToLower("00:11:22:33:44:55")) {
		t.Fatal("Has must be case-insensitive")
	}
	if err := s.Add("aa:bb:cc:dd:ee:ff", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLabel("AA:BB:CC:DD:EE:FF", "LifeDrive"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetClass(0x104); err != nil {
		t.Fatal(err)
	}

	// Reload from disk.
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	devs := s2.Devices()
	if len(devs) != 2 || devs[0].Address != "00:11:22:33:44:55" || devs[1].Label != "LifeDrive" {
		t.Fatalf("reload mismatch: %+v", devs)
	}
	if devs[0].Name != "Palm T|X" || devs[0].FirstPaired.IsZero() || devs[0].LastSeen.IsZero() {
		t.Fatalf("fields lost: %+v", devs[0])
	}
	if s2.Class() != 0x104 {
		t.Fatalf("class not persisted: %x", s2.Class())
	}

	// Re-adding keeps first_paired, refreshes name.
	first := devs[0].FirstPaired
	if err := s2.Add("00:11:22:33:44:55", "Renamed"); err != nil {
		t.Fatal(err)
	}
	d, _ := s2.Get("00:11:22:33:44:55")
	if !d.FirstPaired.Equal(first) || d.Name != "Renamed" {
		t.Fatalf("re-add changed first_paired or missed name: %+v", d)
	}

	removed, err := s2.Remove("00:11:22:33:44:55")
	if err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	removed, _ = s2.Remove("00:11:22:33:44:55")
	if removed {
		t.Fatal("second remove must report absent")
	}
	if err := s2.SetLabel("00:11:22:33:44:55", "x"); err == nil {
		t.Fatal("label on unknown device must fail")
	}

	// No temp file left behind; the file is valid JSON.
	if _, err := os.Stat(p + ".tmp"); err == nil {
		t.Fatal("temp file left behind")
	}
	if _, err := Open(p); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("corrupt state must be an error, not silently reset")
	}
}
