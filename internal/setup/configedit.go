package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"palm-gw/internal/config"
)

// Typed edits to config.toml for the ctl config.set command, the web UI and
// palm-gwctl set (PLAN.md section 8.8). Single-line keys are edited in place
// with SetKeys, keeping comments and alignment. [[firewall.allow]] is written
// as one managed block, since an array of tables cannot be edited a line at a
// time (section 8.12, "Saves to config.toml").

const (
	allowBegin = "# BEGIN palm-gw firewall.allow: written by the web UI and palm-gwctl firewall; edit with those"
	allowEnd   = "# END palm-gw firewall.allow"
)

// ErrUnknownKey means the path names no configuration key.
var ErrUnknownKey = errors.New("unknown configuration key")

// keyKinds maps "section.key" to the key's Go type, from the schema.
func keyKinds() map[string]reflect.Type {
	out := map[string]reflect.Type{}
	t := reflect.TypeOf(config.Config{})
	for i := 0; i < t.NumField(); i++ {
		sec := t.Field(i)
		sname := strings.Split(sec.Tag.Get("toml"), ",")[0]
		if sec.Type.Kind() != reflect.Struct {
			continue
		}
		for j := 0; j < sec.Type.NumField(); j++ {
			f := sec.Type.Field(j)
			out[sname+"."+strings.Split(f.Tag.Get("toml"), ",")[0]] = f.Type
		}
	}
	return out
}

// EditConfig returns text with the key at path set to value (JSON). It does
// not validate the result; the caller parses and validates before writing.
func EditConfig(text, path string, value json.RawMessage) (string, error) {
	typ, ok := keyKinds()[path]
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknownKey, path)
	}
	if path == "firewall.allow" {
		var rules []config.AllowRule
		if err := strictJSON(value, &rules); err != nil {
			return "", fmt.Errorf("firewall.allow: %w", err)
		}
		return SetFirewallAllow(text, rules), nil
	}
	section, key, _ := strings.Cut(path, ".")
	var lit string
	switch typ.Kind() {
	case reflect.String:
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return "", fmt.Errorf("%s: want a string: %w", path, err)
		}
		lit = TOMLString(v)
	case reflect.Bool:
		var v bool
		if err := json.Unmarshal(value, &v); err != nil {
			return "", fmt.Errorf("%s: want true or false: %w", path, err)
		}
		lit = fmt.Sprint(v)
	case reflect.Int, reflect.Uint32:
		var v float64
		if err := json.Unmarshal(value, &v); err != nil || v != math.Trunc(v) || v < 0 || v > math.MaxUint32 {
			return "", fmt.Errorf("%s: want a whole number of 0 or more", path)
		}
		if strings.HasPrefix(key, "class") {
			lit = fmt.Sprintf("0x%06X", uint32(v))
		} else {
			lit = fmt.Sprint(int64(v))
		}
	case reflect.Slice:
		if typ.Elem().Kind() != reflect.String {
			return "", fmt.Errorf("%s cannot be set this way", path)
		}
		var v []string
		if err := json.Unmarshal(value, &v); err != nil {
			return "", fmt.Errorf("%s: want a list of strings: %w", path, err)
		}
		lit = TOMLStrings(v)
	default:
		return "", fmt.Errorf("%s cannot be set this way", path)
	}
	return SetKeys(text, []KV{{Section: section, Key: key, Value: lit}}), nil
}

func strictJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// SetFirewallAllow replaces every [[firewall.allow]] table in text with one
// managed block holding rules, appended at the end. Tables outside the block
// are taken out (header, key lines and blank lines up to the next header or
// comment), as is a single-line `allow = …` under [firewall], which would
// clash with the tables.
func SetFirewallAllow(text string, rules []config.AllowRule) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var out []string
	section := ""
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		switch {
		case t == allowBegin:
			for i < len(lines) && strings.TrimSpace(lines[i]) != allowEnd {
				i++
			}
			continue
		case t == "[[firewall.allow]]":
			section = "[[firewall.allow]]"
			for i+1 < len(lines) {
				n := strings.TrimSpace(lines[i+1])
				if strings.HasPrefix(n, "[") || strings.HasPrefix(n, "#") {
					break
				}
				i++
			}
			continue
		case strings.HasPrefix(t, "["):
			section = t
		case section == "[firewall]" && strings.HasPrefix(t, "allow") && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(t, "allow")), "="):
			continue
		}
		out = append(out, l)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(rules) > 0 {
		out = append(out, "", allowBegin)
		for _, r := range rules {
			out = append(out, "[[firewall.allow]]",
				"host    = "+TOMLString(r.Host),
				"ports   = "+TOMLStrings(r.Ports))
			if r.Comment != "" {
				out = append(out, "comment = "+TOMLString(r.Comment))
			}
			out = append(out, "")
		}
		out = append(out, allowEnd)
	}
	return strings.Join(out, "\n") + "\n"
}
