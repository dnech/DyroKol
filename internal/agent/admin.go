package agent

import (
	"net"
	"time"

	"github.com/dnech/DyroKol/internal/config"
	"github.com/dnech/DyroKol/internal/copyio"
	"github.com/dnech/DyroKol/internal/protocol"
)

// DefaultAdminAddr is where the local admin UI binds when none is configured.
// It stays on the loopback interface: the admin UI is reachable through the
// tunnel only, never exposed to the network.
const DefaultAdminAddr = "127.0.0.1:9181"

// defaultAdminPort is the port of the first relay's admin entrance; the n-th
// relay in the config uses defaultAdminPort+n unless it sets admin_listen, so
// several relays on one client never fight over a port.
const defaultAdminPort = 9181

// serveAdmin exposes the relay admin UI on a local listener, forwarding every
// connection over an admin stream. It returns the function that stops it.
func (a *Agent) serveAdmin(sess *session, listenAddr, adminToken string) (stop func()) {
	if listenAddr == "" {
		listenAddr = DefaultAdminAddr
	}
	listenAddr = config.ListenAddr(listenAddr)
	// The token is logged first: it must be visible even when the local
	// address cannot be bound (e.g. the relay's own admin ui is on it).
	if adminToken != "" {
		a.log.Printf("[%s] admin ui token: %s", sess.addr, adminToken)
	} else {
		a.log.Printf("[%s] admin ui token: unknown (the relay is configured with a token hash); ask the relay operator", sess.addr)
	}
	// The port is fixed (configured or default): never pick a random one, the
	// address must stay predictable. After a reconnect the previous listener
	// may still be closing, so retry for a moment.
	var ln net.Listener
	var err error
	for i := 0; i < 20; i++ {
		if ln, err = net.Listen("tcp", listenAddr); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		a.log.Printf("[%s] admin ui: cannot listen on %s: %v (change admin_listen in the client config or pass --admin-listen)", sess.addr, listenAddr, err)
		return func() {}
	}
	// Printed on every (re)start of the session, so the address and the token
	// are always at hand in the log.
	a.log.Printf("[%s] admin ui available at http://%s", sess.addr, ln.Addr())
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				stream, err := a.openStream(sess, protocol.StreamHeader{T: protocol.StreamAdmin})
				if err != nil {
					conn.Close()
					return
				}
				copyio.Pipe(conn, stream, nil, nil)
			}()
		}
	}()
	return func() { ln.Close() }
}
