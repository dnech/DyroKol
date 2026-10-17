// Package agent implements the machine side of dyrokol: it keeps a session
// to every configured relay, applies the port configuration pushed by the
// relay (binding ingress ports and serving egress requests) and reports
// traffic of its ingress ports back.
package agent

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/dnech/DyroKol/internal/copyio"
	"github.com/dnech/DyroKol/internal/egress"
	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/transport"
)

// Options tune an Agent; zero values pick defaults.
type Options struct {
	// PinsPath is the trust-on-first-use pin store ("" disables it).
	PinsPath       string
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	Logger         *log.Logger
}

const (
	handshakeTimeout = 15 * time.Second
	// stableSession resets the reconnect backoff once a session lived this long.
	stableSession = 30 * time.Second
)

// session is one live tunnel to a relay.
type session struct {
	addr    string // relay address, the key of everything owned by it
	keyHash string // our identity on that relay
	role    string
	mux     *yamux.Session
}

// Agent keeps sessions to relays and serves their configuration.
type Agent struct {
	cfg  Config
	opts Options
	log  *log.Logger

	mu       sync.Mutex
	sessions map[string]*session     // by relay address
	ingress  map[ingressKey]*ingress // bound ingress ports
}

// New creates an agent for cfg.
func New(cfg Config, opts Options) *Agent {
	if opts.BackoffInitial <= 0 {
		opts.BackoffInitial = time.Second
	}
	if opts.BackoffMax <= 0 {
		opts.BackoffMax = 15 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	return &Agent{
		cfg:      cfg,
		opts:     opts,
		log:      opts.Logger,
		sessions: map[string]*session{},
		ingress:  map[ingressKey]*ingress{},
	}
}

// Run keeps a session to every configured relay alive until ctx is
// cancelled, then releases every bound port.
func (a *Agent) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i, entry := range a.cfg.Servers {
		if entry.AdminListen == "" {
			entry.AdminListen = net.JoinHostPort("127.0.0.1", strconv.Itoa(defaultAdminPort+i))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.runServer(ctx, entry)
		}()
	}
	wg.Wait()
	a.closeIngress(func(*ingress) bool { return true })
}

func (a *Agent) runServer(ctx context.Context, e ServerEntry) {
	delay := a.opts.BackoffInitial
	for {
		start := time.Now()
		err := a.session(ctx, e)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			a.log.Printf("[%s] session: %v", e.Addr, err)
		}
		if time.Since(start) > stableSession {
			delay = a.opts.BackoffInitial
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, a.opts.BackoffMax)
	}
}

// dial opens a TLS connection to a relay and performs the handshake.
func (a *Agent) dial(addr, pin string, hello protocol.Hello) (net.Conn, protocol.Reply, error) {
	conn, err := transport.DialTLS(addr, pin, a.opts.PinsPath)
	if err != nil {
		return nil, protocol.Reply{}, fmt.Errorf("dial: %w", err)
	}
	conn.SetDeadline(time.Now().Add(handshakeTimeout))
	reply, err := protocol.Handshake(conn, hello)
	if err != nil {
		conn.Close()
		return nil, protocol.Reply{}, err
	}
	conn.SetDeadline(time.Time{})
	return conn, reply, nil
}

func (a *Agent) session(ctx context.Context, e ServerEntry) error {
	conn, reply, err := a.dial(e.Addr, e.TLSPin, protocol.Hello{
		Purpose:    protocol.PurposeConnect,
		Token:      e.Token,
		DBPassword: e.DBPassword,
	})
	if err != nil {
		return err
	}
	mux, err := yamux.Client(conn, transport.MuxConfig())
	if err != nil {
		conn.Close()
		return err
	}
	defer mux.Close()

	sess := &session{addr: e.Addr, keyHash: protocol.KeyHash(e.Token), role: reply.Role, mux: mux}
	a.mu.Lock()
	a.sessions[e.Addr] = sess
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.sessions[e.Addr] == sess {
			delete(a.sessions, e.Addr)
		}
		a.mu.Unlock()
	}()

	report, err := a.openStream(sess, protocol.StreamHeader{T: protocol.StreamReport})
	if err != nil {
		return fmt.Errorf("report stream: %w", err)
	}
	go a.reportLoop(ctx, sess, report)
	go a.acceptStreams(sess)

	if reply.Role == protocol.RoleAdmin && !e.DisableAdmin {
		defer a.serveAdmin(sess, e.AdminListen, reply.AdminToken)()
	}

	a.log.Printf("[%s] connected, role=%s", e.Addr, reply.Role)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-mux.CloseChan():
		return fmt.Errorf("connection closed by peer")
	}
}

func (a *Agent) currentSession(addr string) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[addr]
}

// openStream opens a yamux stream to the relay and writes its header.
func (a *Agent) openStream(sess *session, hdr protocol.StreamHeader) (net.Conn, error) {
	stream, err := sess.mux.OpenStream()
	if err != nil {
		return nil, err
	}
	if err := protocol.WriteMsg(stream, hdr); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

// --- streams opened by the relay ---

func (a *Agent) acceptStreams(sess *session) {
	for {
		stream, err := sess.mux.Accept()
		if err != nil {
			return
		}
		go a.handleStream(sess, stream)
	}
}

func (a *Agent) handleStream(sess *session, stream net.Conn) {
	defer stream.Close()
	var hdr protocol.StreamHeader
	if err := protocol.ReadMsg(stream, &hdr); err != nil {
		return
	}
	switch hdr.T {
	case protocol.StreamPush:
		a.handlePush(sess, stream)
	case protocol.StreamDial:
		a.handleDial(hdr, stream)
	}
}

func (a *Agent) handlePush(sess *session, stream net.Conn) {
	stream.SetDeadline(time.Now().Add(handshakeTimeout))
	var push protocol.ConfigPush
	if err := protocol.ReadMsg(stream, &push); err != nil {
		return
	}
	bindErrs, err := a.applyConfig(sess, &push)
	ack := protocol.ConfigAck{Version: push.Version, BindErrors: bindErrs}
	if err != nil {
		ack.Error = err.Error()
	}
	protocol.WriteMsg(stream, ack)

	switch {
	case err != nil:
		a.log.Printf("[%s] config v%d rejected: %v", sess.addr, push.Version, err)
	case len(bindErrs) > 0:
		for _, be := range bindErrs {
			a.log.Printf("[%s] config v%d: %s", sess.addr, push.Version, be.Error)
		}
		a.log.Printf("[%s] config v%d partially applied: %d ingress, %d egress, %d bind errors",
			sess.addr, push.Version, len(push.In), len(push.Out), len(bindErrs))
	default:
		a.log.Printf("[%s] config v%d applied: %d ingress, %d egress",
			sess.addr, push.Version, len(push.In), len(push.Out))
		for _, out := range push.Out {
			if len(out.From) > 0 {
				a.log.Printf("[%s] egress %s → %s serving %v", sess.addr, out.Type, out.Target, out.From)
			}
		}
	}
}

// handleDial executes an egress request coming from the relay.
func (a *Agent) handleDial(hdr protocol.StreamHeader, stream net.Conn) {
	if hdr.Egress == nil {
		return
	}
	remote, err := egress.Dial(hdr.Egress)
	if err != nil {
		a.log.Printf("dial %s: %v", egress.Target(hdr.Egress), err)
		return
	}
	copyio.Pipe(stream, remote, nil, nil)
}

// Bootstrap initializes a fresh relay database over the network: it
// generates the admin machine token and the database password, both
// returned to the caller (shown once). The connection is closed by the
// relay right after; the machine then connects normally.
func Bootstrap(addr, pinsPath string) (token, password string, err error) {
	token = protocol.NewToken("adm_")
	password = protocol.NewPassword()
	a := New(Config{}, Options{PinsPath: pinsPath})
	conn, _, err := a.dial(addr, "", protocol.Hello{
		Purpose:    protocol.PurposeInit,
		AdminToken: token,
		DBPassword: password,
	})
	if err != nil {
		return "", "", err
	}
	conn.Close()
	return token, password, nil
}
