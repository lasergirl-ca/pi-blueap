package setup

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"palm-gw/internal/config"
)

// The `palm-gw setup` questions (PLAN.md section 8.11). The wizard asks only
// what detection cannot answer, pre-fills from an existing config, and takes
// answers from flags and PALM_GW_* variables first. It produces a Plan: the
// config.toml keys to change and what to do after writing them. Writing and
// applying are the caller's job, so this is testable with a scripted
// Prompter.

// Option is one choice in a Select.
type Option struct {
	Value string
	Label string
}

// Prompter asks the user. The huh-based one is in cmd/palm-gw; a plain
// line-based one serves when there is no terminal.
type Prompter interface {
	Note(text string)
	Input(title, help, def string, validate func(string) error) (string, error)
	Password(title string, validate func(string) error) (string, error)
	Select(title, help string, opts []Option, def string) (string, error)
	Confirm(title, help string, def bool) (bool, error)
}

// Preset holds answers given up front (flags, PALM_GW_* variables). An empty
// field was not given.
type Preset struct {
	Name        string
	Adapter     string // onboard, dongle, or an hciN / address
	Uplink      string
	Subnet      string
	Web         string // on | off
	WebUser     string
	WebPassword *string
	TLS         string // self-signed | off
	Pair        string // yes | no
	// Yes accepts the default for every question not preset and never
	// prompts (unattended installs).
	Yes bool
}

// Plan is the wizard's result.
type Plan struct {
	Edits       []KV
	AdapterMode string // onboard, dongle, or "" to keep
	Pair        bool
	Notes       []string // what was decided without asking
}

// ErrNeedAnswer is returned in unattended mode when a required answer is
// missing.
var ErrNeedAnswer = errors.New("answer required")

type wizard struct {
	r    Report
	cur  *config.Config
	pre  Preset
	p    Prompter
	plan Plan
}

func (w *wizard) note(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	w.plan.Notes = append(w.plan.Notes, msg)
	if w.p != nil {
		w.p.Note(msg)
	}
}

func (w *wizard) set(section, key, value, old string) {
	if value != old {
		w.plan.Edits = append(w.plan.Edits, KV{Section: section, Key: key, Value: value})
	}
}

// RunWizard asks the questions and returns the plan. cur is the existing
// configuration (config.Default() when there is none).
func RunWizard(r Report, cur *config.Config, pre Preset, p Prompter) (Plan, error) {
	if cur == nil {
		cur = config.Default()
	}
	w := &wizard{r: r, cur: cur, pre: pre, p: p}
	for _, step := range []func() error{w.name, w.adapter, w.uplink, w.subnet, w.web, w.pair} {
		if err := step(); err != nil {
			return w.plan, err
		}
	}
	return w.plan, nil
}

func checkName(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 {
		return errors.New("1 to 64 characters")
	}
	return nil
}

func (w *wizard) name() error {
	name := w.cur.Bluetooth.Name
	switch {
	case w.pre.Name != "":
		if err := checkName(w.pre.Name); err != nil {
			return fmt.Errorf("name: %w", err)
		}
		name = w.pre.Name
	case !w.pre.Yes:
		v, err := w.p.Input("Gateway name, as the Palm will see it", "Shown in the Palm's Bluetooth device list.", name, checkName)
		if err != nil {
			return err
		}
		name = strings.TrimSpace(v)
	}
	w.set("bluetooth", "name", TOMLString(name), TOMLString(w.cur.Bluetooth.Name))
	return nil
}

// adapter picks [bluetooth].adapter and the adapter mode. Adapters are pinned
// by address (D-035), because hciN order can change between boots.
func (w *wizard) adapter() error {
	var onboard, dongle *Adapter
	for i := range w.r.Adapters {
		a := &w.r.Adapters[i]
		if a.Onboard() && onboard == nil {
			onboard = a
		}
		if !a.Onboard() && dongle == nil {
			dongle = a
		}
	}
	setAdapter := func(v string) {
		w.set("bluetooth", "adapter", TOMLString(v), TOMLString(w.cur.Bluetooth.Adapter))
	}
	choice := w.pre.Adapter
	if choice == "" {
		switch {
		case onboard == nil && dongle == nil:
			return errors.New("no Bluetooth adapter found; see the warnings from `palm-gw setup --check`")
		case dongle == nil:
			setAdapter(onboard.Address)
			w.plan.AdapterMode = "onboard"
			w.note("Bluetooth: the onboard radio (%s %s)", onboard.HCI, onboard.Address)
			return nil
		case onboard == nil:
			setAdapter(dongle.Address)
			if w.r.DisableBT {
				w.plan.AdapterMode = "dongle"
			}
			w.note("Bluetooth: the USB dongle (%s %s)", dongle.HCI, dongle.Address)
			return nil
		}
		def := "onboard"
		if strings.EqualFold(w.cur.Bluetooth.Adapter, dongle.Address) || w.cur.Bluetooth.Adapter == dongle.HCI {
			def = "dongle-keep"
		}
		if w.pre.Yes {
			choice = def
		} else {
			v, err := w.p.Select("Which Bluetooth adapter should the gateway use?",
				"A USB dongle is the fix when Wi-Fi on the onboard radio disturbs Bluetooth 1.1 Palms.",
				[]Option{
					{"onboard", fmt.Sprintf("Onboard radio (%s %s)", onboard.HCI, onboard.Address)},
					{"dongle-keep", fmt.Sprintf("USB dongle (%s %s), onboard radio stays on", dongle.HCI, dongle.Address)},
					{"dongle", fmt.Sprintf("USB dongle (%s %s), turn the onboard radio off (reboot needed)", dongle.HCI, dongle.Address)},
				}, def)
			if err != nil {
				return err
			}
			choice = v
		}
	}
	switch choice {
	case "onboard":
		if onboard == nil {
			// Disabled by dtoverlay=disable-bt: its address is unknown until
			// it comes back, so let "auto" pick it after the reboot.
			setAdapter("auto")
			w.note("the onboard radio is off now; adapter = \"auto\" until it is back after the reboot")
		} else {
			setAdapter(onboard.Address)
		}
		w.plan.AdapterMode = "onboard"
	case "dongle", "dongle-keep":
		if dongle == nil {
			return errors.New("adapter: no USB dongle is plugged in")
		}
		setAdapter(dongle.Address)
		if choice == "dongle" {
			w.plan.AdapterMode = "dongle"
		} else {
			w.plan.AdapterMode = "onboard"
		}
	default:
		setAdapter(choice) // an explicit hciN or address; the mode stays
	}
	return nil
}

func (w *wizard) uplink() error {
	cur := w.cur.Network.Uplink
	ups := w.r.Uplinks
	choice := w.pre.Uplink
	if choice == "" {
		switch len(ups) {
		case 0:
			w.note("no interface has a default route; keeping uplink = %q", cur)
			return nil
		case 1:
			choice = ups[0].Iface
			w.note("uplink: %s (the only interface with a default route)", choice)
		default:
			def := ups[0].Iface
			for _, u := range ups {
				if u.Iface == cur {
					def = cur
				}
			}
			if w.pre.Yes {
				choice = def
			} else {
				var opts []Option
				for _, u := range ups {
					label := u.Iface + " via " + u.Gateway
					if u.Wireless {
						if u.FreqMHz > 0 {
							label += fmt.Sprintf(" (Wi-Fi %d MHz)", u.FreqMHz)
						} else {
							label += " (Wi-Fi)"
						}
					}
					opts = append(opts, Option{u.Iface, label})
				}
				v, err := w.p.Select("Which interface connects the Pi to the internet?", "Palm traffic is NATed out of this interface.", opts, def)
				if err != nil {
					return err
				}
				choice = v
			}
		}
	}
	w.set("network", "uplink", TOMLString(choice), TOMLString(cur))
	return nil
}

// subnet moves the Palm network when it collides with the host's networks.
func (w *wizard) subnet() error {
	want := w.pre.Subnet
	if want == "" {
		if w.r.Subnet.Free || w.r.Subnet.Suggest == "" {
			return nil
		}
		want = w.r.Subnet.Suggest
		if !w.pre.Yes {
			ok, err := w.p.Confirm(fmt.Sprintf("The Palm network %s overlaps %s. Use %s instead?",
				w.r.Subnet.Want, strings.Join(w.r.Subnet.Conflicts, ", "), want), "", true)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
		} else {
			w.note("Palm network moved to %s (%s overlaps %s)", want, w.r.Subnet.Want, strings.Join(w.r.Subnet.Conflicts, ", "))
		}
	}
	edits, err := SubnetEdits(want, w.cur)
	if err != nil {
		return err
	}
	w.plan.Edits = append(w.plan.Edits, edits...)
	return nil
}

// SubnetEdits renumbers a /24 Palm network: gateway .1, PPP pool .10-.99,
// DHCP pool .100-.199, as the defaults are laid out.
func SubnetEdits(subnet string, cur *config.Config) ([]KV, error) {
	p, err := netip.ParsePrefix(subnet)
	if err != nil || !p.Addr().Is4() || p.Bits() != 24 || p.Masked() != p {
		return nil, fmt.Errorf("subnet %q: want an IPv4 /24 such as 10.43.0.0/24", subnet)
	}
	a := p.Addr().As4()
	ip := func(last byte) string { return netip.AddrFrom4([4]byte{a[0], a[1], a[2], last}).String() }
	var out []KV
	add := func(key, v, old string) {
		if v != old {
			out = append(out, KV{"network", key, v})
		}
	}
	n := cur.Network
	add("subnet", TOMLString(p.String()), TOMLString(n.Subnet))
	add("gateway", TOMLString(ip(1)), TOMLString(n.Gateway))
	add("ppp_pool", TOMLStrings([]string{ip(10), ip(99)}), TOMLStrings(n.PPPPool))
	add("dhcp_pool", TOMLStrings([]string{ip(100), ip(199)}), TOMLStrings(n.DHCPPool))
	return out, nil
}

func (w *wizard) web() error {
	cur := w.cur.Web
	enabled := cur.Enabled
	switch w.pre.Web {
	case "on":
		enabled = true
	case "off":
		enabled = false
	case "":
		if !w.pre.Yes {
			v, err := w.p.Confirm("Enable the web UI?", "Pairing, devices, firewall and settings from a browser on your LAN, and the Palm app (Phase 7).", enabled)
			if err != nil {
				return err
			}
			enabled = v
		}
	default:
		return fmt.Errorf("web: want on or off, got %q", w.pre.Web)
	}
	w.set("web", "enabled", fmt.Sprint(enabled), fmt.Sprint(cur.Enabled))
	if !enabled {
		return nil
	}

	user := cur.Username
	if user == "" {
		user = "admin"
	}
	switch {
	case w.pre.WebUser != "":
		user = w.pre.WebUser
	case !w.pre.Yes:
		v, err := w.p.Input("Web UI username", "", user, func(s string) error {
			if !webUserOK(s) {
				return errors.New("1 to 32 letters, digits, '.', '_' or '-'")
			}
			return nil
		})
		if err != nil {
			return err
		}
		user = v
	}
	if !webUserOK(user) {
		return fmt.Errorf("web username %q: 1 to 32 letters, digits, '.', '_' or '-'", user)
	}
	w.set("web", "username", TOMLString(user), TOMLString(cur.Username))

	var pw string
	switch {
	case w.pre.WebPassword != nil:
		pw = *w.pre.WebPassword
		if err := CheckNewPassword(pw); err != nil {
			return fmt.Errorf("web password: %w", err)
		}
	case cur.PasswordHash != "" && w.pre.Yes:
		w.note("web password: unchanged")
	case cur.PasswordHash != "":
		keep, err := w.p.Confirm("Keep the current web UI password?", "", true)
		if err != nil {
			return err
		}
		if !keep {
			if pw, err = w.askPassword(); err != nil {
				return err
			}
		}
	case w.pre.Yes:
		return fmt.Errorf("%w: a web UI password (PALM_GW_WEB_PASSWORD or --web-password-file), or turn the web UI off (PALM_GW_WEB=off)", ErrNeedAnswer)
	default:
		var err error
		if pw, err = w.askPassword(); err != nil {
			return err
		}
	}
	if pw != "" {
		hash, err := HashPassword(pw)
		if err != nil {
			return err
		}
		w.plan.Edits = append(w.plan.Edits, KV{"web", "password_hash", TOMLString(hash)})
	}

	tls := cur.TLS
	switch w.pre.TLS {
	case "self-signed", "off":
		tls = w.pre.TLS
	case "":
		if !w.pre.Yes && tls != "file" {
			v, err := w.p.Confirm("Use HTTPS with a self-signed certificate?",
				"Recommended. The Palm app and the Bluetooth side need it (D-039); setup prints the fingerprint to pin.", tls != "off")
			if err != nil {
				return err
			}
			tls = map[bool]string{true: "self-signed", false: "off"}[v]
		}
	default:
		return fmt.Errorf("tls: want self-signed or off, got %q", w.pre.TLS)
	}
	w.set("web", "tls", TOMLString(tls), TOMLString(cur.TLS))
	if tls == "off" && cur.BluetoothSide {
		// The Bluetooth side is TLS only (D-039), so without TLS it goes.
		w.note("Without HTTPS the web UI is LAN only: the Bluetooth side (the Palm app) needs TLS.")
		w.set("web", "bluetooth_side", "false", "true")
	}
	return nil
}

var webUserRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)

func webUserOK(s string) bool { return webUserRe.MatchString(s) }

func (w *wizard) askPassword() (string, error) {
	for {
		a, err := w.p.Password("Web UI password", CheckNewPassword)
		if err != nil {
			return "", err
		}
		b, err := w.p.Password("Web UI password again", nil)
		if err != nil {
			return "", err
		}
		if a == b {
			return a, nil
		}
		w.p.Note("The passwords did not match; try again.")
	}
}

func (w *wizard) pair() error {
	switch w.pre.Pair {
	case "yes":
		w.plan.Pair = true
	case "no":
	case "":
		if w.pre.Yes {
			return nil
		}
		v, err := w.p.Confirm("Pair a Palm now?", "Opens a 5-minute pairing window and shows the PIN to enter on the Palm.", true)
		if err != nil {
			return err
		}
		w.plan.Pair = v
	default:
		return fmt.Errorf("pair: want yes or no, got %q", w.pre.Pair)
	}
	return nil
}
