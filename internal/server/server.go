// Package server implements the relay: it accepts agent tunnels, keeps the
// encrypted store, binds relay-side ingress ports, pushes configuration to
// agents, routes connections between machines and serves the admin API.
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dnech/DyroKol/internal/config"
	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/store"
	"github.com/dnech/DyroKol/internal/traffic"
	"github.com/dnech/DyroKol/internal/transport"
)

// Config configures a relay; zero values pick defaults.
type Config struct {
	Listen string
	// AdminAddr is an optional local TCP entrance to the admin UI. Empty (the
	// default) means no port is opened on the relay at all: the UI is reachable
	// only through the tunnel of an admin machine.
	AdminAddr string
	DBPath    string
	CertFile  string
	KeyFile   string
	Logger    *log.Logger

	// AdminToken guards the HTTP API: every /api request must carry it in
	// the X-Dyrokol-Api header. AdminTokenSHA256 (hex sha256 of the token)
	// may be supplied instead, so the plaintext never has to be stored.
	// When both are empty a token is generated once and kept inside the
	// encrypted database; admin machines receive it when they connect.
	AdminToken       string
	AdminTokenSHA256 string
}

// Defaults.
const (
	DefaultListen = ":7443"

	handshakeTimeout = 15 * time.Second
	flushInterval    = 5 * time.Second
	repushInterval   = 15 * time.Second
)

// Server is the relay.
type Server struct {
	cfg           Config
	log           *log.Logger
	tokMu         sync.RWMutex
	adminSHA      string // hex sha256 of the admin token
	adminTok      string // plaintext admin token when known; shared with admin machines
	adminExplicit bool   // the token comes from the configuration, not from the database
	adminTunnel   *pipeListener
	fingerprint   string // set by Run before Ready closes

	mu         sync.Mutex
	st         *store.Store // nil until the database is unlocked
	sessions   map[string]*Session
	ports      map[int64]*relayPort // relay-side ingress listeners
	bindErrs   map[int64]string     // relay-side bind failures
	agentStats map[int64]*agentStat // reported by agents, per ingress port
	ln         net.Listener
	adminSrv   *http.Server
	closed     bool

	routes routeCache
	peers  traffic.Peers
	events eventBuffer

	workers      sync.WaitGroup
	ready        chan struct{}
	stop         chan struct{}
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

// New creates a relay. Nothing is opened until Run.
func New(cfg Config) *Server {
	cfg.Listen = orDefault(cfg.Listen, DefaultListen)
	cfg.AdminAddr = config.ListenAddr(cfg.AdminAddr)
	cfg.DBPath = orDefault(cfg.DBPath, "dyrokol.db.enc")
	cfg.CertFile = orDefault(cfg.CertFile, "dyrokol.crt")
	cfg.KeyFile = orDefault(cfg.KeyFile, "dyrokol.key")
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	s := &Server{
		cfg:        cfg,
		log:        cfg.Logger,
		sessions:   map[string]*Session{},
		ports:      map[int64]*relayPort{},
		bindErrs:   map[int64]string{},
		agentStats: map[int64]*agentStat{},
		ready:      make(chan struct{}),
		stop:       make(chan struct{}),
		shutdown:   make(chan struct{}),
	}
	switch {
	case cfg.AdminTokenSHA256 != "":
		s.adminSHA = strings.ToLower(strings.TrimSpace(cfg.AdminTokenSHA256))
		s.adminExplicit = true
	case cfg.AdminToken != "":
		s.adminTok = cfg.AdminToken
		s.adminSHA = protocol.KeyHash(s.adminTok)
		s.adminExplicit = true
	}
	return s
}

// AdminToken returns the plaintext admin API token, when known. A token kept
// in the database is available once the database is unlocked.
func (s *Server) AdminToken() string {
	s.tokMu.RLock()
	defer s.tokMu.RUnlock()
	return s.adminTok
}

// adminTokenHash returns the hex sha256 the API compares tokens against.
func (s *Server) adminTokenHash() string {
	s.tokMu.RLock()
	defer s.tokMu.RUnlock()
	return s.adminSHA
}

// loadAdminToken takes the admin token from the unlocked database unless one
// was configured explicitly. The token is generated on first use.
func (s *Server) loadAdminToken(st *store.Store) {
	if s.adminExplicit {
		return
	}
	tok, err := st.AdminToken()
	if err != nil {
		s.log.Printf("admin api token: %v", err)
		return
	}
	s.tokMu.Lock()
	s.adminTok, s.adminSHA = tok, protocol.KeyHash(tok)
	s.tokMu.Unlock()
}

// Ready is closed once the tunnel listener is up.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Fingerprint is the TLS certificate pin (valid after Ready).
func (s *Server) Fingerprint() string { return s.fingerprint }

// Addr is the tunnel listener address ("" before Ready).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Shutdown asks Run to return (used by the admin API).
func (s *Server) Shutdown() {
	s.shutdownOnce.Do(func() { close(s.shutdown) })
}

// Run serves until ctx is cancelled or Shutdown is called. The database
// stays locked until the first agent brings its password.
func (s *Server) Run(ctx context.Context) error {
	cert, fp, err := transport.EnsureCertificate(s.cfg.CertFile, s.cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("tls certificate: %w", err)
	}
	s.fingerprint = fp

	raw, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Listen, err)
	}
	ln := tls.NewListener(raw, transport.ServerConfig(cert))
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	close(s.ready)

	s.log.Printf("dyrokol server listening on %s (tls pin %s); database locked, waiting for an agent", ln.Addr(), fp)
	go s.acceptLoop(ln)

	select {
	case <-ctx.Done():
	case <-s.shutdown:
	}
	return s.Close()
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

// Close stops every listener and session, flushes pending stats and seals
// the database.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	ln, adminSrv, st := s.ln, s.adminSrv, s.st
	sessions := s.takeSessionsLocked(func(*Session) bool { return true })
	for id, p := range s.ports {
		p.close()
		delete(s.ports, id)
	}
	s.mu.Unlock()

	if ln != nil {
		ln.Close()
	}
	if adminSrv != nil {
		adminSrv.Close()
	}
	for _, sess := range sessions {
		sess.mux.Close()
	}
	s.workers.Wait()
	if st == nil {
		return nil
	}
	s.flush()
	return st.Close()
}

// store returns the unlocked store, or nil while the database is locked.
func (s *Server) store() *store.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

// activateLocked installs a freshly unlocked store and starts the workers
// that need it. Caller holds s.mu.
func (s *Server) activateLocked(st *store.Store) {
	s.st = st
	s.loadAdminToken(st)
	s.routes.invalidate()
	s.syncListenersLocked()
	s.startAdminLocked(st)
	s.goWorker(flushInterval, s.flush)
	s.goWorker(repushInterval, s.repush)
}

// goWorker runs fn every interval until the server stops.
func (s *Server) goWorker(interval time.Duration, fn func()) {
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				fn()
			}
		}
	}()
}

// startAdminLocked starts the admin HTTP server. It always serves the tunnel
// of admin machines (an in-memory listener, no port); a TCP listener is added
// only when Config.AdminAddr is set.
func (s *Server) startAdminLocked(st *store.Store) {
	srv := &http.Server{Handler: s.adminHandler(st), ReadHeaderTimeout: 10 * time.Second}
	s.adminSrv = srv
	tunnel := newPipeListener()
	s.adminTunnel = tunnel
	serve := func(ln net.Listener) {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Printf("admin ui: %v", err)
		}
	}
	go serve(tunnel)
	if s.cfg.AdminAddr == "" {
		s.log.Printf("admin ui: reachable only through an admin machine (no local port is open)")
		return
	}
	ln, err := net.Listen("tcp", s.cfg.AdminAddr)
	if err != nil {
		s.event(levelError, "admin ui: cannot listen on %s: %v", s.cfg.AdminAddr, err)
		return
	}
	go serve(ln)
	s.log.Printf("admin ui: also on http://%s (optional local entrance)", ln.Addr())
}

// configChanged applies a configuration change: drops memoized routes,
// re-syncs relay listeners and pushes the new version to agents. It returns
// the relay ports that failed to bind.
func (s *Server) configChanged() map[int64]error {
	s.routes.invalidate()
	s.mu.Lock()
	failed := s.syncListenersLocked()
	s.mu.Unlock()
	s.broadcastConfig()
	return failed
}

// --- helpers ---

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// short renders a key hash for logs.
func short(hash string) string {
	if len(hash) > 16 {
		return hash[:16]
	}
	return hash
}

// shortMachine renders a machine id: the relay keeps its name, a client is
// shown as a short key-hash prefix.
func shortMachine(machine string) string {
	if machine == store.MachineRelay {
		return machine
	}
	return short(machine)
}
