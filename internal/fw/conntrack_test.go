package fw

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// nla builds one netlink attribute: native-endian header, payload, padding.
func nla(typ uint16, payload ...[]byte) []byte {
	var p []byte
	for _, x := range payload {
		p = append(p, x...)
	}
	b := make([]byte, 4, 4+len(p)+3)
	binary.NativeEndian.PutUint16(b[0:2], uint16(4+len(p)))
	binary.NativeEndian.PutUint16(b[2:4], typ)
	b = append(b, p...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func be16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func be64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }
func ip4(s string) []byte  { a := netip.MustParseAddr(s).As4(); return a[:] }

const nested = 0x8000

func tupleAttr(typ uint16, src, dst string, proto byte, sp, dp uint16) []byte {
	return nla(typ|nested,
		nla(ctaTupleIP|nested, nla(ctaIPv4Src, ip4(src)), nla(ctaIPv4Dst, ip4(dst))),
		nla(ctaTupleProto|nested, nla(ctaProtoNum, []byte{proto}), nla(ctaProtoSrc, be16(sp)), nla(ctaProtoDst, be16(dp))))
}

func TestParseConn(t *testing.T) {
	// A Palm's HTTP connection, masqueraded out of eth0, with accounting on.
	msg := append([]byte{},
		tupleAttr(ctaTupleOrig, "10.42.0.10", "93.184.215.14", 6, 1028, 80)...)
	msg = append(msg, tupleAttr(ctaTupleReply, "93.184.215.14", "10.4.1.118", 6, 80, 61000)...)
	msg = append(msg, nla(ctaStatus, be32(ipsSeenReply|ipsAssured|ipsSrcNAT|1<<3))...)
	msg = append(msg, nla(ctaTimeout, be32(431999))...)
	msg = append(msg, nla(ctaProtoinfo|nested, nla(ctaProtoinfoTCP|nested, nla(ctaProtoinfoTCPState, []byte{3})))...)
	msg = append(msg, nla(ctaCountersOrig|nested, nla(ctaCountersPackets, be64(7)), nla(ctaCountersBytes, be64(700)))...)
	msg = append(msg, nla(ctaCountersReply|nested, nla(ctaCountersPackets, be64(5)), nla(ctaCountersBytes, be64(5000)))...)
	c, err := ParseConn(msg)
	if err != nil {
		t.Fatal(err)
	}
	want := Conn{Proto: "tcp", Src: netip.MustParseAddr("10.42.0.10"), SrcPort: 1028, Dst: netip.MustParseAddr("93.184.215.14"), DstPort: 80,
		NATAddr: netip.MustParseAddr("10.4.1.118"), NATPort: 61000, State: "established", Assured: true, Replied: true, Timeout: 431999,
		Packets: 12, Bytes: 5700, Accounts: true}
	if c != want {
		t.Errorf("got  %+v\nwant %+v", c, want)
	}

	// DNS to the gateway: not NATed, no TCP state, no counters.
	msg = append(tupleAttr(ctaTupleOrig, "10.42.0.10", "10.42.0.1", 17, 1029, 53),
		tupleAttr(ctaTupleReply, "10.42.0.1", "10.42.0.10", 17, 53, 1029)...)
	msg = append(msg, nla(ctaStatus, be32(ipsSeenReply))...)
	msg = append(msg, nla(ctaTimeout, be32(25))...)
	c, err = ParseConn(msg)
	if err != nil || c.Proto != "udp" || c.NATAddr.IsValid() || !c.Replied || c.Assured || c.Accounts || c.Timeout != 25 {
		t.Errorf("dns: %+v %v", c, err)
	}

	if _, err := ParseConn(nla(ctaStatus, be32(0))); err == nil {
		t.Error("no tuple: no error")
	}
	if _, err := ParseConn([]byte{200, 0, 1, 0}); err == nil {
		t.Error("bad length: no error")
	}
}

func TestFromSubnet(t *testing.T) {
	a := netip.MustParseAddr
	cs := []Conn{
		{Src: a("10.4.0.54"), Dst: a("10.4.1.118"), DstPort: 22},
		{Src: a("10.42.0.11"), Dst: a("1.1.1.1"), DstPort: 443},
		{Src: a("10.42.0.10"), Dst: a("1.1.1.1"), DstPort: 443},
		{Src: a("10.42.0.10"), Dst: a("1.1.1.1"), DstPort: 80},
	}
	got := FromSubnet(cs, netip.MustParsePrefix("10.42.0.0/24"))
	if len(got) != 3 || got[0].DstPort != 80 || got[1].DstPort != 443 || got[2].Src != a("10.42.0.11") {
		t.Errorf("%+v", got)
	}
}
