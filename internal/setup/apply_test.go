package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticFiles(t *testing.T) {
	for _, l := range []Layout{SourceLayout, DebLayout} {
		files, err := l.StaticFiles()
		if err != nil {
			t.Fatal(err)
		}
		byPath := map[string]File{}
		for _, f := range files {
			if strings.Contains(string(f.Data), "{{") {
				t.Errorf("%s: %s still has a template action", l.Name, f.Path)
			}
			byPath[f.Path] = f
		}
		unit := byPath[filepath.Join(l.UnitDir, "palm-gwd.service")]
		if !strings.Contains(string(unit.Data), "ExecStart="+l.BinDir+"/palm-gwd -config") {
			t.Errorf("%s: palm-gwd.service ExecStart:\n%s", l.Name, unit.Data)
		}
		bridge := string(byPath[filepath.Join(l.UnitDir, "palm-gwd-nm-bridge.service")].Data)
		if !strings.Contains(bridge, "EnvironmentFile=/etc/palm-gw/bridge.env") ||
			!strings.Contains(bridge, "ExecStart="+l.LibDir+"/nm-bridge.sh ${BRIDGE} ${GATEWAY} ${PREFIX}") {
			t.Errorf("%s: bridge unit:\n%s", l.Name, bridge)
		}
		if f := byPath[filepath.Join(l.LibDir, "nm-bridge.sh")]; f.Mode != 0o755 {
			t.Errorf("%s: nm-bridge.sh mode %v", l.Name, f.Mode)
		}
		if _, ok := byPath[PPPOptionsPath]; !ok {
			t.Errorf("%s: no %s", l.Name, PPPOptionsPath)
		}
	}
}

// sysRoot is a minimal Pi: main.conf from Debian, a config.txt, os-release.
func sysRoot(t *testing.T, cfg string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"etc/palm-gw/config.toml":  cfg,
		"etc/bluetooth/main.conf":  "[General]\n#Name = BlueZ\n\n[Policy]\n#AutoEnable=true\n",
		"boot/firmware/config.txt": "[all]\ndtparam=audio=on\n",
		"etc/os-release":           "PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n",
	}
	for p, body := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type recorder struct{ cmds []string }

func (r *recorder) run(name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	r.cmds = append(r.cmds, line)
	switch line {
	case "uname -r":
		return []byte("6.18.50+rpt-rpi-2712\n"), nil
	case "bluetoothctl --version":
		return []byte("bluetoothctl: 5.82\n"), nil
	case "nft --version":
		return []byte("nftables v1.1.3 (Commodore Bullseye #4)\n"), nil
	}
	return nil, nil
}

func (r *recorder) index(t *testing.T, line string) int {
	t.Helper()
	for i, c := range r.cmds {
		if c == line {
			return i
		}
	}
	t.Fatalf("command %q not run; ran:\n%s", line, strings.Join(r.cmds, "\n"))
	return -1
}

func TestApplySystem(t *testing.T) {
	root := sysRoot(t, "[network]\nuplink = \"eth0\"\n")
	rec := &recorder{}
	opts := ApplyOptions{Root: root, Layout: SourceLayout, Run: rec.run, Board: Board{ID: "pi5"}, Version: "test", StartDaemon: true}
	res, err := ApplySystem(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dnsmasqPath, nftPath, sysctlPath, bridgeEnvPath, mainConfPath} {
		found := false
		for _, c := range res.Changed {
			found = found || c == p
		}
		if !found {
			t.Errorf("%s not reported changed: %v", p, res.Changed)
		}
	}
	env, _ := os.ReadFile(filepath.Join(root, bridgeEnvPath))
	if !strings.Contains(string(env), "BRIDGE=br-palm\nGATEWAY=10.42.0.1\nPREFIX=24\n") {
		t.Errorf("bridge.env:\n%s", env)
	}
	nftConf, _ := os.ReadFile(filepath.Join(root, nftConfPath))
	if !strings.Contains(string(nftConf), nftInclude) || !strings.HasPrefix(string(nftConf), "#!/usr/sbin/nft -f") {
		t.Errorf("nftables.conf:\n%s", nftConf)
	}
	mc, _ := os.ReadFile(filepath.Join(root, mainConfPath))
	if !strings.Contains(string(mc), "ReverseServiceDiscovery = false") {
		t.Errorf("main.conf block missing:\n%s", mc)
	}
	// The firewall is checked before anything starts, the bridge before
	// dnsmasq, and sysctl after the bridge.
	check := rec.index(t, "nft -c -f /etc/nftables.conf")
	bridge := rec.index(t, "systemctl restart palm-gwd-nm-bridge")
	dnsmasq := rec.index(t, "systemctl restart palm-gw-dnsmasq")
	sysctl := rec.index(t, "sysctl -q -p /etc/sysctl.d/90-palm-gw.conf")
	daemon := rec.index(t, "systemctl restart palm-gwd")
	if check > bridge || bridge > dnsmasq || dnsmasq > sysctl || sysctl > daemon {
		t.Errorf("order: %v", rec.cmds)
	}
	rec.index(t, "systemctl restart bluetooth") // main.conf changed
	var info InstallInfo
	data, _ := os.ReadFile(filepath.Join(root, StateDir, "install-info.json"))
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	if info.AdapterMode != "onboard" || info.BlueZ != "5.82" || info.Nftables != "v1.1.3" || info.InstallPath != "source" || info.Uplink != "eth0" {
		t.Errorf("install-info: %+v", info)
	}

	// A second run changes nothing and leaves the bridge and Bluetooth alone.
	rec2 := &recorder{}
	opts.Run = rec2.run
	res, err = ApplySystem(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 0 {
		t.Errorf("second run changed %v", res.Changed)
	}
	rec2.index(t, "systemctl start palm-gwd-nm-bridge")
	for _, c := range rec2.cmds {
		if c == "systemctl restart bluetooth" || c == "systemctl restart palm-gwd-nm-bridge" {
			t.Errorf("second run ran %q", c)
		}
	}
	nftConf2, _ := os.ReadFile(filepath.Join(root, nftConfPath))
	if strings.Count(string(nftConf2), nftInclude) != 1 {
		t.Errorf("include line duplicated:\n%s", nftConf2)
	}
}

func TestApplyDongleMode(t *testing.T) {
	// The onboard radio pinned by address (D-035): dongle mode must warn.
	root := sysRoot(t, "[bluetooth]\nadapter = \"2C:CF:67:2F:29:F5\"\n[network]\nuplink = \"eth0\"\n")
	rec := &recorder{}
	opts := ApplyOptions{Root: root, Layout: SourceLayout, Run: rec.run, AdapterMode: "dongle",
		Adapters: []Adapter{{HCI: "hci0", Address: "2C:CF:67:2F:29:F5", Bus: "serial"}, {HCI: "hci1", Address: "60:15:6F:E9:EB:03", Bus: "usb"}}}
	res, err := ApplySystem(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.RebootRequired || res.AdapterMode != "dongle" || len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "not a USB dongle") {
		t.Fatalf("got %+v", res)
	}
	rec.index(t, "systemctl disable --now hciuart")
	boot, _ := os.ReadFile(filepath.Join(root, bootConfig))
	if !strings.Contains(string(boot), "\n"+disableBTLine+"\n") {
		t.Fatalf("config.txt:\n%s", boot)
	}
	// Keeping the mode ("") on a re-run keeps dongle and does not add the line twice.
	opts.AdapterMode = ""
	res, err = ApplySystem(opts)
	if err != nil || res.AdapterMode != "dongle" || res.RebootRequired {
		t.Fatalf("re-run: %+v %v", res, err)
	}
	boot, _ = os.ReadFile(filepath.Join(root, bootConfig))
	if strings.Count(string(boot), disableBTLine) != 1 {
		t.Fatalf("config.txt:\n%s", boot)
	}
	// Back to onboard: palm-gw added the line, so it takes it out again.
	opts.AdapterMode = "onboard"
	res, err = ApplySystem(opts)
	if err != nil || !res.RebootRequired {
		t.Fatalf("onboard: %+v %v", res, err)
	}
	boot, _ = os.ReadFile(filepath.Join(root, bootConfig))
	if strings.Contains(string(boot), "disable-bt") || !strings.Contains(string(boot), "dtparam=audio=on") {
		t.Fatalf("config.txt:\n%s", boot)
	}
}

func TestRemoveSystem(t *testing.T) {
	root := sysRoot(t, "[network]\nuplink = \"eth0\"\n")
	if _, err := InstallStatic(root, SourceLayout, root, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplySystem(ApplyOptions{Root: root, Layout: SourceLayout, Run: (&recorder{}).run, AdapterMode: "dongle"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, StateDir, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	if err := RemoveSystem(RemoveOptions{Root: root, Run: rec.run}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{nftPath, sysctlPath, dnsmasqPath, bridgeEnvPath, "/etc/systemd/system/palm-gwd.service", "/usr/local/lib/palm-gw/nm-bridge.sh", PPPOptionsPath} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Errorf("%s still there", p)
		}
	}
	for _, p := range []string{"/etc/palm-gw/config.toml", StateDir + "/state.json", mainConfPath} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
	mc, _ := os.ReadFile(filepath.Join(root, mainConfPath))
	if strings.Contains(string(mc), "palm-gw") {
		t.Errorf("main.conf still has the block:\n%s", mc)
	}
	nftConf, _ := os.ReadFile(filepath.Join(root, nftConfPath))
	if strings.Contains(string(nftConf), "palm-gw") {
		t.Errorf("nftables.conf:\n%s", nftConf)
	}
	boot, _ := os.ReadFile(filepath.Join(root, bootConfig))
	if strings.Contains(string(boot), "disable-bt") {
		t.Errorf("config.txt:\n%s", boot)
	}
	rec.index(t, "systemctl disable --now palm-gwd")
	rec.index(t, "nft delete table inet palm_gw")
	// A reinstall with the mode unset restores dongle mode.
	if _, err := InstallStatic(root, SourceLayout, root, nil); err != nil {
		t.Fatal(err)
	}
	res, err := ApplySystem(ApplyOptions{Root: root, Layout: SourceLayout, Run: (&recorder{}).run})
	if err != nil || res.AdapterMode != "dongle" || !res.RebootRequired {
		t.Fatalf("reinstall: %+v %v", res, err)
	}

	if err := RemoveSystem(RemoveOptions{Root: root, Run: rec.run, Purge: true}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{EtcDir, StateDir} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Errorf("purge left %s", p)
		}
	}
}

// The operator who ran setup under sudo joins palm-gw, once (D-048).
func TestApplySystemOperatorGroup(t *testing.T) {
	root := sysRoot(t, "[network]\nuplink = \"eth0\"\n")
	rec := &recorder{}
	res, err := ApplySystem(ApplyOptions{Root: root, Layout: SourceLayout, Run: rec.run, Operator: "sysadmin"})
	if err != nil {
		t.Fatal(err)
	}
	rec.index(t, "usermod -aG palm-gw sysadmin")
	if res.GroupAdded != "sysadmin" {
		t.Errorf("GroupAdded = %q", res.GroupAdded)
	}

	member := func(name string, args ...string) ([]byte, error) {
		if name == "id" {
			return []byte("sysadmin adm sudo palm-gw\n"), nil
		}
		return rec.run(name, args...)
	}
	rec.cmds = nil
	res, err = ApplySystem(ApplyOptions{Root: root, Layout: SourceLayout, Run: member, Operator: "sysadmin"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.cmds {
		if strings.HasPrefix(c, "usermod") {
			t.Errorf("ran %q for a member", c)
		}
	}
	if res.GroupAdded != "" {
		t.Errorf("GroupAdded = %q for a member", res.GroupAdded)
	}

	rec.cmds = nil
	if _, err := ApplySystem(ApplyOptions{Root: root, Layout: SourceLayout, Run: rec.run, Operator: "root"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.cmds {
		if strings.HasPrefix(c, "id ") || strings.HasPrefix(c, "usermod") {
			t.Errorf("ran %q for root", c)
		}
	}
}
