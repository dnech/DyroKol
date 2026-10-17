// Package socks5 implements the small client subset of SOCKS5 (RFC 1928)
// needed to open TCP connections through a proxy, with optional
// username/password authentication (RFC 1929).
package socks5

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	version5         = 0x05
	authNone         = 0x00
	authUser         = 0x02
	authNoAcceptable = 0xFF

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04
)

// Dial connects to the proxy and asks it to open a TCP connection to
// targetHost:targetPort. An empty user disables authentication.
func Dial(proxyAddr, user, pass, targetHost string, targetPort int, timeout time.Duration) (_ net.Conn, err error) {
	req, err := connectRequest(targetHost, targetPort)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", proxyAddr, timeout)
	if err != nil {
		return nil, fmt.Errorf("socks5: dial proxy %s: %w", proxyAddr, err)
	}
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}

	// greeting
	methods := []byte{authNone}
	if user != "" {
		methods = []byte{authNone, authUser}
	}
	if _, err := conn.Write(append([]byte{version5, byte(len(methods))}, methods...)); err != nil {
		return nil, fmt.Errorf("socks5: greeting: %w", err)
	}
	var sel [2]byte
	if _, err := io.ReadFull(conn, sel[:]); err != nil {
		return nil, fmt.Errorf("socks5: method reply: %w", err)
	}
	if sel[0] != version5 {
		return nil, fmt.Errorf("socks5: proxy speaks version %d", sel[0])
	}
	switch sel[1] {
	case authNone:
	case authUser:
		if err := auth(conn, user, pass); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("socks5: no acceptable auth method (0x%02x)", sel[1])
	}

	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("socks5: connect request: %w", err)
	}
	if err := readConnectReply(conn); err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return conn, nil
}

func auth(conn net.Conn, user, pass string) error {
	if len(user) > 255 || len(pass) > 255 {
		return fmt.Errorf("socks5: credentials too long")
	}
	req := []byte{0x01, byte(len(user))}
	req = append(req, user...)
	req = append(req, byte(len(pass)))
	req = append(req, pass...)
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("socks5: auth write: %w", err)
	}
	var rep [2]byte
	if _, err := io.ReadFull(conn, rep[:]); err != nil {
		return fmt.Errorf("socks5: auth reply: %w", err)
	}
	if rep[1] != 0x00 {
		return fmt.Errorf("socks5: authentication failed (status 0x%02x)", rep[1])
	}
	return nil
}

func connectRequest(host string, port int) ([]byte, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("socks5: bad port %d", port)
	}
	req := []byte{version5, cmdConnect, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, atypIPv4)
			req = append(req, v4...)
		} else {
			req = append(req, atypIPv6)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("socks5: host too long")
		}
		req = append(req, atypDomain, byte(len(host)))
		req = append(req, host...)
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	req = append(req, p[:]...)
	return req, nil
}

func readConnectReply(conn net.Conn) error {
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return fmt.Errorf("socks5: connect reply: %w", err)
	}
	if head[1] != 0x00 {
		return fmt.Errorf("socks5: proxy refused connection (0x%02x)", head[1])
	}
	var addrLen int
	switch head[3] {
	case atypIPv4:
		addrLen = 4
	case atypIPv6:
		addrLen = 16
	case atypDomain:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return fmt.Errorf("socks5: reply domain len: %w", err)
		}
		addrLen = int(l[0])
	default:
		return fmt.Errorf("socks5: unknown address type 0x%02x", head[3])
	}
	// bound address (addrLen) + bound port (2)
	if _, err := io.CopyN(io.Discard, conn, int64(addrLen+2)); err != nil {
		return fmt.Errorf("socks5: reply body: %w", err)
	}
	return nil
}
