package fw

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"palm-gw/deploy"
	"palm-gw/internal/config"
)

func TestScript(t *testing.T) {
	cases := []struct {
		name string
		fw   config.Firewall
		want string
	}{
		{
			name: "plan example",
			fw: config.Firewall{
				PalmToPi: []string{"udp/53", "udp/67", "icmp"},
				Allow: []config.AllowRule{
					{Host: "192.168.1.20", Ports: []string{"tcp/14238", "tcp/14237"}},
					{Host: "192.168.1.0/24", Ports: []string{"udp/5353"}},
				},
			},
			want: `flush set inet palm_gw lan_allow
flush set inet palm_gw lan_allow_icmp
flush set inet palm_gw pi_allow
add element inet palm_gw lan_allow { 192.168.1.20/32 . tcp . 14237, 192.168.1.20/32 . tcp . 14238, 192.168.1.0/24 . udp . 5353 }
add element inet palm_gw pi_allow { udp . 53, udp . 67 }
`,
		},
		{
			name: "empty",
			fw:   config.Firewall{},
			want: `flush set inet palm_gw lan_allow
flush set inet palm_gw lan_allow_icmp
flush set inet palm_gw pi_allow
`,
		},
		{
			name: "icmp, host bits, nested and duplicate hosts",
			fw: config.Firewall{
				PalmToPi: []string{"tcp/8080", "tcp/8080"},
				Allow: []config.AllowRule{
					{Host: "10.4.0.7", Ports: []string{"icmp", "tcp/22"}},
					{Host: "10.4.0.9/23", Ports: []string{"tcp/22", "icmp"}}, // masks to 10.4.0.0/23, covers .7
					{Host: "10.4.0.7", Ports: []string{"tcp/22", "udp/22"}},
					{Host: "172.16.5.5", Ports: []string{"icmp"}},
				},
			},
			want: `flush set inet palm_gw lan_allow
flush set inet palm_gw lan_allow_icmp
flush set inet palm_gw pi_allow
add element inet palm_gw lan_allow { 10.4.0.0/23 . tcp . 22, 10.4.0.7/32 . udp . 22 }
add element inet palm_gw lan_allow_icmp { 10.4.0.0/23, 172.16.5.5/32 }
add element inet palm_gw pi_allow { tcp . 8080 }
`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Script(c.fw); got != c.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
}

// TestNftAcceptsTemplateAndScript runs `nft -c` over the rendered table plus
// the generated elements (PLAN.md section 8.7). Skipped without nft, or where
// nft may not open netlink (it needs CAP_NET_ADMIN even to check).
func TestNftAcceptsTemplateAndScript(t *testing.T) {
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	tmpl, err := deploy.FS.ReadFile(deploy.Nftables)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, mss := range []bool{true, false} {
		cfg := config.Default()
		cfg.Network.MSSClamp = mss
		cfg.Web.BluetoothSide = mss // both renders of the 8042 rule, too
		cfg.Firewall.Allow = []config.AllowRule{
			{Host: "192.168.1.20", Ports: []string{"tcp/14238", "tcp/14237", "icmp"}},
			{Host: "192.168.1.0/24", Ports: []string{"udp/5353", "tcp/14238"}},
		}
		cfg.Firewall.PalmToPi = []string{"udp/53", "tcp/8080", "icmp"}
		table, err := cfg.Render(deploy.Nftables, string(tmpl))
		if err != nil {
			t.Fatal(err)
		}
		err = Nft{}.Check(ctx, table+Script(cfg.Firewall))
		if err != nil && strings.Contains(err.Error(), "Operation not permitted") {
			t.Skip("nft -c needs CAP_NET_ADMIN here")
		}
		if err != nil {
			t.Fatalf("mss_clamp=%v: %v", mss, err)
		}
	}
}

// The web port: accepted on the gateway address only when the Bluetooth side
// is on (D-026, D-039), and dropped from the Palm side on every other address.
func TestTemplateWebRules(t *testing.T) {
	tmpl, err := deploy.FS.ReadFile(deploy.Nftables)
	if err != nil {
		t.Fatal(err)
	}
	accept := "ip daddr 10.42.0.1 tcp dport 8042 accept"
	for _, c := range []struct {
		name   string
		edit   func(*config.Config)
		accept bool
	}{
		{"default", func(*config.Config) {}, true},
		{"bluetooth side off", func(c *config.Config) { c.Web.BluetoothSide = false }, false},
		{"tls off", func(c *config.Config) { c.Web.TLS = "off"; c.Web.BluetoothSide = false }, false},
		{"web off", func(c *config.Config) { c.Web.Enabled = false }, false},
	} {
		cfg := config.Default()
		c.edit(cfg)
		table, err := cfg.Render(deploy.Nftables, string(tmpl))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(table, accept); got != c.accept {
			t.Errorf("%s: 8042 accept present = %v", c.name, got)
		}
		if !strings.Contains(table, `tcp dport 8042 counter drop comment "palm->webui-lan"`) {
			t.Errorf("%s: no 8042 drop", c.name)
		}
		if !strings.Contains(table, "ip daddr 10.42.0.1 tcp dport 80 accept") {
			t.Errorf("%s: port 80 not limited to the gateway", c.name)
		}
		if i, j := strings.Index(table, accept), strings.Index(table, "palm->webui-lan"); c.accept && i > j {
			t.Errorf("%s: the accept must come before the drop", c.name)
		}
	}
	cfg := config.Default()
	cfg.Web.Listen = []string{"0.0.0.0:9443"}
	table, _ := cfg.Render(deploy.Nftables, string(tmpl))
	if !strings.Contains(table, "ip daddr 10.42.0.1 tcp dport 9443 accept") || strings.Contains(table, "8042") {
		t.Errorf("listen port 9443 not followed:\n%s", table)
	}
}

func TestParseCounters(t *testing.T) {
	// Trimmed from `nft -j list table inet palm_gw` on the Pi.
	in := `{"nftables": [{"metainfo": {"version": "1.1.3"}}, {"table": {"family": "inet", "name": "palm_gw"}},
 {"rule": {"family": "inet", "table": "palm_gw", "chain": "palm_forward", "comment": "palm->rfc1918",
   "expr": [{"match": {"op": "==", "left": {"payload": {"protocol": "ip", "field": "daddr"}}, "right": {"set": []}}}, {"counter": {"packets": 12, "bytes": 720}}, {"drop": null}]}},
 {"rule": {"family": "inet", "table": "palm_gw", "chain": "palm_input", "comment": "the you-are-connected page (D-039)", "expr": [{"accept": null}]}},
 {"rule": {"family": "inet", "table": "palm_gw", "chain": "palm_input", "comment": "palm->pi", "expr": [{"counter": {"packets": 3, "bytes": 180}}, {"drop": null}]}},
 {"rule": {"family": "inet", "table": "palm_gw", "chain": "palm_input", "expr": [{"counter": {"packets": 9, "bytes": 9}}, {"drop": null}]}}]}`
	got, err := ParseCounters([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Counter{"palm_forward", "palm->rfc1918", 12, 720}) || got[1].Comment != "palm->pi" || got[1].Packets != 3 {
		t.Errorf("counters %+v", got)
	}
}
