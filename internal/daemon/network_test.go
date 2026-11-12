package daemon

import (
	"reflect"
	"testing"

	"palm-gw/internal/config"
)

func TestLiveMergeSplitsAppliedFromRestart(t *testing.T) {
	cur := config.Default()
	next := config.Default()
	next.Firewall.Allow = []config.AllowRule{{Host: "10.4.0.1", Ports: []string{"tcp/80"}}}
	next.Firewall.PalmToPi = []string{"udp/53"}
	next.Network.DNSUpstream = []string{"1.1.1.1"}
	next.Bluetooth.Allow = []string{"00:07:E0:3C:5E:FA"}
	next.Network.Subnet = "10.43.0.0/24"
	next.Bluetooth.Name = "Other"
	next.PPP.Debug = true
	next.Web.PasswordHash = "$argon2id$x"
	next.Web.SessionHours = 1
	next.LAP.Channel = 5
	next.Handshake.MSTimeoutMS = 4000

	merged := liveMerge(cur, next)
	applied := diffKeys(cur, merged)
	want := []string{"bluetooth.allow", "bluetooth.name", "firewall.allow", "firewall.palm_to_pi", "handshake.ms_timeout_ms", "network.dns_upstream", "ppp.debug", "web.password_hash"}
	if !reflect.DeepEqual(applied, want) {
		t.Errorf("applied = %v, want %v", applied, want)
	}
	restart := diffKeys(merged, next)
	want = []string{"lap.channel", "network.subnet", "web.session_hours"}
	if !reflect.DeepEqual(restart, want) {
		t.Errorf("needs restart = %v, want %v", restart, want)
	}
	if len(cur.Firewall.Allow) != 0 {
		t.Errorf("liveMerge changed the running config: %+v", cur.Firewall)
	}
	if got := diffKeys(cur, cur); len(got) != 0 {
		t.Errorf("diff of a config with itself = %v", got)
	}
}
