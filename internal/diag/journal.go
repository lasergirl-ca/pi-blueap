// Package diag holds the web UI's logs and diagnostics helpers (PLAN.md
// section 8.12, D-057): reading the journal, btmon captures and support
// bundles, and redaction.
package diag

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Units are the journals the logs page reads, by the names the page uses.
var Units = map[string]string{
	"palm-gwd":  "palm-gwd.service",
	"dnsmasq":   "palm-gw-dnsmasq.service",
	"bluetooth": "bluetooth.service",
	"apply":     "palm-gw-apply.service",
	"bridge":    "palm-gwd-nm-bridge.service",
}

// UnitNames is Units' keys in display order.
var UnitNames = []string{"palm-gwd", "bluetooth", "dnsmasq", "bridge", "apply"}

// Entry is one journal line, with palm-gwd's JSON log decoded.
type Entry struct {
	Time   time.Time         `json:"time"`
	Unit   string            `json:"unit"`  // a key of Units
	Level  string            `json:"level"` // DEBUG, INFO, WARN, ERROR
	Msg    string            `json:"msg"`
	Event  string            `json:"event,omitempty"`
	Addr   string            `json:"addr,omitempty"`
	Fields map[string]string `json:"fields,omitempty"` // palm-gwd's other attributes
	Cursor string            `json:"cursor"`
}

// priority levels (syslog) to slog's names.
var levels = map[string]string{"0": "ERROR", "1": "ERROR", "2": "ERROR", "3": "ERROR", "4": "WARN", "5": "INFO", "6": "INFO", "7": "DEBUG"}

// ParseEntry decodes one line of `journalctl -o json`.
func ParseEntry(line []byte) (Entry, bool) {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return Entry{}, false
	}
	str := func(k string) string { s, _ := raw[k].(string); return s }
	e := Entry{Cursor: str("__CURSOR"), Level: levels[str("PRIORITY")]}
	if us, err := strconv.ParseInt(str("__REALTIME_TIMESTAMP"), 10, 64); err == nil {
		e.Time = time.UnixMicro(us)
	}
	// systemd's own lines about a unit ("Started …") come from init.scope
	// with UNIT naming the unit they are about.
	unit := str("UNIT")
	if unit == "" {
		unit = str("_SYSTEMD_UNIT")
	}
	for k, v := range Units {
		if v == unit {
			e.Unit = k
		}
	}
	if e.Unit == "" {
		e.Unit = strings.TrimSuffix(unit, ".service")
	}
	switch m := raw["MESSAGE"].(type) {
	case string:
		e.Msg = m
	case []any: // not UTF-8: journald sends bytes
		b := make([]byte, 0, len(m))
		for _, x := range m {
			if f, ok := x.(float64); ok {
				b = append(b, byte(f))
			}
		}
		e.Msg = strings.ToValidUTF8(string(b), "?")
	}
	if strings.HasPrefix(e.Msg, "{") {
		var slog map[string]any
		if json.Unmarshal([]byte(e.Msg), &slog) == nil {
			e.Fields = map[string]string{}
			for k, v := range slog {
				s, ok := v.(string)
				if !ok {
					b, _ := json.Marshal(v)
					s = string(b)
				}
				switch k {
				case "time":
				case "level":
					e.Level = s
				case "msg":
					e.Msg = s
				case "event":
					e.Event = s
				case "addr":
					e.Addr = s
				default:
					e.Fields[k] = s
				}
			}
			if len(e.Fields) == 0 {
				e.Fields = nil
			}
		}
	}
	return e, true
}

// FieldList is Fields as "k=v" in key order, for display.
func (e Entry) FieldList() []string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+e.Fields[k])
	}
	return out
}

// Filter selects entries: by event name (exact, or a prefix ending in *),
// and by a Bluetooth address or any text, matched case-insensitively
// anywhere in the line.
type Filter struct {
	Event string
	Text  string
}

// Match reports whether e passes f.
func (f Filter) Match(e Entry) bool {
	if f.Event != "" {
		if p, ok := strings.CutSuffix(f.Event, "*"); ok {
			if !strings.HasPrefix(e.Event, p) {
				return false
			}
		} else if e.Event != f.Event {
			return false
		}
	}
	if f.Text != "" {
		t := strings.ToLower(f.Text)
		hay := strings.ToLower(e.Msg + " " + e.Addr + " " + strings.Join(e.FieldList(), " "))
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// ReadOptions selects what Read returns.
type ReadOptions struct {
	Units  []string // keys of Units; empty means palm-gwd, dnsmasq and bluetooth
	Lines  int      // the newest this many matching lines; 0 means 500
	After  string   // only entries after this cursor (Follow)
	Filter Filter
}

func (o ReadOptions) args() ([]string, error) {
	units := o.Units
	if len(units) == 0 {
		units = []string{"palm-gwd", "dnsmasq", "bluetooth"}
	}
	args := []string{"-o", "json", "--no-pager", "-q"}
	for _, u := range units {
		full, ok := Units[u]
		if !ok {
			return nil, fmt.Errorf("logs: unknown unit %q", u)
		}
		args = append(args, "-u", full)
	}
	if o.After != "" {
		args = append(args, "--after-cursor="+o.After)
	} else {
		n := o.Lines
		if n <= 0 {
			n = 500
		}
		if o.Filter != (Filter{}) {
			n = max(n*10, 5000) // filtering happens here, so read further back
		}
		args = append(args, "-n", strconv.Itoa(n))
	}
	return args, nil
}

// Read runs journalctl and returns matching entries oldest first, and the
// cursor of the last entry read (matching or not), for the next Follow.
func Read(ctx context.Context, o ReadOptions) ([]Entry, string, error) {
	args, err := o.args()
	if err != nil {
		return nil, "", err
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("journalctl: %w", err)
	}
	entries, cursor := Collect(out, o)
	if err := cmd.Wait(); err != nil && len(entries) == 0 {
		return nil, "", fmt.Errorf("journalctl: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if cursor == "" {
		cursor = o.After
	}
	return entries, cursor, nil
}

// Collect reads journal JSON lines, keeping the newest o.Lines matches.
func Collect(r io.Reader, o ReadOptions) ([]Entry, string) {
	max := o.Lines
	if max <= 0 {
		max = 500
	}
	var out []Entry
	cursor := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		e, ok := ParseEntry(sc.Bytes())
		if !ok {
			continue
		}
		cursor = e.Cursor
		if o.Filter.Match(e) {
			out = append(out, e)
		}
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out, cursor
}
