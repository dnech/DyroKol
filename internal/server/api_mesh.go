package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/store"
)

// --- machines ---

// tokenPrefix is the readable prefix of a machine token: adm_ for admin
// machines, cl_ for the others.
func tokenPrefix(role string) string {
	if role == protocol.RoleAdmin {
		return "adm_"
	}
	return "cl_"
}

func (a *api) createClient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label string `json:"label"`
		Role  string `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	role := orDefault(req.Role, protocol.RoleClient)
	token := protocol.NewToken(tokenPrefix(role))
	c, err := a.st.CreateClient(protocol.KeyHash(token), role, req.Label)
	if err != nil {
		writeStoreError(w, err, "machine")
		return
	}
	a.configChanged()
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "client": c})
}

func (a *api) patchClient(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	var req store.ClientPatch
	if !decode(w, r, &req) {
		return
	}
	if err := a.st.UpdateClient(hash, req); err != nil {
		writeStoreError(w, err, "machine")
		return
	}
	if req.Role != nil {
		// The role is applied at authentication: reconnect the machine.
		a.kickMachine(hash)
	}
	a.configChanged()
	w.WriteHeader(http.StatusNoContent)
}

// regenerateClient rotates a machine's token and drops its session.
func (a *api) regenerateClient(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	// the prefix follows the machine's current role, not the one it was created with
	prefix := tokenPrefix(protocol.RoleClient)
	if cur, err := a.st.GetClient(hash); err == nil {
		prefix = tokenPrefix(cur.Role)
	}
	token := protocol.NewToken(prefix)
	c, err := a.st.RotateClientKey(hash, protocol.KeyHash(token))
	if err != nil {
		writeStoreError(w, err, "machine")
		return
	}
	a.kickMachine(hash)
	a.configChanged()
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "client": c})
}

func (a *api) deleteClient(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if err := a.st.DeleteClient(hash); err != nil {
		writeStoreError(w, err, "machine")
		return
	}
	a.kickMachine(hash)
	a.configChanged()
	w.WriteHeader(http.StatusNoContent)
}

// --- ingress ports ---

func (a *api) createInPort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Machine string   `json:"machine"`
		Port    int      `json:"port"`
		Label   string   `json:"label"`
		Allow   []string `json:"allow"`
		Deny    []string `json:"deny"`
	}
	if !decode(w, r, &req) {
		return
	}
	ip, err := a.st.CreateInPort(store.InPort{
		Machine: req.Machine, Port: req.Port, Label: req.Label, Allow: req.Allow, Deny: req.Deny,
	})
	if err != nil {
		writeStoreError(w, err, "machine")
		return
	}
	// A relay-owned port is bound right away; refuse it when that fails.
	// Agent-owned ports report bind failures asynchronously.
	if bindErr := a.configChanged()[ip.ID]; bindErr != nil {
		a.st.DeleteInPort(ip.ID)
		a.configChanged()
		writeError(w, http.StatusConflict, "cannot bind port: "+bindErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, ip)
}

func (a *api) patchInPort(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	var req store.InPortPatch
	if !ok || !decode(w, r, &req) {
		return
	}
	old, err := a.st.GetInPort(id)
	if err != nil {
		writeStoreError(w, err, "ingress port")
		return
	}
	if err := a.st.UpdateInPort(id, req); err != nil {
		writeStoreError(w, err, "ingress port")
		return
	}
	// A relay-owned port is rebound right away; if the new number cannot be
	// bound the change is rolled back. Agent-owned ports report bind failures
	// asynchronously.
	if bindErr := a.configChanged()[id]; bindErr != nil && req.Port != nil && old.Machine == store.MachineRelay {
		a.st.UpdateInPort(id, store.InPortPatch{Port: &old.Port})
		a.configChanged()
		writeError(w, http.StatusConflict, "cannot bind port: "+bindErr.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) deleteInPort(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.st.DeleteInPort(id); err != nil {
		writeStoreError(w, err, "ingress port")
		return
	}
	a.configChanged()
	w.WriteHeader(http.StatusNoContent)
}

// --- egress endpoints ---

func (a *api) createEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Machine    string   `json:"machine"`
		Type       string   `json:"type"`
		Host       string   `json:"host"`
		Port       int      `json:"port"`
		Socks5User string   `json:"socks5_user"`
		Socks5Pass string   `json:"socks5_pass"`
		Label      string   `json:"label"`
		Allow      []string `json:"allow"`
		Deny       []string `json:"deny"`
	}
	if !decode(w, r, &req) {
		return
	}
	ep, err := a.st.CreateEndpoint(store.Endpoint{
		Machine: req.Machine, Type: req.Type, Host: req.Host, Port: req.Port,
		Socks5User: req.Socks5User, Socks5Pass: req.Socks5Pass,
		Label: req.Label, Allow: req.Allow, Deny: req.Deny,
	})
	if err != nil {
		writeStoreError(w, err, "machine")
		return
	}
	a.configChanged()
	writeJSON(w, http.StatusOK, ep)
}

func (a *api) patchEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	var req store.EndpointPatch
	if !ok || !decode(w, r, &req) {
		return
	}
	if err := a.st.UpdateEndpoint(id, req); err != nil {
		writeStoreError(w, err, "egress port")
		return
	}
	a.configChanged()
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) deleteEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	unrouted, err := a.st.DeleteEndpoint(id)
	if err != nil {
		writeStoreError(w, err, "egress port")
		return
	}
	a.dropLive(unrouted...)
	a.configChanged()
	w.WriteHeader(http.StatusNoContent)
}

// --- routes ---

func (a *api) createRoute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InPortID   int64 `json:"inport_id"`
		EndpointID int64 `json:"endpoint_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	rt, err := a.st.CreateRoute(req.InPortID, req.EndpointID)
	if err != nil {
		writeStoreError(w, err, "ingress or egress port")
		return
	}
	a.configChanged()
	writeJSON(w, http.StatusOK, rt)
}

// patchRoute re-points a route to another ingress port and/or endpoint.
func (a *api) patchRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	var req store.RoutePatch
	if !ok || !decode(w, r, &req) {
		return
	}
	before, after, err := a.st.UpdateRoute(id, req)
	if err != nil {
		writeStoreError(w, err, "route")
		return
	}
	if before != after {
		a.dropLive(before.InPortID)
		a.configChanged()
	}
	w.WriteHeader(http.StatusNoContent)
}

// getMap and putMap serve the shared layout of the route map.
func (a *api) getMap(w http.ResponseWriter, r *http.Request) {
	layout, err := a.st.GetMapLayout()
	if err != nil {
		writeStoreError(w, err, "map")
		return
	}
	writeJSON(w, http.StatusOK, layout)
}

func (a *api) putMap(w http.ResponseWriter, r *http.Request) {
	var layout store.MapLayout
	if !decode(w, r, &layout) {
		return
	}
	if err := a.st.SaveMapLayout(layout); err != nil {
		writeStoreError(w, err, "map")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setEndpointRoutes replaces the set of ingress ports routed to an endpoint.
func (a *api) setEndpointRoutes(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	var req struct {
		InPortIDs []int64 `json:"inport_ids"`
	}
	if !ok || !decode(w, r, &req) {
		return
	}
	affected, err := a.st.SetEndpointRoutes(id, req.InPortIDs)
	if err != nil {
		writeStoreError(w, err, "egress port")
		return
	}
	a.dropLive(affected...)
	a.configChanged()
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) deleteRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rt, err := a.st.DeleteRoute(id)
	if err != nil {
		writeStoreError(w, err, "route")
		return
	}
	a.dropLive(rt.InPortID)
	a.configChanged()
	w.WriteHeader(http.StatusNoContent)
}

// --- backup / restore ---

func (a *api) backup(w http.ResponseWriter, r *http.Request) {
	snap, err := a.st.Export(r.URL.Query().Get("secrets") != "0")
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="dyrokol-config.json"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(snap)
}

func (a *api) restore(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxSnapshot))
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot read body")
		return
	}
	snap, err := store.ParseSnapshot(data)
	if err == nil {
		err = a.st.Import(snap)
	}
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	// The whole configuration changed: every machine re-authenticates
	// against the restored client set and every relay port starts over.
	a.kick(func(*Session) bool { return true })
	a.mu.Lock()
	for id, p := range a.ports {
		p.close()
		delete(a.ports, id)
	}
	a.mu.Unlock()
	failed := a.configChanged()
	bindErrs := map[string]string{}
	for id, err := range failed {
		bindErrs[strconv.FormatInt(id, 10)] = err.Error()
	}
	a.event(levelInfo, "configuration restored from snapshot")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bind_errors": bindErrs})
}

// --- settings / lifecycle ---

func (a *api) putSettings(w http.ResponseWriter, r *http.Request) {
	var req store.SettingsPatch
	if !decode(w, r, &req) {
		return
	}
	cur, err := a.st.GetSettings()
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if err := a.st.SaveSettings(req.Apply(cur)); err != nil {
		writeStoreError(w, err, "")
		return
	}
	saved, err := a.st.GetSettings()
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (a *api) shutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		a.Shutdown()
	}()
}
