// Package state persists what the daemon learns at runtime and must keep
// across restarts: the dynamic allowlist of paired devices with first_paired,
// last_seen and an operator label (PLAN.md section 8.9). The file is written
// atomically (temp + rename).
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultDir is where systemd's StateDirectory=palm-gw puts us.
const DefaultDir = "/var/lib/palm-gw"

// Device is one dynamically allowlisted device.
type Device struct {
	Address     string    `json:"address"`
	Name        string    `json:"name,omitempty"`
	Label       string    `json:"label,omitempty"`
	FirstPaired time.Time `json:"first_paired"`
	LastSeen    time.Time `json:"last_seen"`
}

type file struct {
	Version int               `json:"version"`
	Devices map[string]Device `json:"devices"`
	Class   uint32            `json:"class,omitempty"` // persisted `class toggle` choice; 0 = use config
}

// Store is the state file.
type Store struct {
	path string
	mu   sync.Mutex
	f    file
}

// Path returns the state file path for a directory.
func Path(dir string) string { return filepath.Join(dir, "state.json") }

// Open loads the state file, or starts empty when it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, f: file{Version: 1, Devices: map[string]Device{}}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	if err := json.Unmarshal(data, &s.f); err != nil {
		return nil, fmt.Errorf("state: %s: %w", path, err)
	}
	if s.f.Devices == nil {
		s.f.Devices = map[string]Device{}
	}
	return s, nil
}

// Norm canonicalises a Bluetooth address (upper case).
func Norm(addr string) string { return strings.ToUpper(strings.TrimSpace(addr)) }

// Devices returns every device sorted by address.
func (s *Store) Devices() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Device, 0, len(s.f.Devices))
	for _, d := range s.f.Devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// Has reports whether addr is in the dynamic allowlist.
func (s *Store) Has(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.f.Devices[Norm(addr)]
	return ok
}

// Get returns one device.
func (s *Store) Get(addr string) (Device, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.f.Devices[Norm(addr)]
	return d, ok
}

// Add inserts a device (or refreshes its name and last_seen) and saves.
func (s *Store) Add(addr, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	addr = Norm(addr)
	now := time.Now()
	d, ok := s.f.Devices[addr]
	if !ok {
		d = Device{Address: addr, FirstPaired: now}
	}
	if name != "" {
		d.Name = name
	}
	d.LastSeen = now
	s.f.Devices[addr] = d
	return s.save()
}

// Seen refreshes last_seen (and the name) for a known device; unknown
// addresses are ignored.
func (s *Store) Seen(addr, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.f.Devices[Norm(addr)]
	if !ok {
		return nil
	}
	if name != "" {
		d.Name = name
	}
	d.LastSeen = time.Now()
	s.f.Devices[d.Address] = d
	return s.save()
}

// Remove deletes a device and saves. It reports whether it was present.
func (s *Store) Remove(addr string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	addr = Norm(addr)
	if _, ok := s.f.Devices[addr]; !ok {
		return false, nil
	}
	delete(s.f.Devices, addr)
	return true, s.save()
}

// SetLabel sets the operator label of a known device.
func (s *Store) SetLabel(addr, label string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.f.Devices[Norm(addr)]
	if !ok {
		return fmt.Errorf("state: %s is not in the allowlist", Norm(addr))
	}
	d.Label = label
	s.f.Devices[d.Address] = d
	return s.save()
}

// Class returns the persisted Class of Device override (0 = none).
func (s *Store) Class() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Class
}

// SetClass persists a Class of Device override.
func (s *Store) SetClass(c uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Class = c
	return s.save()
}

// save writes the file atomically. Callers hold s.mu.
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	data, err := json.MarshalIndent(&s.f, "", "  ")
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o640); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("state: %w", err)
	}
	return nil
}
