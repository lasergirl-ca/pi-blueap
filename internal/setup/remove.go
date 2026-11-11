package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"palm-gw/internal/config"
)

// ManifestName is the list of files palm-gw placed outside the package's own
// files, under the state directory (PLAN.md section 8.11, Path C).
const ManifestName = "installed-files.txt"

// Manifest is a set of absolute paths, kept sorted on disk.
type Manifest struct {
	path  string
	paths map[string]bool
}

// LoadManifest reads the manifest at path; a missing file is an empty set.
func LoadManifest(path string) (*Manifest, error) {
	m := &Manifest{path: path, paths: map[string]bool{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			m.paths[l] = true
		}
	}
	return m, nil
}

// Add records a path.
func (m *Manifest) Add(p string) { m.paths[p] = true }

// Paths returns the recorded paths, sorted.
func (m *Manifest) Paths() []string {
	out := make([]string, 0, len(m.paths))
	for p := range m.paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Save writes the manifest.
func (m *Manifest) Save() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o750); err != nil {
		return err
	}
	_, err := writeIfChanged(m.path, []byte(strings.Join(m.Paths(), "\n")+"\n"), 0o644)
	return err
}

// RemoveOptions drives RemoveSystem.
type RemoveOptions struct {
	Root       string
	ConfigPath string
	StateDir   string
	// Purge also deletes config.toml and the state directory (paired devices).
	Purge bool
	Run   func(name string, args ...string) ([]byte, error)
	Out   io.Writer
}

// RemoveSystem undoes ApplySystem and a source install's placed files: it stops the
// units, deletes the bridge connection, strips the main.conf block and the
// nftables include, takes out dtoverlay=disable-bt only if palm-gw added it,
// and deletes every file in the manifest. config.toml and the paired devices
// are kept unless Purge is set. The .deb's prerm runs it; its own files are
// left to dpkg.
func RemoveSystem(o RemoveOptions) error {
	if o.Root == "" {
		o.Root = "/"
	}
	if o.ConfigPath == "" {
		o.ConfigPath = config.DefaultPath
	}
	if o.StateDir == "" {
		o.StateDir = StateDir
	}
	a := &applier{o: ApplyOptions{Root: o.Root, StateDir: o.StateDir, Run: o.Run, Out: o.Out}}
	info := a.readInstallInfo()

	a.say("Stopping units")
	for _, u := range []string{"palm-gwd", "palm-gw-dnsmasq", "palm-gwd-nm-bridge"} {
		_, _ = a.run("systemctl", "disable", "--now", u)
	}

	bridge := config.Default().PAN.Bridge
	if cfg, err := config.Load(a.path(o.ConfigPath)); err == nil {
		bridge = cfg.PAN.Bridge
	}
	if out, err := a.run("nmcli", "-t", "-f", "NAME", "con", "show"); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(l) == bridge {
				if _, err := a.run("nmcli", "con", "delete", bridge); err == nil {
					a.note("NetworkManager connection %s removed", bridge)
				}
			}
		}
	}

	a.say("Restoring %s", mainConfPath)
	if ch, err := RemoveFile(a.path(mainConfPath)); err == nil && ch {
		_, _ = a.run("systemctl", "restart", "bluetooth")
		a.note("managed block removed, bluetooth restarted")
	} else {
		a.note("no managed block present")
	}

	a.say("Removing the firewall table")
	_, _ = a.run("nft", "delete", "table", "inet", "palm_gw")
	if data, err := os.ReadFile(a.path(nftConfPath)); err == nil {
		var keep []string
		for _, l := range strings.Split(string(data), "\n") {
			if t := strings.TrimSpace(l); t == "# palm-gw" || t == nftInclude {
				continue
			}
			keep = append(keep, l)
		}
		if err := os.WriteFile(a.path(nftConfPath), []byte(strings.Join(keep, "\n")), 0o755); err != nil {
			return err
		}
	}

	a.say("Removing files")
	m, err := LoadManifest(a.path(filepath.Join(o.StateDir, ManifestName)))
	if err != nil {
		return err
	}
	for _, p := range m.Paths() {
		if p == mainConfPath || strings.HasPrefix(p, o.StateDir+"/") || p == config.DefaultPath || p == o.ConfigPath {
			continue
		}
		if err := os.Remove(a.path(p)); err == nil {
			a.note("%s", p)
		}
	}
	for _, d := range []string{SourceLayout.LibDir, "/etc/nftables.d"} {
		_ = os.Remove(a.path(d)) // only if empty
	}

	if info.AddedDisableBT {
		if err := removeDisableBT(a.path(bootConfig)); err == nil {
			a.note("%s removed from %s (reboot to re-enable the onboard radio)", disableBTLine, bootConfig)
		}
	}
	_, _ = a.run("systemctl", "daemon-reload")
	_, _ = a.run("systemctl", "reload-or-restart", "nftables")

	if o.Purge {
		for _, d := range []string{EtcDir, o.StateDir} {
			if err := os.RemoveAll(a.path(d)); err != nil {
				return err
			}
		}
		a.say("Purged %s and %s", EtcDir, o.StateDir)
		return nil
	}
	_ = os.Remove(a.path(filepath.Join(o.StateDir, ManifestName)))
	// Keep install-info.json so a reinstall restores the adapter mode, but it
	// no longer holds a disable-bt line palm-gw added.
	if info.AddedDisableBT {
		info.AddedDisableBT = false
		if data, err := json.MarshalIndent(info, "", "  "); err == nil {
			_ = os.WriteFile(a.path(filepath.Join(o.StateDir, "install-info.json")), append(data, '\n'), 0o644)
		}
	}
	a.say("Kept %s and %s (purge removes them)", o.ConfigPath, filepath.Join(o.StateDir, "state.json"))
	return nil
}

// InstallStatic places a layout's static files under root and records them in
// the manifest at manifestRoot (pass "" to skip, as `make deb` does: dpkg
// tracks the package's files).
func InstallStatic(root string, l Layout, manifestRoot string, out io.Writer) ([]string, error) {
	files, err := l.StaticFiles()
	if err != nil {
		return nil, err
	}
	var m *Manifest
	if manifestRoot != "" {
		if m, err = LoadManifest(filepath.Join(manifestRoot, StateDir, ManifestName)); err != nil {
			return nil, err
		}
	}
	changed, err := WriteFiles(root, files, func(p string) {
		if m != nil {
			m.Add(p)
		}
	})
	if err != nil {
		return changed, err
	}
	if out != nil {
		for _, f := range files {
			state := "unchanged"
			for _, c := range changed {
				if c == f.Path {
					state = "written"
				}
			}
			fmt.Fprintf(out, "    %s %s\n", f.Path, state)
		}
	}
	if m != nil {
		return changed, m.Save()
	}
	return changed, nil
}
