// Command palm-gw is the user-facing entry point: `palm-gw setup` (wizard),
// `palm-gw status`, `palm-gw web`, `palm-gw upgrade` (PLAN.md section 8.11).
// `palm-gw web` arrives in Phase 7.
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
	case "setup":
		return cmdSetup(args[1:], os.Stdout, os.Stderr)
	case "upgrade":
		return cmdUpgrade(args[1:], os.Stdout, os.Stderr)
	case "web":
		return cmdWeb(args[1:], os.Stdin, os.Stdout, os.Stderr)
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
  web        web UI: set-password [--viewer] [--user NAME]
  upgrade    install the latest release (--check only looks)
  version    print version
`)
}
