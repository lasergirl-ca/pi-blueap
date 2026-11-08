// Command palm-gwctl is the operator CLI for palm-gwd's control socket
// (PLAN.md section 8.8). The implementation lives in internal/ctl so that
// `palm-gwd ctl ...` behaves identically.
package main

import (
	"os"

	"palm-gw/internal/ctl"
)

var version = "dev"

func main() { os.Exit(ctl.Main(os.Args[1:], os.Stdout, os.Stderr, version)) }
