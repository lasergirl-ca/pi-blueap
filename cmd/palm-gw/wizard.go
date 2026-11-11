package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	palmgw "palm-gw"
	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/setup"
)

// wizardFlags are `palm-gw setup`'s answer flags (PLAN.md section 8.11).
type wizardFlags struct {
	name, adapter, uplink, subnet, web, webUser, webPasswordFile, tls, pair string
	yes, plain, dryRun                                                      bool
}

// preset merges PALM_GW_* variables (bootstrap installs) under the flags.
func (f wizardFlags) preset(getenv func(string) string) (setup.Preset, error) {
	pick := func(flag, env string) string {
		if flag != "" {
			return flag
		}
		return strings.TrimSpace(getenv(env))
	}
	p := setup.Preset{
		Name:    pick(f.name, "PALM_GW_NAME"),
		Adapter: pick(f.adapter, "PALM_GW_ADAPTER"),
		Uplink:  pick(f.uplink, "PALM_GW_UPLINK"),
		Subnet:  pick(f.subnet, "PALM_GW_SUBNET"),
		Web:     onOff(pick(f.web, "PALM_GW_WEB")),
		WebUser: pick(f.webUser, "PALM_GW_WEB_USER"),
		TLS:     pick(f.tls, "PALM_GW_TLS"),
		Pair:    yesNoAnswer(pick(f.pair, "PALM_GW_PAIR")),
		Yes:     f.yes || getenv("PALM_GW_UNATTENDED") == "1",
	}
	switch {
	case f.webPasswordFile != "":
		b, err := os.ReadFile(f.webPasswordFile)
		if err != nil {
			return p, fmt.Errorf("--web-password-file: %w", err)
		}
		pw := strings.TrimRight(string(b), "\r\n")
		p.WebPassword = &pw
	case getenv("PALM_GW_WEB_PASSWORD") != "":
		pw := getenv("PALM_GW_WEB_PASSWORD")
		p.WebPassword = &pw
	}
	return p, nil
}

func onOff(s string) string {
	switch strings.ToLower(s) {
	case "1", "on", "yes", "true":
		return "on"
	case "0", "off", "no", "false":
		return "off"
	}
	return s
}

func yesNoAnswer(s string) string {
	switch strings.ToLower(s) {
	case "1", "on", "yes", "true", "y":
		return "yes"
	case "0", "off", "no", "false", "n":
		return "no"
	}
	return s
}

// runWizard is `palm-gw setup` without --check/--apply/--remove: detect, ask,
// write config.toml, apply, pair, summarise.
func runWizard(f wizardFlags, cfgPath string, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "palm-gw setup: run as root (sudo palm-gw setup)")
		return 1
	}
	pre, err := f.preset(os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "palm-gw setup:", err)
		return 2
	}

	fmt.Fprintln(stdout, "palm-gw setup: looking at this Pi")
	r := setup.LocalHost(bluezAdapters).Detect(cfgPath)
	printReport(stdout, r)
	fmt.Fprintln(stdout)
	if r.ConfigError != "" {
		fmt.Fprintf(stderr, "palm-gw setup: %s does not load: %s\nFix or move it, then run setup again.\n", cfgPath, r.ConfigError)
		return 1
	}

	var p setup.Prompter
	switch {
	case pre.Yes:
		p = notePrinter{stdout}
	case !f.plain && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())):
		p = huhPrompter{out: stdout}
	default:
		p = newPlainPrompter(os.Stdin, stdout)
	}
	plan, err := setup.RunWizard(r, r.Config, pre, p)
	if errors.Is(err, errAborted) {
		fmt.Fprintln(stderr, "palm-gw setup: cancelled; nothing was changed")
		return 130
	}
	if err != nil {
		fmt.Fprintln(stderr, "palm-gw setup:", err)
		return 1
	}

	// Write config.toml: the existing file edited key by key, or the example.
	text := palmgw.ExampleConfig
	if r.ConfigFound {
		b, err := os.ReadFile(cfgPath)
		if err != nil {
			fmt.Fprintln(stderr, "palm-gw setup:", err)
			return 1
		}
		text = string(b)
	}
	newText := setup.SetKeys(text, plan.Edits)
	cfg, err := config.Parse(newText)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		fmt.Fprintf(stderr, "palm-gw setup: the new configuration does not validate (nothing written): %v\n", err)
		return 1
	}
	if f.dryRun {
		fmt.Fprintln(stdout, "==> Dry run: nothing written or applied")
		printLineDiff(stdout, text, newText)
		mode := plan.AdapterMode
		if mode == "" {
			mode = "unchanged"
		}
		fmt.Fprintf(stdout, "    adapter mode: %s, pair at the end: %v\n", mode, plan.Pair)
		return 0
	}
	fmt.Fprintln(stdout, "==> Configuration")
	if newText != text || !r.ConfigFound {
		if err := os.MkdirAll(setup.EtcDir, 0o750); err != nil {
			fmt.Fprintln(stderr, "palm-gw setup:", err)
			return 1
		}
		if err := writeConfig(cfgPath, newText); err != nil {
			fmt.Fprintln(stderr, "palm-gw setup:", err)
			return 1
		}
		for _, e := range plan.Edits {
			v := e.Value
			if e.Key == "password_hash" {
				v = "(new argon2id hash)"
			}
			fmt.Fprintf(stdout, "    %s.%s = %s\n", e.Section, e.Key, v)
		}
		fmt.Fprintf(stdout, "    %s written\n", cfgPath)
	} else {
		fmt.Fprintf(stdout, "    %s unchanged\n", cfgPath)
	}

	adapters, _ := bluezAdapters()
	res, err := setup.ApplySystem(setup.ApplyOptions{
		ConfigPath:  cfgPath,
		Layout:      currentLayout(),
		AdapterMode: plan.AdapterMode,
		Adapters:    adapters,
		Board:       r.Board,
		Version:     version,
		StartDaemon: true,
		Run:         runCombined,
		Out:         stdout,
	})
	if err != nil {
		fmt.Fprintln(stderr, "palm-gw setup:", err)
		return 1
	}

	fingerprint := ""
	if cfg.Web.Enabled && cfg.Web.TLS == "self-signed" {
		fmt.Fprintln(stdout, "==> Web UI certificate")
		fp, created, err := setup.EnsureCert(setup.TLSDir, setup.LocalCertNames(cfg.Network.Gateway, []string{cfg.Network.Uplink}))
		if err != nil {
			fmt.Fprintln(stderr, "palm-gw setup: certificate:", err)
			return 1
		}
		fingerprint = fp
		fmt.Fprintf(stdout, "    %s/cert.pem %s\n", setup.TLSDir, map[bool]string{true: "created", false: "unchanged"}[created])
	}

	paired := ""
	switch {
	case plan.Pair && res.RebootRequired:
		fmt.Fprintln(stdout, "\nThe adapter mode changed, so pair after the reboot: sudo reboot, then palm-gwctl pair")
	case plan.Pair:
		paired = pairNow(cfg, stdout, stderr)
	}

	summary(stdout, cfg, fingerprint, paired, res.RebootRequired)
	return 0
}

// writeConfig writes config.toml atomically, 0640 (it holds the password hash).
func writeConfig(path, text string) error {
	tmp := path + ".palm-gw-new"
	if err := os.WriteFile(tmp, []byte(text), 0o640); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// pairNow opens a one-device pairing window (palm-gwctl pair 5 --one), then
// waits for the Palm to connect so the user sees it has a network before the
// wizard exits (PLAN.md Phase 6 accept). It returns a one-line result.
func pairNow(cfg *config.Config, stdout, stderr io.Writer) string {
	fmt.Fprintln(stdout, "\n==> Pairing")
	fmt.Fprintln(stdout, "On the Palm, add the gateway as a trusted device, then set up a connection to it")
	fmt.Fprintln(stdout, "(docs/PALM-SETUP.md has every step). Ctrl-C skips pairing.")
	if code := ctl.Main([]string{"pair", "5", "--one"}, stdout, stderr, version); code != 0 {
		return "not paired"
	}
	fmt.Fprintln(stdout, "\nNow tap Connect on the Palm. Waiting up to 3 minutes for it to come online...")
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if st, err := gatewayStatus(cfg.Ctl.Socket); err == nil {
			for _, s := range st.Sessions {
				if s.State == "up" && s.Peer != "" {
					name := s.Name
					if name == "" {
						name = s.Addr
					}
					msg := fmt.Sprintf("%s is online at %s (%s)", name, s.Peer, strings.ToUpper(s.Profile))
					fmt.Fprintln(stdout, msg)
					return msg
				}
			}
			for _, c := range st.PAN.Clients {
				if c.IP != "" {
					msg := fmt.Sprintf("%s is online at %s (PAN)", c.Name, c.IP)
					fmt.Fprintln(stdout, msg)
					return msg
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Fprintln(stdout, "No connection yet; the Palm can connect any time (palm-gwctl status shows it).")
	return "paired, not connected yet"
}

func gatewayStatus(socket string) (*ctl.StatusReply, error) {
	c, err := ctl.Dial(socket)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := c.Send(ctl.Request{Cmd: "status"}); err != nil {
		return nil, err
	}
	var st ctl.StatusReply
	if err := c.Next(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

func summary(w io.Writer, cfg *config.Config, fingerprint, paired string, reboot bool) {
	fmt.Fprintln(w, "\npalm-gw is running.")
	if cfg.Web.Enabled {
		host := lanAddress(cfg.Network.Uplink)
		scheme := "https"
		if cfg.Web.TLS == "off" {
			scheme = "http"
		}
		fmt.Fprintf(w, "  Web UI:    %s://%s:8042   (user: %s; the web UI itself arrives in Phase 7)\n", scheme, host, cfg.Web.Username)
		if fingerprint != "" {
			fmt.Fprintf(w, "  Cert:      SHA-256 %s\n", fingerprint)
		}
	}
	fmt.Fprintln(w, "  CLI:       palm-gwctl status")
	fmt.Fprintln(w, "  Pair:      palm-gwctl pair")
	fmt.Fprintln(w, "  Docs:      docs/PALM-SETUP.md")
	if paired != "" {
		fmt.Fprintf(w, "  Palm:      %s\n", paired)
	}
	if reboot {
		fmt.Fprintln(w, "Reboot required: YES (the Bluetooth adapter mode changed)")
	} else {
		fmt.Fprintln(w, "Reboot required: no")
	}
}

// lanAddress is the uplink's first IPv4 address, or the host name.
func lanAddress(iface string) string {
	n := setup.LocalCertNames("", []string{iface})
	if len(n.IPs) > 0 {
		return n.IPs[0].String()
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "<pi>"
}

// notePrinter is the prompter for unattended runs: it never asks.
type notePrinter struct{ out io.Writer }

func (n notePrinter) Note(text string) { fmt.Fprintln(n.out, "  "+text) }
func (notePrinter) Input(title, _, _ string, _ func(string) error) (string, error) {
	return "", fmt.Errorf("%w: %s", setup.ErrNeedAnswer, title)
}
func (notePrinter) Password(title string, _ func(string) error) (string, error) {
	return "", fmt.Errorf("%w: %s", setup.ErrNeedAnswer, title)
}
func (notePrinter) Select(title, _ string, _ []setup.Option, _ string) (string, error) {
	return "", fmt.Errorf("%w: %s", setup.ErrNeedAnswer, title)
}
func (notePrinter) Confirm(title, _ string, _ bool) (bool, error) {
	return false, fmt.Errorf("%w: %s", setup.ErrNeedAnswer, title)
}

// printLineDiff shows the lines that differ (SetKeys keeps line positions,
// adding new keys only after a section's last key, so a positional walk with
// look-ahead for inserted lines is enough).
func printLineDiff(w io.Writer, a, b string) {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	i, j := 0, 0
	for i < len(al) || j < len(bl) {
		switch {
		case i < len(al) && j < len(bl) && al[i] == bl[j]:
			i++
			j++
		case j < len(bl) && (i >= len(al) || (j+1 < len(bl) && al[i] == bl[j+1])):
			fmt.Fprintf(w, "    + %s\n", redactHash(bl[j]))
			j++
		default:
			if i < len(al) {
				fmt.Fprintf(w, "    - %s\n", redactHash(al[i]))
				i++
			}
			if j < len(bl) {
				fmt.Fprintf(w, "    + %s\n", redactHash(bl[j]))
				j++
			}
		}
	}
	if a == b {
		fmt.Fprintln(w, "    config.toml would not change")
	}
}

func redactHash(line string) string {
	if k, _, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "password_hash" && strings.Contains(line, "$argon2id$") {
		return k + "= \"(new argon2id hash)\""
	}
	return line
}
