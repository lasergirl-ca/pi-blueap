package config

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Error is one validation failure, always attributed to a configuration key.
type Error struct {
	Key string
	Msg string
}

func (e Error) Error() string { return e.Key + ": " + e.Msg }

// Errors is every validation failure found in one pass.
type Errors []Error

func (es Errors) Error() string {
	if len(es) == 1 {
		return "config: " + es[0].Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "config: %d problems:", len(es))
	for _, e := range es {
		b.WriteString("\n  - ")
		b.WriteString(e.Error())
	}
	return b.String()
}

var (
	adapterRe = regexp.MustCompile(`^hci[0-9]+$`)
	portRe    = regexp.MustCompile(`^(tcp|udp)/([0-9]{1,5})$`)
	pinRe     = regexp.MustCompile(`^[0-9]{1,16}$`)
	btAddrRe  = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)
	ifnameRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
	userRe    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
)

// Validate applies the rules in PLAN.md section 7 ("Validation rules") plus
// basic type and range checks for every key. It is pure: it does not look at
// the running system. See CheckRuntime for the checks that do.
func (c *Config) Validate() error {
	var errs Errors
	add := func(key, format string, args ...any) {
		errs = append(errs, Error{Key: key, Msg: fmt.Sprintf(format, args...)})
	}

	// [bluetooth]
	b := &c.Bluetooth
	if b.Adapter != "auto" && !adapterRe.MatchString(b.Adapter) && !btAddrRe.MatchString(b.Adapter) {
		add("bluetooth.adapter", `must be "auto", hciN, or the adapter's address XX:XX:XX:XX:XX:XX, got %q`, b.Adapter)
	}
	if strings.TrimSpace(b.Name) == "" {
		add("bluetooth.name", "must not be empty")
	}
	if b.Class > 0xFFFFFF {
		add("bluetooth.class", "must fit in 24 bits, got 0x%X", b.Class)
	}
	if b.ClassFallback > 0xFFFFFF {
		add("bluetooth.class_fallback", "must fit in 24 bits, got 0x%X", b.ClassFallback)
	}
	if b.PIN != "" && !pinRe.MatchString(b.PIN) {
		add("bluetooth.pin", "must be 1 to 16 digits")
	}
	if b.PINLength < 1 || b.PINLength > 16 {
		add("bluetooth.pin_length", "must be 1 to 16, got %d", b.PINLength)
	}
	if b.PairWindowDefaultMinutes < 1 {
		add("bluetooth.pair_window_default_minutes", "must be at least 1, got %d", b.PairWindowDefaultMinutes)
	}
	for i, a := range b.Allow {
		if !btAddrRe.MatchString(a) {
			add(fmt.Sprintf("bluetooth.allow[%d]", i), "not a Bluetooth address (XX:XX:XX:XX:XX:XX): %q", a)
		}
	}

	// [spp] [lap] [dun]: channels 1..30, unique across enabled profiles.
	seen := map[int]string{}
	for _, pr := range []struct {
		key string
		p   Profile
	}{{"spp", c.SPP}, {"lap", c.LAP}, {"dun", c.DUN}} {
		if !pr.p.Enabled {
			continue
		}
		switch {
		case pr.p.Channel < 1 || pr.p.Channel > 30:
			add(pr.key+".channel", "must be 1 to 30, got %d", pr.p.Channel)
		case seen[pr.p.Channel] != "":
			add(pr.key+".channel", "channel %d is already used by [%s]", pr.p.Channel, seen[pr.p.Channel])
		default:
			seen[pr.p.Channel] = pr.key
		}
	}

	// [pan]
	if c.PAN.Enabled && !ifnameRe.MatchString(c.PAN.Bridge) {
		add("pan.bridge", "must be an interface name (1 to 15 characters), got %q", c.PAN.Bridge)
	}

	// [handshake]
	if c.Handshake.MSTimeoutMS < 0 {
		add("handshake.ms_timeout_ms", "must not be negative")
	}
	if c.Handshake.ATTimeoutMS < 0 {
		add("handshake.at_timeout_ms", "must not be negative")
	}
	if c.Handshake.ATEmulator && strings.TrimSpace(c.Handshake.ATConnectBanner) == "" {
		add("handshake.at_connect_banner", "must not be empty when at_emulator is true")
	}

	// [network]
	n := &c.Network
	subnet, subnetOK := netip.ParsePrefix(n.Subnet)
	switch {
	case subnetOK != nil:
		add("network.subnet", "not a CIDR: %v", subnetOK)
	case !subnet.Addr().Is4():
		add("network.subnet", "must be IPv4 (Palm NetLib is IPv4 only)")
	case subnet != subnet.Masked():
		add("network.subnet", "must be a network address, e.g. %s", subnet.Masked())
	case subnet.Bits() > 29:
		add("network.subnet", "must be /29 or larger to hold a gateway and both pools")
	}
	haveSubnet := subnetOK == nil && subnet.Addr().Is4() && subnet == subnet.Masked()

	gw, gwErr := netip.ParseAddr(n.Gateway)
	if gwErr != nil || !gw.Is4() {
		add("network.gateway", "not an IPv4 address: %q", n.Gateway)
	} else if haveSubnet && !hostInSubnet(gw, subnet) {
		add("network.gateway", "%s is not a usable host address inside %s", gw, subnet)
	}

	pppLo, pppHi, pppOK := parsePool("network.ppp_pool", n.PPPPool, add)
	dhcpLo, dhcpHi, dhcpOK := parsePool("network.dhcp_pool", n.DHCPPool, add)
	if haveSubnet && pppOK {
		pppOK = checkPoolInSubnet("network.ppp_pool", pppLo, pppHi, subnet, add)
	}
	if haveSubnet && dhcpOK {
		dhcpOK = checkPoolInSubnet("network.dhcp_pool", dhcpLo, dhcpHi, subnet, add)
	}
	if gwErr == nil && pppOK && inRange(gw, pppLo, pppHi) {
		add("network.ppp_pool", "contains the gateway address %s", gw)
	}
	if gwErr == nil && dhcpOK && inRange(gw, dhcpLo, dhcpHi) {
		add("network.dhcp_pool", "contains the gateway address %s", gw)
	}
	if pppOK && dhcpOK && rangesOverlap(pppLo, pppHi, dhcpLo, dhcpHi) {
		add("network.dhcp_pool", "overlaps network.ppp_pool")
	}
	if pppOK && c.PPP.MaxSessions > 0 {
		if have := rangeSize(pppLo, pppHi); have < uint64(c.PPP.MaxSessions) {
			add("network.ppp_pool", "has %d addresses but ppp.max_sessions is %d", have, c.PPP.MaxSessions)
		}
	}
	if !ifnameRe.MatchString(n.Uplink) {
		add("network.uplink", "must be an interface name (1 to 15 characters), got %q", n.Uplink)
	}
	for i, s := range n.DNSUpstream {
		if a, err := netip.ParseAddr(s); err != nil || !a.Is4() {
			add(fmt.Sprintf("network.dns_upstream[%d]", i), "not an IPv4 address: %q", s)
		}
	}

	// [ppp]
	if strings.TrimSpace(c.PPP.OptionsFile) == "" {
		add("ppp.options_file", "must not be empty")
	}
	if c.PPP.IdleSeconds < 0 {
		add("ppp.idle_seconds", "must not be negative")
	}
	if c.PPP.MaxSessions < 1 {
		add("ppp.max_sessions", "must be at least 1, got %d", c.PPP.MaxSessions)
	}
	if c.PPP.PerDeviceMax < 1 {
		add("ppp.per_device_max", "must be at least 1, got %d", c.PPP.PerDeviceMax)
	}

	// [firewall]
	for i, p := range c.Firewall.PalmToPi {
		if msg := checkPort(p); msg != "" {
			add(fmt.Sprintf("firewall.palm_to_pi[%d]", i), "%s", msg)
		}
	}
	for i, r := range c.Firewall.Allow {
		key := fmt.Sprintf("firewall.allow[%d]", i)
		if !isIPOrCIDR(r.Host) {
			add(key+".host", "must be an IPv4 address or CIDR, got %q", r.Host)
		}
		if len(r.Ports) == 0 {
			add(key+".ports", "must list at least one entry such as \"tcp/14238\" or \"icmp\"")
		}
		for j, p := range r.Ports {
			if msg := checkPort(p); msg != "" {
				add(fmt.Sprintf("%s.ports[%d]", key, j), "%s", msg)
			}
		}
	}

	// [logging]
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		add("logging.level", "must be debug, info, warn or error, got %q", c.Logging.Level)
	}
	switch c.Logging.Format {
	case "json", "text":
	default:
		add("logging.format", "must be json or text, got %q", c.Logging.Format)
	}

	// [ctl]
	if !filepath.IsAbs(c.Ctl.Socket) {
		add("ctl.socket", "must be an absolute path, got %q", c.Ctl.Socket)
	}
	if strings.TrimSpace(c.Ctl.Group) == "" {
		add("ctl.group", "must not be empty")
	}

	// [web]
	w := &c.Web
	if w.Enabled {
		if len(w.Listen) == 0 {
			add("web.listen", "must have at least one host:port when web.enabled is true")
		}
		for i, l := range w.Listen {
			if _, _, err := net.SplitHostPort(l); err != nil {
				add(fmt.Sprintf("web.listen[%d]", i), "not a host:port: %q", l)
			}
		}
		switch w.TLS {
		case "off", "self-signed":
		case "file":
			if w.TLSCertFile == "" {
				add("web.tls_cert_file", `required when web.tls = "file"`)
			}
			if w.TLSKeyFile == "" {
				add("web.tls_key_file", `required when web.tls = "file"`)
			}
		default:
			add("web.tls", "must be off, self-signed or file, got %q", w.TLS)
		}
		if !userRe.MatchString(w.Username) {
			add("web.username", "must be 1 to 32 letters, digits, '.', '_' or '-', got %q", w.Username)
		}
		if w.SessionHours < 1 {
			add("web.session_hours", "must be at least 1, got %d", w.SessionHours)
		}
		for i, cidr := range w.AllowCIDRs {
			if !isIPOrCIDR(cidr) {
				add(fmt.Sprintf("web.allow_cidrs[%d]", i), "must be an IPv4 address or CIDR, got %q", cidr)
			}
		}
		if w.BluetoothSide && w.TLS == "off" {
			add("web.tls", `must not be "off" while web.bluetooth_side is true: the Bluetooth side is TLS only (D-039)`)
		}
		if w.ViewerUsername != "" {
			if !userRe.MatchString(w.ViewerUsername) {
				add("web.viewer_username", "must be 1 to 32 letters, digits, '.', '_' or '-', got %q", w.ViewerUsername)
			} else if w.ViewerUsername == w.Username {
				add("web.viewer_username", "must differ from web.username")
			}
		} else if w.ViewerPasswordHash != "" {
			add("web.viewer_password_hash", "set without web.viewer_username")
		}
	}

	c.validateWireGuard(subnet, haveSubnet, add)

	if len(errs) == 0 {
		return nil
	}
	return errs
}

// validateWireGuard checks [wireguard] (D-069). The choices are checked always;
// the tunnel's own keys only when it is enabled.
func (c *Config) validateWireGuard(subnet netip.Prefix, subnetOK bool, add func(string, string, ...any)) {
	g := &c.WireGuard
	if !ifnameRe.MatchString(g.Interface) {
		add("wireguard.interface", "must be an interface name (1 to 15 characters), got %q", g.Interface)
	} else if g.Interface == c.Network.Uplink || g.Interface == c.PAN.Bridge || strings.HasPrefix(g.Interface, "ppp") {
		add("wireguard.interface", "must differ from network.uplink, pan.bridge and ppp*, got %q", g.Interface)
	}
	full := g.Mode == "full"
	if g.Mode != "full" && g.Mode != "split" {
		add("wireguard.mode", "must be full or split, got %q", g.Mode)
	}
	if g.OnDown != "fail_closed" && g.OnDown != "fallback" {
		add("wireguard.on_down", "must be fail_closed or fallback, got %q", g.OnDown)
	}
	if g.PublicKey != "" {
		if k, err := base64.StdEncoding.DecodeString(g.PublicKey); err != nil || len(k) != 32 {
			add("wireguard.public_key", "must be a WireGuard public key (44 characters of base64), or empty")
		}
	}
	if g.KeepaliveSeconds < 0 || g.KeepaliveSeconds > 65535 {
		add("wireguard.keepalive_seconds", "must be 0 to 65535, got %d", g.KeepaliveSeconds)
	}
	if !g.Enabled {
		return
	}
	if a, err := netip.ParsePrefix(g.Address); err != nil || !a.Addr().Is4() {
		add("wireguard.address", "must be the Pi's IPv4 tunnel address with its prefix, e.g. 10.17.200.5/32, got %q", g.Address)
	} else if subnetOK && a.Masked().Overlaps(subnet) {
		add("wireguard.address", "%s overlaps network.subnet %s", a, subnet)
	}
	if host, port, err := net.SplitHostPort(g.Endpoint); err != nil || host == "" {
		add("wireguard.endpoint", "must be host:port, got %q", g.Endpoint)
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		add("wireguard.endpoint", "port must be 1 to 65535, got %q", port)
	}
	if k, err := base64.StdEncoding.DecodeString(g.PeerPublicKey); err != nil || len(k) != 32 {
		add("wireguard.peer_public_key", "must be a WireGuard public key (44 characters of base64)")
	}
	var allowed []netip.Prefix
	everything := false
	for i, s := range g.AllowedIPs {
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.Addr().Is4() || p != p.Masked() {
			add(fmt.Sprintf("wireguard.allowed_ips[%d]", i), "must be an IPv4 network, e.g. 10.17.0.0/16, got %q", s)
			continue
		}
		if p.Bits() == 0 {
			everything = true
		} else if subnetOK && p.Overlaps(subnet) {
			add(fmt.Sprintf("wireguard.allowed_ips[%d]", i), "%s overlaps network.subnet %s", p, subnet)
		}
		allowed = append(allowed, p)
	}
	switch {
	case len(g.AllowedIPs) == 0:
		add("wireguard.allowed_ips", "must name at least one network")
	case full && !everything:
		add("wireguard.allowed_ips", `must include 0.0.0.0/0 when mode = "full": the tunnel carries all Palm traffic`)
	case !full && everything:
		add("wireguard.allowed_ips", `must not include 0.0.0.0/0 when mode = "split"; use mode = "full"`)
	}
	for i, s := range g.DNS {
		a, err := netip.ParseAddr(s)
		if err != nil || !a.Is4() {
			add(fmt.Sprintf("wireguard.dns[%d]", i), "must be an IPv4 address, got %q", s)
			continue
		}
		inside := false
		for _, p := range allowed {
			if p.Contains(a) {
				inside = true
			}
		}
		if !inside {
			add(fmt.Sprintf("wireguard.dns[%d]", i), "%s is not in wireguard.allowed_ips, so it would not be reached through the tunnel", a)
		}
	}
}

// Warnings returns the startup warnings PLAN.md asks for that are not errors.
// The daemon logs each at WARN level every time it starts.
func (c *Config) Warnings() []string {
	var w []string
	if c.Bluetooth.AllowAny {
		w = append(w, "bluetooth.allow_any = true: any paired device may connect, not only allowlisted ones")
	}
	if c.Bluetooth.PIN != "" {
		w = append(w, "bluetooth.pin is fixed; a random PIN per pairing window is the recommended setting")
	}
	return w
}

// InterfaceState is what CheckRuntime needs to know about an interface.
type InterfaceState struct {
	Exists bool
	Up     bool
}

// InterfaceLookup reports the state of a network interface by name.
type InterfaceLookup func(name string) InterfaceState

// SystemInterfaces is the InterfaceLookup for the running host.
func SystemInterfaces(name string) InterfaceState {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return InterfaceState{}
	}
	return InterfaceState{Exists: true, Up: ifc.Flags&net.FlagUp != 0}
}

// CheckRuntime applies the validation rules that depend on the running system:
// the uplink must exist (an error), but a wlan0 uplink that is currently down
// only warns. Tests inject their own lookup.
func (c *Config) CheckRuntime(lookup InterfaceLookup) (warnings []string, err error) {
	st := lookup(c.Network.Uplink)
	if !st.Exists {
		return nil, Errors{{Key: "network.uplink", Msg: fmt.Sprintf("interface %q does not exist", c.Network.Uplink)}}
	}
	if !st.Up {
		if strings.HasPrefix(c.Network.Uplink, "wlan") {
			warnings = append(warnings, fmt.Sprintf("network.uplink %q is currently down; Palms will have no internet until it comes up", c.Network.Uplink))
		} else {
			warnings = append(warnings, fmt.Sprintf("network.uplink %q is currently down", c.Network.Uplink))
		}
	}
	return warnings, nil
}

// --- helpers ---

func parsePool(key string, pool []string, add func(string, string, ...any)) (lo, hi netip.Addr, ok bool) {
	if len(pool) != 2 {
		add(key, "must be exactly two addresses [first, last], got %d entries", len(pool))
		return lo, hi, false
	}
	var perr error
	lo, perr = netip.ParseAddr(pool[0])
	if perr != nil || !lo.Is4() {
		add(key+"[0]", "not an IPv4 address: %q", pool[0])
		return lo, hi, false
	}
	hi, perr = netip.ParseAddr(pool[1])
	if perr != nil || !hi.Is4() {
		add(key+"[1]", "not an IPv4 address: %q", pool[1])
		return lo, hi, false
	}
	if lo.Compare(hi) > 0 {
		add(key, "first address %s is after last address %s", lo, hi)
		return lo, hi, false
	}
	return lo, hi, true
}

func checkPoolInSubnet(key string, lo, hi netip.Addr, subnet netip.Prefix, add func(string, string, ...any)) bool {
	ok := true
	if !hostInSubnet(lo, subnet) {
		add(key+"[0]", "%s is not a usable host address inside %s", lo, subnet)
		ok = false
	}
	if !hostInSubnet(hi, subnet) {
		add(key+"[1]", "%s is not a usable host address inside %s", hi, subnet)
		ok = false
	}
	return ok
}

// hostInSubnet is true when a is inside p and is neither the network nor the
// broadcast address.
func hostInSubnet(a netip.Addr, p netip.Prefix) bool {
	if !p.Contains(a) {
		return false
	}
	if p.Bits() >= 31 {
		return true
	}
	if a == p.Addr() {
		return false
	}
	return a != broadcast(p)
}

func broadcast(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	v := binary.BigEndian.Uint32(a[:])
	v |= ^uint32(0) >> uint(p.Bits())
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], v)
	return netip.AddrFrom4(out)
}

func u32(a netip.Addr) uint32 {
	b := a.As4()
	return binary.BigEndian.Uint32(b[:])
}

func inRange(a, lo, hi netip.Addr) bool {
	return a.Compare(lo) >= 0 && a.Compare(hi) <= 0
}

func rangesOverlap(aLo, aHi, bLo, bHi netip.Addr) bool {
	return aLo.Compare(bHi) <= 0 && bLo.Compare(aHi) <= 0
}

func rangeSize(lo, hi netip.Addr) uint64 {
	return uint64(u32(hi)) - uint64(u32(lo)) + 1
}

// checkPort validates "tcp/NNNN", "udp/NNNN" or "icmp". It returns an empty
// string when valid, else the message.
func checkPort(p string) string {
	if p == "icmp" {
		return ""
	}
	m := portRe.FindStringSubmatch(p)
	if m == nil {
		return fmt.Sprintf("must match ^(tcp|udp)/[0-9]{1,5}$ or be \"icmp\", got %q", p)
	}
	n, _ := strconv.Atoi(m[2])
	if n < 1 || n > 65535 {
		return fmt.Sprintf("port must be 1 to 65535, got %d", n)
	}
	return ""
}

func isIPOrCIDR(s string) bool {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Is4()
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Addr().Is4()
	}
	return false
}
