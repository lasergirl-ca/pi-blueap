package bluez

import (
	"path/filepath"
	"strings"
)

// SysfsRoot is where HWBus looks; tests point it at a temporary tree.
var SysfsRoot = "/sys"

// HWBus reports which hardware bus a controller sits on, read from sysfs:
// "usb" for a dongle, "serial" for the Pi's onboard UART radio, "sdio",
// "pci", or "" if it cannot tell. Adapter1.Modalias cannot answer this: it is
// BlueZ's own DeviceID and the same for every adapter (D-014).
func HWBus(hci string) string {
	dev := filepath.Join(SysfsRoot, "class", "bluetooth", hci, "device")
	if sub, err := filepath.EvalSymlinks(filepath.Join(dev, "subsystem")); err == nil {
		return filepath.Base(sub)
	}
	// No subsystem link: fall back to the resolved device path.
	p, err := filepath.EvalSymlinks(dev)
	if err != nil {
		return ""
	}
	for _, bus := range []string{"usb", "sdio", "pci", "serial"} {
		if strings.Contains(p, "/"+bus) {
			return bus
		}
	}
	return ""
}

// chooseAuto implements adapter = "auto" (PLAN.md section 8.1, as amended by
// D-014): one adapter → it; several → the first on the USB bus, else the
// lowest hciN. adapters must be sorted by hciN.
func chooseAuto(adapters []*Adapter) (*Adapter, string) {
	if len(adapters) == 1 {
		return adapters[0], "auto: only adapter"
	}
	for _, a := range adapters {
		if a.HWBus == "usb" {
			return a, "auto: USB dongle preferred"
		}
	}
	return adapters[0], "auto: lowest hciN"
}
