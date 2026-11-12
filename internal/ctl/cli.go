package ctl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"palm-gw/internal/config"
	"palm-gw/internal/diag"
	"palm-gw/internal/setup"
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
	if len(args) > 0 && args[0] == "upgrade" {
		// An alias for `palm-gw upgrade` (PLAN.md section 8.11), with its flags.
		return execPalmGW(args, stderr)
	}
	fs := flag.NewFlagSet("palm-gwctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", DefaultSocket, "control socket path")
	asJSON := fs.Bool("json", false, "print the raw JSON reply")
	one := fs.Bool("one", false, "pair: close the window after one device pairs")
	comment := fs.String("comment", "", "firewall add: a comment for the rule")
	var nc setup.NetworkChange
	fs.StringVar(&nc.Uplink, "uplink", "", "network: the interface Palm traffic goes out of")
	fs.StringVar(&nc.Subnet, "subnet", "", "network: the Palm network, an IPv4 /24")
	fs.StringVar(&nc.TLS, "tls", "", "network: web UI HTTPS, self-signed or off")
	btSide := fs.String("bluetooth-side", "", "network: web UI over Bluetooth, on or off")
	hidden := fs.Bool("hidden", false, "wifi join: the network does not broadcast its name")
	evFilter := fs.String("event", "", "logs: only this event (a prefix ending in * matches several)")
	grep := fs.String("grep", "", "logs: only lines containing this text or address")
	units := fs.String("unit", "", "logs: palm-gwd, bluetooth, dnsmasq, bridge, apply (comma-separated)")
	follow := fs.Bool("f", false, "logs: keep printing new lines")
	withCapture := fs.Bool("with-capture", false, "bundle: add the newest btmon capture (it can hold link keys)")
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
	case "rename":
		if len(cmdArgs) < 1 || !btAddrRe.MatchString(cmdArgs[0]) {
			fmt.Fprintln(stderr, "palm-gwctl rename: expected <addr> [label] (no label clears it)")
			return 2
		}
		return c.rename(cmdArgs[0], strings.Join(cmdArgs[1:], " "))
	case "events":
		recent := 0
		if len(cmdArgs) > 0 {
			n, err := strconv.Atoi(cmdArgs[0])
			if err != nil || n < 0 {
				fmt.Fprintf(stderr, "palm-gwctl events: the number of past events must be 0 or more, got %q\n", cmdArgs[0])
				return 2
			}
			recent = n
		}
		return c.events(recent)
	case "unpair":
		return c.pairClose()
	case "get":
		if len(cmdArgs) > 1 {
			fmt.Fprintln(stderr, "palm-gwctl get: expected [section.key]")
			return 2
		}
		key := ""
		if len(cmdArgs) == 1 {
			key = cmdArgs[0]
		}
		return c.get(key)
	case "set":
		if len(cmdArgs) < 2 {
			fmt.Fprintln(stderr, "palm-gwctl set: expected <section.key> <value>")
			return 2
		}
		return c.set(cmdArgs[0], cliValue(strings.Join(cmdArgs[1:], " ")))
	case "firewall":
		return c.firewall(cmdArgs, *comment)
	case "restart":
		return c.restart()
	case "choices":
		return c.choices()
	case "nat":
		return c.nat()
	case "internet":
		if len(cmdArgs) == 2 && cmdArgs[0] == "use" {
			return c.internetUse(cmdArgs[1])
		}
		if len(cmdArgs) != 0 {
			fmt.Fprintln(stderr, "palm-gwctl internet [use ethernet|wifi]")
			return 2
		}
		return c.internet()
	case "wifi":
		return c.wifi(cmdArgs, *hidden)
	case "network":
		switch *btSide {
		case "":
		case "on", "off":
			v := *btSide == "on"
			nc.BluetoothSide = &v
		default:
			fmt.Fprintln(stderr, "palm-gwctl network: --bluetooth-side wants on or off")
			return 2
		}
		if nc == (setup.NetworkChange{}) {
			return c.choices()
		}
		return c.network(nc)
	case "logs":
		n := 50
		if len(cmdArgs) > 0 {
			v, err := strconv.Atoi(cmdArgs[0])
			if err != nil || v < 1 {
				fmt.Fprintln(stderr, "palm-gwctl logs [n] [--event E] [--grep TEXT] [--unit U,…] [-f]")
				return 2
			}
			n = v
		}
		var us []string
		if *units != "" {
			us = strings.Split(*units, ",")
		}
		return c.logs(LogsRequest{Units: us, Lines: n, Event: *evFilter, Text: *grep}, *follow)
	case "capture":
		secs := 60
		if len(cmdArgs) > 0 {
			v, err := strconv.Atoi(cmdArgs[0])
			if err != nil || v < 1 || v > 600 {
				fmt.Fprintln(stderr, "palm-gwctl capture [seconds, 1 to 600]")
				return 2
			}
			secs = v
		}
		return c.capture(secs)
	case "check":
		return c.check()
	case "bundle":
		return c.bundle(*withCapture)
	case "class":
		if len(cmdArgs) != 1 || cmdArgs[0] != "toggle" {
			fmt.Fprintln(stderr, "palm-gwctl class toggle")
			return 2
		}
		return c.classToggle()
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
  unpair                      close the pairing window, whoever opened it
  rename <addr> [label]       set a local label for a device (no label clears it)
  reload                      re-read config, re-apply firewall and dnsmasq
  capture [seconds]           record a btmon trace (default 60) into /var/lib/palm-gw/captures
  logs [n] [--event E] [--grep TEXT] [--unit U,…] [-f]
                              the journal of palm-gwd, bluetooth and dnsmasq, decoded
  events [n]                  stream daemon events, after the last n past ones
  get [key] / set <key> <v>   read or change a config value (validated, then reloaded)
  firewall list               the [[firewall.allow]] rules, numbered
  firewall add <host> <proto/port>... [--comment TEXT]
  firewall rm <n>             remove rule n
  firewall counters           packets the drop rules have stopped
  firewall test <host> <port> TCP connect from the Pi
  restart                     restart palm-gwd
  nat                         the NAT table: connections Palms have open now
  internet                    how the Pi reaches the internet: Ethernet, Wi-Fi, saved networks
  internet use ethernet|wifi  make it the internet connection (restarts palm-gwd if it changes)
  wifi scan                   Wi-Fi networks in range
  wifi join <name> [--hidden] join a network (asks for its password) and use it for the internet
  wifi forget <name>          delete a saved network
  wifi on|off                 the Wi-Fi radio
  choices / network           adapters, uplinks and the Palm network check, and the last apply
  network [--uplink IF] [--subnet A.B.C.0/24] [--tls self-signed|off] [--bluetooth-side on|off]
                              change them; runs palm-gw setup --apply, which restarts palm-gwd
  check                       health report
  class toggle                swap Class of Device with class_fallback
  bundle [--with-capture]     write a support bundle (hashes redacted) into /var/lib/palm-gw/bundles
  upgrade [--check]           install the latest release (runs palm-gw upgrade)

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
		fmt.Fprintf(w, "network:   %s gw %s uplink %s\n", cfg.Network.Subnet, cfg.Network.Gateway, cfg.Network.Uplink)
	}
	switch p := st.PAN; {
	case !p.Enabled:
		fmt.Fprintf(w, "pan:       off ([pan].enabled = false)\n")
	case p.Error != "":
		fmt.Fprintf(w, "pan:       NAP NOT REGISTERED on %s: %s\n", p.Bridge, p.Error)
	case !p.Registered:
		fmt.Fprintf(w, "pan:       NAP not registered yet (bridge %s)\n", p.Bridge)
	default:
		pool := ""
		if st.Config != nil {
			pool = fmt.Sprintf(", DHCP %s-%s", st.Config.Network.DHCPPool[0], st.Config.Network.DHCPPool[1])
		}
		fmt.Fprintf(w, "pan:       NAP on %s since %s, %d client(s)%s\n", p.Bridge, p.Since.Format("15:04:05"), len(p.Clients), pool)
	}
	for _, c := range st.PAN.Clients {
		ip := c.IP
		if ip == "" {
			ip = "(no lease yet)"
		}
		fmt.Fprintf(w, "  %s  %-20q pan %-6s %-14s up %s\n", c.Addr, c.Name, c.Ifname, ip, time.Since(c.Since).Round(time.Second))
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
	if r.DroppedPAN {
		fmt.Fprint(c.stdout, ", PAN connection dropped")
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
	switch {
	case r.Sessions == 0 && !r.PAN:
		fmt.Fprintf(c.stdout, "%s: no session\n", r.Address)
		return 1
	case r.PAN && r.Sessions == 0:
		fmt.Fprintf(c.stdout, "%s: PAN connection dropped\n", r.Address)
	case r.PAN:
		fmt.Fprintf(c.stdout, "%s: %d session(s) terminated, PAN connection dropped\n", r.Address, r.Sessions)
	default:
		fmt.Fprintf(c.stdout, "%s: %d session(s) terminated\n", r.Address, r.Sessions)
	}
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

// execPalmGW replaces this process with palm-gw, found next to this binary
// (both layouts put the binaries in one directory) or else on PATH.
func execPalmGW(args []string, stderr io.Writer) int {
	path := ""
	if exe, err := os.Executable(); err == nil {
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			exe = p
		}
		if cand := filepath.Join(filepath.Dir(exe), "palm-gw"); fileExists(cand) {
			path = cand
		}
	}
	if path == "" {
		p, err := exec.LookPath("palm-gw")
		if err != nil {
			fmt.Fprintln(stderr, "palm-gwctl upgrade: palm-gw not found")
			return 1
		}
		path = p
	}
	err := syscall.Exec(path, append([]string{"palm-gw"}, args...), os.Environ())
	fmt.Fprintf(stderr, "palm-gwctl upgrade: %v\n", err)
	return 1
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func (c *cli) rename(addr, label string) int {
	var r RenameReply
	printed, err := c.call(Request{Cmd: "rename", Addr: addr, Label: label}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	if r.Label == "" {
		fmt.Fprintf(c.stdout, "%s: label cleared\n", r.Address)
	} else {
		fmt.Fprintf(c.stdout, "%s: label %q\n", r.Address, r.Label)
	}
	return 0
}

func (c *cli) pairClose() int {
	var r PairCloseReply
	printed, err := c.call(Request{Cmd: "pair.close"}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	if r.WasOpen {
		fmt.Fprintln(c.stdout, "Pairing window closed.")
	} else {
		fmt.Fprintln(c.stdout, "No pairing window was open.")
	}
	return 0
}

// events streams the daemon's events until Ctrl-C, one line each (the JSON
// line itself with --json).
func (c *cli) events(recent int) int {
	conn, err := Dial(c.socket)
	if err != nil {
		return c.fail(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Send(Request{Cmd: "events", Recent: recent}); err != nil {
		return c.fail(err)
	}
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
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "use of closed network connection") {
				return 0
			}
			return c.fail(err)
		}
		if c.json {
			fmt.Fprintln(c.stdout, string(raw))
			continue
		}
		var e struct {
			Seq    uint64            `json:"seq"`
			Time   time.Time         `json:"time"`
			Event  string            `json:"event"`
			Addr   string            `json:"addr"`
			Name   string            `json:"name"`
			Fields map[string]string `json:"fields"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return c.fail(err)
		}
		line := fmt.Sprintf("%s  %-20s", e.Time.Local().Format("15:04:05"), e.Event)
		if e.Addr != "" {
			line += "  " + e.Addr
		}
		if e.Name != "" {
			line += fmt.Sprintf("  %q", e.Name)
		}
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == "pin" {
				continue // never echo a PIN into a scrolling log
			}
			line += fmt.Sprintf("  %s=%s", k, e.Fields[k])
		}
		fmt.Fprintln(c.stdout, line)
	}
}

// cliValue turns a command-line value into JSON: anything that already is
// JSON (5, true, ["a","b"], "quoted") as it is, anything else as a string.
func cliValue(v string) json.RawMessage {
	if json.Valid([]byte(v)) {
		return json.RawMessage(v)
	}
	b, _ := json.Marshal(v)
	return b
}

func (c *cli) get(key string) int {
	var r ConfigValue
	printed, err := c.call(Request{Cmd: "config.get", Path: key}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	var v any
	_ = json.Unmarshal(r.Value, &v)
	if s, ok := v.(string); ok {
		fmt.Fprintln(c.stdout, s)
		return 0
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(c.stdout, string(b))
	return 0
}

func (c *cli) set(key string, value json.RawMessage) int {
	var r ConfigSetReply
	printed, err := c.call(Request{Cmd: "config.set", Path: key, Value: value}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	fmt.Fprintf(c.stdout, "%s saved.\n", key)
	c.printReload(r.Reload)
	return 0
}

func (c *cli) printReload(r ReloadReply) {
	if len(r.Applied) > 0 {
		fmt.Fprintf(c.stdout, "applied now: %s\n", strings.Join(r.Applied, ", "))
	}
	if len(r.NeedsRestart) > 0 {
		fmt.Fprintf(c.stdout, "takes effect when palm-gwd restarts: %s (palm-gwctl restart)\n", strings.Join(r.NeedsRestart, ", "))
	}
	for _, e := range r.Errors {
		fmt.Fprintln(c.stdout, "error:", e)
	}
}

func (c *cli) allowRules() ([]config.AllowRule, error) {
	var r ConfigValue
	if err := Call(c.socket, Request{Cmd: "config.get", Path: "firewall.allow"}, &r); err != nil {
		return nil, err
	}
	var rules []config.AllowRule
	if err := json.Unmarshal(r.Value, &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

var portRe = regexp.MustCompile(`^(tcp|udp)/[0-9]{1,5}$|^icmp$`)

func (c *cli) firewall(args []string, comment string) int {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		rules, err := c.allowRules()
		if err != nil {
			return c.fail(err)
		}
		if c.json {
			b, _ := json.Marshal(rules)
			printJSON(c.stdout, b)
			return 0
		}
		if len(rules) == 0 {
			fmt.Fprintln(c.stdout, "No [[firewall.allow]] rules: Palms reach the internet and nothing on the LAN.")
		}
		for i, r := range rules {
			line := fmt.Sprintf("%2d  %-18s %s", i+1, r.Host, strings.Join(r.Ports, " "))
			if r.Comment != "" {
				line += "  # " + r.Comment
			}
			fmt.Fprintln(c.stdout, line)
		}
		return 0
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(c.stderr, "palm-gwctl firewall add: expected <host> <proto/port>... [--comment TEXT]")
			return 2
		}
		for _, p := range args[1:] {
			if !portRe.MatchString(p) {
				fmt.Fprintf(c.stderr, "palm-gwctl firewall add: %q is not tcp/N, udp/N or icmp\n", p)
				return 2
			}
		}
		rules, err := c.allowRules()
		if err != nil {
			return c.fail(err)
		}
		rules = append(rules, config.AllowRule{Host: args[0], Ports: args[1:], Comment: comment})
		b, _ := json.Marshal(rules)
		return c.set("firewall.allow", b)
	case "rm":
		if len(args) != 1 {
			fmt.Fprintln(c.stderr, "palm-gwctl firewall rm: expected the rule's number from firewall list")
			return 2
		}
		rules, err := c.allowRules()
		if err != nil {
			return c.fail(err)
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 || n > len(rules) {
			fmt.Fprintf(c.stderr, "palm-gwctl firewall rm: no rule %q (there are %d)\n", args[0], len(rules))
			return 2
		}
		rules = append(rules[:n-1], rules[n:]...)
		b, _ := json.Marshal(rules)
		return c.set("firewall.allow", b)
	case "counters":
		var r CountersReply
		printed, err := c.call(Request{Cmd: "firewall.counters"}, &r)
		if err != nil {
			return c.fail(err)
		}
		if printed {
			return 0
		}
		for _, x := range r.Counters {
			fmt.Fprintf(c.stdout, "%-14s %-26s %8d packets %10d bytes\n", x.Chain, x.Comment, x.Packets, x.Bytes)
		}
		return 0
	case "test":
		if len(args) != 2 {
			fmt.Fprintln(c.stderr, "palm-gwctl firewall test: expected <host> <port>")
			return 2
		}
		port, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(c.stderr, "palm-gwctl firewall test: port %q is not a number\n", args[1])
			return 2
		}
		var r TestReply
		printed, err := c.call(Request{Cmd: "firewall.test", Host: args[0], Port: port}, &r)
		if err != nil {
			return c.fail(err)
		}
		if printed {
			return 0
		}
		if r.OK {
			fmt.Fprintf(c.stdout, "%s:%d answers from the Pi (%d ms).", r.Host, r.Port, r.Millis)
		} else {
			fmt.Fprintf(c.stdout, "%s:%d does not answer from the Pi: %s.", r.Host, r.Port, r.Problem)
		}
		if r.Allowed {
			fmt.Fprintln(c.stdout, " An allow rule lets Palms reach it.")
		} else {
			fmt.Fprintln(c.stdout, " No allow rule covers it, so Palms cannot reach it.")
		}
		return 0
	}
	fmt.Fprintf(c.stderr, "palm-gwctl firewall: unknown subcommand %q (list, add, rm, counters, test)\n", sub)
	return 2
}

func (c *cli) restart() int {
	var r OKReply
	printed, err := c.call(Request{Cmd: "restart"}, &r)
	if err != nil {
		return c.fail(err)
	}
	if !printed {
		fmt.Fprintln(c.stdout, "palm-gwd is restarting.")
	}
	return 0
}

func (c *cli) choices() int {
	var r ChoicesReply
	printed, err := c.call(Request{Cmd: "choices"}, &r)
	if err != nil || printed {
		if err != nil {
			return c.fail(err)
		}
		return 0
	}
	var st StatusReply
	_ = Call(c.socket, Request{Cmd: "status"}, &st)
	cfg := st.Config
	fmt.Fprintln(c.stdout, "Bluetooth adapters:")
	if len(r.Adapters) == 0 {
		fmt.Fprintln(c.stdout, "  none (is bluetooth.service running?)")
	}
	for _, a := range r.Adapters {
		mark := ""
		if strings.EqualFold(a.Address, r.AdapterIn) {
			mark = "  <- in use"
		}
		fmt.Fprintf(c.stdout, "  %s%s\n", a.Label(), mark)
	}
	fmt.Fprintf(c.stdout, "Internet connections (uplink = %q):\n", cfg.Network.Uplink)
	if len(r.Uplinks) == 0 {
		fmt.Fprintln(c.stdout, "  none has a default route")
	}
	for _, u := range r.Uplinks {
		mark := ""
		if u.Iface == cfg.Network.Uplink {
			mark = "  <- in use"
		}
		fmt.Fprintf(c.stdout, "  %s%s\n", u.Label(), mark)
	}
	fmt.Fprintf(c.stdout, "Palm network: %s, gateway %s", cfg.Network.Subnet, cfg.Network.Gateway)
	if s := r.Subnet; s != nil && !s.Free {
		fmt.Fprintf(c.stdout, " (overlaps %s; %s is free)", strings.Join(s.Conflicts, ", "), s.Suggest)
	}
	fmt.Fprintln(c.stdout)
	side := "off"
	if cfg.Web.BluetoothSide {
		side = "on"
	}
	fmt.Fprintf(c.stdout, "Web UI: tls = %s, Bluetooth side %s\n", cfg.Web.TLS, side)
	switch a := r.LastApply; {
	case r.Applying:
		fmt.Fprintln(c.stdout, "Applying network settings now.")
	case a == nil:
	case a.OK:
		fmt.Fprintf(c.stdout, "Last network apply: OK, %s\n", a.Finished.Local().Format("2006-01-02 15:04:05"))
	default:
		fmt.Fprintf(c.stdout, "Last network apply FAILED, %s: %s\n", a.Finished.Local().Format("2006-01-02 15:04:05"), a.Error)
	}
	return 0
}

func (c *cli) network(nc setup.NetworkChange) int {
	var r NetworkSetReply
	printed, err := c.call(Request{Cmd: "network.set", Network: &nc}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	if !r.Applying {
		fmt.Fprintln(c.stdout, "Nothing changed.")
		return 0
	}
	fmt.Fprintf(c.stdout, "Saved %s.\n", strings.Join(r.Changed, ", "))
	fmt.Fprintf(c.stdout, "Applying with palm-gw setup --apply (journalctl -u %s); palm-gwd restarts when it is done.\n", r.Unit)
	return 0
}

func (c *cli) nat() int {
	var r NATReply
	printed, err := c.call(Request{Cmd: "nat"}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	if len(r.Entries) == 0 {
		fmt.Fprintf(c.stdout, "No Palm has a connection open now (%d other connections tracked).\n", r.Total)
		return 0
	}
	hp := func(a fmt.Stringer, p uint16) string {
		if p == 0 {
			return a.String()
		}
		return a.String() + ":" + strconv.Itoa(int(p))
	}
	fmt.Fprintf(c.stdout, "%-18s %-5s %-21s %-21s %-21s %-12s %s\n", "DEVICE", "PROTO", "FROM", "TO", "OUT AS", "STATE", "TIMEOUT")
	for _, e := range r.Entries {
		dev, out, state := e.Device, "-", e.State
		if dev == "" {
			dev = "unknown"
		}
		if e.NATAddr.IsValid() {
			out = hp(e.NATAddr, e.NATPort)
		}
		if state == "" {
			state = map[bool]string{true: "answered", false: "no answer"}[e.Replied]
		}
		fmt.Fprintf(c.stdout, "%-18s %-5s %-21s %-21s %-21s %-12s %ds\n", dev, e.Proto, hp(e.Src, e.SrcPort), hp(e.Dst, e.DstPort), out, state, e.Timeout)
	}
	fmt.Fprintf(c.stdout, "%d from %s, of %d tracked.\n", len(r.Entries), r.Subnet, r.Total)
	return 0
}

func (c *cli) internetStatus() (InternetReply, bool, error) {
	var r InternetReply
	printed, err := c.call(Request{Cmd: "internet"}, &r)
	return r, printed, err
}

func (c *cli) internet() int {
	r, printed, err := c.internetStatus()
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	if r.Error != "" {
		fmt.Fprintln(c.stdout, "NetworkManager:", r.Error)
	}
	for _, d := range r.Devices {
		mark := ""
		if d.Iface == r.Uplink {
			mark = "  <- the internet connection"
		}
		switch {
		case d.Type == "ethernet" && !d.Carrier:
			fmt.Fprintf(c.stdout, "Ethernet %s: no cable%s\n", d.Iface, mark)
		case d.Connected:
			fmt.Fprintf(c.stdout, "%s %s: %s, %s via %s%s\n", kindName(d.Type), d.Iface, d.Connection, d.IP, d.Gateway, mark)
		default:
			fmt.Fprintf(c.stdout, "%s %s: %s%s\n", kindName(d.Type), d.Iface, d.State, mark)
		}
	}
	if !r.WifiHW {
		fmt.Fprintln(c.stdout, "Wi-Fi radio: blocked by a hardware switch (rfkill)")
	} else if !r.WifiOn {
		fmt.Fprintln(c.stdout, "Wi-Fi radio: off (palm-gwctl wifi on)")
	}
	if len(r.Saved) > 0 {
		fmt.Fprintf(c.stdout, "Saved Wi-Fi networks: %s\n", strings.Join(r.Saved, ", "))
	}
	if j := r.Job; j != nil {
		switch {
		case !j.Done:
			fmt.Fprintf(c.stdout, "Joining %q now.\n", j.SSID)
		case j.OK:
			fmt.Fprintf(c.stdout, "Last join: %q, OK.\n", j.SSID)
		default:
			fmt.Fprintf(c.stdout, "Last join: %q failed: %s\n", j.SSID, j.Error)
		}
	}
	return 0
}

func kindName(t string) string {
	if t == "wifi" {
		return "Wi-Fi"
	}
	return "Ethernet"
}

func (c *cli) internetUse(kind string) int {
	var r InternetUseReply
	printed, err := c.call(Request{Cmd: "internet.use", WiFi: &WifiRequest{Use: kind}}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	if r.Switching {
		fmt.Fprintf(c.stdout, "The internet connection is now %s. palm-gw setup --apply is switching the firewall; palm-gwd restarts.\n", r.Iface)
	} else {
		fmt.Fprintf(c.stdout, "The internet connection is %s.\n", r.Iface)
	}
	return 0
}

func (c *cli) wifi(args []string, hidden bool) int {
	if len(args) == 0 {
		fmt.Fprintln(c.stderr, "palm-gwctl wifi scan | join <name> [--hidden] | forget <name> | on | off")
		return 2
	}
	switch args[0] {
	case "scan":
		var r WifiScanReply
		printed, err := c.call(Request{Cmd: "wifi.scan", WiFi: &WifiRequest{Rescan: true}}, &r)
		if err != nil {
			return c.fail(err)
		}
		if printed {
			return 0
		}
		if len(r.Networks) == 0 {
			fmt.Fprintln(c.stdout, "No Wi-Fi networks in range.")
		}
		for _, n := range r.Networks {
			var notes []string
			if n.Active {
				notes = append(notes, "connected")
			}
			if n.Saved {
				notes = append(notes, "saved")
			}
			if !n.Supported() {
				notes = append(notes, "not supported here")
			}
			fmt.Fprintf(c.stdout, "%-32s %3d%%  %-7s %-10s %s\n", n.SSID, n.Signal, n.Band(), n.Security, strings.Join(notes, ", "))
		}
		return 0
	case "join":
		if len(args) != 2 {
			fmt.Fprintln(c.stderr, "palm-gwctl wifi join <name> [--hidden]")
			return 2
		}
		pw, err := readSecret(c.stderr, "Wi-Fi password (Enter for an open or saved network): ")
		if err != nil {
			return c.fail(err)
		}
		var ok OKReply
		if _, err := c.call(Request{Cmd: "wifi.join", WiFi: &WifiRequest{SSID: args[1], Password: pw, Hidden: hidden}}, &ok); err != nil {
			return c.fail(err)
		}
		fmt.Fprintf(c.stdout, "Joining %q", args[1])
		for i := 0; i < 120; i++ {
			time.Sleep(time.Second)
			r, _, err := c.internetStatus()
			if err != nil {
				// palm-gwd restarting after the uplink switch
				fmt.Fprintln(c.stdout, "\nJoined; palm-gwd is restarting to switch the internet connection to Wi-Fi.")
				return 0
			}
			if j := r.Job; j != nil && j.Done {
				if !j.OK {
					fmt.Fprintf(c.stdout, "\n%s\n", j.Error)
					return 1
				}
				if j.Switching {
					fmt.Fprintln(c.stdout, "\nJoined. The internet connection is now Wi-Fi; palm-gwd restarts.")
				} else {
					fmt.Fprintln(c.stdout, "\nJoined.")
				}
				return 0
			}
			fmt.Fprint(c.stdout, ".")
		}
		fmt.Fprintln(c.stdout, "\nStill joining; see palm-gwctl internet.")
		return 1
	case "forget":
		if len(args) != 2 {
			fmt.Fprintln(c.stderr, "palm-gwctl wifi forget <name>")
			return 2
		}
		var ok OKReply
		if _, err := c.call(Request{Cmd: "wifi.forget", WiFi: &WifiRequest{SSID: args[1]}}, &ok); err != nil {
			return c.fail(err)
		}
		fmt.Fprintf(c.stdout, "Forgot %q.\n", args[1])
		return 0
	case "on", "off":
		on := args[0] == "on"
		var ok OKReply
		if _, err := c.call(Request{Cmd: "wifi.radio", WiFi: &WifiRequest{On: &on}}, &ok); err != nil {
			return c.fail(err)
		}
		fmt.Fprintf(c.stdout, "Wi-Fi radio %s.\n", args[0])
		return 0
	}
	fmt.Fprintf(c.stderr, "palm-gwctl wifi: unknown %q\n", args[0])
	return 2
}

// readSecret reads a line without echo from a terminal, or one line from a
// pipe.
func readSecret(prompt io.Writer, text string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(prompt, text)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(prompt)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *cli) printEntries(es []diag.Entry) {
	for _, e := range es {
		line := e.Time.Local().Format("Jan 02 15:04:05") + " " + fmt.Sprintf("%-9s %-5s ", e.Unit, e.Level)
		if e.Event != "" {
			line += "[" + e.Event + "] "
		}
		line += e.Msg
		if e.Addr != "" {
			line += " addr=" + e.Addr
		}
		if f := e.FieldList(); len(f) > 0 {
			line += " " + strings.Join(f, " ")
		}
		fmt.Fprintln(c.stdout, line)
	}
}

func (c *cli) logs(req LogsRequest, follow bool) int {
	var r LogsReply
	printed, err := c.call(Request{Cmd: "logs", Logs: &req}, &r)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	c.printEntries(r.Entries)
	for follow {
		time.Sleep(2 * time.Second)
		req.After, req.Lines = r.Cursor, 1000
		if err := Call(c.socket, Request{Cmd: "logs", Logs: &req}, &r); err != nil {
			return c.fail(err)
		}
		c.printEntries(r.Entries)
	}
	return 0
}

func (c *cli) capture(secs int) int {
	var st CaptureState
	printed, err := c.call(Request{Cmd: "btmon", Seconds: secs}, &st)
	if err != nil {
		return c.fail(err)
	}
	if printed {
		return 0
	}
	fmt.Fprintf(c.stdout, "Capturing for %d s into %s", secs, st.Name)
	for {
		time.Sleep(time.Second)
		var d DiagnosticsReply
		if err := Call(c.socket, Request{Cmd: "diagnostics"}, &d); err != nil {
			return c.fail(err)
		}
		if !d.Capture.Running {
			fmt.Fprintln(c.stdout)
			if d.Capture.Error != "" {
				return c.fail(errors.New(d.Capture.Error))
			}
			fmt.Fprintf(c.stdout, "Wrote /var/lib/palm-gw/captures/%s (open it in Wireshark).\n", d.Capture.Name)
			return 0
		}
		fmt.Fprint(c.stdout, ".")
	}
}

func (c *cli) check() int {
	var r CheckReply
	printed, err := c.call(Request{Cmd: "check"}, &r)
	if err != nil || printed {
		if err != nil {
			return c.fail(err)
		}
		return 0
	}
	fmt.Fprintf(c.stdout, "board:    %s (%s)\n", r.Board.ID, r.Board.Model)
	for _, a := range r.Adapters {
		fmt.Fprintf(c.stdout, "adapter:  %s, powered %s\n", a.Label(), yn(a.Powered))
	}
	if r.AdapterError != "" {
		fmt.Fprintf(c.stdout, "adapter:  %s\n", r.AdapterError)
	}
	for _, u := range r.Uplinks {
		fmt.Fprintf(c.stdout, "uplink:   %s\n", u.Label())
	}
	for _, u := range r.Units {
		fmt.Fprintf(c.stdout, "unit:     %-20s %s\n", u.Name, u.Active)
	}
	for _, m := range r.Modules {
		fmt.Fprintf(c.stdout, "module:   %-20s %s\n", m.Name, map[bool]string{true: "loaded", false: "NOT LOADED"}[m.Loaded])
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(c.stdout, "WARNING:  %s\n", w)
	}
	return 0
}

func (c *cli) bundle(withCapture bool) int {
	var r BundleReply
	printed, err := c.call(Request{Cmd: "bundle", WithCapture: withCapture}, &r)
	if err != nil {
		return c.fail(err)
	}
	if !printed {
		fmt.Fprintf(c.stdout, "Wrote %s (%d bytes). Password hashes are redacted.\n", r.Path, r.Size)
	}
	return 0
}

func (c *cli) classToggle() int {
	var r ClassReply
	printed, err := c.call(Request{Cmd: "class.toggle"}, &r)
	if err != nil {
		return c.fail(err)
	}
	if !printed {
		fmt.Fprintf(c.stdout, "Class of Device %s -> %s. palm-gw setup --apply rewrites main.conf and restarts bluetooth and palm-gwd.\n", r.Previous, r.Class)
	}
	return 0
}
