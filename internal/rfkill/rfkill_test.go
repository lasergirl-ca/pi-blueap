package rfkill

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fake lays out /sys/class/rfkill-style switches and a /dev/rfkill stand-in.
func fake(t *testing.T, switches ...Switch) string {
	t.Helper()
	dir := t.TempDir()
	SysDir = filepath.Join(dir, "sys")
	DevPath = filepath.Join(dir, "rfkill")
	t.Cleanup(func() { SysDir, DevPath = "/sys/class/rfkill", "/dev/rfkill" })
	b := map[bool]string{false: "0\n", true: "1\n"}
	for _, s := range switches {
		d := filepath.Join(SysDir, "rfkill"+itoa(s.Index))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, v := range map[string]string{
			"index": itoa(s.Index) + "\n", "name": s.Name + "\n", "type": s.Type + "\n",
			"soft": b[s.Soft], "hard": b[s.Hard],
		} {
			if err := os.WriteFile(filepath.Join(d, f), []byte(v), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(DevPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return DevPath
}

func itoa(i uint32) string {
	return string([]byte{byte('0' + i)})
}

func TestUnblockSoftBlocked(t *testing.T) {
	dev := fake(t,
		Switch{Index: 0, Name: "hci0", Type: "bluetooth"},
		Switch{Index: 1, Name: "hci1", Type: "bluetooth", Soft: true},
		Switch{Index: 2, Name: "phy0", Type: "wlan", Soft: true},
	)
	was, err := Unblock("hci1")
	if err != nil || !was {
		t.Fatalf("Unblock(hci1) = %v, %v; want true, nil", was, err)
	}
	got, _ := os.ReadFile(dev)
	want := []byte{1, 0, 0, 0, typeBluetooth, opChange, 0, 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("wrote % x, want % x", got, want)
	}
}

func TestUnblockNothingToDo(t *testing.T) {
	dev := fake(t,
		Switch{Index: 0, Name: "hci0", Type: "bluetooth"},
		Switch{Index: 2, Name: "phy0", Type: "wlan", Soft: true},
	)
	for _, hci := range []string{"hci0", "hci7", "phy0"} {
		was, err := Unblock(hci)
		if err != nil || was {
			t.Errorf("Unblock(%s) = %v, %v; want false, nil", hci, was, err)
		}
	}
	if got, _ := os.ReadFile(dev); len(got) != 0 {
		t.Fatalf("wrote % x to /dev/rfkill, want nothing", got)
	}
}

func TestUnblockHardBlocked(t *testing.T) {
	fake(t, Switch{Index: 3, Name: "hci0", Type: "bluetooth", Soft: true, Hard: true})
	if _, err := Unblock("hci0"); !errors.Is(err, ErrHardBlocked) {
		t.Fatalf("Unblock = %v, want ErrHardBlocked", err)
	}
}

func TestNoRfkillAtAll(t *testing.T) {
	SysDir = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { SysDir = "/sys/class/rfkill" })
	if was, err := Unblock("hci0"); err != nil || was {
		t.Fatalf("Unblock = %v, %v; want false, nil", was, err)
	}
}
