package agent

import (
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"
)

type fakeAdapter struct {
	mu       sync.Mutex
	pairable []bool
	fail     bool
}

func (f *fakeAdapter) set(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("adapter gone")
	}
	f.pairable = append(f.pairable, on)
	return nil
}

func (f *fakeAdapter) last() (bool, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pairable) == 0 {
		return false, 0
	}
	return f.pairable[len(f.pairable)-1], len(f.pairable)
}

func TestWindowOpenCloseReasons(t *testing.T) {
	fa := &fakeAdapter{}
	var reasons []string
	var mu sync.Mutex
	w := NewWindow(fa.set, func(r string) { mu.Lock(); reasons = append(reasons, r); mu.Unlock() })

	if w.IsOpen() {
		t.Fatal("new window must be closed")
	}
	if _, ok := w.PIN(); ok {
		t.Fatal("no PIN while closed")
	}
	st, err := w.Open(time.Minute, false, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Open || !regexp.MustCompile(`^[0-9]{4}$`).MatchString(st.PIN) || time.Until(st.Expires) < 50*time.Second {
		t.Fatalf("bad state: %+v", st)
	}
	if on, _ := fa.last(); !on {
		t.Fatal("Pairable must be set true on open")
	}
	if _, err := w.Open(time.Minute, false, 4, ""); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("second open: %v", err)
	}
	// Not --one: pairing does not close it.
	w.Paired("AA:BB:CC:DD:EE:01")
	if !w.IsOpen() || len(w.State().Paired) != 1 {
		t.Fatalf("window should stay open: %+v", w.State())
	}
	if !w.Close(ReasonCancelled) {
		t.Fatal("Close should report it was open")
	}
	if w.Close(ReasonCancelled) {
		t.Fatal("second Close must report closed")
	}
	if on, n := fa.last(); on || n != 2 {
		t.Fatalf("Pairable must be false after close: %v %d", on, n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reasons) != 1 || reasons[0] != ReasonCancelled {
		t.Fatalf("reasons: %v", reasons)
	}
}

func TestWindowOneDeviceAndFixedPIN(t *testing.T) {
	fa := &fakeAdapter{}
	done := make(chan string, 1)
	w := NewWindow(fa.set, func(r string) { done <- r })
	st, err := w.Open(time.Minute, true, 4, "1234")
	if err != nil || st.PIN != "1234" || !st.One {
		t.Fatalf("%+v %v", st, err)
	}
	w.Paired("AA:BB:CC:DD:EE:01")
	select {
	case r := <-done:
		if r != ReasonOneDevice {
			t.Fatalf("reason %s", r)
		}
	case <-time.After(time.Second):
		t.Fatal("--one did not close the window")
	}
	if w.IsOpen() {
		t.Fatal("still open")
	}
	// Paired after close is ignored.
	w.Paired("AA:BB:CC:DD:EE:02")
}

func TestWindowTimeout(t *testing.T) {
	fa := &fakeAdapter{}
	done := make(chan string, 1)
	w := NewWindow(fa.set, func(r string) { done <- r })
	if _, err := w.Open(60*time.Millisecond, false, 6, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r != ReasonTimeout {
			t.Fatalf("reason %s", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timer did not fire")
	}
	if on, _ := fa.last(); on {
		t.Fatal("Pairable left on after timeout")
	}
}

func TestWindowAdapterFailure(t *testing.T) {
	fa := &fakeAdapter{fail: true}
	w := NewWindow(fa.set, nil)
	if _, err := w.Open(time.Minute, false, 4, ""); err == nil {
		t.Fatal("adapter failure must fail Open")
	}
	if w.IsOpen() {
		t.Fatal("window must not be open after a failed Open")
	}
}

func TestRandomPIN(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, err := RandomPIN(6)
		if err != nil || len(p) != 6 {
			t.Fatalf("%q %v", p, err)
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				t.Fatalf("non-digit in %q", p)
			}
		}
		seen[p] = true
	}
	if len(seen) < 45 {
		t.Fatalf("PINs not random enough: %d distinct of 50", len(seen))
	}
	if _, err := RandomPIN(0); err == nil {
		t.Fatal("length 0 must fail")
	}
	if _, err := RandomPIN(17); err == nil {
		t.Fatal("length 17 must fail")
	}
	if k, err := RandomPasskey(); err != nil || k > 999999 {
		t.Fatalf("passkey %d %v", k, err)
	}
}
