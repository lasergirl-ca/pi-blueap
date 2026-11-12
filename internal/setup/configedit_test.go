package setup

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"palm-gw/internal/config"
)

func exampleText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustParse(t *testing.T, text string) *config.Config {
	t.Helper()
	c, err := config.Parse(text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	return c
}

func TestEditConfigScalars(t *testing.T) {
	text := exampleText(t)
	for _, c := range []struct {
		path, value string
		check       func(*config.Config) bool
		line        string
	}{
		{"bluetooth.name", `"Zoë's GW"`, func(c *config.Config) bool { return c.Bluetooth.Name == "Zoë's GW" }, `name             = "Zoë's GW"`},
		{"ppp.debug", `true`, func(c *config.Config) bool { return c.PPP.Debug }, ""},
		{"ppp.idle_seconds", `600`, func(c *config.Config) bool { return c.PPP.IdleSeconds == 600 }, ""},
		{"bluetooth.class", `260`, func(c *config.Config) bool { return c.Bluetooth.Class == 0x104 }, "class            = 0x000104"},
		{"network.dns_upstream", `["1.1.1.1","9.9.9.9"]`, func(c *config.Config) bool {
			return reflect.DeepEqual(c.Network.DNSUpstream, []string{"1.1.1.1", "9.9.9.9"})
		}, ""},
	} {
		got, err := EditConfig(text, c.path, json.RawMessage(c.value))
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if !c.check(mustParse(t, got)) {
			t.Errorf("%s = %s not applied", c.path, c.value)
		}
		if c.line != "" && !strings.Contains(got, c.line) {
			t.Errorf("%s: no line %q", c.path, c.line)
		}
		// Comments survive.
		if !strings.Contains(got, "# Every key has a default") || strings.Count(got, "\n") < strings.Count(text, "\n")-1 {
			t.Errorf("%s: the file lost lines or comments", c.path)
		}
	}
	for _, bad := range []struct{ path, value string }{
		{"bluetooth.nmae", `"x"`},
		{"ppp.idle_seconds", `"ten"`},
		{"ppp.idle_seconds", `1.5`},
		{"ppp.idle_seconds", `-3`},
		{"ppp.debug", `"yes"`},
		{"network.dns_upstream", `"1.1.1.1"`},
	} {
		if _, err := EditConfig(text, bad.path, json.RawMessage(bad.value)); err == nil {
			t.Errorf("%s = %s accepted", bad.path, bad.value)
		}
	}
	if _, err := EditConfig(text, "nope.key", json.RawMessage(`1`)); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("unknown key: %v", err)
	}
}

func TestSetFirewallAllow(t *testing.T) {
	text := exampleText(t)
	rules := []config.AllowRule{
		{Host: "192.168.1.20", Ports: []string{"tcp/14238", "tcp/14237"}, Comment: "NetSync"},
		{Host: "192.168.1.0/24", Ports: []string{"icmp"}},
	}
	b, _ := json.Marshal(rules)
	got, err := EditConfig(text, "firewall.allow", b)
	if err != nil {
		t.Fatal(err)
	}
	c := mustParse(t, got)
	if !reflect.DeepEqual(c.Firewall.Allow, rules) {
		t.Fatalf("allow = %+v", c.Firewall.Allow)
	}
	// The commented examples stay; the block appears once.
	if !strings.Contains(got, "# [[firewall.allow]]") || strings.Count(got, allowBegin) != 1 {
		t.Errorf("text:\n%s", got)
	}
	// Writing again replaces the block rather than adding one.
	got2 := SetFirewallAllow(got, rules[:1])
	if strings.Count(got2, allowBegin) != 1 || len(mustParse(t, got2).Firewall.Allow) != 1 {
		t.Errorf("rewrite:\n%s", got2)
	}
	// An empty list removes the block and leaves the file as it was.
	if back := SetFirewallAllow(got2, nil); back != text {
		t.Errorf("emptying did not give back the original file")
	}

	// Tables written by hand are taken into the block, and a one-line
	// `allow = []` under [firewall] goes too.
	hand := "[firewall]\nallow = []\nmanage_nftables = true\n\n[[firewall.allow]]\nhost = \"10.4.0.5\"\nports = [\"tcp/22\"]\n\n# trailing note\n[logging]\nlevel = \"info\"\n"
	out := SetFirewallAllow(hand, rules)
	c = mustParse(t, out)
	if len(c.Firewall.Allow) != 2 || strings.Contains(out, "10.4.0.5") || strings.Contains(out, "allow = []") || !strings.Contains(out, "# trailing note") {
		t.Errorf("hand-written:\n%s", out)
	}
	// Unknown fields in the JSON are refused.
	if _, err := EditConfig(text, "firewall.allow", json.RawMessage(`[{"host":"x","prots":["tcp/1"]}]`)); err == nil {
		t.Error("misspelled field accepted")
	}
}

// Setting a value and setting it back gives back the file byte for byte, even
// when the longer value pushed the comment right (found on the Pi, test17).
func TestEditRoundTrip(t *testing.T) {
	text := exampleText(t)
	text, _ = EditConfig(text, "bluetooth.name", json.RawMessage(`"Palm Gateway - Lyman"`))
	longer, _ := EditConfig(text, "bluetooth.name", json.RawMessage(`"Palm Gateway - Lyman test"`))
	back, _ := EditConfig(longer, "bluetooth.name", json.RawMessage(`"Palm Gateway - Lyman"`))
	if back != text {
		t.Errorf("round trip changed the file:\n%s", back)
	}
	short, _ := EditConfig(text, "bluetooth.name", json.RawMessage(`"GW"`))
	if !strings.Contains(short, `name             = "GW"            # Adapter Alias`) {
		t.Errorf("a short name's comment is not at the section's column:\n%s", short)
	}
}

// A key missing from [web] goes under [web], not into the [[firewall.allow]]
// tables after it (found on the Pi: an older config.toml without
// web.bluetooth_side and with a firewall rule added from the UI).
func TestSetKeyMissingBeforeArrayTables(t *testing.T) {
	text := "[web]\nenabled = true\ntls     = \"self-signed\"\n\n" + allowBegin + "\n[[firewall.allow]]\nhost    = \"10.4.0.5\"\nports   = [\"tcp/22\"]\ncomment = \"x\"\n\n" + allowEnd + "\n"
	got := SetKeys(text, []KV{{"web", "bluetooth_side", "false"}, {"web", "comment", `"y"`}})
	want := "[web]\nenabled = true\ntls     = \"self-signed\"\nbluetooth_side = false\ncomment = \"y\"\n\n" + allowBegin + "\n[[firewall.allow]]\nhost    = \"10.4.0.5\"\nports   = [\"tcp/22\"]\ncomment = \"x\"\n\n" + allowEnd + "\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
