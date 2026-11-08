// Command palm-gwd is the Palm Bluetooth Gateway daemon. Phase 0 (scaffold):
// it loads and validates the configuration, sets up logging, serves the
// control socket with `status`, and integrates with systemd (READY, watchdog).
// BlueZ, pppd and nftables arrive in later phases.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/logging"
)

// version is set by the Makefile: -X main.version=$(VERSION).
var version = "dev"

// phase is the PLAN.md section 10 phase this build has completed.
const phase = "0"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	// `palm-gwd ctl ...` is the same as palm-gwctl.
	if len(args) > 0 && args[0] == "ctl" {
		return ctl.Main(args[1:], os.Stdout, os.Stderr, version)
	}

	fs := flag.NewFlagSet("palm-gwd", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultPath, "configuration file")
	check := fs.Bool("check", false, "load and validate the configuration, then exit")
	render := fs.String("render", "", "render this deploy/ template with values from the configuration to stdout, then exit")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println("palm-gwd", version)
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
	if *check {
		fmt.Printf("%s: configuration is valid\n", *cfgPath)
		for _, w := range cfg.Warnings() {
			fmt.Println("warning:", w)
		}
		return 0
	}

	log := logging.New(cfg.Logging, os.Stderr)
	log.Info("palm-gwd starting", "version", version, "phase", phase, "config", *cfgPath)
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
	allWarnings := append(cfg.Warnings(), warnings...)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	started := time.Now()
	srv := ctl.NewServer(cfg.Ctl.Socket, cfg.Ctl.Group, log)
	srv.Handle("status", func(_ context.Context, _ ctl.Request, w *ctl.Writer) error {
		return w.Send(ctl.StatusReply{
			Version:       version,
			Phase:         phase,
			StartedAt:     started,
			UptimeSeconds: int64(time.Since(started).Seconds()),
			ConfigPath:    *cfgPath,
			Warnings:      allWarnings,
			Config:        cfg.Redacted(),
		})
	})
	if err := srv.Listen(); err != nil {
		log.Error("control socket", "err", err)
		return 1
	}
	go srv.Serve(ctx)

	// systemd integration: READY once we serve, WATCHDOG at a third of the
	// configured interval (WatchdogSec=60 in the unit gives 20 s, as PLAN.md
	// section 8.10 asks). Both are no-ops outside systemd.
	if _, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		log.Warn("sd_notify READY failed", "err", err)
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
	log.Info("palm-gwd ready", "ctl_socket", cfg.Ctl.Socket)

	<-ctx.Done()
	log.Info("palm-gwd stopping")
	_, _ = daemon.SdNotify(false, daemon.SdNotifyStopping)
	srv.Close()
	return 0
}
