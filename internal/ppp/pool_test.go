package ppp

import (
	"errors"
	"net/netip"
	"testing"
)

func TestPool(t *testing.T) {
	p, err := NewPool("10.42.0.10", "10.42.0.12")
	if err != nil {
		t.Fatal(err)
	}
	if p.Size() != 3 {
		t.Fatalf("size %d", p.Size())
	}
	var got []string
	for i := 0; i < 3; i++ {
		a, err := p.Get()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, a.String())
	}
	if got[0] != "10.42.0.10" || got[2] != "10.42.0.12" {
		t.Errorf("order %v", got)
	}
	if _, err := p.Get(); !errors.Is(err, ErrPoolExhausted) {
		t.Errorf("exhaustion: %v", err)
	}
	mid := mustAddr(t, "10.42.0.11")
	p.Put(mid)
	p.Put(mid)
	if a, err := p.Get(); err != nil || a != mid {
		t.Errorf("reuse: got %v %v, want %v", a, err, mid)
	}
	p.Put(mustAddr(t, "10.42.0.10"))
	p.Put(mustAddr(t, "10.42.0.12"))
	if a, _ := p.Get(); a.String() != "10.42.0.10" {
		t.Errorf("lowest free first: got %v", a)
	}
	if p.InUse() != 2 {
		t.Errorf("in use %d", p.InUse())
	}
	for _, bad := range [][2]string{{"10.42.0.12", "10.42.0.10"}, {"x", "10.42.0.1"}, {"::1", "::2"}} {
		if _, err := NewPool(bad[0], bad[1]); err == nil {
			t.Errorf("NewPool(%v) accepted", bad)
		}
	}
	one, _ := NewPool("10.42.0.99", "10.42.0.99")
	if one.Size() != 1 {
		t.Error("single-address pool")
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
