//go:build linux

package pan

import (
	"fmt"
	"net"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// From include/net/bluetooth/bnep.h (kernel net/bluetooth/bnep/bnep.h).
const (
	btprotoBNEP     = 4
	bnepConnDel     = 0x400442C9 // _IOW('B', 201, int)
	bnepGetConnList = 0x800442D2 // _IOR('B', 210, int)
	maxConns        = 64
)

// struct bnep_conninfo
type connInfo struct {
	Flags  uint32
	Role   uint16
	State  uint16
	Dst    [6]byte
	Device [16]byte
}

// struct bnep_connlist_req
type connListReq struct {
	Cnum uint32
	CI   uintptr
}

// struct bnep_conndel_req
type connDelReq struct {
	Flags uint32
	Dst   [6]byte
}

func bnepSocket() (int, error) {
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, btprotoBNEP)
	if err != nil {
		return -1, fmt.Errorf("BNEP socket (is the bnep module loaded?): %w", err)
	}
	return fd, nil
}

// Conns lists the kernel's BNEP connections. The kernel keeps the address in
// display order (it byte-swaps the bdaddr when it builds the session).
func Conns() ([]Conn, error) {
	fd, err := bnepSocket()
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	var ci [maxConns]connInfo
	req := connListReq{Cnum: maxConns, CI: uintptr(unsafe.Pointer(&ci[0]))}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), bnepGetConnList, uintptr(unsafe.Pointer(&req))); e != 0 {
		return nil, fmt.Errorf("BNEPGETCONNLIST: %w", e)
	}
	out := make([]Conn, 0, req.Cnum)
	for i := 0; i < int(req.Cnum) && i < maxConns; i++ {
		c := ci[i]
		name := string(c.Device[:])
		if n := strings.IndexByte(name, 0); n >= 0 {
			name = name[:n]
		}
		out = append(out, Conn{
			Addr:   strings.ToUpper(net.HardwareAddr(c.Dst[:]).String()),
			Ifname: name,
			Role:   c.Role,
			State:  c.State,
		})
	}
	return out, nil
}

// Kill drops the BNEP connection to addr (needs CAP_NET_ADMIN). BlueZ sees
// the L2CAP channel close and the bnepN interface goes away.
func Kill(addr string) error {
	mac, err := net.ParseMAC(addr)
	if err != nil || len(mac) != 6 {
		return fmt.Errorf("bad address %q", addr)
	}
	fd, err := bnepSocket()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var req connDelReq
	copy(req.Dst[:], mac)
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), bnepConnDel, uintptr(unsafe.Pointer(&req))); e != 0 {
		return fmt.Errorf("BNEPCONNDEL %s: %w", addr, e)
	}
	return nil
}
