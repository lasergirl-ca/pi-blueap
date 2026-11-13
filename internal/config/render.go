package config

import (
	"bytes"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"text/template"
)

// Funcs exposes the configuration to deploy/ templates as zero-argument
// functions, so a template reads exactly as PLAN.md writes it:
//
//	oifname "{{UPLINK}}"  ip saddr {{SUBNET}}  {{if MSS_CLAMP}}...{{end}}
//
// The configuration must already be valid.
func (c *Config) Funcs() template.FuncMap {
	subnet := netip.MustParsePrefix(c.Network.Subnet)
	return template.FuncMap{
		"NAME":         func() string { return c.Bluetooth.Name },
		"GATEWAY":      func() string { return c.Network.Gateway },
		"SUBNET":       subnet.String,
		"PREFIX":       subnet.Bits,
		"NETMASK":      func() string { return netmask(subnet.Bits()) },
		"UPLINK":       func() string { return c.Network.Uplink },
		"ADAPTER":      func() string { return c.Bluetooth.Adapter },
		"BRIDGE":       func() string { return c.PAN.Bridge },
		"PPP_START":    func() string { return c.Network.PPPPool[0] },
		"PPP_END":      func() string { return c.Network.PPPPool[1] },
		"DHCP_START":   func() string { return c.Network.DHCPPool[0] },
		"DHCP_END":     func() string { return c.Network.DHCPPool[1] },
		"MSS_CLAMP":    func() bool { return c.Network.MSSClamp },
		"DNS_UPSTREAM": c.DNSServers,
		"PPP_OPTIONS":  func() string { return c.PPP.OptionsFile },
		"CTL_SOCKET":   func() string { return c.Ctl.Socket },
		"CTL_GROUP":    func() string { return c.Ctl.Group },
		// The web UI's port, and whether the Bluetooth side serves it (D-026, D-039).
		"WEB_PORT":    c.WebPort,
		"WEB_BT_SIDE": func() bool { return c.Web.Enabled && c.Web.BluetoothSide && c.Web.TLS != "off" },
		// The WireGuard tunnel (D-069): whether it is on, its interface, and whether Palm
		// traffic may still leave through the uplink (not in full, fail-closed mode).
		"WG":          func() bool { return c.WireGuard.Enabled },
		"WG_IF":       func() string { return c.WireGuard.Interface },
		"UPLINK_EXIT": c.UplinkExit,
	}
}

// DNSServers is dnsmasq's upstream: the tunnel's resolvers while WireGuard is on
// and names some, otherwise [network].dns_upstream (empty: the Pi's resolv.conf).
func (c *Config) DNSServers() []string {
	if c.WireGuard.Enabled && len(c.WireGuard.DNS) > 0 {
		return c.WireGuard.DNS
	}
	return c.Network.DNSUpstream
}

// UplinkExit says whether Palm traffic may leave through network.uplink: always,
// except with a full tunnel that fails closed (D-069). Allowed LAN hosts stay
// reachable either way; their rules come first.
func (c *Config) UplinkExit() bool {
	g := c.WireGuard
	return !g.Enabled || g.Mode != "full" || g.OnDown != "fail_closed"
}

// Render fills a deploy/ template with values from the configuration. A file
// without template actions passes through unchanged.
func (c *Config) Render(name, text string) (string, error) {
	t, err := template.New(name).Funcs(c.Funcs()).Parse(text)
	if err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, nil); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return buf.String(), nil
}

// netmask returns the dotted-quad mask for an IPv4 prefix length.
func netmask(bits int) string {
	var m uint32
	if bits > 0 {
		m = ^uint32(0) << uint(32-bits)
	}
	parts := []string{
		fmt.Sprint(m >> 24),
		fmt.Sprint(m >> 16 & 0xFF),
		fmt.Sprint(m >> 8 & 0xFF),
		fmt.Sprint(m & 0xFF),
	}
	return strings.Join(parts, ".")
}

// WebPort is the port of the first [web].listen entry, 8042 if there is none.
func (c *Config) WebPort() string {
	for _, l := range c.Web.Listen {
		if _, p, err := net.SplitHostPort(l); err == nil && p != "" {
			return p
		}
	}
	return "8042"
}
