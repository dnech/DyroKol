package server

import (
	"net"
	"sync"
)

// pipeListener is a net.Listener fed by hand: the admin HTTP server serves the
// streams that admin machines open through their tunnel, so the admin UI needs
// no TCP port on the relay.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

// serve hands conn to the HTTP server and blocks until the connection is
// closed, so the caller can keep the stream open meanwhile.
func (l *pipeListener) serve(conn net.Conn) {
	tracked := &trackedConn{Conn: conn, closed: make(chan struct{})}
	select {
	case l.conns <- tracked:
		<-tracked.closed
	case <-l.done:
		conn.Close()
	}
}

type trackedConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "tunnel" }
func (pipeAddr) String() string  { return "admin-tunnel" }
