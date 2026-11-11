// Package rfkill clears the soft rfkill block on a Bluetooth adapter (D-046).
// A soft-blocked adapter is listed by BlueZ but cannot be powered ("set
// hciN.Powered: Failed"). Raspberry Pi OS restores each radio's saved block
// state at boot (systemd-rfkill, /var/lib/systemd/rfkill), and the stock image
// ships a USB Bluetooth port saved as blocked, so a dongle in that port starts
// blocked. palm-gwd unblocks the adapter it is configured to use; systemd-rfkill
// saves the new state, so the block does not come back on the next boot.
package rfkill

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Where the kernel lists rfkill switches and takes changes. Variables so tests can
// point them at a temporary directory.
var (
	SysDir  = "/sys/class/rfkill"
	DevPath = "/dev/rfkill"
)

// linux/rfkill.h
const (
	typeBluetooth = 2
	opChange      = 2
)

// ErrHardBlocked means a hardware switch blocks the radio; software cannot clear it.
var ErrHardBlocked = errors.New("rfkill: hard-blocked")

// Switch is one rfkill switch as /sys/class/rfkill shows it.
type Switch struct {
	Index uint32
	Name  string // hci0, phy0, ...
	Type  string // bluetooth, wlan, ...
	Soft  bool
	Hard  bool
}

// Find returns the Bluetooth rfkill switch named hci (hci0, hci1, ...), or
// found=false if the adapter has none.
func Find(hci string) (sw Switch, found bool, err error) {
	entries, err := os.ReadDir(SysDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Switch{}, false, nil
		}
		return Switch{}, false, err
	}
	for _, e := range entries {
		dir := filepath.Join(SysDir, e.Name())
		if read(dir, "name") != hci || read(dir, "type") != "bluetooth" {
			continue
		}
		idx, err := strconv.ParseUint(read(dir, "index"), 10, 32)
		if err != nil {
			return Switch{}, false, fmt.Errorf("rfkill: %s: index: %w", dir, err)
		}
		return Switch{
			Index: uint32(idx),
			Name:  hci,
			Type:  "bluetooth",
			Soft:  read(dir, "soft") == "1",
			Hard:  read(dir, "hard") == "1",
		}, true, nil
	}
	return Switch{}, false, nil
}

func read(dir, f string) string {
	b, err := os.ReadFile(filepath.Join(dir, f))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Unblock clears the soft block on hci's switch. It reports whether there was one
// to clear; a hard block returns ErrHardBlocked. An adapter with no rfkill switch
// is left alone.
func Unblock(hci string) (bool, error) {
	sw, found, err := Find(hci)
	if err != nil || !found {
		return false, err
	}
	if sw.Hard {
		return false, fmt.Errorf("%s: %w", hci, ErrHardBlocked)
	}
	if !sw.Soft {
		return false, nil
	}
	// struct rfkill_event: __u32 idx; __u8 type, op, soft, hard. The kernel takes
	// this 8-byte form from any version of the struct.
	ev := make([]byte, 8)
	binary.LittleEndian.PutUint32(ev, sw.Index)
	ev[4], ev[5], ev[6], ev[7] = typeBluetooth, opChange, 0, 0
	f, err := os.OpenFile(DevPath, os.O_WRONLY, 0)
	if err != nil {
		return false, fmt.Errorf("rfkill: %w", err)
	}
	if _, err := f.Write(ev); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("rfkill: unblock %s: %w", hci, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("rfkill: %w", err)
	}
	return true, nil
}
