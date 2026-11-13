// Package devkey keeps the device keys palm-gw issues to Palms (D-063): a
// long-lived secret a Palm logs in with instead of the password, which it
// never stores. The gateway keeps only each secret's SHA-256, with the
// account and role it was issued for, the Palm's name and when it was last
// used, in device-keys.json (0600, written atomically). A key is revoked by
// deleting it.
package devkey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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

// FileName is the store's file in the state directory.
const FileName = "device-keys.json"

// prefix starts every key, so one is easy to recognise in a log or a
// paste, and versions the format.
const prefix = "pgk1."

// MaxKeys bounds the store; an issue beyond it is refused.
const MaxKeys = 64

// Entry is one issued key, as listed. The secret itself is never kept.
type Entry struct {
	ID       string    `json:"id"`
	Hash     string    `json:"hash,omitempty"` // hex SHA-256 of the secret; not in listings
	User     string    `json:"user"`
	Role     string    `json:"role"`
	Name     string    `json:"name"` // the Palm's name, as it gave it
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used,omitzero"`
	LastIP   string    `json:"last_ip,omitempty"`
}

// Store is the key file.
type Store struct {
	path string
	mu   sync.Mutex
	keys []Entry
	now  func() time.Time
}

// ErrUnknown means no key matches.
var ErrUnknown = errors.New("unknown or revoked device key")

// Open loads the store, or starts empty when the file does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("devkey: %w", err)
	}
	if err := json.Unmarshal(b, &s.keys); err != nil {
		return nil, fmt.Errorf("devkey: %s: %w", path, err)
	}
	return s, nil
}

// SetClock replaces the clock, for tests.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.keys, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".device-keys-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func hashOf(secret []byte) string {
	h := sha256.Sum256(secret)
	return hex.EncodeToString(h[:])
}

// Issue makes a key for user (role admin or viewer) on the Palm called
// name and returns it, whole, once: "pgk1.<id>.<secret>".
func (s *Store) Issue(user, role, name string) (string, Entry, error) {
	if user == "" || (role != "admin" && role != "viewer") {
		return "", Entry{}, errors.New("devkey: a key needs a user and a role")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Palm"
	}
	if len(name) > 48 {
		name = name[:48]
	}
	id := make([]byte, 6)
	secret := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return "", Entry{}, err
	}
	if _, err := rand.Read(secret); err != nil {
		return "", Entry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.keys) >= MaxKeys {
		return "", Entry{}, fmt.Errorf("devkey: %d keys already; revoke some on the settings page", MaxKeys)
	}
	e := Entry{ID: hex.EncodeToString(id), Hash: hashOf([]byte(hex.EncodeToString(secret))), User: user, Role: role,
		Name: name, Created: s.now().UTC().Truncate(time.Second)}
	s.keys = append(s.keys, e)
	if err := s.save(); err != nil {
		s.keys = s.keys[:len(s.keys)-1]
		return "", Entry{}, err
	}
	return prefix + e.ID + "." + hex.EncodeToString(secret), e.listed(), nil
}

// Check finds the key and notes its use from ip. Every key is compared,
// in constant time, so the time taken says nothing about which part of a
// key was right.
func (s *Store) Check(key, ip string) (Entry, error) {
	rest, ok := strings.CutPrefix(key, prefix)
	id, secret, ok2 := strings.Cut(rest, ".")
	if !ok || !ok2 || id == "" || secret == "" {
		return Entry{}, ErrUnknown
	}
	h := hashOf([]byte(secret))
	s.mu.Lock()
	defer s.mu.Unlock()
	hit := -1
	for i := range s.keys {
		idOK := subtle.ConstantTimeCompare([]byte(s.keys[i].ID), []byte(id))
		hashOK := subtle.ConstantTimeCompare([]byte(s.keys[i].Hash), []byte(h))
		if idOK&hashOK == 1 {
			hit = i
		}
	}
	if hit < 0 {
		return Entry{}, ErrUnknown
	}
	s.keys[hit].LastUsed = s.now().UTC().Truncate(time.Second)
	s.keys[hit].LastIP = ip
	_ = s.save() // a failed note of the last use does not refuse the login
	return s.keys[hit].listed(), nil
}

// List returns every key, newest first, without hashes.
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.keys))
	for _, e := range s.keys {
		out = append(out, e.listed())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// Revoke deletes the key with this id.
func (s *Store) Revoke(id string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.keys {
		if e.ID == id {
			s.keys = append(s.keys[:i], s.keys[i+1:]...)
			if err := s.save(); err != nil {
				s.keys = append(s.keys[:i], append([]Entry{e}, s.keys[i:]...)...)
				return Entry{}, err
			}
			return e.listed(), nil
		}
	}
	return Entry{}, ErrUnknown
}

func (e Entry) listed() Entry {
	e.Hash = ""
	return e
}
