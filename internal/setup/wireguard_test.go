package setup

import (
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	palmgw "palm-gw"
	"palm-gw/deploy"
	"palm-gw/internal/config"
)

// Made-up keys: never a real tunnel's.
var (
	testPriv = base64.StdEncoding.EncodeToString(mustHex("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"))
	testPub  = base64.StdEncoding.EncodeToString(mustHex("8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a"))
	peerPub  = base64.StdEncoding.EncodeToString(make([]byte, 32))
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

const sampleConf = `[Interface]
PrivateKey = %PRIV%
Address = 10.17.200.5/32, fd00::5/128
DNS = 10.17.200.1, example.lan

[Peer]
PublicKey = %PEER%
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = vpn.example.net:51820
PersistentKeepalive = 25
`

func sample() string {
	return strings.NewReplacer("%PRIV%", testPriv, "%PEER%", peerPub).Replace(sampleConf)
}

func TestWGPublicKeyRFC7748(t *testing.T) {
	// RFC 7748 section 6.1: Alice's key pair.
	got, err := WGPublicKey(testPriv)
	if err != nil || got != testPub {
		t.Fatalf("WGPublicKey = %q, %v; want %q", got, err, testPub)
	}
	priv, pub, err := NewWGKey()
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := WGPublicKey(priv); again != pub || !wgKey(priv) || !wgKey(pub) {
		t.Fatalf("NewWGKey: %q does not derive %q", priv, pub)
	}
}

func TestParseWGConf(t *testing.T) {
	c, err := ParseWGConf(sample())
	if err != nil {
		t.Fatal(err)
	}
	if c.Address != "10.17.200.5/32" || c.Endpoint != "vpn.example.net:51820" || c.Keepalive != 25 ||
		c.PublicKey != peerPub || strings.Join(c.DNS, ",") != "10.17.200.1" || strings.Join(c.AllowedIPs, ",") != "0.0.0.0/0" {
		t.Fatalf("parsed %+v", c)
	}
	kvs := WGKVs(c, testPub)
	if kvs[6].Value != `"full"` {
		t.Fatalf("mode = %s, want full", kvs[6].Value)
	}
	split := strings.Replace(sample(), "AllowedIPs = 0.0.0.0/0, ::/0", "AllowedIPs = 10.17.0.0/16", 1)
	c, err = ParseWGConf(split)
	if err != nil || WGKVs(c, testPub)[6].Value != `"split"` {
		t.Fatalf("split: %v %+v", err, c)
	}
	for name, text := range map[string]string{
		"two peers":   sample() + "\n[Peer]\nPublicKey = " + peerPub + "\nAllowedIPs = 10.0.0.0/8\nEndpoint = b:1\n",
		"no peer":     "[Interface]\nPrivateKey = " + testPriv + "\nAddress = 10.1.1.1/32\n",
		"bad key":     strings.Replace(sample(), testPriv, "AAAA", 1),
		"no endpoint": strings.Replace(sample(), "Endpoint = vpn.example.net:51820\n", "", 1),
		"ipv6 only":   strings.Replace(sample(), "Address = 10.17.200.5/32, fd00::5/128", "Address = fd00::5/128", 1),
		"unknown key": sample() + "Bogus = 1\n",
	} {
		if _, err := ParseWGConf(text); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestImportWGWritesConfigAndStashesKey(t *testing.T) {
	root := t.TempDir()
	cfgPath, state := "/etc/palm-gw/config.toml", "/var/lib/palm-gw"
	if err := os.MkdirAll(filepath.Join(root, "/etc/palm-gw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, cfgPath), []byte(palmgw.ExampleConfig), 0o640); err != nil {
		t.Fatal(err)
	}
	res, err := ImportWG(root, cfgPath, state, sample())
	if err != nil {
		t.Fatal(err)
	}
	if res.PublicKey != testPub || res.Mode != "full" {
		t.Fatalf("result %+v", res)
	}
	cfg, err := config.Load(filepath.Join(root, cfgPath))
	if err != nil {
		t.Fatal(err)
	}
	g := cfg.WireGuard
	if !g.Enabled || g.Address != "10.17.200.5/32" || g.PeerPublicKey != peerPub || g.OnDown != "fail_closed" || g.PublicKey != testPub {
		t.Fatalf("[wireguard] = %+v", g)
	}
	raw, _ := os.ReadFile(filepath.Join(root, cfgPath))
	if strings.Contains(string(raw), testPriv) {
		t.Fatal("the private key reached config.toml")
	}
	fi, err := os.Stat(filepath.Join(root, state, WGPendingKey))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("pending key: %v %v", fi, err)
	}
}

func TestWGRouting(t *testing.T) {
	cfg := config.Default()
	if r, rt := WGBridgeRouting(cfg); r != "" || rt != "" {
		t.Fatalf("off: %q %q", r, rt)
	}
	cfg.WireGuard.Enabled = true
	cfg.WireGuard.DNS = []string{"10.17.200.1"}
	r, rt := WGBridgeRouting(cfg)
	if r != "priority 4241 from 10.42.0.0/24 table 254 suppress_prefixlength 0, priority 4242 from 10.42.0.0/24 table 4242" {
		t.Fatalf("rules %q", r)
	}
	if rt != "0.0.0.0/0 4096 table=4242 type=unreachable" {
		t.Fatalf("fail closed: %q", rt)
	}
	if got := WGTunnelRoutes(cfg); got != "0.0.0.0/0 50 table=4242, 10.17.200.1/32 50 table=254" {
		t.Fatalf("tunnel routes %q", got)
	}
	cfg.WireGuard.OnDown = "fallback"
	if _, rt := WGBridgeRouting(cfg); rt != "" {
		t.Fatalf("fallback keeps an unreachable route: %q", rt)
	}
	cfg.WireGuard.Mode, cfg.WireGuard.AllowedIPs = "split", []string{"10.17.0.0/16"}
	if got := WGTunnelRoutes(cfg); got != "10.17.0.0/16 50 table=4242, 10.17.200.1/32 50 table=254" {
		t.Fatalf("split routes %q", got)
	}
	if env := BridgeEnv(cfg); !strings.Contains(env, `WG_RULES="priority 4241`) || !strings.Contains(env, `WG_ROUTES=""`) {
		t.Fatalf("bridge.env:\n%s", env)
	}
}

func renderNft(t *testing.T, cfg *config.Config) string {
	t.Helper()
	text, err := deploy.FS.ReadFile(deploy.Nftables)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.Render(deploy.Nftables, string(text))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWGFirewall(t *testing.T) {
	cfg := config.Default()
	off := renderNft(t, cfg)
	if strings.Contains(off, "wg0") || !strings.Contains(off, `oifname "eth0" accept`) {
		t.Fatalf("off:\n%s", off)
	}
	cfg.WireGuard.Enabled = true // full, fail_closed by default
	on := renderNft(t, cfg)
	for _, want := range []string{
		`iifname "wg0" oifname { "br-palm", "ppp*" } ct state new counter drop comment "wg->palm"`,
		`iifname "wg0" ct state new counter drop comment "wg->pi"`,
		`ip saddr 10.42.0.0/24 oifname "wg0" masquerade`,
		`oifname "wg0" accept`,
	} {
		if !strings.Contains(on, want) {
			t.Errorf("full, fail closed: missing %s", want)
		}
	}
	if strings.Contains(on, `oifname "eth0" accept`) {
		t.Error("full, fail closed: Palm traffic may still leave through the uplink")
	}
	// The tunnel exit comes after the private-address drop, so hosts behind it get the LAN's guards.
	if strings.Index(on, `oifname "wg0" accept`) < strings.Index(on, `palm->rfc1918`) {
		t.Error("the tunnel exit is before the private-address drop")
	}
	cfg.WireGuard.OnDown = "fallback"
	if fb := renderNft(t, cfg); !strings.Contains(fb, `oifname "eth0" accept`) {
		t.Error("fallback: no uplink exit")
	}
	cfg.WireGuard.OnDown, cfg.WireGuard.Mode = "fail_closed", "split"
	if sp := renderNft(t, cfg); !strings.Contains(sp, `oifname "eth0" accept`) {
		t.Error("split: no uplink exit")
	}
}

func TestApplyWireGuardCommands(t *testing.T) {
	root := t.TempDir()
	state := "/var/lib/palm-gw"
	if err := stashWGKey(filepath.Join(root, state), testPriv); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WireGuard.Enabled = true
	cfg.WireGuard.Address = "10.17.200.5/32"
	cfg.WireGuard.Endpoint = "vpn.example.net:51820"
	cfg.WireGuard.PeerPublicKey = peerPub
	var calls []string
	var imported string
	run := func(name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		switch {
		case strings.Contains(line, "connection show "+WGProfile) && !strings.Contains(line, "-s"):
			return nil, os.ErrNotExist // no profile yet
		case strings.Contains(line, "connection import"):
			b, err := os.ReadFile(args[len(args)-1])
			if err != nil {
				t.Fatal(err)
			}
			imported = string(b)
			return []byte("Connection 'wg0' (5e1f2c3a-1111-4222-8333-944455556666) successfully added.\n"), nil
		}
		return nil, nil
	}
	a := &applier{o: ApplyOptions{Root: root, StateDir: state, Run: run}}
	if err := a.applyWireGuard(cfg); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(calls, "\n")
	if strings.Contains(all, testPriv) {
		t.Fatal("the private key appeared on a command line")
	}
	if !strings.Contains(imported, "PrivateKey = "+testPriv) || strings.Contains(imported, "DNS") {
		t.Fatalf("import file:\n%s", imported)
	}
	for _, want := range []string{
		"nmcli connection import type wireguard file " + filepath.Join(root, "/run/palm-gw/wg-import/wg0.conf"),
		"nmcli connection modify 5e1f2c3a-1111-4222-8333-944455556666 connection.id palm-gw-wg",
		"ipv4.never-default yes",
		"ipv4.routes 0.0.0.0/0 50 table=4242",
		"wireguard.peer-routes no",
		"nmcli connection up palm-gw-wg",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "/run/palm-gw/wg-import/wg0.conf")); !os.IsNotExist(err) {
		t.Error("the import file was left behind")
	}
	if _, err := os.Stat(filepath.Join(root, state, WGPendingKey)); !os.IsNotExist(err) {
		t.Error("the pending key was left behind")
	}
	// Enabled with no key anywhere is an error, not a silent tunnel without a key.
	calls = nil
	if err := a.applyWireGuard(cfg); err == nil || !strings.Contains(err.Error(), "no WireGuard key") {
		t.Fatalf("no key: %v", err)
	}
}
