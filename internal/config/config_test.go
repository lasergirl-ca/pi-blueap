package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keys returns the keys named by a validation error, or nil if err is not one.
func keys(t *testing.T, err error) []string {
	t.Helper()
	var es Errors
	if !errors.As(err, &es) {
		t.Fatalf("expected config.Errors, got %T: %v", err, err)
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Key)
	}
	return out
}

func mustHaveKey(t *testing.T, err error, key string) {
	t.Helper()
	for _, k := range keys(t, err) {
		if k == key {
			return
		}
	}
	t.Fatalf("error does not name key %q: %v", key, err)
}

func TestEmptyFileIsValidAndEqualsDefault(t *testing.T) {
	cfg, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	def := Default()
	if cfg.Bluetooth.Adapter != def.Bluetooth.Adapter || cfg.Network.Gateway != "10.42.0.1" || cfg.SPP.Channel != 1 || cfg.DUN.Channel != 3 {
		t.Fatalf("empty file did not yield defaults: %+v", cfg)
	}
	if cfg.Bluetooth.AllowAny {
		t.Fatal("allow_any must default to false")
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	text := `
[bluetooth]
name = "Zoë's Palm GW"
class = 0x000104
allow = ["00:07:E0:12:34:56"]

[network]
uplink = "wlan0"
dns_upstream = ["1.1.1.1", "9.9.9.9"]

[[firewall.allow]]
host = "192.168.1.20"
ports = ["tcp/14238", "tcp/14237"]
comment = "NetSync"

[[firewall.allow]]
host = "192.168.1.0/24"
ports = ["udp/5353", "icmp"]
`
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bluetooth.Name != "Zoë's Palm GW" || cfg.Bluetooth.Class != 0x104 {
		t.Fatalf("bluetooth not decoded: %+v", cfg.Bluetooth)
	}
	if cfg.Network.Uplink != "wlan0" || len(cfg.Network.DNSUpstream) != 2 {
		t.Fatalf("network not decoded: %+v", cfg.Network)
	}
	if len(cfg.Firewall.Allow) != 2 || cfg.Firewall.Allow[1].Ports[1] != "icmp" {
		t.Fatalf("firewall.allow not decoded: %+v", cfg.Firewall.Allow)
	}
	// Arrays replace the defaults rather than appending to them.
	if len(cfg.Bluetooth.Allow) != 1 {
		t.Fatalf("allow should have exactly one entry, got %v", cfg.Bluetooth.Allow)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Fatal("expected error for a missing file")
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	_, err := Parse("[bluetooth]\nadaptor = \"hci0\"\n")
	if err == nil {
		t.Fatal("expected error")
	}
	mustHaveKey(t, err, "bluetooth.adaptor")
}

func TestSyntaxErrorNamesPosition(t *testing.T) {
	_, err := Parse("[bluetooth\nname = 1\n")
	if err == nil || !strings.Contains(err.Error(), "config:") {
		t.Fatalf("expected a config parse error, got %v", err)
	}
}

func TestValidationRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *Config)
		key    string
	}{
		{"adapter garbage", func(c *Config) { c.Bluetooth.Adapter = "hci" }, "bluetooth.adapter"},
		{"adapter usb", func(c *Config) { c.Bluetooth.Adapter = "usb0" }, "bluetooth.adapter"},
		{"empty name", func(c *Config) { c.Bluetooth.Name = "  " }, "bluetooth.name"},
		{"class too wide", func(c *Config) { c.Bluetooth.Class = 0x1000000 }, "bluetooth.class"},
		{"pin letters", func(c *Config) { c.Bluetooth.PIN = "12ab" }, "bluetooth.pin"},
		{"pin too long", func(c *Config) { c.Bluetooth.PIN = "12345678901234567" }, "bluetooth.pin"},
		{"pin length zero", func(c *Config) { c.Bluetooth.PINLength = 0 }, "bluetooth.pin_length"},
		{"window zero", func(c *Config) { c.Bluetooth.PairWindowDefaultMinutes = 0 }, "bluetooth.pair_window_default_minutes"},
		{"bad allow addr", func(c *Config) { c.Bluetooth.Allow = []string{"00:07:E0:12:34"} }, "bluetooth.allow[0]"},
		{"channel 0", func(c *Config) { c.SPP.Channel = 0 }, "spp.channel"},
		{"channel 31", func(c *Config) { c.LAP.Channel = 31 }, "lap.channel"},
		{"duplicate channel", func(c *Config) { c.DUN.Channel = 1 }, "dun.channel"},
		{"bridge too long", func(c *Config) { c.PAN.Bridge = "a-very-long-bridge-name" }, "pan.bridge"},
		{"negative ms timeout", func(c *Config) { c.Handshake.MSTimeoutMS = -1 }, "handshake.ms_timeout_ms"},
		{"empty banner", func(c *Config) { c.Handshake.ATConnectBanner = "" }, "handshake.at_connect_banner"},
		{"subnet garbage", func(c *Config) { c.Network.Subnet = "10.42.0.0" }, "network.subnet"},
		{"subnet not masked", func(c *Config) { c.Network.Subnet = "10.42.0.1/24" }, "network.subnet"},
		{"subnet v6", func(c *Config) { c.Network.Subnet = "fd00::/64" }, "network.subnet"},
		{"gateway outside", func(c *Config) { c.Network.Gateway = "10.43.0.1" }, "network.gateway"},
		{"gateway is network addr", func(c *Config) { c.Network.Gateway = "10.42.0.0" }, "network.gateway"},
		{"gateway is broadcast", func(c *Config) { c.Network.Gateway = "10.42.0.255" }, "network.gateway"},
		{"ppp pool one entry", func(c *Config) { c.Network.PPPPool = []string{"10.42.0.10"} }, "network.ppp_pool"},
		{"ppp pool reversed", func(c *Config) { c.Network.PPPPool = []string{"10.42.0.99", "10.42.0.10"} }, "network.ppp_pool"},
		{"ppp pool outside", func(c *Config) { c.Network.PPPPool = []string{"10.42.1.10", "10.42.1.99"} }, "network.ppp_pool[0]"},
		{"ppp pool contains gateway", func(c *Config) { c.Network.PPPPool = []string{"10.42.0.1", "10.42.0.99"} }, "network.ppp_pool"},
		{"ppp pool too small", func(c *Config) { c.Network.PPPPool = []string{"10.42.0.10", "10.42.0.12"} }, "network.ppp_pool"},
		{"pools overlap", func(c *Config) { c.Network.DHCPPool = []string{"10.42.0.50", "10.42.0.199"} }, "network.dhcp_pool"},
		{"dhcp pool bad ip", func(c *Config) { c.Network.DHCPPool = []string{"10.42.0.100", "nope"} }, "network.dhcp_pool[1]"},
		{"uplink empty", func(c *Config) { c.Network.Uplink = "" }, "network.uplink"},
		{"dns upstream bad", func(c *Config) { c.Network.DNSUpstream = []string{"one.one.one.one"} }, "network.dns_upstream[0]"},
		{"options file empty", func(c *Config) { c.PPP.OptionsFile = "" }, "ppp.options_file"},
		{"idle negative", func(c *Config) { c.PPP.IdleSeconds = -5 }, "ppp.idle_seconds"},
		{"max sessions zero", func(c *Config) { c.PPP.MaxSessions = 0 }, "ppp.max_sessions"},
		{"per device zero", func(c *Config) { c.PPP.PerDeviceMax = 0 }, "ppp.per_device_max"},
		{"palm_to_pi bad", func(c *Config) { c.Firewall.PalmToPi = []string{"sctp/53"} }, "firewall.palm_to_pi[0]"},
		{"allow host bad", func(c *Config) { c.Firewall.Allow = []AllowRule{{Host: "nas.local", Ports: []string{"tcp/80"}}} }, "firewall.allow[0].host"},
		{"allow ports empty", func(c *Config) { c.Firewall.Allow = []AllowRule{{Host: "192.168.1.2"}} }, "firewall.allow[0].ports"},
		{"allow port out of range", func(c *Config) { c.Firewall.Allow = []AllowRule{{Host: "192.168.1.2", Ports: []string{"tcp/70000"}}} }, "firewall.allow[0].ports[0]"},
		{"allow port syntax", func(c *Config) { c.Firewall.Allow = []AllowRule{{Host: "192.168.1.2", Ports: []string{"80"}}} }, "firewall.allow[0].ports[0]"},
		{"log level", func(c *Config) { c.Logging.Level = "verbose" }, "logging.level"},
		{"log format", func(c *Config) { c.Logging.Format = "xml" }, "logging.format"},
		{"ctl socket relative", func(c *Config) { c.Ctl.Socket = "ctl.sock" }, "ctl.socket"},
		{"ctl group empty", func(c *Config) { c.Ctl.Group = "" }, "ctl.group"},
		{"web listen empty", func(c *Config) { c.Web.Listen = nil }, "web.listen"},
		{"web listen bad", func(c *Config) { c.Web.Listen = []string{"8042"} }, "web.listen[0]"},
		{"web tls bad", func(c *Config) { c.Web.TLS = "letsencrypt" }, "web.tls"},
		{"web tls file no cert", func(c *Config) { c.Web.TLS = "file"; c.Web.TLSKeyFile = "k" }, "web.tls_cert_file"},
		{"web tls file no key", func(c *Config) { c.Web.TLS = "file"; c.Web.TLSCertFile = "c" }, "web.tls_key_file"},
		{"web session hours", func(c *Config) { c.Web.SessionHours = 0 }, "web.session_hours"},
		{"web allow cidr", func(c *Config) { c.Web.AllowCIDRs = []string{"lan"} }, "web.allow_cidrs[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mutate(c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected an error naming %s", tc.key)
			}
			mustHaveKey(t, err, tc.key)
		})
	}
}

func TestValidationAccepts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *Config)
	}{
		{"adapter auto", func(c *Config) { c.Bluetooth.Adapter = "auto" }},
		{"adapter hci12", func(c *Config) { c.Bluetooth.Adapter = "hci12" }},
		{"fixed pin", func(c *Config) { c.Bluetooth.PIN = "1234" }},
		{"disabled profile may share channel", func(c *Config) { c.DUN.Enabled = false; c.DUN.Channel = 1 }},
		{"disabled profile may have channel 0", func(c *Config) { c.LAP.Enabled = false; c.LAP.Channel = 0 }},
		{"other subnet", func(c *Config) {
			c.Network.Subnet = "192.168.77.0/24"
			c.Network.Gateway = "192.168.77.1"
			c.Network.PPPPool = []string{"192.168.77.10", "192.168.77.99"}
			c.Network.DHCPPool = []string{"192.168.77.100", "192.168.77.199"}
		}},
		{"cidr allow host with icmp", func(c *Config) {
			c.Firewall.Allow = []AllowRule{{Host: "192.168.1.0/24", Ports: []string{"icmp", "udp/5353"}}}
		}},
		{"web disabled ignores web keys", func(c *Config) { c.Web.Enabled = false; c.Web.TLS = "whatever"; c.Web.Listen = nil }},
		{"pool exactly max_sessions", func(c *Config) { c.Network.PPPPool = []string{"10.42.0.10", "10.42.0.17"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mutate(c)
			if err := c.Validate(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMultipleErrorsAreAllReported(t *testing.T) {
	c := Default()
	c.Bluetooth.Adapter = "x"
	c.Logging.Level = "y"
	c.PPP.MaxSessions = 0
	err := c.Validate()
	ks := keys(t, err)
	if len(ks) != 3 {
		t.Fatalf("expected 3 errors, got %d: %v", len(ks), err)
	}
	if !strings.Contains(err.Error(), "3 problems") {
		t.Fatalf("summary line missing: %v", err)
	}
}

func TestCheckRuntime(t *testing.T) {
	c := Default()
	_, err := c.CheckRuntime(func(string) InterfaceState { return InterfaceState{} })
	if err == nil {
		t.Fatal("missing uplink must be an error")
	}
	mustHaveKey(t, err, "network.uplink")

	w, err := c.CheckRuntime(func(string) InterfaceState { return InterfaceState{Exists: true, Up: true} })
	if err != nil || len(w) != 0 {
		t.Fatalf("up uplink: warnings=%v err=%v", w, err)
	}

	c.Network.Uplink = "wlan0"
	w, err = c.CheckRuntime(func(string) InterfaceState { return InterfaceState{Exists: true, Up: false} })
	if err != nil || len(w) != 1 {
		t.Fatalf("down wlan0 must warn, not fail: warnings=%v err=%v", w, err)
	}
}

func TestWarnings(t *testing.T) {
	c := Default()
	if len(c.Warnings()) != 0 {
		t.Fatalf("defaults should not warn: %v", c.Warnings())
	}
	c.Bluetooth.AllowAny = true
	if w := c.Warnings(); len(w) != 1 || !strings.Contains(w[0], "allow_any") {
		t.Fatalf("allow_any must warn: %v", w)
	}
}

func TestRedacted(t *testing.T) {
	c := Default()
	c.Bluetooth.PIN = "4821"
	c.Web.PasswordHash = "$argon2id$..."
	r := c.Redacted()
	if r.Bluetooth.PIN != "(redacted)" || r.Web.PasswordHash != "(redacted)" {
		t.Fatalf("secrets leaked: %+v", r)
	}
	if c.Bluetooth.PIN != "4821" {
		t.Fatal("Redacted must not mutate the original")
	}
	if Default().Redacted().Bluetooth.PIN != "" {
		t.Fatal("empty secrets stay empty")
	}
}

func TestRender(t *testing.T) {
	c := Default()
	out, err := c.Render("t", `gw={{GATEWAY}} net={{SUBNET}} pfx={{PREFIX}} mask={{NETMASK}} up={{UPLINK}} br={{BRIDGE}} dhcp={{DHCP_START}}-{{DHCP_END}}{{if MSS_CLAMP}} clamp{{end}}{{range DNS_UPSTREAM}} dns={{.}}{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "gw=10.42.0.1 net=10.42.0.0/24 pfx=24 mask=255.255.255.0 up=eth0 br=br-palm dhcp=10.42.0.100-10.42.0.199 clamp"
	if out != want {
		t.Fatalf("got  %q\nwant %q", out, want)
	}

	c.Network.MSSClamp = false
	c.Network.DNSUpstream = []string{"1.1.1.1", "9.9.9.9"}
	out, err = c.Render("t", `{{if MSS_CLAMP}}clamp{{end}}{{range DNS_UPSTREAM}}server={{.}};{{end}}`)
	if err != nil || out != "server=1.1.1.1;server=9.9.9.9;" {
		t.Fatalf("got %q err %v", out, err)
	}

	// Plain files pass through untouched, braces in nft syntax included.
	plain := "table inet x { set s { type ipv4_addr } }\n"
	out, err = c.Render("plain", plain)
	if err != nil || out != plain {
		t.Fatalf("plain passthrough: %q %v", out, err)
	}

	if _, err := c.Render("bad", "{{NOPE}}"); err == nil {
		t.Fatal("unknown template function must fail")
	}
}

func TestNetmask(t *testing.T) {
	for bits, want := range map[int]string{0: "0.0.0.0", 8: "255.0.0.0", 24: "255.255.255.0", 29: "255.255.255.248", 32: "255.255.255.255"} {
		if got := netmask(bits); got != want {
			t.Errorf("netmask(%d) = %s, want %s", bits, got, want)
		}
	}
}
