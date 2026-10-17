package server

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/hashicorp/yamux"

	"github.com/dnech/DyroKol/internal/copyio"
	"github.com/dnech/DyroKol/internal/egress"
	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/store"
	"github.com/dnech/DyroKol/internal/traffic"
)

// maxLive bounds the live connections kept per agent report.
const maxLive = 500

// Session is one authenticated agent tunnel.
type Session struct {
	keyHash      string
	label        string
	role         string
	agentVersion string
	remoteIP     string
	mux          *yamux.Session

	pushMu  sync.Mutex   // serialises config pushes
	applied atomic.Int64 // config version acked by the agent

	mu       sync.Mutex
	bindErrs []protocol.BindError // from the last ack
	live     []protocol.LiveConn  // from the last report
}

func (s *Session) setBindErrs(errs []protocol.BindError) {
	s.mu.Lock()
	s.bindErrs = errs
	s.mu.Unlock()
}

func (s *Session) bindErrors() []protocol.BindError {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bindErrs
}

func (s *Session) setLive(live []protocol.LiveConn) {
	s.mu.Lock()
	s.live = live[:min(len(live), maxLive)]
	s.mu.Unlock()
}

func (s *Session) liveConns() []protocol.LiveConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

func (s *Session) name() string {
	if s.label != "" {
		return s.label
	}
	return short(s.keyHash)
}

// agentStat accumulates reported deltas of an agent-side ingress port
// between flushes; active is the last reported gauge.
type agentStat struct {
	in, out, conns int64
	active         int
}

func (s *Server) register(sess *Session) {
	s.mu.Lock()
	old := s.sessions[sess.keyHash]
	s.sessions[sess.keyHash] = sess
	s.mu.Unlock()
	if old != nil {
		old.mux.Close()
	}
	s.event(levelInfo, "machine %s connected: role=%s version=%s from %s",
		sess.name(), sess.role, orDash(sess.agentVersion), sess.remoteIP)
	go s.pushConfig(sess, true)
}

func (s *Server) unregister(sess *Session) {
	s.mu.Lock()
	current := s.sessions[sess.keyHash] == sess
	if current {
		delete(s.sessions, sess.keyHash)
	}
	s.mu.Unlock()
	if current {
		s.event(levelInfo, "machine %s disconnected", sess.name())
	}
}

func (s *Server) session(keyHash string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[keyHash]
}

// takeSessionsLocked removes the sessions matching pick from the registry and
// returns them; the caller closes them outside the lock.
func (s *Server) takeSessionsLocked(pick func(*Session) bool) []*Session {
	var out []*Session
	for k, sess := range s.sessions {
		if pick(sess) {
			out = append(out, sess)
			delete(s.sessions, k)
		}
	}
	return out
}

// kick drops the sessions matching pick, so those machines reconnect and
// authenticate again (new role, new key, restored client set, ...).
func (s *Server) kick(pick func(*Session) bool) {
	s.mu.Lock()
	sessions := s.takeSessionsLocked(pick)
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.mux.Close()
	}
}

func (s *Server) kickMachine(keyHash string) {
	s.kick(func(sess *Session) bool { return sess.keyHash == keyHash })
}

func (s *Server) sessionList() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	return out
}

// --- streams opened by agents ---

func (s *Server) serveSession(sess *Session) {
	for {
		stream, err := sess.mux.Accept()
		if err != nil {
			return
		}
		go s.handleStream(sess, stream)
	}
}

func (s *Server) handleStream(sess *Session, stream net.Conn) {
	defer stream.Close()
	var hdr protocol.StreamHeader
	if err := protocol.ReadMsg(stream, &hdr); err != nil {
		return
	}
	switch hdr.T {
	case protocol.StreamReport:
		s.readReports(sess, stream)
	case protocol.StreamAdmin:
		if sess.role != protocol.RoleAdmin {
			return
		}
		s.mu.Lock()
		tunnel := s.adminTunnel
		s.mu.Unlock()
		if tunnel == nil {
			return // the database is locked: there is no admin ui yet
		}
		tunnel.serve(stream) // returns when the http connection is done
	case protocol.StreamRoute:
		s.handleRoute(sess, hdr.InPortID, stream)
	}
}

// readReports consumes the agent's periodic traffic reports.
func (s *Server) readReports(sess *Session, stream net.Conn) {
	for {
		var r protocol.Report
		if err := protocol.ReadMsg(stream, &r); err != nil {
			return
		}
		s.applyReport(sess, &r)
	}
}

// applyReport merges an agent report. Only ports owned by that machine are
// accepted, so an agent cannot skew another machine's counters.
func (s *Server) applyReport(sess *Session, r *protocol.Report) {
	// Resolve ownership before taking s.mu: the route cache may hit the store.
	ownedIDs := map[int64]bool{}
	for _, d := range r.Stats {
		rt := s.route(d.InPortID)
		ownedIDs[d.InPortID] = rt.found && rt.inport.Machine == sess.keyHash
	}
	owned := func(id int64) bool { return ownedIDs[id] }

	s.mu.Lock()
	for _, d := range r.Stats {
		if !owned(d.InPortID) {
			continue
		}
		a := s.agentStats[d.InPortID]
		if a == nil {
			a = &agentStat{}
			s.agentStats[d.InPortID] = a
		}
		a.in += d.BytesIn
		a.out += d.BytesOut
		a.conns += d.Conns
		a.active = d.Active
	}
	s.mu.Unlock()
	for _, p := range r.Peers {
		if owned(p.InPortID) {
			s.peers.Add(traffic.PeerKey{IP: p.IP, InPortID: p.InPortID},
				traffic.PeerDelta{Conns: p.Conns, In: p.BytesIn, Out: p.BytesOut})
		}
	}
	live := r.Live[:0]
	for _, l := range r.Live {
		if owned(l.InPortID) {
			live = append(live, l)
		}
	}
	sess.setLive(live)
}

// handleRoute continues a connection that entered on an agent-side ingress
// port: resolve the route and run the egress.
func (s *Server) handleRoute(sess *Session, inportID int64, stream net.Conn) {
	rt := s.route(inportID)
	if !rt.found || rt.inport.Machine != sess.keyHash {
		s.log.Printf("route stream from %s for foreign ingress %d refused", sess.name(), inportID)
		return
	}
	if rt.spec == nil {
		return // not routed (any more)
	}
	remote, err := s.executeEgress(rt.spec)
	if err != nil {
		s.log.Printf("ingress %d: egress %s: %v", inportID, egress.Target(rt.spec), err)
		return
	}
	copyio.Pipe(stream, remote, nil, nil)
}

// executeEgress opens the final leg described by spec: locally when the
// relay owns the endpoint, otherwise through a dial stream to its machine.
func (s *Server) executeEgress(spec *protocol.EgressSpec) (net.Conn, error) {
	if spec.Machine == store.MachineRelay {
		return egress.Dial(spec)
	}
	sess := s.session(spec.Machine)
	if sess == nil {
		return nil, fmt.Errorf("machine %s is offline", shortMachine(spec.Machine))
	}
	stream, err := sess.mux.OpenStream()
	if err != nil {
		return nil, err
	}
	if err := protocol.WriteMsg(stream, protocol.StreamHeader{T: protocol.StreamDial, Egress: spec}); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
