// Command fake-palm stands in for a Palm handheld from a Linux machine with
// its own Bluetooth adapter (PLAN.md section 9): discover, sdp, pair, connect,
// storm, pan. Phase 0 ships only the command skeleton; the subcommands arrive
// with the server phases they test (Phase 1: discover, sdp, pair; Phase 2:
// connect, storm; Phase 4: pan).
package main

import (
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
	switch args[0] {
	case "version", "-version", "--version":
		fmt.Println("fake-palm", version)
		return 0
	case "discover", "sdp", "pair":
		fmt.Fprintf(os.Stderr, "fake-palm %s: not implemented until Phase 1\n", args[0])
		return 2
	case "connect", "storm":
		fmt.Fprintf(os.Stderr, "fake-palm %s: not implemented until Phase 2\n", args[0])
		return 2
	case "pan":
		fmt.Fprintln(os.Stderr, "fake-palm pan: not implemented until Phase 4")
		return 2
	default:
		fmt.Fprintf(os.Stderr, "fake-palm: unknown command %q\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage: fake-palm <command> [args]

  discover                       inquiry; assert the gateway is visible with its class
  sdp <addr>                     list the gateway's service UUIDs
  pair <addr> --pin NNNN         pair from the client side
  connect <addr> --channel N --mode client|ppp|at [--internet]
  storm <addr> --count N --interval D
  pan <addr>                     connect as PANU and check DHCP, DNS, internet
`)
}
