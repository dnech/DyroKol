package server

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/store"
	"github.com/dnech/DyroKol/internal/traffic"
	"github.com/dnech/DyroKol/internal/transport"
)

func reject(code, format string, args ...any) *protocol.Error {
	return &protocol.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// handleConn runs the handshake of one tunnel and then serves its session.
func (s *Server) handleConn(raw net.Conn) {
	defer raw.Close()
	transport.TuneSocket(raw)
	raw.SetDeadline(time.Now().Add(handshakeTimeout))

	var h protocol.Hello
	if err := protocol.ReadMsg(raw, &h); err != nil {
		s.log.Printf("handshake read from %s: %v", raw.RemoteAddr(), err)
		return
	}
	cl, perr := s.authorize(&h)
	if perr != nil {
		protocol.WriteMsg(raw, protocol.Reply{Error: perr})
		s.event(levelWarn, "handshake from %s rejected: %s", traffic.HostOf(raw.RemoteAddr()), perr.Message)
		return
	}
	reply := protocol.Reply{Role: cl.Role, Version: protocol.AppVersion}
	if cl.Role == protocol.RoleAdmin {
		// The admin machine exposes the admin UI locally and needs the
		// token; other machines never receive it.
		reply.AdminToken = s.AdminToken()
	}
	if err := protocol.WriteMsg(raw, reply); err != nil || h.Purpose == protocol.PurposeInit {
		return // an init connection only creates the database
	}
	raw.SetDeadline(time.Time{})

	mux, err := yamux.Server(raw, transport.MuxConfig())
	if err != nil {
		s.log.Printf("yamux server from %s: %v", raw.RemoteAddr(), err)
		return
	}
	sess := &Session{
		keyHash:      cl.KeyHash,
		label:        cl.Label,
		role:         cl.Role,
		agentVersion: h.AgentVersion,
		remoteIP:     traffic.HostOf(raw.RemoteAddr()),
		mux:          mux,
	}
	s.register(sess)
	defer s.unregister(sess)
	s.serveSession(sess)
}

// authorize checks a Hello: it initializes or unlocks the database when
// needed and resolves the connecting machine.
func (s *Server) authorize(h *protocol.Hello) (store.Client, *protocol.Error) {
	if h.Version != protocol.Version {
		return store.Client{}, reject(protocol.CodeVersion, "protocol version %d not supported (relay speaks %d)", h.Version, protocol.Version)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return store.Client{}, reject(protocol.CodeInternal, "relay is shutting down")
	}
	switch h.Purpose {
	case protocol.PurposeInit:
		return s.initLocked(h)
	case protocol.PurposeConnect:
		if perr := s.unlockLocked(h.DBPassword); perr != nil {
			return store.Client{}, perr
		}
		cl, err := s.st.GetClient(protocol.KeyHash(h.Token))
		if errors.Is(err, store.ErrNotFound) {
			return store.Client{}, reject(protocol.CodeUnauthorized, "unknown token")
		}
		if err != nil {
			return store.Client{}, reject(protocol.CodeInternal, "%v", err)
		}
		return cl, nil
	default:
		return store.Client{}, reject(protocol.CodeBadInit, "unknown purpose %q", h.Purpose)
	}
}

// initLocked creates the database with the caller as its first admin.
func (s *Server) initLocked(h *protocol.Hello) (store.Client, *protocol.Error) {
	if h.AdminToken == "" || h.DBPassword == "" {
		return store.Client{}, reject(protocol.CodeBadInit, "admin token and db password required")
	}
	if s.st != nil {
		return store.Client{}, reject(protocol.CodeDBExists, "database already initialized")
	}
	keyHash := protocol.KeyHash(h.AdminToken)
	st, err := store.Bootstrap(s.cfg.DBPath, h.DBPassword, keyHash)
	if errors.Is(err, store.ErrExists) {
		return store.Client{}, reject(protocol.CodeDBExists, "database file already exists; connect with the db password instead")
	}
	if err != nil {
		return store.Client{}, reject(protocol.CodeBadInit, "%v", err)
	}
	s.activateLocked(st)
	s.event(levelInfo, "database initialized at %s", s.cfg.DBPath)
	cl, err := st.GetClient(keyHash)
	if err != nil {
		return store.Client{}, reject(protocol.CodeInternal, "%v", err)
	}
	return cl, nil
}

// unlockLocked opens the database with password unless it is open already.
func (s *Server) unlockLocked(password string) *protocol.Error {
	if s.st != nil {
		return nil
	}
	if password == "" {
		return reject(protocol.CodeLocked, "database is locked")
	}
	st, err := store.Open(s.cfg.DBPath, password)
	switch {
	case err == nil:
		s.activateLocked(st)
		s.event(levelInfo, "database unlocked")
		return nil
	case errors.Is(err, store.ErrNoDB):
		return reject(protocol.CodeLocked, "no database; register a machine first (dyrokol client --server host:port)")
	case errors.Is(err, store.ErrBadPassword):
		return reject(protocol.CodeUnauthorized, "bad database password")
	default:
		return reject(protocol.CodeInternal, "%v", err)
	}
}
