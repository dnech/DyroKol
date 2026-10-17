package server

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/dnech/DyroKol/internal/store"
)

//go:embed web
var webEmbed embed.FS

// Request body limits.
const (
	maxBody     = 64 << 10
	maxSnapshot = 8 << 20
)

// api serves the admin HTTP API. It exists only once the store is unlocked.
type api struct {
	*Server
	st *store.Store
}

func (s *Server) adminHandler(st *store.Store) http.Handler {
	a := &api{Server: s, st: st}
	mux := http.NewServeMux()
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/config": a.getConfig,
		"GET /api/status": a.getStatus,
		"GET /api/stats":  a.getStats,
		"GET /api/peers":  a.getPeers,
		"GET /api/live":   a.getLive,
		"GET /api/logs":   a.getLogs,

		"POST /api/clients":                   a.createClient,
		"PATCH /api/clients/{hash}":           a.patchClient,
		"POST /api/clients/{hash}/regenerate": a.regenerateClient,
		"DELETE /api/clients/{hash}":          a.deleteClient,

		"POST /api/inports":              a.createInPort,
		"PATCH /api/inports/{id}":        a.patchInPort,
		"DELETE /api/inports/{id}":       a.deleteInPort,
		"POST /api/endpoints":            a.createEndpoint,
		"PATCH /api/endpoints/{id}":      a.patchEndpoint,
		"DELETE /api/endpoints/{id}":     a.deleteEndpoint,
		"POST /api/routes":               a.createRoute,
		"PUT /api/endpoints/{id}/routes": a.setEndpointRoutes,
		"PATCH /api/routes/{id}":         a.patchRoute,
		"GET /api/map":                   a.getMap,
		"PUT /api/map":                   a.putMap,
		"DELETE /api/routes/{id}":        a.deleteRoute,

		"GET /api/backup":    a.backup,
		"POST /api/restore":  a.restore,
		"PUT /api/settings":  a.putSettings,
		"POST /api/shutdown": a.shutdown,
	} {
		mux.HandleFunc(pattern, h)
	}
	web, err := fs.Sub(webEmbed, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(web)))
	return s.guard(mux)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeStoreError maps a store error to an HTTP status. what names the
// entity for a bare "not found".
func writeStoreError(w http.ResponseWriter, err error, what string) {
	var serr *store.Error
	msg := err.Error()
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
		if !errors.As(err, &serr) {
			msg = what + " not found"
		}
	case errors.Is(err, store.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, store.ErrConflict):
		status = http.StatusConflict
	}
	writeError(w, status, msg)
}

// decode reads a JSON body into v, rejecting unknown fields.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return 0, false
	}
	return id, true
}

func queryInt(r *http.Request, key string, def int64) int64 {
	if n, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 64); err == nil {
		return n
	}
	return def
}
