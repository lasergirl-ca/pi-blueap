// Command fake-palm stands in for a Palm handheld from a Linux machine with
// its own Bluetooth adapter (PLAN.md section 9). Phase 1 ships discover, sdp
// and pair, Phase 2 connect, storm and probe, Phase 4 pan.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

var version = "dev"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "-version", "--version":
		fmt.Println("fake-palm", version)
		return 0
	case "discover":
		fs := flag.NewFlagSet("fake-palm discover", flag.ContinueOnError)
		secs := fs.Int("seconds", 12, "how long to run inquiry")
		expect := fs.String("expect", "", "fail unless a device with this name is found")
		adapter := fs.String("adapter", "", "client adapter hciN (default: first)")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		return cmdDiscover(*adapter, *secs, *expect)
	case "sdp":
		fs := flag.NewFlagSet("fake-palm sdp", flag.ContinueOnError)
		adapter := fs.String("adapter", "", "client adapter hciN (default: first)")
		if err := parseAnywhere(fs, rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: fake-palm sdp <addr>")
			return 2
		}
		return cmdSDP(*adapter, fs.Arg(0))
	case "pair":
		fs := flag.NewFlagSet("fake-palm pair", flag.ContinueOnError)
		pin := fs.String("pin", "", "PIN to answer RequestPinCode with (legacy pairing); empty = SSP numeric comparison")
		adapter := fs.String("adapter", "", "client adapter hciN (default: auto)")
		if err := parseAnywhere(fs, rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: fake-palm pair <addr> [--pin NNNN]")
			return 2
		}
		return cmdPair(*adapter, fs.Arg(0), *pin)
	case "connect":
		fs := flag.NewFlagSet("fake-palm connect", flag.ContinueOnError)
		var o connectOpts
		fs.StringVar(&o.adapter, "adapter", "", "client adapter hciN (default: auto)")
		fs.IntVar(&o.channel, "channel", 1, "RFCOMM channel: 1 SPP, 2 LAP, 3 DUN")
		fs.StringVar(&o.mode, "mode", "client", "client (CLIENT/CLIENTSERVER), ppp (straight to PPP), at (AT dial) or at-tx (the Palm T|X dial sequence and timing)")
		fs.StringVar(&o.netns, "netns", "", "run the client pppd in this network namespace (needed on the gateway host itself)")
		fs.BoolVar(&o.internet, "internet", false, "also fetch --url through the link")
		fs.StringVar(&o.url, "url", defaultURL, "plain-HTTP page that --internet fetches")
		fs.BoolVar(&o.verbose, "verbose", false, "print the client pppd's log")
		fs.DurationVar(&o.hold, "hold", 0, "keep the link up this long after the checks (to test palm-gwctl disconnect)")
		fs.DurationVar(&o.upTimeout, "up-timeout", 20*time.Second, "how long to wait for IPCP")
		if err := parseAnywhere(fs, rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: fake-palm connect <addr> --channel N --mode client|ppp|at|at-tx [--netns NS] [--internet [--url U]] [--hold D]")
			return 2
		}
		o.addr = fs.Arg(0)
		return cmdConnect(o)
	case "storm":
		fs := flag.NewFlagSet("fake-palm storm", flag.ContinueOnError)
		var o stormOpts
		fs.StringVar(&o.adapter, "adapter", "", "client adapter hciN (default: auto)")
		fs.IntVar(&o.count, "count", 20, "iterations")
		fs.DurationVar(&o.interval, "interval", 500*time.Millisecond, "pause between iterations")
		fs.StringVar(&o.netns, "netns", "", "network namespace for the client pppd")
		fs.StringVar(&o.ctlSocket, "ctl", "", "gateway control socket to check afterwards (same host only)")
		chans := fs.String("channels", "1,2,3", "RFCOMM channels to rotate through")
		if err := parseAnywhere(fs, rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: fake-palm storm <addr> [--count N] [--interval D] [--netns NS] [--ctl SOCKET]")
			return 2
		}
		for _, c := range strings.Split(*chans, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(c))
			if err != nil || n < 1 || n > 30 {
				fmt.Fprintf(os.Stderr, "fake-palm storm: bad channel %q\n", c)
				return 2
			}
			o.channels = append(o.channels, n)
		}
		o.addr = fs.Arg(0)
		return cmdStorm(o)
	case "probe":
		fs := flag.NewFlagSet("fake-palm probe", flag.ContinueOnError)
		gw := fs.String("gateway", "10.42.0.1", "gateway address to ping and use for DNS")
		internet := fs.Bool("internet", false, "also fetch --url")
		url := fs.String("url", defaultURL, "plain-HTTP page that --internet fetches")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		return probe(*gw, *internet, *url)
	case "pan":
		fs := flag.NewFlagSet("fake-palm pan", flag.ContinueOnError)
		var o panOpts
		fs.StringVar(&o.adapter, "adapter", "", "client adapter hciN (default: auto)")
		fs.StringVar(&o.netns, "netns", "", "move the bnep interface into this network namespace (needed on the gateway host itself)")
		fs.BoolVar(&o.internet, "internet", false, "also fetch --url through the link")
		fs.StringVar(&o.url, "url", defaultURL, "plain-HTTP page that --internet fetches")
		fs.StringVar(&o.pool, "pool", "10.42.0.100-10.42.0.199", "the lease must fall in this range ([network].dhcp_pool)")
		fs.DurationVar(&o.hold, "hold", 0, "keep the connection up this long after the checks")
		if err := parseAnywhere(fs, rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: fake-palm pan <addr> [--netns NS] [--internet [--url U]] [--pool FIRST-LAST] [--hold D]")
			return 2
		}
		o.addr = fs.Arg(0)
		return cmdPan(o)
	default:
		fmt.Fprintf(os.Stderr, "fake-palm: unknown command %q\n", cmd)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage: fake-palm <command> [args]

  discover [--seconds N] [--expect NAME]   inquiry; list devices with class and name
  sdp <addr>                               list the gateway's service UUIDs (connects to trigger SDP)
  pair <addr> [--pin NNNN]                 pair from the client side (legacy PIN, or SSP without --pin)
  connect <addr> --channel N --mode client|ppp|at|at-tx [--netns NS] [--internet [--url U]] [--hold D] [--verbose]
                                           RFCOMM, handshake, client pppd, then ping, DNS, HTTP
  storm <addr> [--count N] [--interval D] [--channels 1,2,3] [--netns NS] [--ctl SOCKET]
                                           rapid connect/hang-up; asserts the gateway cleans up
  probe [--gateway IP] [--internet [--url U]]
                                           the link checks on their own (connect runs them in --netns)
  pan <addr> [--netns NS] [--internet [--url U]] [--pool FIRST-LAST] [--hold D]
                                           PAN (PANU to the gateway's NAP), DHCP, then ping, DNS, HTTP

Runs on Linux with BlueZ as root. The client adapter is picked like adapter = "auto"
(a USB dongle if there is one) unless --adapter is given. When the gateway runs on the
same host, use the dongle and --netns so the client's PPP or bnep interface lives apart.
pan needs busybox (udhcpc).
`)
}

// parseAnywhere lets flags follow the positional address
// (`fake-palm connect 2C:CF:67:2F:29:F5 --channel 1`).
func parseAnywhere(fs *flag.FlagSet, args []string) error {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		rem := fs.Args()
		if len(rem) == 0 {
			break
		}
		pos = append(pos, rem[0])
		args = rem[1:]
	}
	return fs.Parse(append([]string{"--"}, pos...))
}
