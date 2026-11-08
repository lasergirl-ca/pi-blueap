// Command palm-gw is the user-facing entry point: `palm-gw setup` (wizard),
// `palm-gw status`, `palm-gw web`, `palm-gw upgrade` (PLAN.md section 8.11).
// Phase 0 ships `status` (an alias for palm-gwctl status) and stubs for the
// rest, which arrive in Phases 6 and 7.
package main

import (
	"fmt"
	"os"

	"palm-gw/internal/ctl"
)

var version = "dev"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "status":
		return ctl.Main(append([]string{"status"}, args[1:]...), os.Stdout, os.Stderr, version)
	case "version", "-version", "--version":
		fmt.Println("palm-gw", version)
		return 0
	case "setup", "upgrade":
		fmt.Fprintf(os.Stderr, "palm-gw %s: not implemented until Phase 6 (setup wizard and packaging)\n", args[0])
		return 2
	case "web":
		fmt.Fprintln(os.Stderr, "palm-gw web: not implemented until Phase 7 (web UI)")
		return 2
	default:
		fmt.Fprintf(os.Stderr, "palm-gw: unknown command %q\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage: palm-gw <command>

  setup      interactive setup wizard (also flag-driven for unattended installs)
  status     gateway status (same as palm-gwctl status)
  web        web UI management: set-password
  upgrade    install the latest release
  version    print version
`)
}
