package setup

import (
	"errors"
	"net"
	"strings"
	"testing"

	palmgw "palm-gw"
	"palm-gw/internal/config"
)

func TestSetKeys(t *testing.T) {
	in := strings.Join([]string{
		"[bluetooth]",
		`adapter          = "hci0"          # "hci0", "hci1", or "auto"`,
		`name             = "Palm Gateway"`,
		"",
		"[network]",
		`uplink     = "eth0"                             # interface to NAT out of`,
		`note       = "a # in a string"   # real comment`,
		"",
	}, "\n")
	out := SetKeys(in, []KV{
		{"bluetooth", "adapter", TOMLString("60:15:6F:E9:EB:03")},
		{"network", "uplink", TOMLString("wlan0")},
		{"network", "note", TOMLString("x")},
		{"network", "mss_clamp", "false"},
		{"web", "username", TOMLString("zoe")},
	})
	want := strings.Join([]string{
		"[bluetooth]",
		`adapter          = "60:15:6F:E9:EB:03" # "hci0", "hci1", or "auto"`,
		`name             = "Palm Gateway"`,
		"",
		"[network]",
		`uplink     = "wlan0"                            # interface to NAT out of`,
		`note       = "x"                 # real comment`,
		"mss_clamp = false",
		"",
		"[web]",
		`username = "zoe"`,
		"",
	}, "\n")
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if got := TOMLString(`Zoë's "GW"\`); got != `"Zoë's \"GW\"\\"` {
		t.Errorf("TOMLString: %s", got)
	}
}

// Editing the shipped example changes exactly the lines asked for and still
// loads.
func TestSetKeysOnExample(t *testing.T) {
	out := SetKeys(palmgw.ExampleConfig, []KV{
		{"network", "uplink", TOMLString("wlan0")},
		{"web", "username", TOMLString("zoe")},
	})
	a, b := strings.Split(palmgw.ExampleConfig, "\n"), strings.Split(out, "\n")
	if len(a) != len(b) {
		t.Fatalf("line count %d -> %d", len(a), len(b))
	}
	diff := 0
	for i := range a {
		if a[i] != b[i] {
			diff++
		}
	}
	if diff != 2 {
		t.Fatalf("%d lines changed, want 2", diff)
	}
	cfg, err := config.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Network.Uplink != "wlan0" || cfg.Web.Username != "zoe" {
		t.Fatalf("got %+v %+v", cfg.Network, cfg.Web)
	}
}

func TestPassword(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("hash %s", h)
	}
	for pw, want := range map[string]bool{"correct horse": true, "correct horsE": false, "": false} {
		if ok, err := VerifyPassword(pw, h); err != nil || ok != want {
			t.Errorf("Verify(%q) = %v, %v", pw, ok, err)
		}
	}
	if h2, _ := HashPassword("correct horse"); h2 == h {
		t.Error("two hashes of one password should differ (salt)")
	}
	if CheckNewPassword("short") == nil || CheckNewPassword("long enough") != nil {
		t.Error("CheckNewPassword")
	}
}

func TestEnsureCert(t *testing.T) {
	dir := t.TempDir()
	names := CertNames{DNS: []string{"piblueap", "piblueap.local"}, IPs: []net.IP{net.ParseIP("10.4.0.241").To4(), net.ParseIP("10.42.0.1").To4()}}
	fp, created, err := EnsureCert(dir, names)
	if err != nil || !created || len(fp) != 95 {
		t.Fatalf("first: %q %v %v", fp, created, err)
	}
	fp2, created, err := EnsureCert(dir, names)
	if err != nil || created || fp2 != fp {
		t.Fatalf("second should reuse: %q %v %v", fp2, created, err)
	}
	names.IPs = append(names.IPs, net.ParseIP("10.4.0.243").To4())
	fp3, created, err := EnsureCert(dir, names)
	if err != nil || !created || fp3 == fp {
		t.Fatalf("new address should make a new cert: %v %v", created, err)
	}
}

// script answers prompts by title prefix, in order per title.
type script struct {
	t       *testing.T
	answers map[string][]string
	asked   []string
	notes   []string
}

func (s *script) next(title string) string {
	s.asked = append(s.asked, title)
	best := ""
	for prefix, as := range s.answers {
		if strings.HasPrefix(title, prefix) && len(as) > 0 && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		s.t.Fatalf("unexpected prompt %q", title)
	}
	as := s.answers[best]
	s.answers[best] = as[1:]
	return as[0]
}

func (s *script) Note(text string) { s.notes = append(s.notes, text) }
func (s *script) Input(title, _, _ string, v func(string) error) (string, error) {
	a := s.next(title)
	if v != nil {
		if err := v(a); err != nil {
			return "", err
		}
	}
	return a, nil
}
func (s *script) Password(title string, v func(string) error) (string, error) {
	return s.Input(title, "", "", v)
}
func (s *script) Select(title, _ string, opts []Option, _ string) (string, error) {
	a := s.next(title)
	for _, o := range opts {
		if o.Value == a {
			return a, nil
		}
	}
	return "", errors.New("not an option: " + a)
}
func (s *script) Confirm(title, _ string, _ bool) (bool, error) { return s.next(title) == "yes", nil }

func pi5Report() Report {
	return Report{
		Board: Board{ID: "pi5"},
		Adapters: []Adapter{
			{HCI: "hci0", Address: "2C:CF:67:2F:29:F5", Bus: "serial", Powered: true},
			{HCI: "hci1", Address: "60:15:6F:E9:EB:03", Bus: "usb", Powered: true},
		},
		Uplinks: []Uplink{{Iface: "eth0", Gateway: "10.4.0.1", Metric: 100}, {Iface: "wlan0", Gateway: "10.4.0.1", Metric: 600, Wireless: true, FreqMHz: 5240}},
		Subnet:  Subnet{Want: "10.42.0.0/24", Free: true},
	}
}

func edit(p Plan, key string) (string, bool) {
	for _, e := range p.Edits {
		if e.Section+"."+e.Key == key {
			return e.Value, true
		}
	}
	return "", false
}

func TestWizardInteractive(t *testing.T) {
	s := &script{t: t, answers: map[string][]string{
		"Gateway name":            {"Zoë's Gateway"},
		"Which Bluetooth adapter": {"dongle"},
		"Which interface":         {"wlan0"},
		"Enable the web UI":       {"yes"},
		"Web UI username":         {"zoe"},
		"Web UI password again":   {"mismatch!!", "hunter2hunter2"},
		"Web UI password":         {"hunter2hunter2", "hunter2hunter2"},
		"Use HTTPS":               {"yes"},
		"Pair a Palm now":         {"yes"},
	}}
	plan, err := RunWizard(pi5Report(), nil, Preset{}, s)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"bluetooth.name":    `"Zoë's Gateway"`,
		"bluetooth.adapter": `"60:15:6F:E9:EB:03"`,
		"network.uplink":    `"wlan0"`,
		"web.username":      `"zoe"`,
	} {
		if got, _ := edit(plan, key); got != want {
			t.Errorf("%s = %s, want %s", key, got, want)
		}
	}
	h, ok := edit(plan, "web.password_hash")
	if !ok {
		t.Fatal("no password hash")
	}
	unq := strings.Trim(h, `"`)
	if ok, _ := VerifyPassword("hunter2hunter2", unq); !ok {
		t.Error("hash does not verify")
	}
	if _, ok := edit(plan, "web.enabled"); ok {
		t.Error("web.enabled did not change and must not be written")
	}
	if plan.AdapterMode != "dongle" || !plan.Pair {
		t.Errorf("plan %+v", plan)
	}
	if len(s.notes) == 0 || !strings.Contains(s.notes[len(s.notes)-1], "did not match") {
		t.Errorf("mismatch note: %v", s.notes)
	}
}

func TestWizardUnattended(t *testing.T) {
	r := pi5Report()
	r.Adapters = r.Adapters[:1] // onboard only: no adapter question
	r.Uplinks = r.Uplinks[:1]
	r.Subnet = Subnet{Want: "10.42.0.0/24", Conflicts: []string{"eth0 10.42.0.0/16"}, Suggest: "10.43.0.0/24"}

	// No password and none stored: unattended setup must refuse.
	if _, err := RunWizard(r, nil, Preset{Yes: true}, nil); !errors.Is(err, ErrNeedAnswer) {
		t.Fatalf("want ErrNeedAnswer, got %v", err)
	}
	pw := "unattended-pw"
	plan, err := RunWizard(r, nil, Preset{Yes: true, WebPassword: &pw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"bluetooth.adapter": `"2C:CF:67:2F:29:F5"`,
		"network.subnet":    `"10.43.0.0/24"`,
		"network.gateway":   `"10.43.0.1"`,
		"network.ppp_pool":  `["10.43.0.10", "10.43.0.99"]`,
		"network.dhcp_pool": `["10.43.0.100", "10.43.0.199"]`,
	} {
		if got, _ := edit(plan, key); got != want {
			t.Errorf("%s = %s, want %s", key, got, want)
		}
	}
	if _, ok := edit(plan, "network.uplink"); ok {
		t.Error("uplink eth0 is the default and must not be written")
	}
	if plan.Pair || plan.AdapterMode != "onboard" {
		t.Errorf("plan %+v", plan)
	}

	// Web off needs no password.
	if _, err := RunWizard(r, nil, Preset{Yes: true, Web: "off"}, nil); err != nil {
		t.Fatal(err)
	}
}

// A second run that changes nothing writes nothing (PLAN.md Phase 6 accept).
func TestWizardRerunNoChanges(t *testing.T) {
	cur := config.Default()
	cur.Bluetooth.Adapter = "60:15:6F:E9:EB:03"
	cur.Web.PasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"
	s := &script{t: t, answers: map[string][]string{
		"Gateway name":            {"Palm Gateway"},
		"Which Bluetooth adapter": {"dongle-keep"},
		"Which interface":         {"eth0"},
		"Enable the web UI":       {"yes"},
		"Web UI username":         {"admin"},
		"Keep the current web UI": {"yes"},
		"Use HTTPS":               {"yes"},
		"Pair a Palm now":         {"no"},
	}}
	plan, err := RunWizard(pi5Report(), cur, Preset{}, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Edits) != 0 {
		t.Fatalf("edits on an unchanged re-run: %v", plan.Edits)
	}
	if plan.AdapterMode != "onboard" {
		t.Errorf("dongle-keep leaves the onboard radio on: %+v", plan)
	}
}
