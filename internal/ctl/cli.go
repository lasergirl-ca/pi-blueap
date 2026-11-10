package ctl

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var btAddrRe = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

// parseInterleaved parses flags wherever they appear, so `palm-gwctl pair 5
// --one` and `palm-gwctl status -socket /tmp/x.sock` both work. The standard
// flag package stops at the first positional argument; this resumes after
// each one. A literal "--" ends flag parsing as usual.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rem := fs.Args()
		consumed := len(args) - len(rem)
		if consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rem...), nil
		}
		if len(rem) == 0 {
			return pos, nil
		}
		pos = append(pos, rem[0])
		args = rem[1:]
	}
}

// Main is the palm-gwctl command line. It is shared by cmd/palm-gwctl and by
// `palm-gwd ctl ...`. It returns the process exit status.
func Main(args []string, stdout, stderr io.Writer, version string) int {
	fs := flag.NewFlagSet("palm-gwctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", DefaultSocket, "control socket path")
	asJSON := fs.Bool("json", false, "print the raw JSON reply")
	one := fs.Bool("one", false, "pair: close the window after one device pairs")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() { usage(stderr) }
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "palm-gwctl", version)
		return 0
	}
	if len(rest) == 0 {
		usage(stderr)
		return 2
	}
	cmd, cmdArgs := rest[0], rest[1:]
	c := &cli{socket: *socket, json: *asJSON, stdout: stdout, stderr: stderr}

	switch cmd {
	case "status":
		return c.status()
	case "pair":
		minutes := 0
		if len(cmdArgs) > 0 {
			n, err := strconv.Atoi(cmdArgs[0])
			if err != nil || n < 1 {
				fmt.Fprintf(stderr, "palm-gwctl pair: minutes must be a positive integer, got %q\n", cmdArgs[0])
				return 2
			}
			minutes = n
		}
		return c.pair(minutes, *one)
	case "forget", "allow":
		if len(cmdArgs) != 1 || !btAddrRe.MatchString(cmdArgs[0]) {
			fmt.Fprintf(stderr, "palm-gwctl %s: expected one Bluetooth address XX:XX:XX:XX:XX:XX\n", cmd)
			return 2
		}
		if cmd == "forget" {
			return c.forget(cmdArgs[0])
		}
		return c.allow(cmdArgs[0])
	case "disconnect":
		if len(cmdArgs) != 1 || !btAddrRe.MatchString(cmdArgs[0]) {
			fmt.Fprintf(stderr, "palm-gwctl %s: expected one Bluetooth address XX:XX:XX:XX:XX:XX\n", cmd)
			return 2
		}
		return c.disconnect(cmdArgs[0])
	case "reload":
		if len(cmdArgs) != 0 {
			fmt.Fprintf(stderr, "palm-gwctl %s: takes no arguments\n", cmd)
			return 2
		}
		return c.reload()
	case "rename", "events", "capture", "get", "set", "firewall", "check", "class", "bundle", "upgrade":
		fmt.Fprintf(stderr, "palm-gwctl %s: not implemented until Phase 6 or 7\n", cmd)
		return 2
	default:
		fmt.Fprintf(stderr, "palm-gwctl: unknown command %q\n", cmd)
		usage(stderr)
		return 2
	}
}

type cli struct {
	socket string
	json   bool
	stdout io.Writer
	stderr io.Writer
}

func (c *cli) fail(err error) int {
	fmt.Fprintln(c.stderr, "palm-gwctl:", err)
	return 1
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: palm-gwctl [--socket PATH] [--json] <command> [args]

Commands:
  status                      daemon, adapter, pairing window, allowlist, devices
  pair [minutes] [--one]      open a pairing window and print the PIN
  disconnect <addr>           tear down a device's session
  forget <addr>               remove a device from the allowlist and from BlueZ
  allow <addr>                allowlist a device paired some other way
  rename <addr> <label>       set a local label for a device
  reload                      re-read config, re-apply firewall and dnsmasq
  capture [seconds]           record a btmon trace
  events                      stream daemon events
  get <key> / set <key> <v>   read or change a config value
  firewall add|rm ...         edit [[firewall.allow]]
  check                       health report
  class toggle                swap Class of Device with class_fallback
  bundle                      write a support bundle
  upgrade [--check]           install the latest release

Every command accepts --json.
`)
}

// call sends one request; with --json it prints the raw reply and returns
// true so the caller stops.
func (c *cli) call(req Request, reply any) (printed bool, err error) {
	if !c.json {
		return false, Call(c.socket, req, reply)
	}
	conn, err := Dial(c.socket)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := conn.Send(req); err != nil {
		return false, err
	}
	raw, err := conn.NextRaw()
	if err != nil {
		return false, err
	}
	printJSON(c.stdout, raw)
	return true, nil
}

func printJSON(w io.Writer, raw json.RawMessage) {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		fmt.Fprintln(w, string(raw))
		return
	}
	fmt.Fprintln(w, pretty.String())
}

func (c *cli) status() int {
	var st StatusReply
	printed, err := c.call(Request{Cmd: "status"}, &st)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	w := c.stdout
	up := time.Duration(st.UptimeSeconds) * time.Second
	fmt.Fprintf(w, "palm-gwd %s, up %s (phase %s)\n", st.Version, up, st.Phase)
	switch {
	case !st.BlueZ.Present:
		fmt.Fprintf(w, "bluez:     NOT RUNNING (lost %d times)\n", st.BlueZ.Losses)
	case !st.BlueZ.AgentRegistered:
		fmt.Fprintf(w, "bluez:     present, agent not registered yet\n")
	default:
		fmt.Fprintf(w, "bluez:     ok, agent registered since %s", st.BlueZ.Since.Local().Format("15:04:05"))
		if st.BlueZ.Losses > 0 {
			fmt.Fprintf(w, " (recovered %d times)", st.BlueZ.Losses)
		}
		fmt.Fprintln(w)
	}
	if a := st.Adapter; a != nil {
		bus := a.HWBus
		if bus == "" {
			bus = "unknown bus"
		}
		fmt.Fprintf(w, "adapter:   %s %s (%s)  alias %q  class 0x%06X  powered=%s discoverable=%s pairable=%s  (%s)\n",
			a.HCI, a.Address, bus, a.Alias, a.Class, yn(a.Powered), yn(a.Discoverable), yn(a.Pairable), a.Selection)
	} else if st.Config != nil {
		fmt.Fprintf(w, "adapter:   %s (not found yet)\n", st.Config.Bluetooth.Adapter)
	}
	if pw := st.PairingWindow; pw.Open {
		fmt.Fprintf(w, "pairing:   OPEN, PIN %s, closes in %s", spaced(pw.PIN), time.Until(pw.Expires).Round(time.Second))
		if pw.One {
			fmt.Fprint(w, ", closes after one device")
		}
		fmt.Fprintln(w)
	} else {
		fmt.Fprintln(w, "pairing:   closed (palm-gwctl pair to open)")
	}
	fmt.Fprintf(w, "allowlist: %d entries", len(st.Allowlist))
	if st.AllowAny {
		fmt.Fprint(w, ", allow_any=true (any paired device may connect)")
	}
	fmt.Fprintln(w)
	for _, e := range st.Allowlist {
		label := e.Name
		if e.Label != "" {
			label = e.Label
		}
		fmt.Fprintf(w, "  %s  %-8s %s\n", e.Address, e.Source, label)
	}
	if len(st.Devices) > 0 {
		fmt.Fprintf(w, "bluez devices: %d\n", len(st.Devices))
		for _, d := range st.Devices {
			flags := []string{}
			if d.Paired {
				flags = append(flags, "paired")
			}
			if d.Trusted {
				flags = append(flags, "trusted")
			}
			if d.Connected {
				flags = append(flags, "connected")
			}
			fmt.Fprintf(w, "  %s  %-20q class 0x%06X  %s\n", d.Address, d.DisplayName(), d.Class, strings.Join(flags, ","))
		}
	}
	if len(st.Profiles) > 0 {
		parts := make([]string, 0, len(st.Profiles))
		for _, p := range st.Profiles {
			switch {
			case !p.Enabled:
				parts = append(parts, fmt.Sprintf("%s off", p.Kind))
			case p.Registered:
				parts = append(parts, fmt.Sprintf("%s ch%d", p.Kind, p.Channel))
			default:
				parts = append(parts, fmt.Sprintf("%s ch%d NOT REGISTERED", p.Kind, p.Channel))
			}
		}
		fmt.Fprintf(w, "profiles:  %s\n", strings.Join(parts, ", "))
	}
	if st.Config != nil {
		cfg := st.Config
		fmt.Fprintf(w, "sessions:  %d of %d, pool %d of %s-%s in use\n", len(st.Sessions), cfg.PPP.MaxSessions,
			st.PoolInUse, cfg.Network.PPPPool[0], cfg.Network.PPPPool[1])
	}
	for _, s := range st.Sessions {
		ifname := s.Ifname
		if ifname == "" {
			ifname = "-"
		}
		peer := s.Peer
		if peer == "" {
			peer = "-"
		}
		fmt.Fprintf(w, "  %s  %-20q %s %-6s %-9s %-14s %s  up %s\n", s.Addr, s.Name, s.Profile, ifname, s.State, peer,
			s.Handshake, time.Since(s.Started).Round(time.Second))
	}
	if st.Config != nil {
		cfg := st.Config
		fmt.Fprintf(w, "network:   %s gw %s uplink %s, pan %s (%s)   [PAN: Phase 4]\n", cfg.Network.Subnet, cfg.Network.Gateway,
			cfg.Network.Uplink, onOff(cfg.PAN.Enabled), cfg.PAN.Bridge)
	}
	switch f := st.Firewall; {
	case !f.Managed:
		fmt.Fprintf(w, "firewall:  not managed (firewall.manage_nftables = false)\n")
	case f.Error != "":
		fmt.Fprintf(w, "firewall:  ERROR %s\n", f.Error)
	default:
		fmt.Fprintf(w, "firewall:  %d allow rule(s) (%d port, %d icmp), %d palm_to_pi, applied %s\n", f.AllowRules, f.LANPorts,
			f.LANICMP, f.PalmToPi, f.AppliedAt.Format("15:04:05"))
	}
	for _, wn := range st.Warnings {
		fmt.Fprintf(w, "warning:   %s\n", wn)
	}
	return 0
}

func (c *cli) pair(minutes int, one bool) int {
	conn, err := Dial(c.socket)
	if err != nil {
		return c.fail(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Send(Request{Cmd: "pair", Minutes: minutes, One: one}); err != nil {
		return c.fail(err)
	}
	// Ctrl-C closes the connection, which cancels the window on the daemon.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		<-sigs
		_ = conn.Close()
	}()

	for {
		raw, err := conn.NextRaw()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			var rerr *RemoteError
			if errors.As(err, &rerr) {
				return c.fail(rerr)
			}
			if strings.Contains(err.Error(), "use of closed network connection") {
				fmt.Fprintln(c.stdout, "Cancelled; pairing window closed.")
				return 0
			}
			return c.fail(err)
		}
		if c.json {
			fmt.Fprintln(c.stdout, string(raw))
			continue
		}
		var ev PairEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			fmt.Fprintln(c.stdout, string(raw))
			continue
		}
		switch ev.Event {
		case "window_open":
			fmt.Fprintf(c.stdout, "Pairing window open for %s.\n", time.Until(ev.Expires).Round(time.Second))
			fmt.Fprintln(c.stdout, "On the Palm: Prefs > Communication > Bluetooth > Setup Devices > Trusted Devices > Add Device,")
			fmt.Fprintln(c.stdout, "then tap the gateway when it appears in the list.")
			fmt.Fprintf(c.stdout, "When asked for a passkey, enter:   %s\n", spaced(ev.PIN))
			fmt.Fprintln(c.stdout, "Waiting... (Ctrl-C closes the window)")
		case "pin_requested":
			fmt.Fprintf(c.stdout, "  %s  %-16q PIN requested\n", ev.Addr, ev.Name)
		case "paired":
			fmt.Fprintf(c.stdout, "  %s  %-16q paired, added to allowlist\n", ev.Addr, ev.Name)
		case "pairing_rejected":
			fmt.Fprintf(c.stdout, "  %s  %-16q rejected (%s)\n", ev.Addr, ev.Name, ev.Reason)
		case "passkey", "confirm":
			fmt.Fprintf(c.stdout, "  %s  %-16q passkey %s (SSP; confirm on the device)\n", ev.Addr, ev.Name, ev.Passkey)
		case "display_pin":
			fmt.Fprintf(c.stdout, "  %s  %-16q device shows PIN %s\n", ev.Addr, ev.Name, ev.PIN)
		case "window_closed":
			reason := map[string]string{"one_device": "one device", "timeout": "timed out", "cancelled": "cancelled", "bluez_lost": "BlueZ went away"}[ev.Reason]
			if reason == "" {
				reason = ev.Reason
			}
			fmt.Fprintf(c.stdout, "Window closed (%s).\n", reason)
			return 0
		default:
			fmt.Fprintf(c.stdout, "  %s %s %s\n", ev.Event, ev.Addr, ev.Name)
		}
	}
}

func (c *cli) forget(addr string) int {
	var r ForgetReply
	printed, err := c.call(Request{Cmd: "forget", Addr: addr}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	fmt.Fprintf(c.stdout, "%s: allowlist %s, BlueZ device %s", r.Address, didnt(r.RemovedAllow, "removed", "not present"), didnt(r.RemovedBlueZ, "removed", "not known"))
	if r.KilledSession {
		fmt.Fprint(c.stdout, ", session terminated")
	}
	fmt.Fprintln(c.stdout)
	if r.StaticEntry {
		fmt.Fprintf(c.stdout, "note: %s is also listed in [bluetooth].allow in config.toml and stays allowed until removed there\n", r.Address)
	}
	return 0
}

func (c *cli) disconnect(addr string) int {
	var r DisconnectReply
	printed, err := c.call(Request{Cmd: "disconnect", Addr: addr}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	if r.Sessions == 0 {
		fmt.Fprintf(c.stdout, "%s: no session\n", r.Address)
		return 1
	}
	fmt.Fprintf(c.stdout, "%s: %d session(s) terminated\n", r.Address, r.Sessions)
	return 0
}

func (c *cli) reload() int {
	var r ReloadReply
	printed, err := c.call(Request{Cmd: "reload"}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		if r.OK {
			return 0
		}
		return 1
	}
	if len(r.Applied) == 0 {
		fmt.Fprintln(c.stdout, "reloaded: no live settings changed")
	} else {
		fmt.Fprintf(c.stdout, "reloaded: %s\n", strings.Join(r.Applied, ", "))
	}
	if r.DnsmasqRestarted {
		fmt.Fprintln(c.stdout, "dnsmasq configuration rewritten and dnsmasq restarted")
	}
	if len(r.NeedsRestart) > 0 {
		fmt.Fprintf(c.stdout, "not applied until palm-gwd restarts (sudo systemctl restart palm-gwd): %s\n",
			strings.Join(r.NeedsRestart, ", "))
		for _, k := range r.NeedsRestart {
			switch k {
			case "network.subnet", "network.gateway", "network.uplink", "network.mss_clamp", "pan.bridge":
				fmt.Fprintf(c.stdout, "%s is also rendered into the nftables and bridge files: re-run the installer\n", k)
			}
		}
	}
	for _, e := range r.Errors {
		fmt.Fprintf(c.stderr, "error: %s\n", e)
	}
	if !r.OK {
		return 1
	}
	return 0
}

func (c *cli) allow(addr string) int {
	var r AllowReply
	printed, err := c.call(Request{Cmd: "allow", Addr: addr}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	if r.Added {
		fmt.Fprintf(c.stdout, "%s added to the allowlist\n", r.Address)
	} else {
		fmt.Fprintf(c.stdout, "%s was already in the allowlist\n", r.Address)
	}
	return 0
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func didnt(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// spaced renders a PIN as "4 8 2 1" so it is easy to read off a screen.
func spaced(pin string) string {
	return strings.Join(strings.Split(pin, ""), " ")
}
