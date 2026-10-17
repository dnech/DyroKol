package server

import (
	"time"

	"github.com/dnech/DyroKol/internal/egress"
	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/store"
)

// topoIndex is a configuration snapshot with lookup tables.
type topoIndex struct {
	*store.Topology
	label         map[string]string // machine id → label
	inport        map[int64]store.InPort
	endpoint      map[int64]store.Endpoint
	routeByInPort map[int64]store.Route
}

func indexTopology(t *store.Topology) *topoIndex {
	x := &topoIndex{
		Topology:      t,
		label:         map[string]string{store.MachineRelay: store.MachineRelay},
		inport:        make(map[int64]store.InPort, len(t.InPorts)),
		endpoint:      make(map[int64]store.Endpoint, len(t.Endpoints)),
		routeByInPort: make(map[int64]store.Route, len(t.Routes)),
	}
	for _, c := range t.Clients {
		x.label[c.KeyHash] = c.Label
	}
	for _, ip := range t.InPorts {
		x.inport[ip.ID] = ip
	}
	for _, ep := range t.Endpoints {
		x.endpoint[ep.ID] = ep
	}
	for _, rt := range t.Routes {
		x.routeByInPort[rt.InPortID] = rt
	}
	return x
}

// machineName is the label of a machine, or its short id.
func (x *topoIndex) machineName(machine string) string {
	if l := x.label[machine]; l != "" {
		return l
	}
	return shortMachine(machine)
}

// routedEndpoint returns the endpoint an ingress port is routed to.
func (x *topoIndex) routedEndpoint(inportID int64) (store.Route, store.Endpoint, bool) {
	rt, ok := x.routeByInPort[inportID]
	if !ok {
		return store.Route{}, store.Endpoint{}, false
	}
	ep, ok := x.endpoint[rt.EndpointID]
	return rt, ep, ok
}

// buildConfig renders the configuration slice of one machine: the routed
// ingress ports it must bind and the endpoints it serves.
func (x *topoIndex) buildConfig(machine string) *protocol.ConfigPush {
	push := &protocol.ConfigPush{Version: x.Version, In: []protocol.Ingress{}}
	for _, ip := range x.InPorts {
		if ip.Machine != machine {
			continue
		}
		// Unrouted ports are not sent: an agent binds a port only when there
		// is somewhere to send its traffic.
		rt, ep, ok := x.routedEndpoint(ip.ID)
		if !ok || !rt.Enabled {
			continue
		}
		push.In = append(push.In, protocol.Ingress{
			InPortID: ip.ID,
			Port:     ip.Port,
			Label:    ip.Label,
			Allow:    ip.Allow,
			Deny:     ip.Deny,
			Egress:   ep.Spec(),
		})
	}
	users := map[int64][]string{} // endpoint id → machines routing into it
	for _, rt := range x.Routes {
		if !rt.Enabled {
			continue
		}
		if ip, ok := x.inport[rt.InPortID]; ok {
			users[rt.EndpointID] = append(users[rt.EndpointID], x.machineName(ip.Machine))
		}
	}
	for _, ep := range x.Endpoints {
		if ep.Machine != machine {
			continue
		}
		spec := ep.Spec()
		push.Out = append(push.Out, protocol.EgressInfo{
			EndpointID: ep.ID,
			Type:       ep.Type,
			Target:     egress.Target(&spec),
			Label:      ep.Label,
			From:       users[ep.ID],
		})
	}
	return push
}

// pushConfig sends the current configuration to one machine unless it has
// already applied it (or force is set).
func (s *Server) pushConfig(sess *Session, force bool) {
	sess.pushMu.Lock()
	defer sess.pushMu.Unlock()
	st := s.store()
	if st == nil {
		return
	}
	topo, err := st.Topology()
	if err != nil {
		s.log.Printf("config for %s: %v", sess.name(), err)
		return
	}
	if !force && sess.applied.Load() >= topo.Version {
		return
	}
	push := indexTopology(topo).buildConfig(sess.keyHash)

	stream, err := sess.mux.OpenStream()
	if err != nil {
		return
	}
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(handshakeTimeout))
	if protocol.WriteMsg(stream, protocol.StreamHeader{T: protocol.StreamPush}) != nil ||
		protocol.WriteMsg(stream, push) != nil {
		return
	}
	var ack protocol.ConfigAck
	if err := protocol.ReadMsg(stream, &ack); err != nil {
		s.log.Printf("config push to %s: %v", sess.name(), err)
		return
	}
	if ack.Error != "" {
		s.event(levelError, "config v%d rejected by %s: %s", push.Version, sess.name(), ack.Error)
		return
	}
	sess.applied.Store(push.Version)
	sess.setBindErrs(ack.BindErrors)
	for _, be := range ack.BindErrors {
		s.event(levelWarn, "machine %s cannot bind :%d (ingress %d): %s", sess.name(), be.Port, be.InPortID, be.Error)
	}
	s.log.Printf("config v%d applied by %s (%d ingress, %d egress, %d bind errors)",
		push.Version, sess.name(), len(push.In), len(push.Out), len(ack.BindErrors))
}

// broadcastConfig pushes the configuration to every machine behind the
// current version.
func (s *Server) broadcastConfig() {
	for _, sess := range s.sessionList() {
		go s.pushConfig(sess, false)
	}
}

// repush retries machines whose configuration is not fully applied: they
// rejected or never acked the current version, or some port failed to bind
// (e.g. because it was momentarily busy).
func (s *Server) repush() {
	for _, sess := range s.sessionList() {
		if len(sess.bindErrors()) > 0 {
			go s.pushConfig(sess, true)
		} else {
			go s.pushConfig(sess, false)
		}
	}
}
