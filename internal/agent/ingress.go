package agent

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/dnech/DyroKol/internal/egress"
	"github.com/dnech/DyroKol/internal/filter"
	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/traffic"
	"github.com/dnech/DyroKol/internal/transport"
)

// reportInterval is how often the agent reports traffic to each relay.
const reportInterval = 2500 * time.Millisecond

// ingressKey identifies a bound port: ids are only unique per relay.
type ingressKey struct {
	owner string // relay address
	id    int64
}

// ingress is one port bound on this machine for a relay.
type ingress struct {
	traffic.Port
	owner string
	port  int
	ln    net.Listener
	peers traffic.Peers

	mu    sync.Mutex
	cfg   protocol.Ingress
	rules *filter.Rules
}

func (in *ingress) config() (protocol.Ingress, *filter.Rules) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.cfg, in.rules
}

func (in *ingress) setConfig(cfg protocol.Ingress, rules *filter.Rules) {
	in.mu.Lock()
	in.cfg, in.rules = cfg, rules
	in.mu.Unlock()
}

// close releases the socket and drops every live connection.
func (in *ingress) close() {
	in.ln.Close()
	in.Live.Shut()
}

type desiredIngress struct {
	cfg   protocol.Ingress
	rules *filter.Rules
}

// applyConfig makes the ports bound for sess match push. It applies as much
// as it can and reports the ports it could not bind, so a single busy port
// does not block the rest. A malformed push is rejected as a whole.
func (a *Agent) applyConfig(sess *session, push *protocol.ConfigPush) ([]protocol.BindError, error) {
	desired := make(map[int64]desiredIngress, len(push.In))
	ports := map[int]bool{}
	for _, cfg := range push.In {
		if cfg.Port < 1 || cfg.Port > 65535 {
			return nil, fmt.Errorf("ingress %d: invalid port %d", cfg.InPortID, cfg.Port)
		}
		if ports[cfg.Port] {
			return nil, fmt.Errorf("port %d listed twice", cfg.Port)
		}
		rules, err := filter.Parse(cfg.Allow, cfg.Deny)
		if err != nil {
			return nil, fmt.Errorf("ingress :%d: filter: %w", cfg.Port, err)
		}
		ports[cfg.Port] = true
		desired[cfg.InPortID] = desiredIngress{cfg, rules}
	}

	// Ports that are gone or moved free their socket before anything new
	// is bound, so port swaps always work.
	removed := a.closeIngress(func(in *ingress) bool {
		d, ok := desired[in.ID]
		return in.owner == sess.addr && (!ok || d.cfg.Port != in.port)
	})
	for _, in := range removed {
		a.log.Printf("[%s] ingress %d (:%d) closed", sess.addr, in.ID, in.port)
	}

	a.mu.Lock()
	var started []*ingress
	var bindErrs []protocol.BindError
	for id, d := range desired {
		key := ingressKey{sess.addr, id}
		if in, ok := a.ingress[key]; ok {
			in.setConfig(d.cfg, d.rules)
			continue
		}
		bindErr := func(format string, args ...any) {
			bindErrs = append(bindErrs, protocol.BindError{
				InPortID: id, Port: d.cfg.Port, Label: d.cfg.Label, Error: fmt.Sprintf(format, args...),
			})
		}
		if other := a.ingressByPortLocked(d.cfg.Port); other != nil {
			bindErr("port %d is already bound by ingress %d of %s", d.cfg.Port, other.ID, other.owner)
			continue
		}
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(d.cfg.Port))
		if err != nil {
			bindErr("cannot bind port %d: %v", d.cfg.Port, err)
			continue
		}
		in := &ingress{Port: traffic.Port{ID: id}, owner: sess.addr, port: d.cfg.Port, ln: ln}
		in.setConfig(d.cfg, d.rules)
		a.ingress[key] = in
		started = append(started, in)
	}
	a.mu.Unlock()

	for _, in := range started {
		cfg, _ := in.config()
		a.log.Printf("[%s] ingress %d (%s): listening on :%d", sess.addr, in.ID, cfg.Label, in.port)
		go a.acceptIngress(in)
	}
	return bindErrs, nil
}

// closeIngress closes and forgets every bound port matching drop.
func (a *Agent) closeIngress(drop func(*ingress) bool) []*ingress {
	a.mu.Lock()
	var removed []*ingress
	for key, in := range a.ingress {
		if drop(in) {
			delete(a.ingress, key)
			removed = append(removed, in)
		}
	}
	a.mu.Unlock()
	for _, in := range removed {
		in.close()
	}
	return removed
}

func (a *Agent) ingressByPortLocked(port int) *ingress {
	for _, in := range a.ingress {
		if in.port == port {
			return in
		}
	}
	return nil
}

func (a *Agent) acceptIngress(in *ingress) {
	for {
		conn, err := in.ln.Accept()
		if err != nil {
			return
		}
		transport.TuneSocket(conn)
		go a.handleIngress(in, conn)
	}
}

// handleIngress serves one visitor that arrived on an agent-side port.
func (a *Agent) handleIngress(in *ingress, conn net.Conn) {
	cfg, rules := in.config()
	ip := traffic.HostOf(conn.RemoteAddr())
	if !rules.MatchHost(ip) {
		a.log.Printf("[%s] ingress %d (%s): %s blocked by source filter", in.owner, in.ID, cfg.Label, ip)
		conn.Close()
		return
	}
	sess := a.currentSession(in.owner)
	if sess == nil {
		a.log.Printf("[%s] ingress %d: relay offline", in.owner, in.ID)
		conn.Close()
		return
	}

	var remote net.Conn
	var err error
	if cfg.Egress.Machine == sess.keyHash {
		// we execute the egress ourselves: no detour through the relay
		remote, err = egress.Dial(&cfg.Egress)
	} else {
		remote, err = a.openStream(sess, protocol.StreamHeader{T: protocol.StreamRoute, InPortID: in.ID})
	}
	if err != nil {
		a.log.Printf("[%s] ingress %d: %v", in.owner, in.ID, err)
		conn.Close()
		return
	}
	in.Relay(conn, remote, &in.peers)
}

// --- reports ---

// reportLoop periodically sends the traffic of the relay's ports on the
// report stream. It is the only writer of that stream.
func (a *Agent) reportLoop(ctx context.Context, sess *session, stream net.Conn) {
	defer stream.Close()
	t := time.NewTicker(reportInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.mux.CloseChan():
			return
		case <-t.C:
			if err := protocol.WriteMsg(stream, a.report(sess.addr)); err != nil {
				return
			}
		}
	}
}

// report drains the counters of the ports owned by relay addr. Every owned
// port is listed so the active gauge stays fresh even when idle.
func (a *Agent) report(addr string) protocol.Report {
	a.mu.Lock()
	var owned []*ingress
	for _, in := range a.ingress {
		if in.owner == addr {
			owned = append(owned, in)
		}
	}
	a.mu.Unlock()

	var r protocol.Report
	for _, in := range owned {
		d := in.Counters.Drain()
		r.Stats = append(r.Stats, protocol.StatDelta{
			InPortID: in.ID, BytesIn: d.In, BytesOut: d.Out, Conns: d.Conns, Active: d.Active,
		})
		for key, p := range in.peers.Drain() {
			r.Peers = append(r.Peers, protocol.PeerDelta{
				IP: key.IP, InPortID: in.ID, Conns: p.Conns, BytesIn: p.In, BytesOut: p.Out,
			})
		}
		for _, c := range in.Live.List() {
			r.Live = append(r.Live, protocol.LiveConn{InPortID: in.ID, Remote: c.Remote, Since: c.Since.Unix()})
		}
	}
	return r
}
