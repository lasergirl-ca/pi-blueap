// Package event is the daemon's in-process event broker. Every notable thing
// (pin_requested, paired, pairing_rejected, bluez_lost, session_start, ...)
// is published once; the logger, the `palm-gwctl pair` stream and later the
// web UI's SSE feed all subscribe. Event names are the stable strings from
// PLAN.md section 8.9.
package event

import (
	"sync"
	"time"
)

// Event is one daemon event. Addr and Name identify the Bluetooth device when
// there is one; Fields carries anything else (pin, reason, uuid, ifname).
type Event struct {
	Seq    uint64            `json:"seq"` // 1, 2, 3, ... in publish order, for "events since N"
	Time   time.Time         `json:"time"`
	Event  string            `json:"event"`
	Addr   string            `json:"addr,omitempty"`
	Name   string            `json:"name,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
}

// Attrs flattens the event for slog: event=, addr=, name= and every field.
func (e Event) Attrs() []any {
	out := []any{"event", e.Event}
	if e.Addr != "" {
		out = append(out, "addr", e.Addr)
	}
	if e.Name != "" {
		out = append(out, "name", e.Name)
	}
	for k, v := range e.Fields {
		out = append(out, k, v)
	}
	return out
}

// Bus fans events out to subscribers. Publishing never blocks: a subscriber
// that falls more than bufSize behind loses events rather than stalling the
// daemon.
type Bus struct {
	mu     sync.Mutex
	subs   map[int]chan Event
	next   int
	seq    uint64
	recent []Event // the last historySize events, oldest first
}

const (
	bufSize     = 64
	historySize = 200 // kept for the web UI's event list and "events since N"
)

// New returns an empty bus.
func New() *Bus { return &Bus{subs: map[int]chan Event{}} }

// Publish stamps, numbers, records and delivers e to every subscriber.
func (b *Bus) Publish(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e.Seq = b.seq
	b.recent = append(b.recent, e)
	if len(b.recent) > historySize {
		b.recent = append(b.recent[:0:0], b.recent[len(b.recent)-historySize:]...)
	}
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Emit is Publish with the common fields spelled out.
func (b *Bus) Emit(name, addr, devName string, kv ...string) {
	e := Event{Event: name, Addr: addr, Name: devName}
	if len(kv) > 0 {
		e.Fields = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			e.Fields[kv[i]] = kv[i+1]
		}
	}
	b.Publish(e)
}

// Subscribe returns a channel of future events and a function that stops it.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, bufSize)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
}

// Recent returns the recorded events with Seq above since, oldest first, at
// most max of them (the newest; 0 = no limit).
func (b *Bus) Recent(since uint64, max int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Event
	for _, e := range b.recent {
		if e.Seq > since {
			out = append(out, e)
		}
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return append([]Event(nil), out...)
}
