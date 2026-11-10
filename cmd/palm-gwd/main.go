// Command palm-gwd is the Palm Bluetooth Gateway daemon. It loads and
// validates the configuration, sets up logging, and runs the lifecycle in
// internal/daemon: control socket, BlueZ adapter and agent (Phase 1), RFCOMM
// profiles and PPP (Phase 2), firewall (Phase 3), PAN (Phase 4).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	gwdaemon "palm-gw/internal/daemon"
	"palm-gw/internal/logging"
	"palm-gw/internal/setup"
	"palm-gw/internal/state"
)

// version is set by the Makefile: -X main.version=$(VERSION).
var version = "dev"

// phase is the PLAN.md section 10 phase this build has completed.
const phase = "2"

func main() { os.Exit(run(os.Args[1:])) }

func defaultStateDir() string {
	if d := os.Getenv("STATE_DIRECTORY"); d != "" {
		return d
	}
	return state.DefaultDir
}

func run(args []string) int {
	// `palm-gwd ctl ...` is the same as palm-gwctl.
	if len(args) > 0 && args[0] == "ctl" {
		return ctl.Main(args[1:], os.Stdout, os.Stderr, version)
	}

	fs := flag.NewFlagSet("palm-gwd", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultPath, "configuration file")
	stateDir := fs.String("state", defaultStateDir(), "state directory (state.json lives here)")
	check := fs.Bool("check", false, "load and validate the configuration, then exit")
	render := fs.String("render", "", "render this deploy/ template with values from the configuration to stdout, then exit")
	mainConf := fs.String("main-conf", "", "apply the managed block to this /etc/bluetooth/main.conf, then exit (prints changed|unchanged)")
	mainConfRm := fs.String("main-conf-remove", "", "remove the managed block from this main.conf, then exit")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println("palm-gwd", version)
		return 0
	}
	if *mainConfRm != "" {
		changed, err := setup.RemoveFile(*mainConfRm)
		if err != nil {
			fmt.Fprintln(os.Stderr, "palm-gwd:", err)
			return 1
		}
		fmt.Println(map[bool]string{true: "changed", false: "unchanged"}[changed])
		return 0
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "palm-gwd:", err)
		return 1
	}

	if *render != "" {
		text, err := os.ReadFile(*render)
		if err != nil {
			fmt.Fprintln(os.Stderr, "palm-gwd:", err)
			return 1
		}
		out, err := cfg.Render(*render, string(text))
		if err != nil {
			fmt.Fprintln(os.Stderr, "palm-gwd:", err)
			return 1
		}
		fmt.Print(out)
		return 0
	}
	if *mainConf != "" {
		class := cfg.Bluetooth.Class
		if st, err := state.Open(state.Path(*stateDir)); err == nil && st.Class() != 0 {
			class = st.Class() // persisted `class toggle`
		}
		changed, err := setup.ApplyFile(*mainConf, setup.ManagedSettings(cfg, class))
		if err != nil {
			fmt.Fprintln(os.Stderr, "palm-gwd:", err)
			return 1
		}
		fmt.Println(map[bool]string{true: "changed", false: "unchanged"}[changed])
		return 0
	}
	if *check {
		fmt.Printf("%s: configuration is valid\n", *cfgPath)
		for _, w := range cfg.Warnings() {
			fmt.Println("warning:", w)
		}
		return 0
	}

	log := logging.New(cfg.Logging, os.Stderr)
	log.Info("palm-gwd starting", "version", version, "phase", phase, "config", *cfgPath, "state", *stateDir)
	for _, w := range cfg.Warnings() {
		log.Warn(w)
	}
	warnings, err := cfg.CheckRuntime(config.SystemInterfaces)
	if err != nil {
		log.Error("configuration rejected", "err", err)
		fmt.Fprintln(os.Stderr, "palm-gwd:", err)
		return 1
	}
	for _, w := range warnings {
		log.Warn(w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// systemd integration: READY once the agent is registered, WATCHDOG at a
	// third of the configured interval (WatchdogSec=60 gives 20 s, PLAN.md
	// section 8.10). Both are no-ops outside systemd.
	d, err := gwdaemon.New(gwdaemon.Options{
		Config:     cfg,
		ConfigPath: *cfgPath,
		StatePath:  filepath.Join(*stateDir, "state.json"),
		Version:    version,
		Phase:      phase,
		Warnings:   append(cfg.Warnings(), warnings...),
		Log:        log,
		OnReady: func() {
			if _, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
				log.Warn("sd_notify READY failed", "err", err)
			}
			log.Info("palm-gwd ready", "ctl_socket", cfg.Ctl.Socket)
		},
	})
	if err != nil {
		log.Error("startup failed", "err", err)
		fmt.Fprintln(os.Stderr, "palm-gwd:", err)
		return 1
	}
	if interval, err := daemon.SdWatchdogEnabled(false); err == nil && interval > 0 {
		go func() {
			t := time.NewTicker(interval / 3)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					_, _ = daemon.SdNotify(false, daemon.SdNotifyWatchdog)
				}
			}
		}()
	}

	err = d.Run(ctx)
	_, _ = daemon.SdNotify(false, daemon.SdNotifyStopping)
	if err != nil {
		log.Error("palm-gwd failed", "err", err)
		fmt.Fprintln(os.Stderr, "palm-gwd:", err)
		return 1
	}
	return 0
}
