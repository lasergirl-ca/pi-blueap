package ctl

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"
)

// Main is the palm-gwctl command line. It is shared by cmd/palm-gwctl and by
// `palm-gwd ctl ...`. It returns the process exit status.
func Main(args []string, stdout, stderr io.Writer, version string) int {
	fs := flag.NewFlagSet("palm-gwctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", DefaultSocket, "control socket path")
	asJSON := fs.Bool("json", false, "print the raw JSON reply")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() { usage(stderr) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "palm-gwctl", version)
		return 0
	}
	rest := fs.Args()
	if len(rest) == 0 {
		usage(stderr)
		return 2
	}
	// Allow flags after the command too: `palm-gwctl status --json`,
	// `palm-gw status -socket /tmp/x.sock`.
	cmd := rest[0]
	if err := fs.Parse(rest[1:]); err != nil {
		return 2
	}

	switch cmd {
	case "status":
		return cmdStatus(*socket, *asJSON, stdout, stderr)
	case "pair", "forget", "allow":
		fmt.Fprintf(stderr, "palm-gwctl %s: not implemented until Phase 1 (adapter and agent)\n", cmd)
		return 2
	case "disconnect":
		fmt.Fprintf(stderr, "palm-gwctl %s: not implemented until Phase 2 (RFCOMM profiles and PPP)\n", cmd)
		return 2
	case "reload":
		fmt.Fprintf(stderr, "palm-gwctl %s: not implemented until Phase 3 (routing, NAT and isolation)\n", cmd)
		return 2
	case "rename", "events", "capture", "get", "set", "firewall", "check", "class", "bundle", "upgrade":
		fmt.Fprintf(stderr, "palm-gwctl %s: not implemented until Phase 6 or 7\n", cmd)
		return 2
	default:
		fmt.Fprintf(stderr, "palm-gwctl: unknown command %q\n", cmd)
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: palm-gwctl [--socket PATH] [--json] <command> [args]

Commands:
  status                      daemon, adapter and session state
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

func cmdStatus(socket string, asJSON bool, stdout, stderr io.Writer) int {
	if asJSON {
		conn, err := Dial(socket)
		if err != nil {
			fmt.Fprintln(stderr, "palm-gwctl:", err)
			return 1
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if err := conn.Send(Request{Cmd: "status"}); err != nil {
			fmt.Fprintln(stderr, "palm-gwctl:", err)
			return 1
		}
		raw, err := conn.NextRaw()
		if err != nil {
			fmt.Fprintln(stderr, "palm-gwctl:", err)
			return 1
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err != nil {
			fmt.Fprintln(stdout, string(raw))
		} else {
			fmt.Fprintln(stdout, pretty.String())
		}
		return 0
	}

	var st StatusReply
	if err := Call(socket, Request{Cmd: "status"}, &st); err != nil {
		fmt.Fprintln(stderr, "palm-gwctl:", err)
		return 1
	}
	up := time.Duration(st.UptimeSeconds) * time.Second
	fmt.Fprintf(stdout, "palm-gwd %s, up %s (phase %s)\n", st.Version, up, st.Phase)
	fmt.Fprintf(stdout, "config:    %s\n", st.ConfigPath)
	if st.Config != nil {
		c := st.Config
		fmt.Fprintf(stdout, "adapter:   %s  name %q  class 0x%06X\n", c.Bluetooth.Adapter, c.Bluetooth.Name, c.Bluetooth.Class)
		fmt.Fprintf(stdout, "profiles:  spp ch%d %s, lap ch%d %s, dun ch%d %s, pan %s (%s)\n",
			c.SPP.Channel, onOff(c.SPP.Enabled), c.LAP.Channel, onOff(c.LAP.Enabled),
			c.DUN.Channel, onOff(c.DUN.Enabled), onOff(c.PAN.Enabled), c.PAN.Bridge)
		fmt.Fprintf(stdout, "network:   %s gw %s uplink %s  ppp %s-%s  dhcp %s-%s\n",
			c.Network.Subnet, c.Network.Gateway, c.Network.Uplink,
			c.Network.PPPPool[0], c.Network.PPPPool[1], c.Network.DHCPPool[0], c.Network.DHCPPool[1])
		fmt.Fprintf(stdout, "allowlist: %d static, allow_any=%t\n", len(c.Bluetooth.Allow), c.Bluetooth.AllowAny)
	}
	for _, w := range st.Warnings {
		fmt.Fprintf(stdout, "warning:   %s\n", w)
	}
	return 0
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
