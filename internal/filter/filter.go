// Package filter implements white/black address lists used to control which
// sources may reach an ingress port and which destinations an egress may
// dial. Deny always wins; an empty allow list allows everything that deny
// does not block.
package filter

import (
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
)

const (
	maxRules   = 512
	maxPattern = 255
)

type pattern struct {
	raw   string
	host  string // lower-case host or wildcard ("" = any)
	ipNet *net.IPNet
	port  int // -1 = any port
}

// Rules is a parsed allow/deny pair. The zero value allows everything.
type Rules struct {
	allow []pattern
	deny  []pattern
}

// Parse validates and compiles the two lists.
func Parse(allow, deny []string) (*Rules, error) {
	a, err := parseList(allow)
	if err != nil {
		return nil, fmt.Errorf("allow list: %w", err)
	}
	d, err := parseList(deny)
	if err != nil {
		return nil, fmt.Errorf("deny list: %w", err)
	}
	return &Rules{allow: a, deny: d}, nil
}

// ValidateLists reports whether the lists are well-formed without compiling
// a Rules value for matching.
func ValidateLists(allow, deny []string) error {
	_, err := Parse(allow, deny)
	return err
}

// ValidateSource is ValidateLists for source filters. A visitor's port is
// ephemeral and never matched, so a pattern carrying one (such as "*:25")
// would silently match every visitor; it is rejected instead.
func ValidateSource(allow, deny []string) error {
	r, err := Parse(allow, deny)
	if err != nil {
		return err
	}
	for _, list := range []struct {
		name     string
		patterns []pattern
	}{{"allow", r.allow}, {"deny", r.deny}} {
		for _, p := range list.patterns {
			if p.port >= 0 {
				return fmt.Errorf("%s list: %q: source filters cannot match ports", list.name, p.raw)
			}
		}
	}
	return nil
}

// Empty reports whether the rules allow everything.
func (r *Rules) Empty() bool {
	return r == nil || (len(r.allow) == 0 && len(r.deny) == 0)
}

// MatchHost matches a source address ignoring the port component of the
// patterns (source ports are ephemeral and not meaningful).
func (r *Rules) MatchHost(host string) bool {
	return r.decide(host, -1)
}

// Match matches a host (name or ip) and a destination port; port < 0 skips
// the port check.
func (r *Rules) Match(host string, port int) bool {
	return r.decide(host, port)
}

func (r *Rules) decide(host string, port int) bool {
	if r == nil {
		return true
	}
	for _, p := range r.deny {
		if p.match(host, port) {
			return false
		}
	}
	if len(r.allow) == 0 {
		return true
	}
	for _, p := range r.allow {
		if p.match(host, port) {
			return true
		}
	}
	return false
}

func parseList(list []string) ([]pattern, error) {
	if len(list) > maxRules {
		return nil, fmt.Errorf("too many entries (%d > %d)", len(list), maxRules)
	}
	var out []pattern
	for _, raw := range list {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		p, err := parsePattern(s)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// parsePattern accepts:
//
//	host            exact host or wildcard (example.com, *.example.com)
//	host:port       host with a port (example.com:443, *:443)
//	ip              exact address (192.0.2.1)
//	cidr[/port]     network (10.0.0.0/8, 192.0.2.0/24:443)
func parsePattern(s string) (pattern, error) {
	if len(s) > maxPattern {
		return pattern{}, fmt.Errorf("pattern too long: %.40q", s)
	}
	hostPart := s
	port := -1
	if h, p, err := net.SplitHostPort(s); err == nil {
		hostPart = h
		if p == "*" {
			port = -1
		} else {
			n, cerr := strconv.Atoi(p)
			if cerr != nil || n < 1 || n > 65535 {
				return pattern{}, fmt.Errorf("bad port in pattern %q", s)
			}
			port = n
		}
	}
	if hostPart == "" {
		hostPart = "*"
	}
	hostPart = strings.ToLower(hostPart)
	if strings.Contains(hostPart, "/") {
		_, n, err := net.ParseCIDR(hostPart)
		if err != nil {
			return pattern{}, fmt.Errorf("bad cidr in pattern %q", s)
		}
		return pattern{raw: s, ipNet: n, port: port}, nil
	}
	if _, err := path.Match(hostPart, ""); err != nil {
		return pattern{}, fmt.Errorf("bad pattern %q: %v", s, err)
	}
	return pattern{raw: s, host: hostPart, port: port}, nil
}

func (p pattern) match(host string, port int) bool {
	if p.port >= 0 && port >= 0 && p.port != port {
		return false
	}
	if p.ipNet != nil {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		return ip != nil && p.ipNet.Contains(ip)
	}
	h := strings.ToLower(strings.Trim(host, "[]"))
	if p.host == "" || p.host == "*" {
		return true
	}
	if strings.ContainsAny(p.host, "*?[") {
		ok, err := path.Match(p.host, h)
		return err == nil && ok
	}
	return p.host == h
}
