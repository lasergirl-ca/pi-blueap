// Package agent implements org.bluez.Agent1 for the gateway (PLAN.md section
// 8.2): legacy PIN pairing gated by an operator-opened pairing window, and
// service authorisation gated by the allowlist.
package agent

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// Window is the pairing window state machine. While open, the adapter is
// Pairable and the agent answers RequestPinCode with the window's PIN; when
// it closes (timeout, one device paired, cancelled) the adapter goes back to
// Pairable=false.
type Window struct {
	mu       sync.Mutex
	open     bool
	pin      string
	expires  time.Time
	one      bool
	paired   []string
	timer    *time.Timer
	setPair  func(on bool) error // adapter Pairable toggle
	onClosed func(reason string)
}

// ErrAlreadyOpen is returned by Open when a window is open.
var ErrAlreadyOpen = errors.New("a pairing window is already open")

// Close reasons streamed to palm-gwctl (PLAN.md section 8.8).
const (
	ReasonTimeout   = "timeout"
	ReasonOneDevice = "one_device"
	ReasonCancelled = "cancelled"
	ReasonBlueZLost = "bluez_lost"
)

// NewWindow wires the window to the adapter's Pairable property and a close
// callback (used to publish the window_closed event).
func NewWindow(setPairable func(bool) error, onClosed func(reason string)) *Window {
	return &Window{setPair: setPairable, onClosed: onClosed}
}

// State is a snapshot for status output.
type State struct {
	Open    bool      `json:"open"`
	PIN     string    `json:"pin,omitempty"`
	Expires time.Time `json:"expires,omitempty"`
	One     bool      `json:"one,omitempty"`
	Paired  []string  `json:"paired,omitempty"`
}

// Open starts a window for d. fixedPIN overrides the random PIN when set.
func (w *Window) Open(d time.Duration, one bool, pinLength int, fixedPIN string) (State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.open {
		return w.stateLocked(), ErrAlreadyOpen
	}
	pin := fixedPIN
	if pin == "" {
		var err error
		pin, err = RandomPIN(pinLength)
		if err != nil {
			return State{}, err
		}
	}
	if w.setPair != nil {
		if err := w.setPair(true); err != nil {
			return State{}, fmt.Errorf("pairing window: %w", err)
		}
	}
	w.open = true
	w.pin = pin
	w.one = one
	w.paired = nil
	w.expires = time.Now().Add(d)
	w.timer = time.AfterFunc(d, func() { w.Close(ReasonTimeout) })
	return w.stateLocked(), nil
}

// Close ends the window if it is open. It reports whether it was open.
func (w *Window) Close(reason string) bool {
	w.mu.Lock()
	if !w.open {
		w.mu.Unlock()
		return false
	}
	w.open = false
	w.pin = ""
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	setPair, onClosed := w.setPair, w.onClosed
	w.mu.Unlock()
	if setPair != nil {
		_ = setPair(false)
	}
	if onClosed != nil {
		onClosed(reason)
	}
	return true
}

// PIN returns the current PIN if a window is open.
func (w *Window) PIN() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pin, w.open
}

// IsOpen reports whether a window is open.
func (w *Window) IsOpen() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.open
}

// Paired records a successful pairing. With --one the window closes.
func (w *Window) Paired(addr string) {
	w.mu.Lock()
	if !w.open {
		w.mu.Unlock()
		return
	}
	w.paired = append(w.paired, addr)
	one := w.one
	w.mu.Unlock()
	if one {
		w.Close(ReasonOneDevice)
	}
}

// State returns a snapshot.
func (w *Window) State() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stateLocked()
}

func (w *Window) stateLocked() State {
	s := State{Open: w.open}
	if w.open {
		s.PIN = w.pin
		s.Expires = w.expires
		s.One = w.one
		s.Paired = append([]string(nil), w.paired...)
	}
	return s
}

// RandomPIN returns n random decimal digits from crypto/rand. Leading zeros
// are allowed (PLAN.md section 8.2).
func RandomPIN(n int) (string, error) {
	if n < 1 || n > 16 {
		return "", fmt.Errorf("pin length %d out of range 1..16", n)
	}
	buf := make([]byte, n)
	for i := range buf {
		d, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		buf[i] = byte('0' + d.Int64())
	}
	return string(buf), nil
}

// RandomPasskey returns a 6-digit SSP passkey.
func RandomPasskey() (uint32, error) {
	d, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return 0, err
	}
	return uint32(d.Int64()), nil
}
