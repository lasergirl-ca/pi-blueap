package setup

import (
	"fmt"
	"regexp"
	"strings"
)

// KV is one config.toml key to set. Value is a TOML literal: use TOMLString,
// TOMLStrings or a bare bool or number.
type KV struct {
	Section string
	Key     string
	Value   string
}

func (kv KV) String() string { return kv.Section + "." + kv.Key + " = " + kv.Value }

// TOMLString quotes s as a TOML basic string.
func TOMLString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// TOMLStrings is a TOML array of strings.
func TOMLStrings(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = TOMLString(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

var (
	tomlSectionRe = regexp.MustCompile(`^\s*\[([A-Za-z0-9_.-]+)\]`)
	tomlKeyRe     = regexp.MustCompile(`^(\s*)([A-Za-z0-9_-]+)(\s*=\s*)(.*)$`)
)

// SetKeys edits config.toml text in place: each key's value is replaced on
// its own line, keeping the line's alignment and trailing comment, so a
// re-run of setup changes only what the user changed (PLAN.md section 8.11).
// A key missing from its section is added after the section's last key; a
// missing section is appended. Only single-line values are handled, which is
// every key setup writes.
func SetKeys(text string, kvs []KV) string {
	lines := strings.Split(text, "\n")
	for _, kv := range kvs {
		lines = setKey(lines, kv)
	}
	return strings.Join(lines, "\n")
}

func setKey(lines []string, kv KV) []string {
	section := ""
	start, lastKey := -1, -1
	for i, l := range lines {
		if m := tomlSectionRe.FindStringSubmatch(l); m != nil {
			if section == kv.Section && start >= 0 {
				break // left the section without finding the key
			}
			section = m[1]
			if section == kv.Section {
				start, lastKey = i, i
			}
			continue
		}
		if section != kv.Section {
			continue
		}
		m := tomlKeyRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		lastKey = i
		if m[2] != kv.Key {
			continue
		}
		value, comment := splitComment(m[4])
		pad := ""
		if comment != "" {
			// Keep the comment where it was when the new value fits.
			if w := len(value) - len(kv.Value); w > 0 {
				pad = strings.Repeat(" ", w)
			} else {
				pad = " "
			}
			comment = pad + comment
		}
		lines[i] = m[1] + m[2] + m[3] + kv.Value + strings.TrimRight(comment, " ")
		if comment == "" {
			lines[i] = strings.TrimRight(lines[i], " ")
		}
		return lines
	}
	line := kv.Key + " = " + kv.Value
	if start < 0 {
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
		return append(lines, "", "["+kv.Section+"]", line, "")
	}
	out := append([]string{}, lines[:lastKey+1]...)
	out = append(out, line)
	return append(out, lines[lastKey+1:]...)
}

// splitComment splits a value from a trailing # comment, ignoring # inside
// quoted strings. The returned value keeps its trailing spaces, so the
// comment column can be preserved.
func splitComment(s string) (value, comment string) {
	inStr := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if inStr {
				i++
			}
		case '"':
			inStr = !inStr
		case '#':
			if !inStr {
				return s[:i], s[i:]
			}
		}
	}
	return s, ""
}
