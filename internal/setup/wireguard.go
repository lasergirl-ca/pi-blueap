package setup

import (
	"bufio"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"palm-gw/internal/config"
)

// The gateway as a WireGuard client for the Palm network (D-018, D-069,
// docs/WIREGUARD.md). NetworkManager owns the tunnel (profile WGProfile); setup
// creates it from config.toml's [wireguard] and a private key that lives only in
// NetworkManager's root-only store. A key arrives through ImportWG (a wg-quick
// .conf) or GenerateWG, which leave it in WGPendingKey until the next apply moves
// it into NetworkManager.

const (
	// WGProfile is the NetworkManager connection that holds the tunnel.
	WGProfile = "palm-gw-wg"
	// WGTable is the routing table Palm traffic uses when it goes through the tunnel.
	WGTable = 4242
	// WGPendingKey holds a new private key between ImportWG/GenerateWG and the apply
	// that hands it to NetworkManager: mode 0600, root only, deleted after.
	WGPendingKey = "wg-pending.key"
)

// WGConf is what a wg-quick style .conf says, IPv4 only (Palm NetLib is IPv4).
type WGConf struct {
	PrivateKey   string
	Address      string   // the first IPv4 Address, with its prefix
	DNS          []string // IPv4 resolvers; search domains are dropped
	PublicKey    string   // the peer's
	PresharedKey string
	Endpoint     string
	AllowedIPs   []string // IPv4 networks; ::/0 and other IPv6 are dropped
	Keepalive    int
}

// ParseWGConf reads a .conf with one [Interface] and exactly one [Peer]. Keys are
// case-insensitive, as wg-quick reads them; values may be comma-separated lists.
func ParseWGConf(text string) (WGConf, error) {
	var c WGConf
	section, peers := "", 0
	sc := bufio.NewScanner(strings.NewReader(text))
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			switch section {
			case "interface":
			case "peer":
				peers++
			default:
				return c, fmt.Errorf("line %d: unknown section [%s]", n, section)
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return c, fmt.Errorf("line %d: not key = value", n)
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		list := splitList(v)
		switch section + "." + k {
		case "interface.privatekey":
			c.PrivateKey = v
		case "interface.address":
			for _, s := range list {
				if p, err := netip.ParsePrefix(s); err == nil && p.Addr().Is4() && c.Address == "" {
					c.Address = p.String()
				} else if a, err := netip.ParseAddr(s); err == nil && a.Is4() && c.Address == "" {
					c.Address = a.String() + "/32"
				}
			}
		case "interface.dns":
			for _, s := range list {
				if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
					c.DNS = append(c.DNS, a.String())
				}
			}
		case "interface.listenport", "interface.mtu", "interface.fwmark", "interface.table",
			"interface.preup", "interface.postup", "interface.predown", "interface.postdown", "interface.saveconfig":
			// wg-quick's own settings; the gateway sets these itself or does not use them.
		case "peer.publickey":
			c.PublicKey = v
		case "peer.presharedkey":
			c.PresharedKey = v
		case "peer.endpoint":
			c.Endpoint = v
		case "peer.allowedips":
			for _, s := range list {
				if p, err := netip.ParsePrefix(s); err == nil && p.Addr().Is4() {
					c.AllowedIPs = append(c.AllowedIPs, p.Masked().String())
				}
			}
		case "peer.persistentkeepalive":
			if v == "off" {
				c.Keepalive = 0
			} else if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 65535 {
				c.Keepalive = n
			} else {
				return c, fmt.Errorf("line %d: PersistentKeepalive %q", n, v)
			}
		default:
			if section == "" {
				return c, fmt.Errorf("line %d: %s outside [Interface] or [Peer]", n, k)
			}
			return c, fmt.Errorf("line %d: unknown key %s in [%s]", n, k, section)
		}
	}
	if err := sc.Err(); err != nil {
		return c, err
	}
	switch {
	case peers != 1:
		return c, fmt.Errorf("the gateway takes a .conf with exactly one [Peer], this has %d", peers)
	case !wgKey(c.PrivateKey):
		return c, errors.New("[Interface] PrivateKey is missing or not a WireGuard key")
	case !wgKey(c.PublicKey):
		return c, errors.New("[Peer] PublicKey is missing or not a WireGuard key")
	case c.PresharedKey != "" && !wgKey(c.PresharedKey):
		return c, errors.New("[Peer] PresharedKey is not a WireGuard key")
	case c.Address == "":
		return c, errors.New("[Interface] Address has no IPv4 address")
	case c.Endpoint == "":
		return c, errors.New("[Peer] Endpoint is missing")
	case len(c.AllowedIPs) == 0:
		return c, errors.New("[Peer] AllowedIPs has no IPv4 network")
	}
	return c, nil
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func wgKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}

// WGPublicKey derives the public key from a private one.
func WGPublicKey(priv string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(priv)
	if err != nil || len(b) != 32 {
		return "", errors.New("not a WireGuard private key")
	}
	// X25519 clamps the scalar itself, so any 32 bytes are a usable key.
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// NewWGKey makes a key pair as `wg genkey | wg pubkey` would.
func NewWGKey() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	b := k.Bytes()
	// wg genkey's clamping, so the key reads the same in every tool.
	b[0] &= 248
	b[31] = (b[31] & 127) | 64
	priv = base64.StdEncoding.EncodeToString(b)
	pub, err = WGPublicKey(priv)
	return priv, pub, err
}

// WGKVs are the [wireguard] keys an imported .conf sets. Mode follows AllowedIPs:
// full when they include 0.0.0.0/0, split otherwise. The interface and on_down are
// the gateway's own and stay as they are.
func WGKVs(c WGConf) []KV {
	mode := "split"
	for _, p := range c.AllowedIPs {
		if p == "0.0.0.0/0" {
			mode = "full"
		}
	}
	dns := c.DNS
	if dns == nil {
		dns = []string{}
	}
	return []KV{
		{Section: "wireguard", Key: "enabled", Value: "true"},
		{Section: "wireguard", Key: "address", Value: TOMLString(c.Address)},
		{Section: "wireguard", Key: "endpoint", Value: TOMLString(c.Endpoint)},
		{Section: "wireguard", Key: "peer_public_key", Value: TOMLString(c.PublicKey)},
		{Section: "wireguard", Key: "allowed_ips", Value: TOMLStrings(c.AllowedIPs)},
		{Section: "wireguard", Key: "keepalive_seconds", Value: strconv.Itoa(c.Keepalive)},
		{Section: "wireguard", Key: "mode", Value: TOMLString(mode)},
		{Section: "wireguard", Key: "dns", Value: TOMLStrings(dns)},
	}
}

// WGImportResult says what ImportWG or GenerateWG did.
type WGImportResult struct {
	PublicKey string `json:"public_key"` // the gateway's, to give the peer's admin
	Mode      string `json:"mode,omitempty"`
}

// ImportWG reads a .conf, writes its keys into config.toml (validated before it is
// saved) and leaves its private key for the next apply. Nothing changes on the
// system until `palm-gw setup --apply`.
func ImportWG(root, cfgPath, stateDir, confText string) (WGImportResult, error) {
	c, err := ParseWGConf(confText)
	if err != nil {
		return WGImportResult{}, err
	}
	if c.PresharedKey != "" {
		return WGImportResult{}, errors.New("[Peer] PresharedKey is not supported yet; ask for a .conf without one")
	}
	pub, err := WGPublicKey(c.PrivateKey)
	if err != nil {
		return WGImportResult{}, err
	}
	kvs := WGKVs(c)
	if err := editConfigKVs(filepath.Join(root, cfgPath), kvs); err != nil {
		return WGImportResult{}, err
	}
	if err := stashWGKey(filepath.Join(root, stateDir), c.PrivateKey); err != nil {
		return WGImportResult{}, err
	}
	return WGImportResult{PublicKey: pub, Mode: strings.Trim(kvs[6].Value, `"`)}, nil
}

// GenerateWG makes a new key pair for the gateway and leaves the private key for
// the next apply. The rest of [wireguard] (address, endpoint, peer key) is set by
// the caller; the public key goes to the peer's admin.
func GenerateWG(root, stateDir string) (WGImportResult, error) {
	priv, pub, err := NewWGKey()
	if err != nil {
		return WGImportResult{}, err
	}
	if err := stashWGKey(filepath.Join(root, stateDir), priv); err != nil {
		return WGImportResult{}, err
	}
	return WGImportResult{PublicKey: pub}, nil
}

func stashWGKey(dir, priv string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	p := filepath.Join(dir, WGPendingKey)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(priv+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil { // WriteFile keeps an existing file's mode
		return err
	}
	return os.Rename(tmp, p)
}

func readPendingWGKey(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, WGPendingKey))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	k := strings.TrimSpace(string(b))
	if !wgKey(k) {
		return "", fmt.Errorf("%s does not hold a WireGuard key", WGPendingKey)
	}
	return k, nil
}

// editConfigKVs sets keys in config.toml, refusing a result that does not
// validate, and keeps the file's mode and owner.
func editConfigKVs(path string, kvs []KV) error {
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := SetKeys(string(old), kvs)
	cfg, err := config.Parse(text)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		return fmt.Errorf("not saved: %w", err)
	}
	if text == string(old) {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), fi.Mode().Perm()); err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(tmp, int(st.Uid), int(st.Gid)) // root:palm-gw on the Pi
	}
	return os.Rename(tmp, path)
}

// WGBridgeRouting is the policy routing the bridge's NetworkManager profile carries
// while WireGuard is on (empty otherwise). It sits on the bridge, which is always
// up, so it stays in place when the tunnel drops:
//   - 4241: Palm-network traffic first looks in the main table with its default
//     route ignored (wg-quick's suppress_prefixlength), so the home LAN, the Palms'
//     own addresses and replies from the gateway stay direct;
//   - 4242: everything else from the Palm network uses table WGTable, which holds
//     the tunnel's routes while it is up.
//
// Full mode with on_down = fail_closed also puts an unreachable default in
// WGTable, so with the tunnel down Palm traffic stops instead of leaking out the
// uplink; without it the lookup falls through to the main table's default.
func WGBridgeRouting(cfg *config.Config) (rules, routes string) {
	g := cfg.WireGuard
	if !g.Enabled {
		return "", ""
	}
	sub := cfg.Network.Subnet
	rules = fmt.Sprintf("priority 4241 from %s table 254 suppress_prefixlength 0, priority %d from %s table %d", sub, WGTable, sub, WGTable)
	if g.Mode == "full" && g.OnDown == "fail_closed" {
		routes = fmt.Sprintf("0.0.0.0/0 4096 table=%d type=unreachable", WGTable)
	}
	return rules, routes
}

// WGTunnelRoutes is the tunnel profile's own routes: what it carries for the Palm
// network, in WGTable, and its DNS servers in the main table, so dnsmasq (the Pi
// itself) reaches them through the tunnel too. They come and go with the tunnel.
func WGTunnelRoutes(cfg *config.Config) string {
	g := cfg.WireGuard
	var r []string
	if g.Mode == "full" {
		r = append(r, fmt.Sprintf("0.0.0.0/0 50 table=%d", WGTable))
	} else {
		for _, p := range g.AllowedIPs {
			r = append(r, fmt.Sprintf("%s 50 table=%d", p, WGTable))
		}
	}
	for _, d := range g.DNS {
		r = append(r, d+"/32 50 table=254")
	}
	return strings.Join(r, ", ")
}

// wgConfFile is the .conf NetworkManager's importer reads: the key, the address
// and the one peer. No DNS line: dnsmasq, not NetworkManager, uses the resolvers.
func wgConfFile(priv string, g config.WireGuard) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s\n\n[Peer]\nPublicKey = %s\nAllowedIPs = %s\nEndpoint = %s\n",
		priv, g.Address, g.PeerPublicKey, strings.Join(g.AllowedIPs, ", "), g.Endpoint)
	if g.KeepaliveSeconds > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", g.KeepaliveSeconds)
	}
	return b.String()
}

var nmUUID = regexp.MustCompile(`\(([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\)`)

// applyWireGuard makes NetworkManager's tunnel match [wireguard]: deleted when it
// is off; otherwise created again from the pending key, or from the key the
// profile already holds, so a changed endpoint or mode takes effect. The key goes
// to NetworkManager through its own .conf importer, from a root-only file that is
// deleted straight after, never on a command line.
func (a *applier) applyWireGuard(cfg *config.Config) error {
	g := cfg.WireGuard
	stateDir := a.path(a.o.StateDir)
	_, showErr := a.run("nmcli", "-t", "-f", "connection.id", "connection", "show", WGProfile)
	exists := showErr == nil
	if !g.Enabled {
		if exists {
			if _, err := a.run("nmcli", "connection", "delete", WGProfile); err != nil {
				return err
			}
			a.note("WireGuard off: %s deleted", WGProfile)
		} else {
			a.note("WireGuard off")
		}
		_ = os.Remove(filepath.Join(stateDir, WGPendingKey))
		return nil
	}
	key, err := readPendingWGKey(stateDir)
	if err != nil {
		return err
	}
	if key == "" && exists {
		out, err := a.o.Run("nmcli", "-s", "-g", "wireguard.private-key", "connection", "show", WGProfile)
		if err != nil {
			return fmt.Errorf("reading the tunnel's key from NetworkManager: %w", err) // never echo the output
		}
		key = strings.TrimSpace(string(out))
	}
	if !wgKey(key) {
		return errors.New("[wireguard] is enabled but the gateway has no WireGuard key: run palm-gw setup --wg-import FILE or --wg-generate")
	}
	dir := a.path("/run/palm-gw/wg-import")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	conf := filepath.Join(dir, g.Interface+".conf") // the importer names the interface after the file
	if err := os.WriteFile(conf, []byte(wgConfFile(key, g)), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(conf) }() // it holds the key
	if exists {
		if _, err := a.run("nmcli", "connection", "delete", WGProfile); err != nil {
			return err
		}
	}
	out, err := a.run("nmcli", "connection", "import", "type", "wireguard", "file", conf)
	if err != nil {
		return err
	}
	m := nmUUID.FindSubmatch(out)
	if m == nil {
		return fmt.Errorf("nmcli import: no connection UUID in %q", strings.TrimSpace(string(out)))
	}
	uuid := string(m[1])
	if _, err := a.run("nmcli", "connection", "modify", uuid,
		"connection.id", WGProfile,
		"connection.autoconnect", "yes",
		"ipv4.never-default", "yes",
		"ipv4.ignore-auto-dns", "yes",
		"ipv4.dns", "",
		"ipv4.routes", WGTunnelRoutes(cfg),
		"ipv6.method", "disabled",
		"wireguard.peer-routes", "no",
		"wireguard.ip4-auto-default-route", "false",
	); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(stateDir, WGPendingKey))
	if _, err := a.run("nmcli", "connection", "up", WGProfile); err != nil {
		a.warn("WireGuard: %s did not come up: %v", WGProfile, err)
	} else {
		a.note("WireGuard: %s up (%s mode, %s)", WGProfile, g.Mode, strings.ReplaceAll(g.OnDown, "_", " "))
	}
	return nil
}
