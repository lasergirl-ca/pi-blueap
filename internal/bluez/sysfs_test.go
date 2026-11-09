package bluez

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeSysfs builds a tree shaped like a Pi 5 with its onboard UART radio as
// hci0 and a USB dongle as hci1, plus hci2 with no subsystem link.
func fakeSysfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ln := func(target, link string) {
		mk(filepath.Dir(link))
		if err := os.Symlink(filepath.Join(root, target), filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	mk("bus/serial")
	mk("bus/usb")
	uart := "devices/platform/soc/107d50c000.serial/serial0/serial0-0"
	usb := "devices/platform/axi/1000480000.usb/usb3/3-1/3-1:1.0"
	other := "devices/platform/mmc/mmc1:0001/mmc1:0001:2"
	for _, d := range []string{uart, usb, other} {
		mk(d)
	}
	ln("bus/serial", uart+"/subsystem")
	ln("bus/usb", usb+"/subsystem")
	ln(uart, "class/bluetooth/hci0/device")
	ln(usb, "class/bluetooth/hci1/device")
	ln(other, "class/bluetooth/hci2/device")
	return root
}

func TestHWBus(t *testing.T) {
	old := SysfsRoot
	t.Cleanup(func() { SysfsRoot = old })
	SysfsRoot = fakeSysfs(t)
	for hci, want := range map[string]string{"hci0": "serial", "hci1": "usb", "hci2": "", "hci9": ""} {
		if got := HWBus(hci); got != want {
			t.Errorf("HWBus(%s) = %q, want %q", hci, got, want)
		}
	}
}

func TestChooseAuto(t *testing.T) {
	// Every adapter reports BlueZ's own Modalias; it must not decide (D-014).
	const bluezID = "usb:v1D6Bp0246d0552"
	ad := func(hci, bus string) *Adapter {
		return &Adapter{HCI: hci, HWBus: bus, Props: AdapterProps{Modalias: bluezID}}
	}
	cases := []struct {
		in   []*Adapter
		want string
		how  string
	}{
		{[]*Adapter{ad("hci0", "serial")}, "hci0", "auto: only adapter"},
		{[]*Adapter{ad("hci0", "serial"), ad("hci1", "usb")}, "hci1", "auto: USB dongle preferred"},
		{[]*Adapter{ad("hci0", "usb"), ad("hci1", "serial")}, "hci0", "auto: USB dongle preferred"},
		{[]*Adapter{ad("hci0", "serial"), ad("hci1", "")}, "hci0", "auto: lowest hciN"},
	}
	for _, c := range cases {
		a, how := chooseAuto(c.in)
		if a.HCI != c.want || how != c.how {
			t.Errorf("chooseAuto = %s %q, want %s %q", a.HCI, how, c.want, c.how)
		}
	}
}
