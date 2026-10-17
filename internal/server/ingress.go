package server

import (
	"net"
	"strconv"
	"sync"

	"github.com/dnech/DyroKol/internal/filter"
	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/store"
	"github.com/dnech/DyroKol/internal/traffic"
	"github.com/dnech/DyroKol/internal/transport"
)

// relayPort is an ingress port bound on the relay itself.
type relayPort struct {
	traffic.Port
	ln   net.Listener
	port int
}

func (p *relayPort) close() {
	p.ln.Close()
	p.Live.Shut()
}

// syncListenersLocked makes the relay-side listeners match the store and
// forgets runtime state of deleted ports. It returns the ports that failed
// to bind. Caller holds s.mu.
func (s *Server) syncListenersLocked() map[int64]error {
	failed := map[int64]error{}
	if s.st == nil || s.closed {
		return failed
	}
	inports, err := s.st.ListInPorts()
	if err != nil {
		s.log.Printf("list ingress ports: %v", err)
		return failed
	}
	relayPorts := map[int64]store.InPort{}
	exists := map[int64]bool{}
	for _, ip := range inports {
		exists[ip.ID] = true
		if ip.Machine == store.MachineRelay {
			relayPorts[ip.ID] = ip
		}
	}
	for id, p := range s.ports {
		if _, ok := relayPorts[id]; !ok {
			p.close()
			delete(s.ports, id)
		}
	}
	for id := range s.agentStats {
		if !exists[id] {
			delete(s.agentStats, id)
		}
	}
	for id := range s.bindErrs {
		if _, ok := relayPorts[id]; !ok {
			delete(s.bindErrs, id)
		}
	}
	for id, ip := range relayPorts {
		if p, ok := s.ports[id]; ok {
			if p.port == ip.Port {
				continue
			}
			// the operator changed the port number: release the old socket
			p.close()
			delete(s.ports, id)
		}
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(ip.Port))
		if err != nil {
			failed[id] = err
			if s.bindErrs[id] != err.Error() {
				s.event(levelError, "ingress %d: cannot bind port %d: %v", id, ip.Port, err)
			}
			s.bindErrs[id] = err.Error()
			continue
		}
		delete(s.bindErrs, id)
		p := &relayPort{Port: traffic.Port{ID: id}, ln: ln, port: ip.Port}
		s.ports[id] = p
		s.log.Printf("ingress %d: listening on :%d", id, ip.Port)
		go s.acceptPublic(p)
	}
	return failed
}

// dropLive tears down the live connections of the given relay-side ports.
func (s *Server) dropLive(inportIDs ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range inportIDs {
		if p := s.ports[id]; p != nil {
			p.Live.CloseAll()
		}
	}
}

func (s *Server) acceptPublic(p *relayPort) {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		transport.TuneSocket(conn)
		go s.handlePublic(p, conn)
	}
}

// handlePublic serves one visitor of a relay-side ingress port.
func (s *Server) handlePublic(p *relayPort, conn net.Conn) {
	rt := s.route(p.ID)
	ip := traffic.HostOf(conn.RemoteAddr())
	switch {
	case rt.spec == nil || rt.source == nil:
		// unrouted, or an unparsable stored filter: fail closed
		conn.Close()
		return
	case !rt.source.MatchHost(ip):
		s.log.Printf("ingress %d: %s blocked by source filter", p.ID, ip)
		conn.Close()
		return
	}
	remote, err := s.executeEgress(rt.spec)
	if err != nil {
		s.log.Printf("ingress %d: %v", p.ID, err)
		conn.Close()
		return
	}
	p.Relay(conn, remote, &s.peers)
}

// --- route cache ---

// route is the resolved path of one ingress port.
type route struct {
	inport store.InPort
	found  bool                 // the ingress port exists
	spec   *protocol.EgressSpec // nil when not routed
	source *filter.Rules        // nil when the stored filter does not parse
}

// routeCache memoizes resolved routes so accepting a connection does not
// hit the store. A generation counter keeps a load that raced with an
// invalidation from re-inserting stale data.
type routeCache struct {
	mu  sync.Mutex
	gen uint64
	m   map[int64]*route
}

func (c *routeCache) invalidate() {
	c.mu.Lock()
	c.gen++
	c.m = nil
	c.mu.Unlock()
}

func (c *routeCache) get(id int64, load func(int64) *route) *route {
	c.mu.Lock()
	if r, ok := c.m[id]; ok {
		c.mu.Unlock()
		return r
	}
	gen := c.gen
	c.mu.Unlock()

	r := load(id)

	c.mu.Lock()
	if c.gen == gen {
		if c.m == nil {
			c.m = map[int64]*route{}
		}
		c.m[id] = r
	}
	c.mu.Unlock()
	return r
}

// route resolves the path of an ingress port (cached).
func (s *Server) route(inportID int64) *route {
	return s.routes.get(inportID, s.loadRoute)
}

func (s *Server) loadRoute(inportID int64) *route {
	r := &route{}
	st := s.store()
	if st == nil {
		return r
	}
	ip, ep, err := st.Resolve(inportID)
	if err != nil {
		return r
	}
	r.inport, r.found = ip, true
	if src, err := filter.Parse(ip.Allow, ip.Deny); err == nil {
		r.source = src
	}
	if ep != nil {
		spec := ep.Spec()
		r.spec = &spec
	}
	return r
}
