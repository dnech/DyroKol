// Package egress executes an egress spec: how the final leg of a proxied
// connection is opened by the machine that owns the endpoint.
package egress

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/dnech/DyroKol/internal/filter"
	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/socks5"
)

const dialTimeout = 10 * time.Second

// Dial opens the connection described by spec:
//
//   - direct: dial Host:Port from this machine
//   - socks5: the returned connection speaks SOCKS5 to a server built into
//     this process; whoever is routed in picks the destination, and this
//     machine opens the outbound connection itself
//
// The destination is checked against the spec's allow/deny lists; a denied
// or malformed destination refuses to dial (fail closed).
func Dial(spec *protocol.EgressSpec) (net.Conn, error) {
	if spec == nil {
		return nil, errors.New("egress: empty spec")
	}
	rules, err := filter.Parse(spec.Allow, spec.Deny)
	if err != nil {
		return nil, fmt.Errorf("egress: bad destination filter: %w", err)
	}
	switch spec.Type {
	case protocol.TypeDirect:
		if !rules.Match(spec.Host, spec.Port) {
			return nil, fmt.Errorf("egress: destination %s denied by filter", Target(spec))
		}
		return net.DialTimeout("tcp", Target(spec), dialTimeout)
	case protocol.TypeSocks5:
		near, far := net.Pipe()
		go func() {
			err := socks5.Serve(far, socks5.ServerConfig{
				User: spec.Socks5User,
				Pass: spec.Socks5Pass,
				Dial: func(host string, port int) (net.Conn, error) {
					if !rules.Match(host, port) {
						return nil, socks5.ErrDenied
					}
					return net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), dialTimeout)
				},
			})
			if err != nil {
				log.Printf("egress: %v", err)
			}
		}()
		return near, nil
	default:
		return nil, fmt.Errorf("egress: unknown type %q", spec.Type)
	}
}

// Target renders the final destination of a spec for logs and the ui.
func Target(spec *protocol.EgressSpec) string {
	if spec.Type == protocol.TypeSocks5 {
		return "socks5"
	}
	return net.JoinHostPort(spec.Host, strconv.Itoa(spec.Port))
}
