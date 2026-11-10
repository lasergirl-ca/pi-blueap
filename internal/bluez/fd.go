package bluez

import (
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// FileFromFD takes ownership of a descriptor BlueZ handed over in
// Profile1.NewConnection (PLAN.md section 8.3, steps 1 and 2). godbus has
// already received it over SCM_RIGHTS, so this process owns it. bluetoothd
// keeps its own reference until the link drops, so ending a session needs
// ppp.HangUp, not just a close (DECISIONS.md D-028). The socket is switched to blocking mode
// because pppd expects blocking stdin and stdout.
func FileFromFD(fd dbus.UnixFD, name string) (*os.File, error) {
	if fd < 0 {
		return nil, fmt.Errorf("bluez: invalid fd %d", fd)
	}
	if err := unix.SetNonblock(int(fd), false); err != nil {
		_ = unix.Close(int(fd))
		return nil, fmt.Errorf("bluez: set blocking on fd %d: %w", fd, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}
