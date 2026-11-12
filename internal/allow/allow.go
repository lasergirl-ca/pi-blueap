// Package allow is the device allowlist: static entries from
// [bluetooth].allow, dynamic entries from the state file (devices paired
// through a pairing window or added with `palm-gwctl allow`), and the
// allow_any escape hatch. It is the gate behind Agent1.AuthorizeService
// (PLAN.md section 8.2) and later the NAP server.
package allow

import (
	"sort"
	"sync"
	"time"

	"palm-gw/internal/state"
)

// Source says why an address is allowed.
type Source string

const (
	SourceStatic  Source = "static"
	SourceDynamic Source = "dynamic"
	SourceAny     Source = "allow_any"
)

// Entry is one allowlisted address for status output.
type Entry struct {
	Address     string    `json:"address"`
	Source      Source    `json:"source"`
	Name        string    `json:"name,omitempty"`
	Label       string    `json:"label,omitempty"`
	FirstPaired time.Time `json:"first_paired,omitzero"` // dynamic entries only
	LastSeen    time.Time `json:"last_seen,omitzero"`
}

// List is the combined allowlist.
type List struct {
	mu       sync.RWMutex
	static   map[string]struct{}
	store    *state.Store
	allowAny bool
}

// New builds a list from the static config entries and the state store.
func New(static []string, store *state.Store, allowAny bool) *List {
	l := &List{static: map[string]struct{}{}, store: store, allowAny: allowAny}
	for _, a := range static {
		l.static[state.Norm(a)] = struct{}{}
	}
	return l
}

// Reconfigure replaces the static entries and allow_any (used by reload).
func (l *List) Reconfigure(static []string, allowAny bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.static = map[string]struct{}{}
	for _, a := range static {
		l.static[state.Norm(a)] = struct{}{}
	}
	l.allowAny = allowAny
}

// Check reports whether addr may use the gateway and why.
func (l *List) Check(addr string) (bool, Source) {
	addr = state.Norm(addr)
	l.mu.RLock()
	_, static := l.static[addr]
	any := l.allowAny
	l.mu.RUnlock()
	switch {
	case static:
		return true, SourceStatic
	case l.store != nil && l.store.Has(addr):
		return true, SourceDynamic
	case any:
		return true, SourceAny
	}
	return false, ""
}

// Allowed is Check without the reason.
func (l *List) Allowed(addr string) bool {
	ok, _ := l.Check(addr)
	return ok
}

// Add puts addr in the dynamic allowlist.
func (l *List) Add(addr, name string) error { return l.store.Add(addr, name) }

// Remove takes addr out of the dynamic allowlist. Static entries cannot be
// removed at runtime; the caller reports that.
func (l *List) Remove(addr string) (removed bool, static bool, err error) {
	addr = state.Norm(addr)
	l.mu.RLock()
	_, static = l.static[addr]
	l.mu.RUnlock()
	removed, err = l.store.Remove(addr)
	return removed, static, err
}

// Entries lists static then dynamic entries, each sorted by address.
func (l *List) Entries() []Entry {
	l.mu.RLock()
	statics := make([]string, 0, len(l.static))
	for a := range l.static {
		statics = append(statics, a)
	}
	l.mu.RUnlock()
	sort.Strings(statics)
	out := make([]Entry, 0, len(statics))
	for _, a := range statics {
		out = append(out, Entry{Address: a, Source: SourceStatic})
	}
	if l.store != nil {
		for _, d := range l.store.Devices() {
			out = append(out, Entry{Address: d.Address, Source: SourceDynamic, Name: d.Name, Label: d.Label, FirstPaired: d.FirstPaired, LastSeen: d.LastSeen})
		}
	}
	return out
}

// AllowAny reports the escape hatch.
func (l *List) AllowAny() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.allowAny
}
