package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/dnech/DyroKol/internal/protocol"
)

// SnapshotFormat identifies dyrokol configuration snapshots.
const SnapshotFormat = "dyrokol-config"

// Snapshot is a JSON export of the configuration database: everything needed
// to restore the mesh on another (or the same) relay. Runtime data (totals,
// samples, peers, logs) and machine tokens are never included; machines are
// referenced by key hash.
type Snapshot struct {
	Format        string     `json:"format"`
	Schema        int        `json:"schema"`
	ConfigVersion int64      `json:"config_version"`
	ExportedAt    int64      `json:"exported_at"`
	Secrets       bool       `json:"secrets"`
	Settings      Settings   `json:"settings"`
	Map           MapLayout  `json:"map"`
	Clients       []Client   `json:"clients"`
	InPorts       []InPort   `json:"inports"`
	Endpoints     []Endpoint `json:"endpoints"`
	Routes        []Route    `json:"routes"`
}

// Export snapshots the configuration. With secrets=false socks5 passwords
// are blanked, so the file is safe to share.
func (s *Store) Export(secrets bool) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := topology(s.cfg)
	if err != nil {
		return nil, err
	}
	settings, err := getSettings(s.cfg)
	if err != nil {
		return nil, err
	}
	layout, err := getMap(s.cfg)
	if err != nil {
		return nil, err
	}
	if !secrets {
		for i := range t.Endpoints {
			t.Endpoints[i].Socks5Pass = ""
		}
	}
	return &Snapshot{
		Format:        SnapshotFormat,
		Schema:        schemaVersion,
		ConfigVersion: t.Version,
		ExportedAt:    time.Now().Unix(),
		Secrets:       secrets,
		Settings:      settings,
		Map:           layout,
		Clients:       t.Clients,
		InPorts:       t.InPorts,
		Endpoints:     t.Endpoints,
		Routes:        t.Routes,
	}, nil
}

// ParseSnapshot decodes a snapshot, rejecting unknown fields.
func ParseSnapshot(data []byte) (*Snapshot, error) {
	var snap Snapshot
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&snap); err != nil {
		return nil, invalidf("bad snapshot: %v", err)
	}
	return &snap, nil
}

// Import validates a snapshot and replaces the whole configuration with it.
// Entity ids are reassigned; the config version keeps growing so every agent
// re-syncs. Runtime data of the previous configuration is dropped.
func (s *Store) Import(snap *Snapshot) error {
	if snap == nil || snap.Format != SnapshotFormat {
		return invalidf("not a dyrokol config snapshot")
	}
	if snap.Schema != schemaVersion {
		return invalidf("snapshot schema %d is not supported (want %d)", snap.Schema, schemaVersion)
	}
	if err := snap.validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.mutateLocked(func(tx *sql.Tx) error {
		for _, table := range []string{"routes", "inports", "endpoints", "clients"} {
			if _, err := tx.ExecContext(bg, `DELETE FROM `+table); err != nil {
				return err
			}
		}
		for _, c := range snap.Clients {
			if _, err := tx.ExecContext(bg, `INSERT INTO clients (`+clientCols+`) VALUES (?, ?, ?, ?)`,
				c.KeyHash, c.Role, c.Label, c.CreatedAt); err != nil {
				return err
			}
		}
		inportID := map[int64]int64{}
		for _, ip := range snap.InPorts {
			res, err := tx.ExecContext(bg,
				`INSERT INTO inports (machine, port, label, allow, deny, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				ip.Machine, ip.Port, ip.Label, encodeList(ip.Allow), encodeList(ip.Deny), ip.CreatedAt)
			if err != nil {
				return err
			}
			if inportID[ip.ID], err = res.LastInsertId(); err != nil {
				return err
			}
		}
		endpointID := map[int64]int64{}
		for _, e := range snap.Endpoints {
			res, err := tx.ExecContext(bg,
				`INSERT INTO endpoints (machine, type, host, port, socks5_user, socks5_pass, allow, deny, label, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				e.Machine, e.Type, e.Host, e.Port, e.Socks5User, e.Socks5Pass,
				encodeList(e.Allow), encodeList(e.Deny), e.Label, e.CreatedAt)
			if isUniqueViolation(err) {
				return invalidf("duplicate egress %s:%d on one machine", e.Host, e.Port)
			}
			if err != nil {
				return err
			}
			if endpointID[e.ID], err = res.LastInsertId(); err != nil {
				return err
			}
		}
		for _, r := range snap.Routes {
			if _, err := tx.ExecContext(bg, `INSERT INTO routes (inport_id, endpoint_id, created_at, enabled) VALUES (?, ?, ?, ?)`,
				inportID[r.InPortID], endpointID[r.EndpointID], r.CreatedAt, r.Enabled); err != nil {
				return err
			}
		}
		layout := snap.Map
		layout.remap(inportID, endpointID)
		if err := putMap(tx, layout); err != nil {
			return err
		}
		return putSettings(tx, snap.Settings)
	})
	if err != nil {
		return err
	}
	for _, table := range []string{"totals", "samples", "peers", "machines"} {
		s.data.ExecContext(bg, `DELETE FROM `+table)
	}
	return s.data.save()
}

// validate checks references and every entity before anything is replaced.
func (snap *Snapshot) validate() error {
	machines := map[string]bool{MachineRelay: true}
	admins := 0
	for _, c := range snap.Clients {
		if c.KeyHash == "" || machines[c.KeyHash] {
			return invalidf("bad or duplicate machine key %q", c.KeyHash)
		}
		machines[c.KeyHash] = true
		if !validRole(c.Role) {
			return invalidf("machine %q has unknown role %q", c.Label, c.Role)
		}
		if c.Role == protocol.RoleAdmin {
			admins++
		}
		if err := validateLabel(c.Label); err != nil {
			return err
		}
	}
	if admins == 0 {
		return invalidf("snapshot must contain at least one admin machine")
	}

	if err := snap.Map.normalize(); err != nil {
		return err
	}
	inports := map[int64]bool{}
	ports := map[[2]string]bool{} // machine + port
	for i := range snap.InPorts {
		ip := &snap.InPorts[i]
		if err := ip.normalize(); err != nil {
			return fmt.Errorf("ingress :%d: %w", ip.Port, err)
		}
		if !machines[ip.Machine] {
			return invalidf("ingress :%d belongs to an unknown machine", ip.Port)
		}
		portKey := [2]string{ip.Machine, strconv.Itoa(ip.Port)}
		if ports[portKey] {
			return invalidf("duplicate ingress port %d on one machine", ip.Port)
		}
		if inports[ip.ID] {
			return invalidf("duplicate ingress id %d", ip.ID)
		}
		ports[portKey], inports[ip.ID] = true, true
	}

	endpoints := map[int64]bool{}
	for i := range snap.Endpoints {
		e := &snap.Endpoints[i]
		if err := e.normalize(); err != nil {
			return fmt.Errorf("egress %s:%d: %w", e.Host, e.Port, err)
		}
		if !machines[e.Machine] {
			return invalidf("egress %s:%d belongs to an unknown machine", e.Host, e.Port)
		}
		if endpoints[e.ID] {
			return invalidf("duplicate egress id %d", e.ID)
		}
		endpoints[e.ID] = true
	}

	routed := map[int64]bool{}
	for _, r := range snap.Routes {
		if !inports[r.InPortID] {
			return invalidf("route references unknown ingress id %d", r.InPortID)
		}
		if !endpoints[r.EndpointID] {
			return invalidf("route references unknown egress id %d", r.EndpointID)
		}
		if routed[r.InPortID] {
			return invalidf("ingress id %d has more than one route", r.InPortID)
		}
		routed[r.InPortID] = true
	}
	return nil
}
