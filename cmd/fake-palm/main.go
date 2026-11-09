// Command fake-palm stands in for a Palm handheld from a Linux machine with
// its own Bluetooth adapter (PLAN.md section 9). Phase 1 ships discover, sdp
// and pair; connect and storm arrive with Phase 2, pan with Phase 4.
package main

import (
	"flag"
	"fmt"
	"os"
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
		if err := fs.Parse(rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: fake-palm sdp <addr>")
			return 2
		}
		return cmdSDP(*adapter, fs.Arg(0))
	case "pair":
		fs := flag.NewFlagSet("fake-palm pair", flag.ContinueOnError)
		pin := fs.String("pin", "", "PIN to answer RequestPinCode with (from palm-gwctl pair)")
		adapter := fs.String("adapter", "", "client adapter hciN (default: first)")
		if err := fs.Parse(rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: fake-palm pair <addr> --pin NNNN")
			return 2
		}
		return cmdPair(*adapter, fs.Arg(0), *pin)
	case "connect", "storm":
		fmt.Fprintf(os.Stderr, "fake-palm %s: not implemented until Phase 2\n", cmd)
		return 2
	case "pan":
		fmt.Fprintln(os.Stderr, "fake-palm pan: not implemented until Phase 4")
		return 2
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
  pair <addr> --pin NNNN                   pair from the client side answering the PIN
  connect <addr> --channel N --mode client|ppp|at [--internet]   (Phase 2)
  storm <addr> --count N --interval D                            (Phase 2)
  pan <addr>                                                     (Phase 4)

Runs on Linux with BlueZ; the client adapter is the first hciN unless --adapter is given.
`)
}
