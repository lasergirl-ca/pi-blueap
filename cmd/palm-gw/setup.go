package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"palm-gw/internal/bluez"
	"palm-gw/internal/setup"
)

const defaultConfigPath = "/etc/palm-gw/config.toml"

// bluezAdapters lists controllers through BlueZ for detection.
func bluezAdapters() ([]setup.Adapter, error) {
	bus, err := bluez.Connect(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = bus.Close() }()
	if ok, err := bus.HasBlueZ(); err != nil || !ok {
		return nil, fmt.Errorf("bluetooth.service is not running (org.bluez has no owner)")
	}
	as, err := bus.Adapters()
	if err != nil {
		return nil, err
	}
	out := make([]setup.Adapter, 0, len(as))
	for _, a := range as {
		out = append(out, setup.Adapter{HCI: a.HCI, Address: a.Props.Address, Bus: a.HWBus, Powered: a.Props.Powered})
	}
	return out, nil
}

func cmdSetup(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("palm-gw setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "run detection only and print a health report")
	asJSON := fs.Bool("json", false, "with --check: print the report as JSON")
	cfgPath := fs.String("config", defaultConfigPath, "configuration file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*check {
		fmt.Fprintln(stderr, "palm-gw setup: the wizard is not built yet (Phase 6); only --check works")
		return 2
	}
	r := setup.LocalHost(bluezAdapters).Detect(*cfgPath)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		printReport(stdout, r)
	}
	if len(r.Adapters) == 0 || len(r.Uplinks) == 0 || r.ConfigError != "" {
		return 1
	}
	return 0
}

func printReport(w io.Writer, r setup.Report) {
	model := r.Board.Model
	if model == "" {
		model = "unknown"
	}
	fmt.Fprintf(w, "board:     %s (%s)\n", r.Board.ID, model)
	switch {
	case len(r.Adapters) > 0:
		for i, a := range r.Adapters {
			label := "adapters:"
			if i > 0 {
				label = ""
			}
			kind := "onboard"
			if !a.Onboard() {
				kind = "USB dongle"
			}
			fmt.Fprintf(w, "%-10s %s %s  %s (%s)  powered=%s\n", label, a.HCI, a.Address, kind, a.Bus, yesNo(a.Powered))
		}
	case r.AdapterError != "":
		fmt.Fprintf(w, "adapters:  none (%s)\n", r.AdapterError)
	default:
		fmt.Fprintln(w, "adapters:  none")
	}
	if r.DisableBT {
		fmt.Fprintln(w, "           onboard radio disabled (dtoverlay=disable-bt)")
	}
	if len(r.Uplinks) == 0 {
		fmt.Fprintln(w, "uplinks:   none (no default route)")
	}
	for i, u := range r.Uplinks {
		label := "uplinks:"
		if i > 0 {
			label = ""
		}
		extra := ""
		if u.Wireless {
			if u.FreqMHz > 0 {
				extra = fmt.Sprintf("  Wi-Fi %d MHz", u.FreqMHz)
			} else {
				extra = "  Wi-Fi, band unknown"
			}
		}
		fmt.Fprintf(w, "%-10s %s via %s metric %d%s\n", label, u.Iface, u.Gateway, u.Metric, extra)
	}
	power := "unknown"
	if r.Power.MaxCurrentMA > 0 {
		power = fmt.Sprintf("supply %d mA", r.Power.MaxCurrentMA)
	}
	if r.Power.Throttled >= 0 {
		power += fmt.Sprintf(", get_throttled=%#x", r.Power.Throttled)
	}
	fmt.Fprintf(w, "power:     %s\n", power)
	switch {
	case r.ConfigError != "":
		fmt.Fprintf(w, "config:    %s does not load\n", r.ConfigPath)
	case r.ConfigFound:
		fmt.Fprintf(w, "config:    %s (will be pre-filled)\n", r.ConfigPath)
	default:
		fmt.Fprintf(w, "config:    none yet (%s)\n", r.ConfigPath)
	}
	if r.Subnet.Want != "" {
		if r.Subnet.Free {
			fmt.Fprintf(w, "subnet:    %s free\n", r.Subnet.Want)
		} else {
			fmt.Fprintf(w, "subnet:    %s overlaps %s; suggest %s\n", r.Subnet.Want, strings.Join(r.Subnet.Conflicts, ", "), r.Subnet.Suggest)
		}
	}
	if len(r.Warnings) == 0 {
		fmt.Fprintln(w, "warnings:  none")
		return
	}
	fmt.Fprintln(w, "warnings:")
	for _, s := range r.Warnings {
		fmt.Fprintf(w, "  - %s\n", s)
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
