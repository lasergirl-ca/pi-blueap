//go:build linux

package fw

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	nfnlSubsysCTNetlink = 1
	ipctnlMsgCTGet      = 1
)

// Conntrack dumps the kernel's IPv4 connection tracking table. It needs
// CAP_NET_ADMIN, which palm-gwd has.
func Conntrack() ([]Conn, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_NETFILTER)
	if err != nil {
		return nil, fmt.Errorf("conntrack: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("conntrack: %w", err)
	}
	tv := unix.Timeval{Sec: 3}
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)

	const seq = 1
	req := make([]byte, unix.NLMSG_HDRLEN+4)
	binary.NativeEndian.PutUint32(req[0:4], uint32(len(req)))
	binary.NativeEndian.PutUint16(req[4:6], nfnlSubsysCTNetlink<<8|ipctnlMsgCTGet)
	binary.NativeEndian.PutUint16(req[6:8], unix.NLM_F_REQUEST|unix.NLM_F_DUMP)
	binary.NativeEndian.PutUint32(req[8:12], seq)
	req[unix.NLMSG_HDRLEN] = unix.AF_INET // nfgenmsg: family, version 0, res_id 0
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("conntrack: %w", err)
	}

	var out []Conn
	buf := make([]byte, 1<<16)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return nil, fmt.Errorf("conntrack: %w", err)
		}
		msgs, err := splitNetlink(buf[:n])
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			switch m.typ {
			case unix.NLMSG_DONE:
				return out, nil
			case unix.NLMSG_ERROR:
				if len(m.data) >= 4 {
					if e := int32(binary.NativeEndian.Uint32(m.data[:4])); e != 0 {
						return nil, fmt.Errorf("conntrack: %w", unix.Errno(-e))
					}
				}
				return out, nil
			}
			if len(m.data) < 4 {
				continue
			}
			c, err := ParseConn(m.data[4:])
			if err != nil {
				continue // one odd entry should not hide the rest
			}
			out = append(out, c)
		}
	}
}

type nlMsg struct {
	typ  uint16
	data []byte
}

// splitNetlink cuts a datagram into messages: a 16-byte native-endian
// header (length, type, flags, seq, pid), the payload, padding to 4 bytes.
func splitNetlink(b []byte) ([]nlMsg, error) {
	var out []nlMsg
	for len(b) >= unix.NLMSG_HDRLEN {
		n := int(binary.NativeEndian.Uint32(b[0:4]))
		if n < unix.NLMSG_HDRLEN || n > len(b) {
			return nil, fmt.Errorf("conntrack: message length %d of %d", n, len(b))
		}
		out = append(out, nlMsg{typ: binary.NativeEndian.Uint16(b[4:6]), data: b[unix.NLMSG_HDRLEN:n]})
		n = (n + 3) &^ 3
		if n > len(b) {
			n = len(b)
		}
		b = b[n:]
	}
	return out, nil
}
