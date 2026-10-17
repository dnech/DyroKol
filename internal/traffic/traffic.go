// Package traffic holds the bookkeeping shared by every ingress port, on the
// relay and on agents: byte/connection counters drained as deltas, per-address
// peer accounting and the set of live connections.
package traffic

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dnech/DyroKol/internal/copyio"
)

// Counters tracks the traffic of one ingress port. In is visitor → target,
// Out is target → visitor.
type Counters struct {
	In     atomic.Int64
	Out    atomic.Int64
	Conns  atomic.Int64 // finished connections
	Active atomic.Int64

	flushedIn, flushedOut, flushedConns atomic.Int64
}

// Delta is what changed since the previous Drain, plus the current gauge.
type Delta struct {
	In, Out, Conns int64
	Active         int
}

// Zero reports whether nothing happened and nothing is open.
func (d Delta) Zero() bool { return d.In == 0 && d.Out == 0 && d.Conns == 0 && d.Active == 0 }

// Drain returns the counter deltas since the previous call.
func (c *Counters) Drain() Delta {
	in, out, conns := c.In.Load(), c.Out.Load(), c.Conns.Load()
	return Delta{
		In:     in - c.flushedIn.Swap(in),
		Out:    out - c.flushedOut.Swap(out),
		Conns:  conns - c.flushedConns.Swap(conns),
		Active: int(c.Active.Load()),
	}
}

// PeerKey identifies a remote address seen on one ingress port.
type PeerKey struct {
	IP       string
	InPortID int64
}

// PeerDelta is the traffic of one remote address since the last Drain.
type PeerDelta struct {
	Conns, In, Out int64
}

// Peers accumulates per-address deltas between drains. The zero value is
// ready to use.
type Peers struct {
	mu sync.Mutex
	m  map[PeerKey]*PeerDelta
}

// Add merges a delta for key.
func (p *Peers) Add(key PeerKey, d PeerDelta) {
	if key.IP == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[PeerKey]*PeerDelta{}
	}
	cur := p.m[key]
	if cur == nil {
		cur = &PeerDelta{}
		p.m[key] = cur
	}
	cur.Conns += d.Conns
	cur.In += d.In
	cur.Out += d.Out
}

// Drain returns the accumulated deltas and starts over.
func (p *Peers) Drain() map[PeerKey]PeerDelta {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[PeerKey]PeerDelta, len(p.m))
	for k, d := range p.m {
		if d.Conns != 0 || d.In != 0 || d.Out != 0 {
			out[k] = *d
		}
	}
	p.m = nil
	return out
}

// ConnSet tracks the live connections of one ingress port so they can be
// listed and torn down. The zero value is ready to use.
type ConnSet struct {
	mu     sync.Mutex
	m      map[net.Conn]time.Time
	closed bool
}

// Add registers c. It returns false (and closes c) once the set is shut.
func (s *ConnSet) Add(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		c.Close()
		return false
	}
	if s.m == nil {
		s.m = map[net.Conn]time.Time{}
	}
	s.m[c] = time.Now()
	return true
}

// Remove forgets c.
func (s *ConnSet) Remove(c net.Conn) {
	s.mu.Lock()
	delete(s.m, c)
	s.mu.Unlock()
}

// Len returns the number of live connections.
func (s *ConnSet) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// Conn is a snapshot of one live connection.
type Conn struct {
	Remote string
	Since  time.Time
}

// List snapshots the live connections.
func (s *ConnSet) List() []Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Conn, 0, len(s.m))
	for c, since := range s.m {
		out = append(out, Conn{Remote: HostOf(c.RemoteAddr()), Since: since})
	}
	return out
}

// CloseAll closes every live connection; new ones are still accepted.
func (s *ConnSet) CloseAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.m {
		c.Close()
	}
}

// Shut closes every live connection and refuses new ones.
func (s *ConnSet) Shut() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.CloseAll()
}

// Port is the accounting of one ingress port.
type Port struct {
	ID       int64
	Counters Counters
	Live     ConnSet
}

// Relay pipes visitor ⇄ remote, accounting the traffic to the port and to
// peers. Both connections are closed when it returns.
func (p *Port) Relay(visitor, remote net.Conn, peers *Peers) {
	if !p.Live.Add(visitor) {
		remote.Close()
		return
	}
	defer p.Live.Remove(visitor)
	key := PeerKey{IP: HostOf(visitor.RemoteAddr()), InPortID: p.ID}
	peers.Add(key, PeerDelta{Conns: 1})
	p.Counters.Active.Add(1)
	in, out := copyio.Pipe(visitor, remote, &p.Counters.In, &p.Counters.Out)
	p.Counters.Active.Add(-1)
	p.Counters.Conns.Add(1)
	peers.Add(key, PeerDelta{In: in, Out: out})
}

// HostOf returns the host part of addr ("" for nil).
func HostOf(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
