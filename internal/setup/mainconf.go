// Package setup holds what `palm-gw setup` and the installer need to change
// on the system. Phase 1 ships the /etc/bluetooth/main.conf editor
// (PLAN.md section 8.1); detection, the wizard and template rendering of the
// whole deploy/ tree arrive in Phase 6.
package setup

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"palm-gw/internal/config"
)

// Markers of the managed block inside a section, and the prefix used to park
// a pre-existing live key that the block overrides so uninstall can restore it.
const (
	BeginMarker    = "# BEGIN palm-gw"
	EndMarker      = "# END palm-gw"
	DisabledPrefix = "#palm-gw-disabled: "
)

// Setting is one key the managed block sets.
type Setting struct {
	Section string
	Key     string
	Value   string
}

// ManagedSettings returns the main.conf keys from PLAN.md section 8.1 for a
// configuration. class is the effective Class of Device (config, or the
// persisted `class toggle` override).
func ManagedSettings(cfg *config.Config, class uint32) []Setting {
	return []Setting{
		{"General", "Name", cfg.Bluetooth.Name},
		{"General", "Class", fmt.Sprintf("0x%06X", class)},
		{"General", "DiscoverableTimeout", "0"},
		{"General", "PairableTimeout", "0"},
		{"General", "JustWorksRepairing", "always"},
		{"General", "ControllerMode", "dual"},
		// D-037: without this, BlueZ holds a trusted Palm's profile connection
		// until it has browsed the Palm's SDP record, and after a bluetoothd
		// restart nothing starts that browse, so the Palm never gets past its
		// connecting screen.
		{"General", "ReverseServiceDiscovery", "false"},
		{"Policy", "AutoEnable", "true"},
	}
}

var sectionRe = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)

// Remove strips every managed block and restores keys the block had parked.
func Remove(text string) (out string, changed bool) {
	lines := strings.Split(text, "\n")
	var res []string
	inBlock := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case t == BeginMarker:
			inBlock, changed = true, true
			continue
		case t == EndMarker:
			inBlock, changed = false, true
			continue
		case inBlock:
			changed = true
			continue
		case strings.HasPrefix(l, DisabledPrefix):
			res = append(res, strings.TrimPrefix(l, DisabledPrefix))
			changed = true
			continue
		}
		res = append(res, l)
	}
	return strings.Join(res, "\n"), changed
}

// Apply returns text with a managed block in each relevant section that sets
// the given keys, with any live occurrences of those keys parked. It is
// idempotent: Apply(Apply(x)) == Apply(x), and Remove(Apply(x)) == x.
func Apply(text string, settings []Setting) (out string, changed bool) {
	clean, _ := Remove(text)
	var lines []string
	if clean != "" {
		lines = strings.Split(clean, "\n")
	}

	// Group settings by section, keeping order.
	bySection := map[string][]Setting{}
	var order []string
	for _, s := range settings {
		if _, ok := bySection[s.Section]; !ok {
			order = append(order, s.Section)
		}
		bySection[s.Section] = append(bySection[s.Section], s)
	}
	keysOf := func(sec string) map[string]bool {
		m := map[string]bool{}
		for _, s := range bySection[sec] {
			m[strings.ToLower(s.Key)] = true
		}
		return m
	}
	block := func(sec string) []string {
		b := []string{BeginMarker}
		for _, s := range bySection[sec] {
			b = append(b, s.Key+" = "+s.Value)
		}
		return append(b, EndMarker)
	}

	var res []string
	current := ""
	seen := map[string]bool{}
	var managedKeys map[string]bool
	for _, l := range lines {
		if m := sectionRe.FindStringSubmatch(l); m != nil {
			current = m[1]
			res = append(res, l)
			if _, ok := bySection[current]; ok && !seen[current] {
				seen[current] = true
				res = append(res, block(current)...)
				managedKeys = keysOf(current)
			} else {
				managedKeys = nil
			}
			continue
		}
		if managedKeys != nil {
			if k, ok := liveKey(l); ok && managedKeys[strings.ToLower(k)] {
				res = append(res, DisabledPrefix+l)
				continue
			}
		}
		res = append(res, l)
	}
	for _, sec := range order {
		if seen[sec] {
			continue
		}
		if len(res) > 0 && strings.TrimSpace(res[len(res)-1]) != "" {
			res = append(res, "")
		}
		res = append(res, "["+sec+"]")
		res = append(res, block(sec)...)
	}
	out = strings.Join(res, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, out != text
}

// liveKey returns the key of an uncommented "Key = value" line.
func liveKey(l string) (string, bool) {
	t := strings.TrimSpace(l)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "[") {
		return "", false
	}
	k, _, ok := strings.Cut(t, "=")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(k), true
}

// ApplyFile rewrites path in place (same mode), creating it if absent.
func ApplyFile(path string, settings []Setting) (changed bool, err error) {
	return rewrite(path, func(text string) (string, bool) { return Apply(text, settings) })
}

// RemoveFile strips the managed block from path.
func RemoveFile(path string) (changed bool, err error) {
	return rewrite(path, Remove)
}

func rewrite(path string, fn func(string) (string, bool)) (bool, error) {
	mode := os.FileMode(0o644)
	text := ""
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
		data, err := os.ReadFile(path)
		if err != nil {
			return false, fmt.Errorf("main.conf: %w", err)
		}
		text = string(data)
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("main.conf: %w", err)
	}
	out, changed := fn(text)
	if !changed {
		return false, nil
	}
	tmp := path + ".palm-gw.tmp"
	if err := os.WriteFile(tmp, []byte(out), mode); err != nil {
		return false, fmt.Errorf("main.conf: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("main.conf: %w", err)
	}
	return true, nil
}
