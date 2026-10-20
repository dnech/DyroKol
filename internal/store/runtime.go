package store

import (
	"database/sql"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/dnech/DyroKol/internal/protocol"
)

const (
	sampleTTL = 24 * time.Hour
	peerTTL   = 24 * time.Hour
	logTTL    = 7 * 24 * time.Hour

	maxListLimit     = 1000
	defaultListLimit = 200
)

// StatUpdate is the traffic of one ingress port during a flush window.
type StatUpdate struct {
	InPortID int64
	BytesIn  int64
	BytesOut int64
	Conns    int64
	Active   int
}

// Totals are the lifetime counters of one ingress port.
type Totals struct {
	BytesIn  int64 `json:"bytes_in"`
	BytesOut int64 `json:"bytes_out"`
	Conns    int64 `json:"conns_total"`
}

// Presence is when and from where a machine was last seen.
type Presence struct {
	LastSeen int64  `json:"last_seen"`
	LastIP   string `json:"last_ip,omitempty"`
}

// Sample is one stats data point of an ingress port.
type Sample struct {
	TS       int64 `json:"ts"`
	InPortID int64 `json:"inport_id"`
	BytesIn  int64 `json:"bytes_in"`
	BytesOut int64 `json:"bytes_out"`
	Active   int   `json:"active"`
}

// Peer is a remote address seen on an ingress port, aggregated across
// connections. In a Flush, Conns/BytesIn/BytesOut are deltas.
type Peer struct {
	IP        string `json:"ip"`
	InPortID  int64  `json:"inport_id"`
	Conns     int64  `json:"conns"`
	BytesIn   int64  `json:"bytes_in"`
	BytesOut  int64  `json:"bytes_out"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
}

// LogEntry is one operator-visible event.
type LogEntry struct {
	TS      int64  `json:"ts"`
	Level   string `json:"level"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

// Seen records a machine that is online at flush time.
type Seen struct {
	KeyHash string
	IP      string
}

// Flush is one batch of runtime data recorded at TS.
type Flush struct {
	TS     int64
	Stats  []StatUpdate
	Peers  []Peer
	Online []Seen
	Logs   []LogEntry
}

// Empty reports whether the flush carries nothing.
func (f *Flush) Empty() bool {
	return len(f.Stats) == 0 && len(f.Peers) == 0 && len(f.Online) == 0 && len(f.Logs) == 0
}

// Record applies a flush to the data database in one transaction, prunes
// expired history and seals the database.
func (s *Store) Record(f Flush) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := inTx(s.data, func(tx *sql.Tx) error {
		for _, u := range f.Stats {
			if _, err := tx.ExecContext(bg,
				`INSERT INTO totals (inport_id, bytes_in, bytes_out, conns) VALUES (?, ?, ?, ?)
				 ON CONFLICT(inport_id) DO UPDATE SET
				   bytes_in = bytes_in + excluded.bytes_in,
				   bytes_out = bytes_out + excluded.bytes_out,
				   conns = conns + excluded.conns`,
				u.InPortID, u.BytesIn, u.BytesOut, u.Conns); err != nil {
				return err
			}
			if _, err := tx.ExecContext(bg,
				`INSERT INTO samples (ts, inport_id, bytes_in, bytes_out, active) VALUES (?, ?, ?, ?, ?)`,
				f.TS, u.InPortID, u.BytesIn, u.BytesOut, u.Active); err != nil {
				return err
			}
		}
		for _, m := range f.Online {
			if _, err := tx.ExecContext(bg,
				`INSERT INTO machines (key_hash, last_seen, last_ip) VALUES (?, ?, ?)
				 ON CONFLICT(key_hash) DO UPDATE SET
				   last_seen = excluded.last_seen,
				   last_ip = CASE WHEN excluded.last_ip = '' THEN last_ip ELSE excluded.last_ip END`,
				m.KeyHash, f.TS, m.IP); err != nil {
				return err
			}
		}
		for _, p := range f.Peers {
			if p.IP == "" {
				continue
			}
			if _, err := tx.ExecContext(bg,
				`INSERT INTO peers (ip, inport_id, conns, bytes_in, bytes_out, first_seen, last_seen)
				 VALUES (?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(ip, inport_id) DO UPDATE SET
				   conns = conns + excluded.conns,
				   bytes_in = bytes_in + excluded.bytes_in,
				   bytes_out = bytes_out + excluded.bytes_out,
				   last_seen = excluded.last_seen`,
				p.IP, p.InPortID, p.Conns, p.BytesIn, p.BytesOut, f.TS, f.TS); err != nil {
				return err
			}
		}
		for _, l := range f.Logs {
			if _, err := tx.ExecContext(bg,
				`INSERT INTO logs (ts, level, source, message) VALUES (?, ?, ?, ?)`,
				l.TS, l.Level, l.Source, l.Message); err != nil {
				return err
			}
		}
		for _, prune := range []struct {
			query string
			ttl   time.Duration
		}{
			{`DELETE FROM samples WHERE ts < ?`, sampleTTL},
			{`DELETE FROM peers WHERE last_seen < ?`, peerTTL},
			{`DELETE FROM logs WHERE ts < ?`, logTTL},
		} {
			if _, err := tx.ExecContext(bg, prune.query, f.TS-int64(prune.ttl/time.Second)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.data.save()
}

// Totals returns the lifetime counters of every ingress port.
func (s *Store) Totals() (map[int64]Totals, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type row struct {
		id int64
		t  Totals
	}
	rows, err := queryAll(s.data, func(r scanner) (row, error) {
		var v row
		err := r.Scan(&v.id, &v.t.BytesIn, &v.t.BytesOut, &v.t.Conns)
		return v, err
	}, `SELECT inport_id, bytes_in, bytes_out, conns FROM totals`)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]Totals, len(rows))
	for _, r := range rows {
		out[r.id] = r.t
	}
	return out, nil
}

// Presence returns when and from where each machine was last seen.
func (s *Store) Presence() (map[string]Presence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type row struct {
		key string
		p   Presence
	}
	rows, err := queryAll(s.data, func(r scanner) (row, error) {
		var v row
		err := r.Scan(&v.key, &v.p.LastSeen, &v.p.LastIP)
		return v, err
	}, `SELECT key_hash, last_seen, last_ip FROM machines`)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Presence, len(rows))
	for _, r := range rows {
		out[r.key] = r.p
	}
	return out, nil
}

func clampLimit(limit int) int {
	if limit <= 0 || limit > maxListLimit {
		return defaultListLimit
	}
	return limit
}

// ListPeers returns the most recently seen remote addresses, newest first.
func (s *Store) ListPeers(limit int) ([]Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queryAll(s.data, func(r scanner) (Peer, error) {
		var p Peer
		err := r.Scan(&p.IP, &p.InPortID, &p.Conns, &p.BytesIn, &p.BytesOut, &p.FirstSeen, &p.LastSeen)
		return p, err
	}, `SELECT ip, inport_id, conns, bytes_in, bytes_out, first_seen, last_seen
		FROM peers ORDER BY last_seen DESC, conns DESC LIMIT ?`, clampLimit(limit))
}

// SamplesSince returns stats samples taken at or after ts, oldest first.
func (s *Store) SamplesSince(ts int64) ([]Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queryAll(s.data, func(r scanner) (Sample, error) {
		var sm Sample
		err := r.Scan(&sm.TS, &sm.InPortID, &sm.BytesIn, &sm.BytesOut, &sm.Active)
		return sm, err
	}, `SELECT ts, inport_id, bytes_in, bytes_out, active FROM samples WHERE ts >= ? ORDER BY ts, id`, ts)
}

// ListLogs returns the newest log entries first.
func (s *Store) ListLogs(limit int) ([]LogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queryAll(s.data, func(r scanner) (LogEntry, error) {
		var e LogEntry
		err := r.Scan(&e.TS, &e.Level, &e.Source, &e.Message)
		return e, err
	}, `SELECT ts, level, source, message FROM logs ORDER BY id DESC LIMIT ?`, clampLimit(limit))
}

// --- settings ---

// Settings are operator preferences kept in the config database. They are
// not part of what agents receive, so changing them does not bump the
// config version.
type Settings struct {
	Autostart bool `json:"autostart"`
	// Name is the display name of this relay, shown in the admin UI header.
	Name string `json:"name"`
	// PublicAddr is the address agents use to reach the relay: a domain or an
	// IP, with an optional port (default 7443). It only fills in the client
	// command shown in the admin UI; the relay itself never uses it.
	PublicAddr string `json:"public_addr"`
}

// SettingsPatch changes settings; nil fields are left unchanged.
type SettingsPatch struct {
	Autostart  *bool   `json:"autostart"`
	Name       *string `json:"name"`
	PublicAddr *string `json:"public_addr"`
}

// Apply returns st with the patch applied.
func (p SettingsPatch) Apply(st Settings) Settings {
	if p.Autostart != nil {
		st.Autostart = *p.Autostart
	}
	if p.Name != nil {
		st.Name = *p.Name
	}
	if p.PublicAddr != nil {
		st.PublicAddr = *p.PublicAddr
	}
	return st
}

// normalize trims and validates the settings in place.
func (st *Settings) normalize() error {
	st.Name = strings.TrimSpace(st.Name)
	if len(st.Name) > MaxLabel {
		return invalidf("name too long (max %d)", MaxLabel)
	}
	addr := strings.TrimSpace(st.PublicAddr)
	for _, scheme := range []string{"https://", "http://", "tls://", "tcp://"} {
		addr = strings.TrimPrefix(addr, scheme)
	}
	addr = strings.TrimRight(addr, "/")
	if len(addr) > 255 || strings.ContainsAny(addr, " /\\") {
		return invalidf("public address must be a domain or an ip, optionally with a port")
	}
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if host == "" {
			return invalidf("public address has no host")
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return invalidf("public address port must be 1-65535")
		}
	} else if strings.Count(addr, ":") == 1 {
		return invalidf("public address port must be 1-65535")
	}
	st.PublicAddr = addr
	return nil
}

// GetSettings reads the settings.
func (s *Store) GetSettings() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return getSettings(s.cfg)
}

func getSettings(q querier) (Settings, error) {
	var v string
	err := q.QueryRowContext(bg, `SELECT v FROM meta WHERE k = 'autostart'`).Scan(&v)
	if err != nil && err != sql.ErrNoRows {
		return Settings{}, err
	}
	st := Settings{Autostart: v == "true"}
	for key, dst := range map[string]*string{"server_name": &st.Name, "public_addr": &st.PublicAddr} {
		var val string
		err := q.QueryRowContext(bg, `SELECT v FROM meta WHERE k = ?`, key).Scan(&val)
		if err != nil && err != sql.ErrNoRows {
			return Settings{}, err
		}
		*dst = val
	}
	return st, nil
}

// SaveSettings stores the settings.
func (s *Store) SaveSettings(st Settings) error {
	if err := st.normalize(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := putSettings(s.cfg, st); err != nil {
		return err
	}
	return s.cfg.save()
}

func putSettings(q querier, st Settings) error {
	v := "false"
	if st.Autostart {
		v = "true"
	}
	for _, kv := range [][2]string{{"autostart", v}, {"server_name", st.Name}, {"public_addr", st.PublicAddr}} {
		if _, err := q.ExecContext(bg, `INSERT OR REPLACE INTO meta (k, v) VALUES (?, ?)`, kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// AdminToken returns the admin API token kept in the encrypted config
// database, creating and saving a new one on first use. It never leaves the
// database file in the clear: the file is sealed with the database password.
func (s *Store) AdminToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var tok string
	err := s.cfg.QueryRowContext(bg, `SELECT v FROM meta WHERE k = 'admin_token'`).Scan(&tok)
	if err == nil && tok != "" {
		return tok, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	tok = protocol.NewToken("api_")
	if _, err := s.cfg.ExecContext(bg, `INSERT OR REPLACE INTO meta (k, v) VALUES ('admin_token', ?)`, tok); err != nil {
		return "", err
	}
	if err := s.cfg.save(); err != nil {
		return "", err
	}
	return tok, nil
}
