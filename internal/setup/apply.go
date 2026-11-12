package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"palm-gw/deploy"
	"palm-gw/internal/config"
	"palm-gw/internal/state"
)

// Paths Apply and Remove manage on the target system.
const (
	EtcDir        = "/etc/palm-gw"
	StateDir      = "/var/lib/palm-gw"
	dnsmasqPath   = "/etc/palm-gw/dnsmasq.conf"
	bridgeEnvPath = "/etc/palm-gw/bridge.env"
	nftPath       = "/etc/nftables.d/palm-gw.nft"
	nftConfPath   = "/etc/nftables.conf"
	sysctlPath    = "/etc/sysctl.d/90-palm-gw.conf"
	mainConfPath  = "/etc/bluetooth/main.conf"
	bootConfig    = "/boot/firmware/config.txt"
	nftInclude    = `include "/etc/nftables.d/palm-gw.nft"`
	disableBTMark = "# palm-gw: onboard Bluetooth disabled so the USB dongle is hci0"
	disableBTLine = "dtoverlay=disable-bt"
)

var kernelModules = []string{"bnep", "rfcomm", "ppp_generic", "ppp_async"}

// ApplyOptions drives ApplySystem.
type ApplyOptions struct {
	Root        string // "/" on a real system; a temporary tree in tests
	ConfigPath  string // default /etc/palm-gw/config.toml
	StateDir    string // default /var/lib/palm-gw
	Layout      Layout
	AdapterMode string    // "onboard", "dongle", or "" to keep the current mode
	Adapters    []Adapter // what BlueZ sees now, for the dongle check and install-info
	Board       Board
	Version     string
	StartDaemon bool   // enable and (re)start palm-gwd at the end
	Operator    string // SUDO_USER: added to the palm-gw group so palm-gwctl works without sudo (D-048)
	Run         func(name string, args ...string) ([]byte, error)
	Out         io.Writer // progress lines; nil discards them
}

// ApplyResult says what ApplySystem did.
type ApplyResult struct {
	Changed        []string `json:"changed"`
	Warnings       []string `json:"warnings"`
	RebootRequired bool     `json:"reboot_required"`
	AdapterMode    string   `json:"adapter_mode"`
	GroupAdded     string   `json:"group_added,omitempty"` // the operator, if this run added them to palm-gw
}

// InstallInfo is /var/lib/palm-gw/install-info.json (PLAN.md section 3).
type InstallInfo struct {
	InstalledAt    string `json:"installed_at"`
	Version        string `json:"version"`
	InstallPath    string `json:"install_path"`
	Board          string `json:"board"`
	Model          string `json:"model"`
	Kernel         string `json:"kernel"`
	OS             string `json:"os"`
	BlueZ          string `json:"bluez"`
	PPPD           string `json:"pppd"`
	NetworkManager string `json:"networkmanager"`
	Nftables       string `json:"nftables"`
	Dnsmasq        string `json:"dnsmasq"`
	AdapterMode    string `json:"adapter_mode"`
	Adapters       string `json:"adapters"`
	Uplink         string `json:"uplink"`
	AddedDisableBT bool   `json:"added_disable_bt"`
}

type applier struct {
	o   ApplyOptions
	res ApplyResult
}

func (a *applier) path(p string) string { return filepath.Join(a.o.Root, p) }

func (a *applier) say(f string, args ...any) {
	if a.o.Out != nil {
		fmt.Fprintf(a.o.Out, "==> "+f+"\n", args...)
	}
}

func (a *applier) note(f string, args ...any) {
	if a.o.Out != nil {
		fmt.Fprintf(a.o.Out, "    "+f+"\n", args...)
	}
}

func (a *applier) warn(f string, args ...any) {
	msg := fmt.Sprintf(f, args...)
	a.res.Warnings = append(a.res.Warnings, msg)
	if a.o.Out != nil {
		fmt.Fprintf(a.o.Out, "WARNING: %s\n", msg)
	}
}

func (a *applier) run(name string, args ...string) ([]byte, error) {
	if a.o.Run == nil {
		return nil, errors.New("no command runner")
	}
	out, err := a.o.Run(name, args...)
	if err != nil {
		return out, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// write places one file and records whether it changed.
func (a *applier) write(p string, data []byte, mode fs.FileMode, m *Manifest) (bool, error) {
	ch, err := writeIfChanged(a.path(p), data, mode)
	if err != nil {
		return false, err
	}
	m.Add(p)
	if ch {
		a.res.Changed = append(a.res.Changed, p)
		a.note("%s written", p)
	} else {
		a.note("%s unchanged", p)
	}
	return ch, nil
}

// ApplySystem renders every configuration-dependent system file from config.toml,
// sets the adapter mode, checks the firewall, and (re)starts the units in
// order (PLAN.md section 8.11, "Then the wizard writes ..."). It is what the
// wizard runs after writing config.toml, what install.sh runs for a source
// install, and what the .deb's postinst runs on an upgrade. It is idempotent.
func ApplySystem(o ApplyOptions) (ApplyResult, error) {
	if o.Root == "" {
		o.Root = "/"
	}
	if o.ConfigPath == "" {
		o.ConfigPath = config.DefaultPath
	}
	if o.StateDir == "" {
		o.StateDir = StateDir
	}
	a := &applier{o: o}
	cfg, err := config.Load(a.path(o.ConfigPath))
	if err != nil {
		return a.res, err
	}
	m, err := LoadManifest(a.path(filepath.Join(o.StateDir, ManifestName)))
	if err != nil {
		return a.res, err
	}
	prev := a.readInstallInfo()

	a.say("Configuration files")
	if err := os.MkdirAll(a.path(o.StateDir), 0o750); err != nil {
		return a.res, err
	}
	rendered := []struct {
		tmpl, dst string
	}{
		{deploy.Dnsmasq, dnsmasqPath},
		{deploy.Nftables, nftPath},
		{deploy.Sysctl, sysctlPath},
	}
	changed := map[string]bool{}
	for _, r := range rendered {
		text, err := deploy.FS.ReadFile(r.tmpl)
		if err != nil {
			return a.res, err
		}
		out, err := cfg.Render(r.tmpl, string(text))
		if err != nil {
			return a.res, err
		}
		ch, err := a.write(r.dst, []byte(out), 0o644, m)
		if err != nil {
			return a.res, err
		}
		changed[r.dst] = ch
	}
	ch, err := a.write(bridgeEnvPath, []byte(BridgeEnv(cfg)), 0o644, m)
	if err != nil {
		return a.res, err
	}
	changed[bridgeEnvPath] = ch

	a.say("BlueZ main.conf managed block")
	restartBluetooth := false
	if _, err := os.Stat(a.path(mainConfPath)); err == nil {
		orig := a.path(filepath.Join(o.StateDir, "main.conf.orig"))
		if _, err := os.Stat(orig); errors.Is(err, fs.ErrNotExist) {
			if data, err := os.ReadFile(a.path(mainConfPath)); err == nil {
				_ = os.WriteFile(orig, data, 0o644) // belt and braces; the block is reversible on its own
			}
		}
		class := cfg.Bluetooth.Class
		if st, err := state.Open(state.Path(a.path(o.StateDir))); err == nil && st.Class() != 0 {
			class = st.Class() // persisted `class toggle`
		}
		ch, err := ApplyFile(a.path(mainConfPath), ManagedSettings(cfg, class))
		if err != nil {
			return a.res, err
		}
		m.Add(mainConfPath) // listed so Remove knows to strip the block; never deleted
		if ch {
			restartBluetooth = true
			a.res.Changed = append(a.res.Changed, mainConfPath)
			a.note("%s: managed block written", mainConfPath)
		} else {
			a.note("%s: managed block unchanged", mainConfPath)
		}
	} else {
		a.warn("%s not found; is bluez installed?", mainConfPath)
	}

	a.say("nftables")
	if err := a.ensureNftInclude(); err != nil {
		return a.res, err
	}
	if _, err := a.run("nft", "-c", "-f", nftConfPath); err != nil {
		return a.res, fmt.Errorf("the firewall does not load: %w", err)
	}
	a.note("nft -c accepts %s", nftConfPath)

	a.say("Bluetooth adapter mode")
	mode, added, err := a.adapterMode(cfg, prev)
	if err != nil {
		return a.res, err
	}
	a.res.AdapterMode = mode

	a.ensureOperatorGroup()

	a.say("Units")
	for _, mod := range kernelModules {
		if _, err := a.run("modprobe", mod); err != nil {
			a.warn("modprobe %s failed: %v", mod, err)
		}
	}
	steps := [][]string{{"systemctl", "daemon-reload"}}
	_, btErr := a.run("systemctl", "is-active", "--quiet", "bluetooth")
	if restartBluetooth || btErr != nil {
		steps = append(steps, []string{"systemctl", "restart", "bluetooth"})
	}
	steps = append(steps,
		[]string{"systemctl", "enable", "--now", "nftables"},
		[]string{"systemctl", "reload-or-restart", "nftables"},
		[]string{"systemctl", "enable", "palm-gwd-nm-bridge"},
		[]string{"systemctl", startOrRestart(changed[bridgeEnvPath]), "palm-gwd-nm-bridge"},
		[]string{"systemctl", "enable", "palm-gw-dnsmasq"},
		[]string{"systemctl", startOrRestart(changed[dnsmasqPath]), "palm-gw-dnsmasq"},
		// After the bridge exists, so the per-bridge key applies.
		[]string{"sysctl", "-q", "-p", sysctlPath},
	)
	if o.StartDaemon {
		steps = append(steps, []string{"systemctl", "enable", "palm-gwd"}, []string{"systemctl", "restart", "palm-gwd"})
	}
	for _, s := range steps {
		if _, err := a.run(s[0], s[1:]...); err != nil {
			return a.res, err
		}
		a.note("%s", strings.Join(s, " "))
	}

	a.say("Recording install-info.json")
	info := a.installInfo(cfg, mode, added)
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return a.res, err
	}
	infoPath := filepath.Join(o.StateDir, "install-info.json")
	if _, err := writeIfChanged(a.path(infoPath), append(data, '\n'), 0o644); err != nil {
		return a.res, err
	}
	m.Add(infoPath)
	if err := m.Save(); err != nil {
		return a.res, err
	}
	return a.res, nil
}

// ensureOperatorGroup adds the user who ran setup under sudo to the palm-gw group
// (D-048). The .deb's postinst does the same, but in the image the package is
// installed before Imager creates the user, so the wizard must do it.
func (a *applier) ensureOperatorGroup() {
	u := a.o.Operator
	if u == "" || u == "root" {
		return
	}
	a.say("palm-gw group")
	out, err := a.run("id", "-nG", u)
	if err != nil {
		a.warn("cannot read %s's groups: %v", u, err)
		return
	}
	for _, g := range strings.Fields(string(out)) {
		if g == "palm-gw" {
			a.note("%s already in palm-gw", u)
			return
		}
	}
	if _, err := a.run("usermod", "-aG", "palm-gw", u); err != nil {
		a.warn("could not add %s to palm-gw: %v", u, err)
		return
	}
	a.res.GroupAdded = u
	a.note("%s added to palm-gw (takes effect at the next login)", u)
}

func startOrRestart(changed bool) string {
	if changed {
		return "restart"
	}
	return "start"
}

// BridgeEnv is /etc/palm-gw/bridge.env, read by palm-gwd-nm-bridge.service.
func BridgeEnv(cfg *config.Config) string {
	p := netip.MustParsePrefix(cfg.Network.Subnet)
	return fmt.Sprintf("# Written by palm-gw setup from config.toml; edits are overwritten.\nBRIDGE=%s\nGATEWAY=%s\nPREFIX=%d\n",
		cfg.PAN.Bridge, cfg.Network.Gateway, p.Bits())
}

func (a *applier) ensureNftInclude() error {
	p := a.path(nftConfPath)
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		data = []byte("#!/usr/sbin/nft -f\nflush ruleset\n")
		if err := os.WriteFile(p, data, 0o755); err != nil {
			return err
		}
		a.note("%s created", nftConfPath)
	} else if err != nil {
		return err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == nftInclude {
			a.note("include line already in %s", nftConfPath)
			return nil
		}
	}
	text := strings.TrimRight(string(data), "\n") + "\n\n# palm-gw\n" + nftInclude + "\n"
	if err := os.WriteFile(p, []byte(text), 0o755); err != nil {
		return err
	}
	a.note("include line appended to %s", nftConfPath)
	return nil
}

// adapterMode applies "onboard" or "dongle" to config.txt. It returns the
// mode in effect and whether palm-gw added the disable-bt line (so Remove
// only takes out what it put in).
func (a *applier) adapterMode(cfg *config.Config, prev InstallInfo) (string, bool, error) {
	mode := a.o.AdapterMode
	if mode == "" {
		mode = prev.AdapterMode
	}
	if mode == "" {
		mode = "onboard"
	}
	added := prev.AddedDisableBT
	p := a.path(bootConfig)
	data, err := os.ReadFile(p)
	if err != nil {
		if mode == "dongle" {
			return mode, added, fmt.Errorf("dongle mode needs %s: %w", bootConfig, err)
		}
		a.note("onboard radio stays enabled")
		return mode, added, nil
	}
	has := false
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == disableBTLine {
			has = true
		}
	}
	switch mode {
	case "dongle":
		if has {
			a.note("%s already in %s", disableBTLine, bootConfig)
		} else {
			text := strings.TrimRight(string(data), "\n") + "\n\n" + disableBTMark + "\n" + disableBTLine + "\n"
			if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
				return mode, added, err
			}
			added = true
			a.res.RebootRequired = true
			a.res.Changed = append(a.res.Changed, bootConfig)
			a.note("%s added to %s", disableBTLine, bootConfig)
		}
		_, _ = a.run("systemctl", "disable", "--now", "hciuart")
		a.checkDongleAdapter(cfg)
	case "onboard":
		switch {
		case has && added:
			if err := removeDisableBT(p); err != nil {
				return mode, added, err
			}
			added = false
			a.res.RebootRequired = true
			a.res.Changed = append(a.res.Changed, bootConfig)
			a.note("%s removed from %s (palm-gw had added it)", disableBTLine, bootConfig)
		case has:
			a.warn("%s is in %s but palm-gw did not add it; the onboard radio stays off until you remove it", disableBTLine, bootConfig)
		default:
			a.note("onboard radio stays enabled")
		}
	default:
		return mode, added, fmt.Errorf("adapter mode %q: want onboard or dongle", mode)
	}
	return mode, added, nil
}

// checkDongleAdapter warns when [bluetooth].adapter names something that will
// not exist once the onboard radio is off (it may name the onboard radio by
// address, D-035).
func (a *applier) checkDongleAdapter(cfg *config.Config) {
	want := cfg.Bluetooth.Adapter
	if want == "hci0" || want == "auto" {
		return
	}
	for _, ad := range a.o.Adapters {
		if !ad.Onboard() && strings.EqualFold(ad.Address, want) {
			a.note("adapter = %q is the USB dongle", want)
			return
		}
	}
	a.warn("adapter = %q in config.toml is not a USB dongle here; with the onboard radio off the gateway will not find it. Set adapter to \"auto\", \"hci0\" or the dongle's address", want)
}

func removeDisableBT(p string) error {
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var keep []string
	for _, l := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(l)
		if t == disableBTLine || t == disableBTMark {
			continue
		}
		keep = append(keep, l)
	}
	return os.WriteFile(p, []byte(strings.Join(keep, "\n")), 0o644)
}

func (a *applier) readInstallInfo() InstallInfo {
	var info InstallInfo
	if data, err := os.ReadFile(a.path(filepath.Join(a.o.StateDir, "install-info.json"))); err == nil {
		_ = json.Unmarshal(data, &info)
	}
	return info
}

func (a *applier) installInfo(cfg *config.Config, mode string, added bool) InstallInfo {
	first := func(name string, args ...string) string {
		out, err := a.run(name, args...)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	}
	field := func(s string, i int) string {
		f := strings.Fields(s)
		if i < 0 {
			i = len(f) + i
		}
		if i < 0 || i >= len(f) {
			return ""
		}
		return f[i]
	}
	osName := ""
	if data, err := os.ReadFile(a.path("/etc/os-release")); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
				osName = strings.Trim(v, `"`)
			}
		}
	}
	var ads []string
	for _, ad := range a.o.Adapters {
		ads = append(ads, fmt.Sprintf("%s %s %s", ad.HCI, ad.Address, ad.Bus))
	}
	sort.Strings(ads)
	return InstallInfo{
		InstalledAt:    time.Now().UTC().Format(time.RFC3339),
		Version:        a.o.Version,
		InstallPath:    a.o.Layout.Name,
		Board:          a.o.Board.ID,
		Model:          a.o.Board.Model,
		Kernel:         first("uname", "-r"),
		OS:             osName,
		BlueZ:          field(first("bluetoothctl", "--version"), -1),
		PPPD:           field(first("sh", "-c", "pppd --version 2>&1"), -1),
		NetworkManager: field(first("nmcli", "--version"), -1),
		Nftables:       field(first("nft", "--version"), 1),
		Dnsmasq:        field(first("dnsmasq", "--version"), 2),
		AdapterMode:    mode,
		Adapters:       strings.Join(ads, "|"),
		Uplink:         cfg.Network.Uplink,
		AddedDisableBT: added,
	}
}
