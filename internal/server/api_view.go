package server

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/store"
)

type clientStatus struct {
	store.Client
	store.Presence
	Prefix        string `json:"prefix"`
	Online        bool   `json:"online"`
	InPortCount   int    `json:"inport_count"`
	EndpointCount int    `json:"endpoint_count"`
	AgentVersion  string `json:"agent_version,omitempty"`
}

type inportStatus struct {
	store.InPort
	store.Totals
	MachineLabel   string `json:"machine_label"`
	RouteID        int64  `json:"route_id,omitempty"`
	RouteEnabled   bool   `json:"route_enabled"`
	EndpointID     int64  `json:"endpoint_id,omitempty"`
	Target         string `json:"target,omitempty"`
	EndpointOnline bool   `json:"endpoint_online"`
	Active         int    `json:"active"`
	BindError      string `json:"bind_error,omitempty"`
}

type endpointStatus struct {
	store.Endpoint
	MachineLabel string `json:"machine_label"`
	Online       bool   `json:"online"`
	RouteCount   int    `json:"route_count"`
	Display      string `json:"display"`
}

type routeStatus struct {
	store.Route
	InPort        int    `json:"inport"`
	InPortLabel   string `json:"inport_label"`
	Endpoint      string `json:"endpoint"`
	EndpointLabel string `json:"endpoint_label"`
}

type totalsStatus struct {
	BytesIn  int64 `json:"bytes_in"`
	BytesOut int64 `json:"bytes_out"`
	Conns    int64 `json:"conns_total"`
	Active   int   `json:"active"`
}

type statusResponse struct {
	Fingerprint string           `json:"fingerprint"`
	Now         int64            `json:"now"`
	RelayLabel  string           `json:"relay_label"`
	Version     string           `json:"version"`
	ConfigVer   int64            `json:"config_version"`
	Autostart   bool             `json:"autostart"`
	Name        string           `json:"name"`        // display name of this relay
	PublicAddr  string           `json:"public_addr"` // domain or ip agents connect to
	Totals      totalsStatus     `json:"totals"`
	Clients     []clientStatus   `json:"clients"`
	InPorts     []inportStatus   `json:"inports"`
	Endpoints   []endpointStatus `json:"endpoints"`
	Routes      []routeStatus    `json:"routes"`
}

// runtimeView is the live state of the relay at one instant.
type runtimeView struct {
	sessions map[string]*Session
	active   map[int64]int    // live connections per ingress port
	bindErrs map[int64]string // per ingress port
}

func (s *Server) runtime() runtimeView {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := runtimeView{
		sessions: make(map[string]*Session, len(s.sessions)),
		active:   map[int64]int{},
		bindErrs: map[int64]string{},
	}
	for k, sess := range s.sessions {
		v.sessions[k] = sess
		for _, be := range sess.bindErrors() {
			v.bindErrs[be.InPortID] = be.Error
		}
	}
	for id, msg := range s.bindErrs {
		v.bindErrs[id] = msg
	}
	for id, p := range s.ports {
		v.active[id] = p.Live.Len()
	}
	for id, a := range s.agentStats {
		v.active[id] = a.active
	}
	return v
}

func (v runtimeView) online(machine string) bool {
	return machine == store.MachineRelay || v.sessions[machine] != nil
}

// endpointDisplay renders an endpoint the way it is shown in the ui.
func endpointDisplay(x *topoIndex, ep store.Endpoint) string {
	where := x.machineName(ep.Machine)
	if ep.Type == store.TypeSocks5 {
		return where + " → socks5 exit"
	}
	return where + " → " + net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port))
}

func (a *api) getStatus(w http.ResponseWriter, r *http.Request) {
	topo, err := a.st.Topology()
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	totals, err := a.st.Totals()
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	presence, err := a.st.Presence()
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	settings, err := a.st.GetSettings()
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	x := indexTopology(topo)
	rt := a.runtime()
	now := time.Now().Unix()

	resp := statusResponse{
		Fingerprint: a.fingerprint,
		Now:         now,
		RelayLabel:  store.MachineRelay,
		Version:     protocol.AppVersion,
		ConfigVer:   topo.Version,
		Autostart:   settings.Autostart,
		Name:        settings.Name,
		PublicAddr:  settings.PublicAddr,
		Clients:     make([]clientStatus, 0, len(topo.Clients)),
		InPorts:     make([]inportStatus, 0, len(topo.InPorts)),
		Endpoints:   make([]endpointStatus, 0, len(topo.Endpoints)),
		Routes:      make([]routeStatus, 0, len(topo.Routes)),
	}

	inportCount := map[string]int{}
	for _, ip := range topo.InPorts {
		inportCount[ip.Machine]++
	}
	endpointCount := map[string]int{}
	for _, ep := range topo.Endpoints {
		endpointCount[ep.Machine]++
	}
	routeCount := map[int64]int{}
	for _, r := range topo.Routes {
		routeCount[r.EndpointID]++
	}

	for _, c := range topo.Clients {
		cs := clientStatus{
			Client:        c,
			Presence:      presence[c.KeyHash],
			Prefix:        short(c.KeyHash),
			InPortCount:   inportCount[c.KeyHash],
			EndpointCount: endpointCount[c.KeyHash],
		}
		if sess := rt.sessions[c.KeyHash]; sess != nil {
			cs.Online = true
			cs.AgentVersion = sess.agentVersion
			cs.LastSeen, cs.LastIP = now, sess.remoteIP
		}
		resp.Clients = append(resp.Clients, cs)
	}
	for _, ip := range topo.InPorts {
		is := inportStatus{
			InPort:       ip,
			Totals:       totals[ip.ID],
			MachineLabel: x.machineName(ip.Machine),
			Active:       rt.active[ip.ID],
			BindError:    rt.bindErrs[ip.ID],
		}
		if route, ep, ok := x.routedEndpoint(ip.ID); ok {
			is.RouteID, is.EndpointID, is.RouteEnabled = route.ID, ep.ID, route.Enabled
			is.Target = endpointDisplay(x, ep)
			is.EndpointOnline = rt.online(ep.Machine)
		}
		resp.Totals.BytesIn += is.BytesIn
		resp.Totals.BytesOut += is.BytesOut
		resp.Totals.Conns += is.Conns
		resp.Totals.Active += is.Active
		resp.InPorts = append(resp.InPorts, is)
	}
	for _, ep := range topo.Endpoints {
		resp.Endpoints = append(resp.Endpoints, endpointStatus{
			Endpoint:     ep,
			MachineLabel: x.machineName(ep.Machine),
			Online:       rt.online(ep.Machine),
			RouteCount:   routeCount[ep.ID],
			Display:      endpointDisplay(x, ep),
		})
	}
	for _, route := range topo.Routes {
		rs := routeStatus{Route: route}
		if ip, ok := x.inport[route.InPortID]; ok {
			rs.InPort, rs.InPortLabel = ip.Port, ip.Label
		}
		if ep, ok := x.endpoint[route.EndpointID]; ok {
			rs.Endpoint, rs.EndpointLabel = endpointDisplay(x, ep), ep.Label
		}
		resp.Routes = append(resp.Routes, rs)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ingressRef names the ingress port a peer or live connection belongs to.
type ingressRef struct {
	InPortID   int64  `json:"inport_id"`
	InPort     int    `json:"inport"`
	InPortName string `json:"inport_name"`
	Machine    string `json:"machine"`
}

// ingressRefs maps ingress port ids to their display info.
func (a *api) ingressRefs() (map[int64]ingressRef, error) {
	inports, err := a.st.ListInPorts()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]ingressRef, len(inports))
	for _, ip := range inports {
		out[ip.ID] = ingressRef{InPortID: ip.ID, InPort: ip.Port, InPortName: ip.Label, Machine: ip.Machine}
	}
	return out, nil
}

func (a *api) getStats(w http.ResponseWriter, r *http.Request) {
	since := queryInt(r, "since", time.Now().Add(-time.Hour).Unix())
	samples, err := a.st.SamplesSince(since)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since, "samples": nonNil(samples)})
}

// getPeers lists remote addresses seen on ingress ports.
func (a *api) getPeers(w http.ResponseWriter, r *http.Request) {
	peers, err := a.st.ListPeers(int(queryInt(r, "limit", 0)))
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	refs, err := a.ingressRefs()
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	type peerStatus struct {
		store.Peer
		InPort     int    `json:"inport"`
		InPortName string `json:"inport_name"`
	}
	out := make([]peerStatus, 0, len(peers))
	for _, p := range peers {
		ref := refs[p.InPortID]
		out = append(out, peerStatus{Peer: p, InPort: ref.InPort, InPortName: ref.InPortName})
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": out})
}

// getLive lists connections currently flowing through every ingress port.
func (a *api) getLive(w http.ResponseWriter, r *http.Request) {
	refs, err := a.ingressRefs()
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	type liveStatus struct {
		ingressRef
		Remote string `json:"remote"`
		Since  int64  `json:"since"`
		Where  string `json:"where"` // "relay" or the label of the machine
	}
	out := []liveStatus{}
	a.mu.Lock()
	for id, p := range a.ports {
		for _, c := range p.Live.List() {
			out = append(out, liveStatus{ingressRef: refs[id], Remote: c.Remote, Since: c.Since.Unix(), Where: store.MachineRelay})
		}
	}
	sessions := make([]*Session, 0, len(a.sessions))
	for _, sess := range a.sessions {
		sessions = append(sessions, sess)
	}
	a.mu.Unlock()
	for _, sess := range sessions {
		for _, lc := range sess.liveConns() {
			out = append(out, liveStatus{ingressRef: refs[lc.InPortID], Remote: lc.Remote, Since: lc.Since, Where: sess.name()})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"live": out})
}

func (a *api) getLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := a.st.ListLogs(int(queryInt(r, "limit", 0)))
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": nonNil(logs)})
}

// getConfig exposes the runtime contract for external shells (a desktop
// tray, an Android WebView wrapper, ...). It never contains secrets.
func (a *api) getConfig(w http.ResponseWriter, r *http.Request) {
	settings, err := a.st.GetSettings()
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":      protocol.AppVersion,
		"protocol":     protocol.Version,
		"admin_addr":   a.cfg.AdminAddr,
		"listen":       a.cfg.Listen,
		"fingerprint":  a.fingerprint,
		"api_header":   apiTokenHeader,
		"db_file":      a.cfg.DBPath,
		"autostart":    settings.Autostart,
		"capabilities": []string{"backup", "restore", "live", "logs", "settings", "map"},
	})
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
