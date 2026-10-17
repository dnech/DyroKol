package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/dnech/DyroKol/internal/store"
)

// Event levels.
const (
	levelInfo  = "info"
	levelWarn  = "warn"
	levelError = "error"
)

// maxPendingEvents bounds the events buffered between flushes.
const maxPendingEvents = 1000

// eventBuffer holds operator-visible events until the next flush.
type eventBuffer struct {
	mu      sync.Mutex
	pending []store.LogEntry
	dropped int
}

func (b *eventBuffer) add(e store.LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) >= maxPendingEvents {
		b.dropped++
		return
	}
	b.pending = append(b.pending, e)
}

func (b *eventBuffer) drain() []store.LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.pending
	if b.dropped > 0 {
		out = append(out, store.LogEntry{
			TS: time.Now().Unix(), Level: levelWarn, Source: "relay",
			Message: fmt.Sprintf("%d events dropped", b.dropped),
		})
	}
	b.pending, b.dropped = nil, 0
	return out
}

// event logs a message and records it for the admin UI log view.
func (s *Server) event(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.log.Print(msg)
	s.events.add(store.LogEntry{TS: time.Now().Unix(), Level: level, Source: "relay", Message: msg})
}

// flush records traffic, presence and events gathered since the last flush.
func (s *Server) flush() {
	s.mu.Lock()
	st := s.st
	if st == nil {
		s.mu.Unlock()
		return
	}
	f := store.Flush{TS: time.Now().Unix()}
	for id, p := range s.ports {
		if d := p.Counters.Drain(); !d.Zero() {
			f.Stats = append(f.Stats, store.StatUpdate{
				InPortID: id, BytesIn: d.In, BytesOut: d.Out, Conns: d.Conns, Active: d.Active,
			})
		}
	}
	for id, a := range s.agentStats {
		u := store.StatUpdate{InPortID: id, BytesIn: a.in, BytesOut: a.out, Conns: a.conns, Active: a.active}
		a.in, a.out, a.conns = 0, 0, 0
		if u.BytesIn != 0 || u.BytesOut != 0 || u.Conns != 0 || u.Active != 0 {
			f.Stats = append(f.Stats, u)
		}
	}
	for _, sess := range s.sessions {
		f.Online = append(f.Online, store.Seen{KeyHash: sess.keyHash, IP: sess.remoteIP})
	}
	s.mu.Unlock()

	for key, d := range s.peers.Drain() {
		f.Peers = append(f.Peers, store.Peer{
			IP: key.IP, InPortID: key.InPortID, Conns: d.Conns, BytesIn: d.In, BytesOut: d.Out,
		})
	}
	f.Logs = s.events.drain()
	if f.Empty() {
		return
	}
	if err := st.Record(f); err != nil {
		s.log.Printf("stats flush: %v", err)
	}
}
