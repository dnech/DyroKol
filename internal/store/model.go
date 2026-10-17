package store

import (
	"encoding/json"
	"strings"

	"github.com/dnech/DyroKol/internal/filter"
	"github.com/dnech/DyroKol/internal/protocol"
)

// Client is a machine allowed to connect, identified by its token hash.
type Client struct {
	KeyHash   string `json:"key_hash"`
	Role      string `json:"role"`
	Label     string `json:"label"`
	CreatedAt int64  `json:"created_at"`
}

// InPort is an ingress entity: a port where traffic enters the mesh, bound on
// Machine. Allow/Deny are source filters.
type InPort struct {
	ID        int64    `json:"id"`
	Machine   string   `json:"machine"`
	Port      int      `json:"port"`
	Label     string   `json:"label"`
	Allow     []string `json:"allow,omitempty"`
	Deny      []string `json:"deny,omitempty"`
	CreatedAt int64    `json:"created_at"`
}

// Endpoint is an egress entity: how a tunnel exits on Machine.
//
//	Type "direct":  dial Host:Port from Machine
//	Type "socks5":  Machine runs a built-in SOCKS5 server; whoever is routed in
//	                picks the destination and Machine opens the connection.
//	                Host and Port are unused. Socks5User/Socks5Pass optionally
//	                require authentication.
//
// Allow/Deny are destination filters checked before every dial.
type Endpoint struct {
	ID         int64    `json:"id"`
	Machine    string   `json:"machine"`
	Type       string   `json:"type"`
	Host       string   `json:"host"`
	Port       int      `json:"port"`
	Socks5User string   `json:"socks5_user,omitempty"`
	Socks5Pass string   `json:"socks5_pass,omitempty"`
	Allow      []string `json:"allow,omitempty"`
	Deny       []string `json:"deny,omitempty"`
	Label      string   `json:"label"`
	CreatedAt  int64    `json:"created_at"`
}

// Route connects one ingress port to one egress endpoint (1:1). A route that
// is not Enabled is kept but carries no traffic: its ingress port behaves as
// if it had no route.
type Route struct {
	ID         int64 `json:"id"`
	InPortID   int64 `json:"inport_id"`
	EndpointID int64 `json:"endpoint_id"`
	CreatedAt  int64 `json:"created_at"`
	Enabled    bool  `json:"enabled"`
}

// UnmarshalJSON reads a route; a missing "enabled" (older backups) means on.
func (r *Route) UnmarshalJSON(data []byte) error {
	type plain Route
	p := plain{Enabled: true}
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = Route(p)
	return nil
}

// Spec renders the wire description of an endpoint.
func (e Endpoint) Spec() protocol.EgressSpec {
	return protocol.EgressSpec{
		Machine:    e.Machine,
		Type:       e.Type,
		Host:       e.Host,
		Port:       e.Port,
		Allow:      e.Allow,
		Deny:       e.Deny,
		Socks5User: e.Socks5User,
		Socks5Pass: e.Socks5Pass,
	}
}

// --- validation ---

func validRole(role string) bool {
	return role == protocol.RoleAdmin || role == protocol.RoleClient
}

func validateLabel(label string) error {
	if len(label) > MaxLabel {
		return invalidf("label too long (max %d)", MaxLabel)
	}
	return nil
}

func validatePort(what string, port int) error {
	if port < 1 || port > 65535 {
		return invalidf("%s must be 1-65535", what)
	}
	return nil
}

func validateHost(what, host string) error {
	if strings.ContainsAny(host, ":/ \\") || len(host) > 255 {
		return invalidf("%s must be a plain hostname or ip without port", what)
	}
	return nil
}

func validateFilters(what string, allow, deny []string) error {
	if err := filter.ValidateLists(allow, deny); err != nil {
		return invalidf("%s filter: %v", what, err)
	}
	return nil
}

// normalize fills defaults and validates an ingress port in place.
func (ip *InPort) normalize() error {
	if ip.Machine == "" {
		ip.Machine = MachineRelay
	}
	if err := validatePort("port", ip.Port); err != nil {
		return err
	}
	if err := validateLabel(ip.Label); err != nil {
		return err
	}
	if err := filter.ValidateSource(ip.Allow, ip.Deny); err != nil {
		return invalidf("source filter: %v", err)
	}
	return nil
}

// normalize fills defaults and validates an endpoint in place.
func (e *Endpoint) normalize() error {
	if e.Machine == "" {
		e.Machine = MachineRelay
	}
	if e.Type == "" {
		e.Type = TypeDirect
	}
	switch e.Type {
	case TypeDirect:
		if e.Host == "" {
			e.Host = "127.0.0.1"
		}
		if err := validatePort("port", e.Port); err != nil {
			return err
		}
		if err := validateHost("host", e.Host); err != nil {
			return err
		}
		e.Socks5User, e.Socks5Pass = "", ""
	case TypeSocks5:
		// the exit is a built-in SOCKS5 server: there is no target
		e.Host, e.Port = "", 0
		if (e.Socks5User == "") != (e.Socks5Pass == "") {
			return invalidf("socks5 login and password must be set together")
		}
		if len(e.Socks5User) > 255 || len(e.Socks5Pass) > 255 {
			return invalidf("socks5 credentials too long")
		}
	default:
		return invalidf("unknown endpoint type %q", e.Type)
	}
	if err := validateLabel(e.Label); err != nil {
		return err
	}
	return validateFilters("destination", e.Allow, e.Deny)
}

// --- column codecs ---

// decodeList decodes a stored filter list column.
func decodeList(s string) []string {
	var out []string
	if json.Unmarshal([]byte(s), &out) != nil || len(out) == 0 {
		return nil
	}
	return out
}

// encodeList encodes a filter list for storage.
func encodeList(list []string) string {
	if len(list) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(list)
	return string(b)
}

const clientCols = `key_hash, role, label, created_at`

func scanClient(r scanner) (Client, error) {
	var c Client
	err := r.Scan(&c.KeyHash, &c.Role, &c.Label, &c.CreatedAt)
	return c, err
}

const inportCols = `id, machine, port, label, allow, deny, created_at`

func scanInPort(r scanner) (InPort, error) {
	var ip InPort
	var allow, deny string
	err := r.Scan(&ip.ID, &ip.Machine, &ip.Port, &ip.Label, &allow, &deny, &ip.CreatedAt)
	ip.Allow, ip.Deny = decodeList(allow), decodeList(deny)
	return ip, err
}

const endpointCols = `id, machine, type, host, port, socks5_user, socks5_pass, allow, deny, label, created_at`

func scanEndpoint(r scanner) (Endpoint, error) {
	var e Endpoint
	var allow, deny string
	err := r.Scan(&e.ID, &e.Machine, &e.Type, &e.Host, &e.Port,
		&e.Socks5User, &e.Socks5Pass, &allow, &deny, &e.Label, &e.CreatedAt)
	e.Allow, e.Deny = decodeList(allow), decodeList(deny)
	return e, err
}

const routeCols = `id, inport_id, endpoint_id, created_at, enabled`

func scanRoute(r scanner) (Route, error) {
	var rt Route
	err := r.Scan(&rt.ID, &rt.InPortID, &rt.EndpointID, &rt.CreatedAt, &rt.Enabled)
	return rt, err
}
