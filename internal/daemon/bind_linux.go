package daemon

import "golang.org/x/sys/unix"

// bindToDevice ties a socket to one interface (SO_BINDTODEVICE, which palm-gwd's
// CAP_NET_RAW allows), so a check goes out through that uplink whatever the
// routes prefer (D-074).
func bindToDevice(fd int, iface string) error {
	return unix.SetsockoptString(fd, unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
}
