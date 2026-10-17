package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnech/DyroKol/internal/protocol"
)

// Patch changes the mutable fields of an ingress port or an egress endpoint;
// nil fields are left unchanged.
type Patch struct {
	Label *string   `json:"label"`
	Allow *[]string `json:"allow"`
	Deny  *[]string `json:"deny"`
}

func (p Patch) apply(label *string, allow, deny *[]string) {
	if p.Label != nil {
		*label = *p.Label
	}
	if p.Allow != nil {
		*allow = *p.Allow
	}
	if p.Deny != nil {
		*deny = *p.Deny
	}
}

// InPortPatch changes an ingress port; nil fields are left unchanged. The
// machine that owns the port cannot change.
type InPortPatch struct {
	Patch
	Port *int `json:"port"`
}

// EndpointPatch changes an egress endpoint; nil fields are left unchanged.
// The machine and the type are fixed; Host and Port apply to direct
// endpoints, the SOCKS5 login to socks5 endpoints.
type EndpointPatch struct {
	Patch
	Host       *string `json:"host"`
	Port       *int    `json:"port"`
	Socks5User *string `json:"socks5_user"`
	Socks5Pass *string `json:"socks5_pass"`
}

// RoutePatch re-points a route; nil fields are left unchanged.
type RoutePatch struct {
	InPortID   *int64 `json:"inport_id"`
	EndpointID *int64 `json:"endpoint_id"`
	Enabled    *bool  `json:"enabled"`
}

// ClientPatch changes a machine; nil fields are left unchanged. A new role
// takes effect the next time the machine authenticates.
type ClientPatch struct {
	Label *string `json:"label"`
	Role  *string `json:"role"`
}

func now() int64 { return time.Now().Unix() }

// Topology is a consistent snapshot of the whole configuration.
type Topology struct {
	Version   int64
	Clients   []Client
	InPorts   []InPort
	Endpoints []Endpoint
	Routes    []Route
}

// Topology reads the whole configuration under one lock.
func (s *Store) Topology() (*Topology, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return topology(s.cfg)
}

func topology(q querier) (*Topology, error) {
	t := &Topology{}
	var err error
	if t.Version, err = configVersion(q); err != nil {
		return nil, err
	}
	if t.Clients, err = queryAll(q, scanClient, `SELECT `+clientCols+` FROM clients ORDER BY created_at, key_hash`); err != nil {
		return nil, err
	}
	if t.InPorts, err = queryAll(q, scanInPort, `SELECT `+inportCols+` FROM inports ORDER BY id`); err != nil {
		return nil, err
	}
	if t.Endpoints, err = queryAll(q, scanEndpoint, `SELECT `+endpointCols+` FROM endpoints ORDER BY id`); err != nil {
		return nil, err
	}
	if t.Routes, err = queryAll(q, scanRoute, `SELECT `+routeCols+` FROM routes ORDER BY id`); err != nil {
		return nil, err
	}
	return t, nil
}

// --- machines ---

// CreateClient registers a machine.
func (s *Store) CreateClient(keyHash, role, label string) (Client, error) {
	if keyHash == "" || keyHash == MachineRelay {
		return Client{}, invalidf("bad machine key")
	}
	if !validRole(role) {
		return Client{}, invalidf("unknown role %q", role)
	}
	if err := validateLabel(label); err != nil {
		return Client{}, err
	}
	c := Client{KeyHash: keyHash, Role: role, Label: label, CreatedAt: now()}
	err := s.mutate(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(bg, `INSERT INTO clients (`+clientCols+`) VALUES (?, ?, ?, ?)`,
			c.KeyHash, c.Role, c.Label, c.CreatedAt)
		if isUniqueViolation(err) {
			return ErrKeyInUse
		}
		return err
	})
	return c, err
}

// GetClient returns one machine.
func (s *Store) GetClient(keyHash string) (Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return getClient(s.cfg, keyHash)
}

func getClient(q querier, keyHash string) (Client, error) {
	return queryOne(q, scanClient, `SELECT `+clientCols+` FROM clients WHERE key_hash = ?`, keyHash)
}

// ListClients returns every machine, oldest first.
func (s *Store) ListClients() ([]Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queryAll(s.cfg, scanClient, `SELECT `+clientCols+` FROM clients ORDER BY created_at, key_hash`)
}

// UpdateClient renames a machine and/or switches its role.
func (s *Store) UpdateClient(keyHash string, p ClientPatch) error {
	return s.mutate(func(tx *sql.Tx) error {
		c, err := getClient(tx, keyHash)
		if err != nil {
			return err
		}
		if p.Label != nil {
			c.Label = *p.Label
		}
		if p.Role != nil {
			c.Role = *p.Role
		}
		if !validRole(c.Role) {
			return invalidf("unknown role %q", c.Role)
		}
		if err := validateLabel(c.Label); err != nil {
			return err
		}
		if c.Role != protocol.RoleAdmin {
			if err := keepOneAdmin(tx, keyHash); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(bg, `UPDATE clients SET label = ?, role = ? WHERE key_hash = ?`,
			c.Label, c.Role, keyHash)
		return err
	})
}

// keepOneAdmin refuses changes that would leave no admin besides keyHash.
func keepOneAdmin(q querier, keyHash string) error {
	var n int
	if err := q.QueryRowContext(bg, `SELECT COUNT(*) FROM clients WHERE role = ? AND key_hash != ?`,
		protocol.RoleAdmin, keyHash).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		var role string
		err := q.QueryRowContext(bg, `SELECT role FROM clients WHERE key_hash = ?`, keyHash).Scan(&role)
		if err == nil && role == protocol.RoleAdmin {
			return invalidf("the last admin machine cannot be demoted or deleted")
		}
	}
	return nil
}

// RotateClientKey replaces the key hash of a machine (new access token).
// Every entity owned by the machine follows the new hash.
func (s *Store) RotateClientKey(oldHash, newHash string) (Client, error) {
	if newHash == "" || newHash == oldHash || newHash == MachineRelay {
		return Client{}, invalidf("bad machine key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Client
	err := s.mutateLocked(func(tx *sql.Tx) error {
		var err error
		if c, err = getClient(tx, oldHash); err != nil {
			return err
		}
		if _, err := tx.ExecContext(bg, `UPDATE clients SET key_hash = ? WHERE key_hash = ?`, newHash, oldHash); err != nil {
			if isUniqueViolation(err) {
				return ErrKeyInUse
			}
			return err
		}
		for _, table := range []string{"inports", "endpoints"} {
			if _, err := tx.ExecContext(bg, `UPDATE `+table+` SET machine = ? WHERE machine = ?`, newHash, oldHash); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Client{}, err
	}
	s.data.ExecContext(bg, `UPDATE machines SET key_hash = ? WHERE key_hash = ?`, newHash, oldHash)
	c.KeyHash = newHash
	return c, nil
}

// DeleteClient removes a machine together with every ingress port and
// egress endpoint it owns (and their routes).
func (s *Store) DeleteClient(keyHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var inports []int64
	err := s.mutateLocked(func(tx *sql.Tx) error {
		if err := keepOneAdmin(tx, keyHash); err != nil {
			return err
		}
		var err error
		inports, err = queryAll(tx, scanID, `SELECT id FROM inports WHERE machine = ?`, keyHash)
		if err != nil {
			return err
		}
		for _, table := range []string{"inports", "endpoints"} {
			if _, err := tx.ExecContext(bg, `DELETE FROM `+table+` WHERE machine = ?`, keyHash); err != nil {
				return err
			}
		}
		return execOne(tx, `DELETE FROM clients WHERE key_hash = ?`, keyHash)
	})
	if err == nil {
		s.dropRuntimeLocked(inports, []string{keyHash})
	}
	return err
}

func scanID(r scanner) (int64, error) {
	var id int64
	err := r.Scan(&id)
	return id, err
}

func checkMachine(q querier, machine string) error {
	if machine == MachineRelay {
		return nil
	}
	_, err := getClient(q, machine)
	if errors.Is(err, ErrNotFound) {
		return &Error{ErrNotFound, "machine not found"}
	}
	return err
}

// --- ingress ports ---

// CreateInPort adds an ingress port; Machine defaults to the relay.
func (s *Store) CreateInPort(ip InPort) (InPort, error) {
	if err := ip.normalize(); err != nil {
		return InPort{}, err
	}
	ip.CreatedAt = now()
	err := s.mutate(func(tx *sql.Tx) error {
		if err := checkMachine(tx, ip.Machine); err != nil {
			return err
		}
		res, err := tx.ExecContext(bg,
			`INSERT INTO inports (machine, port, label, allow, deny, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			ip.Machine, ip.Port, ip.Label, encodeList(ip.Allow), encodeList(ip.Deny), ip.CreatedAt)
		if isUniqueViolation(err) {
			return ErrPortInUse
		}
		if err != nil {
			return err
		}
		ip.ID, err = res.LastInsertId()
		return err
	})
	return ip, err
}

// GetInPort returns one ingress port.
func (s *Store) GetInPort(id int64) (InPort, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return getInPort(s.cfg, id)
}

func getInPort(q querier, id int64) (InPort, error) {
	return queryOne(q, scanInPort, `SELECT `+inportCols+` FROM inports WHERE id = ?`, id)
}

// ListInPorts returns every ingress port.
func (s *Store) ListInPorts() ([]InPort, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queryAll(s.cfg, scanInPort, `SELECT `+inportCols+` FROM inports ORDER BY id`)
}

// UpdateInPort changes the label and/or source filters of an ingress port.
func (s *Store) UpdateInPort(id int64, p InPortPatch) error {
	return s.mutate(func(tx *sql.Tx) error {
		ip, err := getInPort(tx, id)
		if err != nil {
			return err
		}
		p.apply(&ip.Label, &ip.Allow, &ip.Deny)
		if p.Port != nil {
			ip.Port = *p.Port
		}
		if err := ip.normalize(); err != nil {
			return err
		}
		_, err = tx.ExecContext(bg, `UPDATE inports SET port = ?, label = ?, allow = ?, deny = ? WHERE id = ?`,
			ip.Port, ip.Label, encodeList(ip.Allow), encodeList(ip.Deny), id)
		if isUniqueViolation(err) {
			return ErrPortInUse
		}
		return err
	})
}

// DeleteInPort removes an ingress port and its route.
func (s *Store) DeleteInPort(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.mutateLocked(func(tx *sql.Tx) error {
		return execOne(tx, `DELETE FROM inports WHERE id = ?`, id)
	})
	if err == nil {
		s.dropRuntimeLocked([]int64{id}, nil)
	}
	return err
}

// --- egress endpoints ---

// CreateEndpoint adds an egress endpoint; Machine defaults to the relay,
// Type to direct and Host to 127.0.0.1.
func (s *Store) CreateEndpoint(e Endpoint) (Endpoint, error) {
	if err := e.normalize(); err != nil {
		return Endpoint{}, err
	}
	e.CreatedAt = now()
	err := s.mutate(func(tx *sql.Tx) error {
		if err := checkMachine(tx, e.Machine); err != nil {
			return err
		}
		res, err := tx.ExecContext(bg,
			`INSERT INTO endpoints (machine, type, host, port, socks5_user, socks5_pass, allow, deny, label, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.Machine, e.Type, e.Host, e.Port, e.Socks5User, e.Socks5Pass,
			encodeList(e.Allow), encodeList(e.Deny), e.Label, e.CreatedAt)
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		if err != nil {
			return err
		}
		e.ID, err = res.LastInsertId()
		return err
	})
	return e, err
}

// GetEndpoint returns one egress endpoint.
func (s *Store) GetEndpoint(id int64) (Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return getEndpoint(s.cfg, id)
}

func getEndpoint(q querier, id int64) (Endpoint, error) {
	return queryOne(q, scanEndpoint, `SELECT `+endpointCols+` FROM endpoints WHERE id = ?`, id)
}

// ListEndpoints returns every egress endpoint.
func (s *Store) ListEndpoints() ([]Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queryAll(s.cfg, scanEndpoint, `SELECT `+endpointCols+` FROM endpoints ORDER BY id`)
}

// UpdateEndpoint changes the label and/or destination filters of an endpoint.
func (s *Store) UpdateEndpoint(id int64, p EndpointPatch) error {
	return s.mutate(func(tx *sql.Tx) error {
		e, err := getEndpoint(tx, id)
		if err != nil {
			return err
		}
		if e.Type != TypeSocks5 && (p.Socks5User != nil || p.Socks5Pass != nil) {
			return invalidf("a direct endpoint has no socks5 settings")
		}
		if e.Type == TypeSocks5 && (p.Host != nil || p.Port != nil) {
			return invalidf("a socks5 endpoint has no target host or port")
		}
		p.apply(&e.Label, &e.Allow, &e.Deny)
		if p.Host != nil {
			e.Host = *p.Host
		}
		if p.Port != nil {
			e.Port = *p.Port
		}
		if p.Socks5User != nil {
			e.Socks5User = *p.Socks5User
		}
		if p.Socks5Pass != nil {
			e.Socks5Pass = *p.Socks5Pass
		}
		if err := e.normalize(); err != nil {
			return err
		}
		_, err = tx.ExecContext(bg, `UPDATE endpoints SET host = ?, port = ?,
			socks5_user = ?, socks5_pass = ?, allow = ?, deny = ?, label = ? WHERE id = ?`,
			e.Host, e.Port, e.Socks5User, e.Socks5Pass,
			encodeList(e.Allow), encodeList(e.Deny), e.Label, id)
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return err
	})
}

// DeleteEndpoint removes an endpoint and the routes pointing at it. It
// returns the ingress ports that lost their route.
func (s *Store) DeleteEndpoint(id int64) ([]int64, error) {
	var unrouted []int64
	err := s.mutate(func(tx *sql.Tx) error {
		var err error
		unrouted, err = queryAll(tx, scanID, `SELECT inport_id FROM routes WHERE endpoint_id = ?`, id)
		if err != nil {
			return err
		}
		return execOne(tx, `DELETE FROM endpoints WHERE id = ?`, id)
	})
	return unrouted, err
}

// --- routes ---

// CreateRoute connects an ingress port to an endpoint.
func (s *Store) CreateRoute(inportID, endpointID int64) (Route, error) {
	rt := Route{InPortID: inportID, EndpointID: endpointID, CreatedAt: now(), Enabled: true}
	err := s.mutate(func(tx *sql.Tx) error {
		if _, err := getInPort(tx, inportID); err != nil {
			return wrapNotFound(err, "ingress port not found")
		}
		if _, err := getEndpoint(tx, endpointID); err != nil {
			return wrapNotFound(err, "egress port not found")
		}
		res, err := tx.ExecContext(bg, `INSERT INTO routes (inport_id, endpoint_id, created_at) VALUES (?, ?, ?)`,
			inportID, endpointID, rt.CreatedAt)
		if isUniqueViolation(err) {
			return ErrAlreadyRouted
		}
		if err != nil {
			return err
		}
		rt.ID, err = res.LastInsertId()
		return err
	})
	return rt, err
}

func wrapNotFound(err error, msg string) error {
	if errors.Is(err, ErrNotFound) {
		return &Error{ErrNotFound, msg}
	}
	return err
}

// errNoChange aborts a mutation that would change nothing, so no config
// version is spent on it.
var errNoChange = errors.New("no change")

// SetEndpointRoutes makes inportIDs exactly the set of ingress ports routed
// to the endpoint, in one transaction: listed ports are routed to it (moved
// from another endpoint if they had a route), ports routed to it but not
// listed lose their route. Any number of ingress ports may feed one endpoint.
// It returns the ingress ports whose previous route went away, so the caller
// can drop their live connections.
func (s *Store) SetEndpointRoutes(endpointID int64, inportIDs []int64) ([]int64, error) {
	want := map[int64]bool{}
	for _, id := range inportIDs {
		want[id] = true
	}
	var affected []int64
	err := s.mutate(func(tx *sql.Tx) error {
		if _, err := getEndpoint(tx, endpointID); err != nil {
			return wrapNotFound(err, "egress port not found")
		}
		for id := range want {
			if _, err := getInPort(tx, id); err != nil {
				return wrapNotFound(err, fmt.Sprintf("ingress port %d not found", id))
			}
		}
		routes, err := queryAll(tx, scanRoute, `SELECT `+routeCols+` FROM routes`)
		if err != nil {
			return err
		}
		current := make(map[int64]Route, len(routes))
		changed := false
		for _, r := range routes {
			current[r.InPortID] = r
			if r.EndpointID == endpointID && !want[r.InPortID] {
				if _, err := tx.ExecContext(bg, `DELETE FROM routes WHERE id = ?`, r.ID); err != nil {
					return err
				}
				affected = append(affected, r.InPortID)
				changed = true
			}
		}
		for id := range want {
			r, routed := current[id]
			switch {
			case routed && r.EndpointID == endpointID:
				continue
			case routed:
				if _, err := tx.ExecContext(bg, `UPDATE routes SET endpoint_id = ? WHERE id = ?`, endpointID, r.ID); err != nil {
					return err
				}
				affected = append(affected, id)
			default:
				if _, err := tx.ExecContext(bg, `INSERT INTO routes (inport_id, endpoint_id, created_at) VALUES (?, ?, ?)`,
					id, endpointID, now()); err != nil {
					return err
				}
			}
			changed = true
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if errors.Is(err, errNoChange) {
		return nil, nil
	}
	return affected, err
}

// UpdateRoute re-points one route atomically: another ingress port, another
// endpoint, or both. It returns the route as it was, so the caller can drop
// live connections of the ingress port that lost it, and as it is now.
func (s *Store) UpdateRoute(id int64, p RoutePatch) (before, after Route, err error) {
	err = s.mutate(func(tx *sql.Tx) error {
		var err error
		if before, err = queryOne(tx, scanRoute, `SELECT `+routeCols+` FROM routes WHERE id = ?`, id); err != nil {
			return err
		}
		after = before
		if p.InPortID != nil {
			after.InPortID = *p.InPortID
		}
		if p.EndpointID != nil {
			after.EndpointID = *p.EndpointID
		}
		if p.Enabled != nil {
			after.Enabled = *p.Enabled
		}
		if after == before {
			return errNoChange
		}
		if _, err := getInPort(tx, after.InPortID); err != nil {
			return wrapNotFound(err, "ingress port not found")
		}
		if _, err := getEndpoint(tx, after.EndpointID); err != nil {
			return wrapNotFound(err, "egress port not found")
		}
		_, err = tx.ExecContext(bg, `UPDATE routes SET inport_id = ?, endpoint_id = ?, enabled = ? WHERE id = ?`,
			after.InPortID, after.EndpointID, after.Enabled, id)
		if isUniqueViolation(err) {
			return ErrAlreadyRouted
		}
		return err
	})
	if errors.Is(err, errNoChange) {
		return before, before, nil
	}
	return before, after, err
}

// ListRoutes returns every route.
func (s *Store) ListRoutes() ([]Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queryAll(s.cfg, scanRoute, `SELECT `+routeCols+` FROM routes ORDER BY id`)
}

// DeleteRoute removes a route and returns it.
func (s *Store) DeleteRoute(id int64) (Route, error) {
	var rt Route
	err := s.mutate(func(tx *sql.Tx) error {
		var err error
		if rt, err = queryOne(tx, scanRoute, `SELECT `+routeCols+` FROM routes WHERE id = ?`, id); err != nil {
			return err
		}
		return execOne(tx, `DELETE FROM routes WHERE id = ?`, id)
	})
	return rt, err
}

// Resolve returns an ingress port and the endpoint it is routed to (nil when
// the port is not routed or its route is switched off).
func (s *Store) Resolve(inportID int64) (InPort, *Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ip, err := getInPort(s.cfg, inportID)
	if err != nil {
		return InPort{}, nil, err
	}
	ep, err := queryOne(s.cfg, scanEndpoint, `SELECT `+prefixed("e.", endpointCols)+`
		FROM routes r JOIN endpoints e ON e.id = r.endpoint_id WHERE r.inport_id = ? AND r.enabled = 1`, inportID)
	if errors.Is(err, ErrNotFound) {
		return ip, nil, nil
	}
	if err != nil {
		return InPort{}, nil, err
	}
	return ip, &ep, nil
}

// prefixed qualifies every column of a column list with p.
func prefixed(p, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = p + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
