package socks5

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/dnech/DyroKol/internal/copyio"
)

// ServerConfig configures Serve.
type ServerConfig struct {
	// User and Pass, when User is not empty, require RFC 1929
	// username/password authentication; otherwise no authentication is used.
	User, Pass string
	// Dial opens the outbound connection for a CONNECT request. Returning an
	// error refuses the request; ErrDenied maps to "not allowed by ruleset".
	Dial func(host string, port int) (net.Conn, error)
	// Timeout bounds the negotiation (default 10s).
	Timeout time.Duration
}

// ErrDenied is returned by ServerConfig.Dial when a destination is refused by
// policy.
var ErrDenied = errors.New("socks5: destination denied")

// Reply codes (RFC 1928 §6).
const (
	repOK             = 0x00
	repFailure        = 0x01
	repNotAllowed     = 0x02
	repHostUnreach    = 0x04
	repCmdUnsupported = 0x07
	repAtypUnsupp     = 0x08
)

// Serve speaks the server side of SOCKS5 on conn (CONNECT only), opens the
// requested destination through cfg.Dial and relays bytes until either side
// closes. It always closes conn.
func Serve(conn net.Conn, cfg ServerConfig) error {
	defer conn.Close()
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	conn.SetDeadline(time.Now().Add(cfg.Timeout))

	if err := negotiate(conn, cfg); err != nil {
		return err
	}
	host, port, err := readRequest(conn)
	if err != nil {
		return err
	}
	remote, err := cfg.Dial(host, port)
	if err != nil {
		code := byte(repHostUnreach)
		if errors.Is(err, ErrDenied) {
			code = repNotAllowed
		}
		writeReply(conn, code)
		return fmt.Errorf("socks5: connect %s: %w", net.JoinHostPort(host, strconv.Itoa(port)), err)
	}
	if err := writeReply(conn, repOK); err != nil {
		remote.Close()
		return err
	}
	conn.SetDeadline(time.Time{})
	copyio.Pipe(conn, remote, nil, nil)
	return nil
}

func negotiate(conn net.Conn, cfg ServerConfig) error {
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return fmt.Errorf("socks5: greeting: %w", err)
	}
	if head[0] != version5 {
		return fmt.Errorf("socks5: client speaks version %d", head[0])
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return fmt.Errorf("socks5: methods: %w", err)
	}
	want := byte(authNone)
	if cfg.User != "" {
		want = authUser
	}
	found := false
	for _, m := range methods {
		if m == want {
			found = true
		}
	}
	if !found {
		conn.Write([]byte{version5, authNoAcceptable})
		return errors.New("socks5: no acceptable auth method offered")
	}
	if _, err := conn.Write([]byte{version5, want}); err != nil {
		return err
	}
	if want == authNone {
		return nil
	}
	// RFC 1929 sub-negotiation
	var v [2]byte
	if _, err := io.ReadFull(conn, v[:]); err != nil || v[0] != 0x01 {
		return errors.New("socks5: bad auth request")
	}
	user := make([]byte, v[1])
	if _, err := io.ReadFull(conn, user); err != nil {
		return err
	}
	var pl [1]byte
	if _, err := io.ReadFull(conn, pl[:]); err != nil {
		return err
	}
	pass := make([]byte, pl[0])
	if _, err := io.ReadFull(conn, pass); err != nil {
		return err
	}
	if string(user) != cfg.User || string(pass) != cfg.Pass {
		conn.Write([]byte{0x01, 0x01})
		return errors.New("socks5: authentication failed")
	}
	_, err := conn.Write([]byte{0x01, 0x00})
	return err
}

func readRequest(conn net.Conn) (string, int, error) {
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return "", 0, fmt.Errorf("socks5: request: %w", err)
	}
	if head[0] != version5 {
		return "", 0, fmt.Errorf("socks5: client speaks version %d", head[0])
	}
	if head[1] != cmdConnect {
		writeReply(conn, repCmdUnsupported)
		return "", 0, fmt.Errorf("socks5: command 0x%02x not supported", head[1])
	}
	var host string
	switch head[3] {
	case atypIPv4:
		var b [4]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(b[:]).String()
	case atypIPv6:
		var b [16]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(b[:]).String()
	case atypDomain:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return "", 0, err
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = string(b)
	default:
		writeReply(conn, repAtypUnsupp)
		return "", 0, fmt.Errorf("socks5: address type 0x%02x not supported", head[3])
	}
	var p [2]byte
	if _, err := io.ReadFull(conn, p[:]); err != nil {
		return "", 0, err
	}
	return host, int(p[0])<<8 | int(p[1]), nil
}

// writeReply sends a reply with a zero bound address.
func writeReply(conn net.Conn, code byte) error {
	_, err := conn.Write([]byte{version5, code, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}
