//go:build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// dialRFCOMM opens an RFCOMM connection from the local adapter address to
// remote:channel and returns it as a blocking *os.File.
func dialRFCOMM(local, remote string, channel int) (*os.File, error) {
	la, err := bdaddr(local)
	if err != nil {
		return nil, err
	}
	ra, err := bdaddr(remote)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_STREAM, unix.BTPROTO_RFCOMM)
	if err != nil {
		return nil, fmt.Errorf("rfcomm socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrRFCOMM{Addr: la, Channel: 0}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("rfcomm bind %s: %w", local, err)
	}
	if err := unix.Connect(fd, &unix.SockaddrRFCOMM{Addr: ra, Channel: uint8(channel)}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("rfcomm connect %s channel %d: %w", remote, channel, err)
	}
	return os.NewFile(uintptr(fd), fmt.Sprintf("rfcomm:%s:%d", remote, channel)), nil
}

// bdaddr parses XX:XX:XX:XX:XX:XX into the little-endian byte order the
// kernel's bdaddr_t uses.
func bdaddr(s string) ([6]uint8, error) {
	var out [6]uint8
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return out, fmt.Errorf("bad Bluetooth address %q", s)
	}
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return out, fmt.Errorf("bad Bluetooth address %q", s)
		}
		out[5-i] = uint8(v)
	}
	return out, nil
}
