// Package store keeps the relay state in two encrypted SQLite databases:
//
//   - the config database (machines, ingress ports, egress endpoints, routes,
//     settings) changes only when the operator edits the mesh; every such
//     change bumps the config version that agents sync to;
//   - the data database (traffic totals, samples, peers, machine presence,
//     logs) is runtime state, written by periodic flushes. It is disposable:
//     deleting it while the relay is down only loses history.
//
// Both live in memory while open and are sealed to disk with AES-GCM under a
// scrypt-derived key (see sealed.go).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/dnech/DyroKol/internal/protocol"
)

// Error kinds. Validation and conflict errors carry a user-facing message;
// test them with errors.Is.
var (
	ErrNoDB        = errors.New("store: no database file")
	ErrExists      = errors.New("store: database already exists")
	ErrBadPassword = errors.New("store: cannot decrypt database: bad password")
	ErrNotFound    = errors.New("not found")
	ErrInvalid     = errors.New("invalid")
	ErrConflict    = errors.New("conflict")

	ErrPortInUse     = &Error{ErrConflict, "port already used by another ingress on this machine"}
	ErrAlreadyRouted = &Error{ErrConflict, "ingress port already has a route"}
	ErrDuplicate     = &Error{ErrConflict, "an identical egress port already exists"}
	ErrKeyInUse      = &Error{ErrConflict, "machine key already exists"}
)

// Error is a store error of a given kind (ErrInvalid, ErrConflict, ...)
// with a message suitable for the operator.
type Error struct {
	Kind error
	Msg  string
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Kind }

func invalidf(format string, args ...any) error {
	return &Error{ErrInvalid, fmt.Sprintf(format, args...)}
}

// MachineRelay is the machine id of the relay itself; every client machine
// is identified by its token key hash.
const MachineRelay = "relay"

// Endpoint types.
const (
	TypeDirect = protocol.TypeDirect
	TypeSocks5 = protocol.TypeSocks5
)

// MaxLabel is the longest label accepted for any entity.
const MaxLabel = 200

// Schema 2: an ingress port is unique per machine, not globally.
// Schema 3: a route can be switched off (routes.enabled).
const schemaVersion = 3

// Store is the relay database. All methods are safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	cfg  *sealedDB
	data *sealedDB
}

// DataPath derives the companion data database path from the config path.
func DataPath(configPath string) string {
	const suffix = ".db.enc"
	if strings.HasSuffix(configPath, suffix) {
		return strings.TrimSuffix(configPath, suffix) + ".data.db.enc"
	}
	return configPath + ".data"
}

// Bootstrap creates brand new encrypted databases containing one admin
// machine identified by adminKeyHash.
func Bootstrap(path, password, adminKeyHash string) (*Store, error) {
	dataPath := DataPath(path)
	for _, p := range []string{path, dataPath} {
		if _, err := os.Stat(p); err == nil {
			return nil, ErrExists
		}
	}
	s := &Store{}
	var err error
	if s.cfg, err = createSealed(path, password); err != nil {
		return nil, err
	}
	if s.data, err = createSealed(dataPath, password); err != nil {
		s.abandon()
		return nil, err
	}
	if err := s.initSchema(); err != nil {
		s.abandon()
		return nil, err
	}
	if _, err := s.CreateClient(adminKeyHash, protocol.RoleAdmin, "admin"); err != nil {
		s.abandon()
		return nil, err
	}
	if err := s.data.save(); err != nil {
		s.abandon()
		return nil, err
	}
	return s, nil
}

// Open decrypts the databases. A missing data database is recreated empty.
func Open(path, password string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoDB
		}
		return nil, err
	}
	s := &Store{}
	var err error
	if s.cfg, err = openSealed(path, password); err != nil {
		return nil, err
	}
	dataPath := DataPath(path)
	if _, statErr := os.Stat(dataPath); statErr == nil {
		s.data, err = openSealed(dataPath, password)
	} else {
		s.data, err = createSealed(dataPath, password)
	}
	if err != nil {
		s.abandon()
		if errors.Is(err, ErrBadPassword) {
			return nil, err
		}
		return nil, fmt.Errorf("store: open data database: %w", err)
	}
	if err := s.initSchema(); err != nil {
		s.abandon()
		return nil, err
	}
	return s, nil
}

// abandon releases the in-memory databases without saving them.
func (s *Store) abandon() {
	if s.cfg != nil {
		s.cfg.close()
	}
	if s.data != nil {
		s.data.close()
	}
}

// Close seals both databases to disk and releases them.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := errors.Join(s.cfg.save(), s.data.save())
	return errors.Join(err, s.cfg.close(), s.data.close())
}

// --- schema ---

const configSchema = `
CREATE TABLE IF NOT EXISTS meta (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS clients (
  key_hash TEXT PRIMARY KEY,
  role TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS inports (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  machine TEXT NOT NULL,
  port INTEGER NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  allow TEXT NOT NULL DEFAULT '[]',
  deny TEXT NOT NULL DEFAULT '[]',
  created_at INTEGER NOT NULL,
  UNIQUE(machine, port)
);
CREATE TABLE IF NOT EXISTS endpoints (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  machine TEXT NOT NULL,
  type TEXT NOT NULL,
  host TEXT NOT NULL,
  port INTEGER NOT NULL,
  socks5_host TEXT NOT NULL DEFAULT '',
  socks5_port INTEGER NOT NULL DEFAULT 0,
  socks5_user TEXT NOT NULL DEFAULT '',
  socks5_pass TEXT NOT NULL DEFAULT '',
  allow TEXT NOT NULL DEFAULT '[]',
  deny TEXT NOT NULL DEFAULT '[]',
  label TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  UNIQUE(machine, type, host, port, socks5_host, socks5_port)
);
CREATE TABLE IF NOT EXISTS routes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  inport_id INTEGER NOT NULL UNIQUE REFERENCES inports(id) ON DELETE CASCADE,
  endpoint_id INTEGER NOT NULL REFERENCES endpoints(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1
);
INSERT OR IGNORE INTO meta (k, v) VALUES ('config_version', '0');
`

const dataSchema = `
CREATE TABLE IF NOT EXISTS totals (
  inport_id INTEGER PRIMARY KEY,
  bytes_in INTEGER NOT NULL DEFAULT 0,
  bytes_out INTEGER NOT NULL DEFAULT 0,
  conns INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS machines (
  key_hash TEXT PRIMARY KEY,
  last_seen INTEGER NOT NULL DEFAULT 0,
  last_ip TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS samples (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  inport_id INTEGER NOT NULL,
  bytes_in INTEGER NOT NULL,
  bytes_out INTEGER NOT NULL,
  active INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS peers (
  ip TEXT NOT NULL,
  inport_id INTEGER NOT NULL,
  conns INTEGER NOT NULL DEFAULT 0,
  bytes_in INTEGER NOT NULL DEFAULT 0,
  bytes_out INTEGER NOT NULL DEFAULT 0,
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  PRIMARY KEY (ip, inport_id)
);
CREATE TABLE IF NOT EXISTS logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  level TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_samples_ts ON samples(ts);
CREATE INDEX IF NOT EXISTS idx_peers_last_seen ON peers(last_seen);
CREATE INDEX IF NOT EXISTS idx_logs_ts ON logs(ts);
`

func (s *Store) initSchema() error {
	var v string
	err := s.cfg.QueryRowContext(bg, `SELECT v FROM meta WHERE k = 'schema_version'`).Scan(&v)
	switch {
	case err == nil:
		if v == "2" {
			// schema 2 → 3: routes can be switched off
			if _, err := s.cfg.ExecContext(bg, `ALTER TABLE routes ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`); err != nil {
				return fmt.Errorf("store: migrate schema 2 to 3: %w", err)
			}
		} else if v != strconv.Itoa(schemaVersion) {
			return fmt.Errorf("store: unsupported schema version %s (want %d)", v, schemaVersion)
		}
	case !isNoTable(err) && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := s.cfg.ExecContext(bg, configSchema); err != nil {
		return fmt.Errorf("store: config schema: %w", err)
	}
	if _, err := s.cfg.ExecContext(bg, `INSERT OR REPLACE INTO meta (k, v) VALUES ('schema_version', ?)`,
		strconv.Itoa(schemaVersion)); err != nil {
		return err
	}
	if _, err := s.data.ExecContext(bg, dataSchema); err != nil {
		return fmt.Errorf("store: data schema: %w", err)
	}
	return nil
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

func isNoTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// --- query helpers ---

var bg = context.Background()

// querier is satisfied by both the database connection and a transaction.
type querier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type scanner interface{ Scan(...any) error }

// queryAll runs query and scans every row with scan.
func queryAll[T any](q querier, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(bg, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// queryOne scans a single row, mapping "no rows" to ErrNotFound.
func queryOne[T any](q querier, scan func(scanner) (T, error), query string, args ...any) (T, error) {
	v, err := scan(q.QueryRowContext(bg, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// execOne runs a statement that must affect exactly one row.
func execOne(q querier, query string, args ...any) error {
	res, err := q.ExecContext(bg, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// inTx runs fn inside a transaction of db.
func inTx(db *sealedDB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(bg, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// mutate applies fn to the configuration in one transaction, bumps the config
// version and seals the database. Every change agents must learn about goes
// through it.
func (s *Store) mutate(fn func(tx *sql.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutateLocked(fn)
}

func (s *Store) mutateLocked(fn func(tx *sql.Tx) error) error {
	err := inTx(s.cfg, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(bg,
			`UPDATE meta SET v = CAST(CAST(v AS INTEGER) + 1 AS TEXT) WHERE k = 'config_version'`)
		return err
	})
	if err != nil {
		return err
	}
	return s.cfg.save()
}

// dropRuntimeLocked forgets runtime data of deleted entities. Best effort:
// the data database is disposable and saved with the next flush.
func (s *Store) dropRuntimeLocked(inportIDs []int64, machines []string) {
	for _, id := range inportIDs {
		for _, table := range []string{"totals", "samples", "peers"} {
			s.data.ExecContext(bg, `DELETE FROM `+table+` WHERE inport_id = ?`, id)
		}
	}
	for _, m := range machines {
		s.data.ExecContext(bg, `DELETE FROM machines WHERE key_hash = ?`, m)
	}
}

func configVersion(q querier) (int64, error) {
	var v int64
	err := q.QueryRowContext(bg, `SELECT CAST(v AS INTEGER) FROM meta WHERE k = 'config_version'`).Scan(&v)
	return v, err
}

// ConfigVersion returns the current configuration revision.
func (s *Store) ConfigVersion() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return configVersion(s.cfg)
}
