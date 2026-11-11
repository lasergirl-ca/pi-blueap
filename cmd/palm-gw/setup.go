package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
	apply := fs.Bool("apply", false, "render the system files from the config and (re)start the units, without asking anything")
	adapterMode := fs.String("adapter-mode", "", "with --apply: onboard or dongle (default: keep the current mode)")
	noStart := fs.Bool("no-start", false, "with --apply: do not enable or restart palm-gwd")
	installFiles := fs.Bool("install-files", false, "write the layout's static files (units, helpers) under --root")
	layoutName := fs.String("layout", "", "source or deb (default: from where this binary lives)")
	root := fs.String("root", "/", "with --install-files: target root (a staging directory for packaging)")
	remove := fs.Bool("remove", false, "undo --apply and a source install's files; keeps config and paired devices")
	purge := fs.Bool("purge", false, "with --remove: also delete config.toml and the paired devices")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	layout := currentLayout()
	if *layoutName != "" {
		l, err := setup.LayoutByName(*layoutName)
		if err != nil {
			fmt.Fprintln(stderr, "palm-gw setup:", err)
			return 2
		}
		layout = l
	}
	switch {
	case *installFiles:
		if *root == "/" && os.Geteuid() != 0 {
			fmt.Fprintln(stderr, "palm-gw setup --install-files: run as root (or give --root)")
			return 1
		}
		manifestRoot := *root
		if layout.Name == "deb" {
			manifestRoot = "" // dpkg tracks the package's own files
		}
		fmt.Fprintf(stdout, "==> Static files (%s layout)\n", layout.Name)
		if _, err := setup.InstallStatic(*root, layout, manifestRoot, stdout); err != nil {
			fmt.Fprintln(stderr, "palm-gw setup:", err)
			return 1
		}
		return 0
	case *apply:
		if os.Geteuid() != 0 {
			fmt.Fprintln(stderr, "palm-gw setup --apply: run as root")
			return 1
		}
		adapters, _ := bluezAdapters()
		host := setup.LocalHost(nil)
		res, err := setup.ApplySystem(setup.ApplyOptions{
			ConfigPath:  *cfgPath,
			Layout:      layout,
			AdapterMode: *adapterMode,
			Adapters:    adapters,
			Board:       host.DetectBoard(),
			Version:     version,
			StartDaemon: !*noStart,
			Run:         runCombined,
			Out:         stdout,
		})
		if err != nil {
			fmt.Fprintln(stderr, "palm-gw setup:", err)
			return 1
		}
		// The same checks as --check (Wi-Fi band, Pi 4 console UART, supply).
		if r := setup.LocalHost(bluezAdapters).Detect(*cfgPath); len(r.Warnings) > 0 {
			fmt.Fprintln(stdout, "==> Checks")
			for _, w := range r.Warnings {
				fmt.Fprintf(stdout, "WARNING: %s\n", w)
			}
		}
		if res.RebootRequired {
			fmt.Fprintf(stdout, "Reboot required: YES (adapter mode is now %s)\n", res.AdapterMode)
		} else {
			fmt.Fprintln(stdout, "Reboot required: no")
		}
		return 0
	case *remove:
		if os.Geteuid() != 0 {
			fmt.Fprintln(stderr, "palm-gw setup --remove: run as root")
			return 1
		}
		if err := setup.RemoveSystem(setup.RemoveOptions{ConfigPath: *cfgPath, Purge: *purge, Run: runCombined, Out: stdout}); err != nil {
			fmt.Fprintln(stderr, "palm-gw setup:", err)
			return 1
		}
		return 0
	case !*check:
		fmt.Fprintln(stderr, "palm-gw setup: the wizard is not built yet (Phase 6); use --check, --apply, --install-files or --remove")
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

// currentLayout is the layout this binary was installed with.
func currentLayout() setup.Layout {
	exe, err := os.Executable()
	if err != nil {
		return setup.SourceLayout
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return setup.LayoutOf(exe)
}

// runCombined runs a command; on failure the error carries its output.
func runCombined(name string, args ...string) ([]byte, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
	}
	return out, err
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
