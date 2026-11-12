package fw

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sort"
)

// The NAT table: the kernel's connection tracking entries for Palm traffic,
// read over ctnetlink (NETLINK_NETFILTER), because Raspberry Pi OS has
// neither /proc/net/nf_conntrack nor the conntrack tool (D-055). Only what
// the page shows is decoded: the two tuples, status, TCP state, timeout and,
// when nf_conntrack_acct is on, the counters.

// Conn is one tracked connection.
type Conn struct {
	Proto   string     `json:"proto"` // tcp, udp, icmp or the number
	Src     netip.Addr `json:"src"`   // the Palm
	SrcPort uint16     `json:"src_port,omitempty"`
	Dst     netip.Addr `json:"dst"`
	DstPort uint16     `json:"dst_port,omitempty"`
	// NATAddr:NATPort is what the far end sees (the reply's destination),
	// set only when the connection is source-NATed out of the uplink.
	NATAddr  netip.Addr `json:"nat_addr,omitzero"`
	NATPort  uint16     `json:"nat_port,omitempty"`
	State    string     `json:"state,omitempty"` // TCP state
	Assured  bool       `json:"assured"`         // replies seen both ways, kept under pressure
	Replied  bool       `json:"replied"`         // the far end has answered
	Timeout  int64      `json:"timeout"`         // seconds left
	Packets  uint64     `json:"packets,omitempty"`
	Bytes    uint64     `json:"bytes,omitempty"` // both directions, with nf_conntrack_acct=1
	Accounts bool       `json:"-"`
}

// ctnetlink attribute numbers (linux/netfilter/nfnetlink_conntrack.h).
const (
	ctaTupleOrig     = 1
	ctaTupleReply    = 2
	ctaStatus        = 3
	ctaProtoinfo     = 4
	ctaTimeout       = 7
	ctaCountersOrig  = 9
	ctaCountersReply = 10

	ctaTupleIP    = 1
	ctaTupleProto = 2
	ctaIPv4Src    = 1
	ctaIPv4Dst    = 2
	ctaProtoNum   = 1
	ctaProtoSrc   = 2
	ctaProtoDst   = 3

	ctaProtoinfoTCP      = 1
	ctaProtoinfoTCPState = 1

	ctaCountersPackets = 1
	ctaCountersBytes   = 2

	ipsSeenReply = 1 << 1
	ipsAssured   = 1 << 2
	ipsSrcNAT    = 1 << 4

	nlaTypeMask = 0x3fff
)

var tcpStates = []string{"none", "syn-sent", "syn-recv", "established", "fin-wait", "close-wait", "last-ack", "time-wait", "close", "syn-sent2"}

// attrs splits netlink attributes: a native-endian 2-byte length and type,
// the payload, padding to 4 bytes.
func attrs(b []byte) (map[uint16][]byte, error) {
	out := map[uint16][]byte{}
	for len(b) >= 4 {
		n := int(binary.NativeEndian.Uint16(b[0:2]))
		typ := binary.NativeEndian.Uint16(b[2:4]) & nlaTypeMask
		if n < 4 || n > len(b) {
			return nil, fmt.Errorf("conntrack: attribute length %d of %d", n, len(b))
		}
		out[typ] = b[4:n]
		n = (n + 3) &^ 3
		if n > len(b) {
			n = len(b)
		}
		b = b[n:]
	}
	return out, nil
}

type tuple struct {
	src, dst         netip.Addr
	proto            uint8
	srcPort, dstPort uint16
}

func parseTuple(b []byte) (tuple, error) {
	var t tuple
	a, err := attrs(b)
	if err != nil {
		return t, err
	}
	ip, err := attrs(a[ctaTupleIP])
	if err != nil {
		return t, err
	}
	if v := ip[ctaIPv4Src]; len(v) == 4 {
		t.src = netip.AddrFrom4([4]byte(v))
	}
	if v := ip[ctaIPv4Dst]; len(v) == 4 {
		t.dst = netip.AddrFrom4([4]byte(v))
	}
	p, err := attrs(a[ctaTupleProto])
	if err != nil {
		return t, err
	}
	if v := p[ctaProtoNum]; len(v) == 1 {
		t.proto = v[0]
	}
	if v := p[ctaProtoSrc]; len(v) == 2 {
		t.srcPort = binary.BigEndian.Uint16(v)
	}
	if v := p[ctaProtoDst]; len(v) == 2 {
		t.dstPort = binary.BigEndian.Uint16(v)
	}
	return t, nil
}

func protoName(n uint8) string {
	switch n {
	case 1:
		return "icmp"
	case 6:
		return "tcp"
	case 17:
		return "udp"
	}
	return fmt.Sprint(n)
}

// ParseConn decodes one IPCTNL_MSG_CT_NEW message's attributes (after the
// 4-byte nfgenmsg).
func ParseConn(b []byte) (Conn, error) {
	var c Conn
	a, err := attrs(b)
	if err != nil {
		return c, err
	}
	orig, ok := a[ctaTupleOrig]
	if !ok {
		return c, errors.New("conntrack: no original tuple")
	}
	o, err := parseTuple(orig)
	if err != nil {
		return c, err
	}
	c.Proto, c.Src, c.SrcPort, c.Dst, c.DstPort = protoName(o.proto), o.src, o.srcPort, o.dst, o.dstPort
	var status uint32
	if v := a[ctaStatus]; len(v) == 4 {
		status = binary.BigEndian.Uint32(v)
	}
	c.Assured, c.Replied = status&ipsAssured != 0, status&ipsSeenReply != 0
	if status&ipsSrcNAT != 0 {
		if r, err := parseTuple(a[ctaTupleReply]); err == nil {
			c.NATAddr, c.NATPort = r.dst, r.dstPort
		}
	}
	if v := a[ctaTimeout]; len(v) == 4 {
		c.Timeout = int64(binary.BigEndian.Uint32(v))
	}
	if pi, err := attrs(a[ctaProtoinfo]); err == nil {
		if tcp, err := attrs(pi[ctaProtoinfoTCP]); err == nil {
			if v := tcp[ctaProtoinfoTCPState]; len(v) == 1 && int(v[0]) < len(tcpStates) {
				c.State = tcpStates[v[0]]
			}
		}
	}
	for _, k := range []uint16{ctaCountersOrig, ctaCountersReply} {
		ct, err := attrs(a[k])
		if err != nil || len(ct) == 0 {
			continue
		}
		c.Accounts = true
		if v := ct[ctaCountersPackets]; len(v) == 8 {
			c.Packets += binary.BigEndian.Uint64(v)
		}
		if v := ct[ctaCountersBytes]; len(v) == 8 {
			c.Bytes += binary.BigEndian.Uint64(v)
		}
	}
	return c, nil
}

// FromSubnet keeps the connections a Palm started, sorted by Palm, then
// destination and port.
func FromSubnet(cs []Conn, subnet netip.Prefix) []Conn {
	var out []Conn
	for _, c := range cs {
		if subnet.Contains(c.Src) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Src != b.Src {
			return a.Src.Less(b.Src)
		}
		if a.Dst != b.Dst {
			return a.Dst.Less(b.Dst)
		}
		return a.DstPort < b.DstPort
	})
	return out
}
