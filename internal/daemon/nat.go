package daemon

import (
	"context"
	"fmt"
	"net/netip"

	"palm-gw/internal/ctl"
	"palm-gw/internal/fw"
)

// conntrack reads the kernel's table; tests replace it.
func (d *Daemon) conntrack() ([]fw.Conn, error) {
	if d.opts.Conntrack != nil {
		return d.opts.Conntrack()
	}
	return fw.Conntrack()
}

// handleNAT answers nat: the connections Palms have open through the
// gateway, as the kernel tracks them, each named by its device (D-055).
func (d *Daemon) handleNAT(_ context.Context, _ ctl.Request, w *ctl.Writer) error {
	cfg := d.config()
	subnet, err := netip.ParsePrefix(cfg.Network.Subnet)
	if err != nil {
		return fmt.Errorf("nat: subnet: %w", err)
	}
	all, err := d.conntrack()
	if err != nil {
		return err
	}
	names := map[netip.Addr]string{}
	if d.ppp != nil {
		for _, s := range d.ppp.Sessions() {
			if a, err := netip.ParseAddr(s.Peer); err == nil {
				names[a] = firstOf(s.Name, s.Addr)
			}
		}
	}
	for _, c := range d.panStatus().Clients {
		if a, err := netip.ParseAddr(c.IP); err == nil {
			names[a] = firstOf(c.Name, c.Hostname, c.Addr)
		}
	}
	r := ctl.NATReply{Subnet: subnet.String(), Entries: []ctl.NATEntry{}, Total: len(all)}
	for _, c := range fw.FromSubnet(all, subnet) {
		r.Accounting = r.Accounting || c.Accounts
		r.Entries = append(r.Entries, ctl.NATEntry{Conn: c, Device: names[c.Src]})
	}
	return w.Send(r)
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
