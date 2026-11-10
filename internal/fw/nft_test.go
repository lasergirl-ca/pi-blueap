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
