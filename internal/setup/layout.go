package setup

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"text/template"

	"palm-gw/deploy"
)

// Layout says where an installation keeps its binaries and its static system
// files (D-042). A source install (`make install`) uses /usr/local and /etc; the
// .deb follows Debian policy with /usr/bin and /usr/lib, where systemd,
// NetworkManager and modules-load.d all look too, so the package owns those
// files outright. Files rendered from config.toml always live under /etc and
// are written by Apply in either layout.
type Layout struct {
	Name       string `json:"name"` // "source" or "deb"
	BinDir     string `json:"bin_dir"`
	LibDir     string `json:"lib_dir"`
	UnitDir    string `json:"unit_dir"`
	NMConfDir  string `json:"nm_conf_dir"`
	ModulesDir string `json:"modules_dir"`
}

var (
	SourceLayout = Layout{
		Name:       "source",
		BinDir:     "/usr/local/bin",
		LibDir:     "/usr/local/lib/palm-gw",
		UnitDir:    "/etc/systemd/system",
		NMConfDir:  "/etc/NetworkManager/conf.d",
		ModulesDir: "/etc/modules-load.d",
	}
	DebLayout = Layout{
		Name:       "deb",
		BinDir:     "/usr/bin",
		LibDir:     "/usr/lib/palm-gw",
		UnitDir:    "/usr/lib/systemd/system",
		NMConfDir:  "/usr/lib/NetworkManager/conf.d",
		ModulesDir: "/usr/lib/modules-load.d",
	}
)

// LayoutByName returns the named layout.
func LayoutByName(name string) (Layout, error) {
	switch name {
	case "source":
		return SourceLayout, nil
	case "deb":
		return DebLayout, nil
	}
	return Layout{}, fmt.Errorf("layout %q: want source or deb", name)
}

// LayoutOf guesses the layout from where a palm-gw binary lives.
func LayoutOf(exe string) Layout {
	if filepath.Dir(exe) == DebLayout.BinDir {
		return DebLayout
	}
	return SourceLayout
}

// File is one file to place on the system.
type File struct {
	Path string      // absolute, on the target system
	Mode fs.FileMode // permission bits
	Data []byte
}

// PPPOptionsPath is where both layouts put the pppd options file. It is a
// conffile in the .deb, because an operator may tune it.
const PPPOptionsPath = "/etc/ppp/options.palm"

// StaticFiles renders the layout-dependent files: the three units, the bridge
// helper, NetworkManager's unmanaged list, the modules-load list and the pppd
// options.
func (l Layout) StaticFiles() ([]File, error) {
	items := []struct {
		src, dst string
		mode     fs.FileMode
	}{
		{deploy.UnitDaemon, filepath.Join(l.UnitDir, "palm-gwd.service"), 0o644},
		{deploy.UnitBridge, filepath.Join(l.UnitDir, "palm-gwd-nm-bridge.service"), 0o644},
		{deploy.UnitDnsmasq, filepath.Join(l.UnitDir, "palm-gw-dnsmasq.service"), 0o644},
		{deploy.BridgeScript, filepath.Join(l.LibDir, "nm-bridge.sh"), 0o755},
		{deploy.NMConf, filepath.Join(l.NMConfDir, "palm-gw.conf"), 0o644},
		{deploy.ModulesLoad, filepath.Join(l.ModulesDir, "palm-gw.conf"), 0o644},
		{deploy.PPPOptions, PPPOptionsPath, 0o644},
	}
	funcs := template.FuncMap{
		"BIN_DIR": func() string { return l.BinDir },
		"LIB_DIR": func() string { return l.LibDir },
	}
	var out []File
	for _, it := range items {
		text, err := deploy.FS.ReadFile(it.src)
		if err != nil {
			return nil, err
		}
		t, err := template.New(it.src).Funcs(funcs).Option("missingkey=error").Parse(string(text))
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", it.src, err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, nil); err != nil {
			return nil, fmt.Errorf("render %s: %w", it.src, err)
		}
		out = append(out, File{Path: it.dst, Mode: it.mode, Data: buf.Bytes()})
	}
	return out, nil
}

// WriteFiles places files under root (a staging directory for packaging, or
// "/"), creating directories. It returns the paths that changed and calls
// track for every path it is responsible for.
func WriteFiles(root string, files []File, track func(string)) (changed []string, err error) {
	for _, f := range files {
		ch, err := writeIfChanged(filepath.Join(root, f.Path), f.Data, f.Mode)
		if err != nil {
			return changed, err
		}
		if ch {
			changed = append(changed, f.Path)
		}
		if track != nil {
			track(f.Path)
		}
	}
	return changed, nil
}

// writeIfChanged writes data atomically unless the file already holds it
// with the same mode.
func writeIfChanged(path string, data []byte, mode fs.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		if st, err := os.Stat(path); err == nil && st.Mode().Perm() == mode.Perm() {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".palm-gw-")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(mode.Perm()); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, err
	}
	return true, nil
}
