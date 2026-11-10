// Package ppp spawns pppd for each RFCOMM session and keeps the session
// table (PLAN.md section 8.5).
package ppp

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
)

// ErrPoolExhausted means every peer address is in use.
var ErrPoolExhausted = errors.New("ppp: address pool exhausted")

// Pool hands out peer addresses from an inclusive IPv4 range, lowest free
// first, and holds each until it is released.
type Pool struct {
	mu    sync.Mutex
	first netip.Addr
	size  int
	used  map[netip.Addr]bool
}

// NewPool makes a pool for [first, last].
func NewPool(first, last string) (*Pool, error) {
	a, err := netip.ParseAddr(first)
	if err != nil {
		return nil, err
	}
	b, err := netip.ParseAddr(last)
	if err != nil {
		return nil, err
	}
	if !a.Is4() || !b.Is4() || b.Less(a) {
		return nil, fmt.Errorf("ppp: bad pool range %s-%s", a, b)
	}
	return &Pool{first: a, size: int(u32(b)-u32(a)) + 1, used: map[netip.Addr]bool{}}, nil
}

func u32(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// Get returns the lowest free address.
func (p *Pool) Get() (netip.Addr, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.first
	for i := 0; i < p.size; i++ {
		if !p.used[a] {
			p.used[a] = true
			return a, nil
		}
		a = a.Next()
	}
	return netip.Addr{}, ErrPoolExhausted
}

// Put releases a; releasing an address twice is harmless.
func (p *Pool) Put(a netip.Addr) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.used, a)
}

// InUse is the number of addresses handed out.
func (p *Pool) InUse() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.used)
}

// Size is the number of addresses in the range.
func (p *Pool) Size() int { return p.size }
