package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/scrypt"

	_ "modernc.org/sqlite"
)

// Sealed file layout: magic | salt | nonce | AES-256-GCM(serialized sqlite).
const (
	magic    = "DYRK1"
	saltLen  = 16
	nonceLen = 12
	filePerm = 0o600

	scryptN = 1 << 15
	scryptR = 8
	scryptP = 1
)

// sealedDB is a SQLite database that lives only in memory while open and is
// sealed (encrypted) into path on every save. No plaintext ever touches the
// disk. A single dedicated connection holds the in-memory database.
type sealedDB struct {
	*sql.Conn
	pool *sql.DB
	path string
	key  []byte
	salt []byte
}

func deriveKey(password string, salt []byte) ([]byte, error) {
	if password == "" {
		return nil, errors.New("store: empty password")
	}
	return scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, 32)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// createSealed starts a brand new empty database for path.
func createSealed(path, password string) (*sealedDB, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return nil, err
	}
	return openMemory(path, key, salt, nil)
}

// openSealed decrypts path into a fresh in-memory database.
func openSealed(path, password string) (*sealedDB, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	head := len(magic) + saltLen + nonceLen
	if len(data) < head+16 || string(data[:len(magic)]) != magic {
		return nil, ErrBadPassword
	}
	salt := data[len(magic) : len(magic)+saltLen]
	nonce := data[len(magic)+saltLen : head]
	key, err := deriveKey(password, salt)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, data[head:], []byte(magic))
	if err != nil {
		return nil, ErrBadPassword
	}
	return openMemory(path, key, append([]byte(nil), salt...), plain)
}

type serializer interface {
	Serialize() ([]byte, error)
	Deserialize([]byte) error
}

func openMemory(path string, key, salt, image []byte) (*sealedDB, error) {
	pool, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	conn, err := pool.Conn(context.Background())
	if err != nil {
		pool.Close()
		return nil, err
	}
	d := &sealedDB{Conn: conn, pool: pool, path: path, key: key, salt: salt}
	if len(image) > 0 {
		err = conn.Raw(func(dc any) error {
			s, ok := dc.(serializer)
			if !ok {
				return errors.New("store: sqlite driver cannot deserialize")
			}
			return s.Deserialize(image)
		})
		if err != nil {
			d.close()
			return nil, fmt.Errorf("store: load database: %w", err)
		}
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
		d.close()
		return nil, err
	}
	return d, nil
}

// save encrypts the current database image and atomically replaces the
// sealed file.
func (d *sealedDB) save() error {
	var image []byte
	err := d.Raw(func(dc any) error {
		s, ok := dc.(serializer)
		if !ok {
			return errors.New("store: sqlite driver cannot serialize")
		}
		var err error
		image, err = s.Serialize()
		return err
	})
	if err != nil {
		return fmt.Errorf("store: serialize: %w", err)
	}
	gcm, err := newGCM(d.key)
	if err != nil {
		return err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out := make([]byte, 0, len(magic)+saltLen+nonceLen+len(image)+gcm.Overhead())
	out = append(out, magic...)
	out = append(out, d.salt...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, image, []byte(magic))
	return writeAtomic(d.path, out)
}

func (d *sealedDB) close() error {
	err := d.Conn.Close()
	if perr := d.pool.Close(); err == nil {
		err = perr
	}
	return err
}

// writeAtomic writes data to a temp file next to path, syncs it and renames
// it over path.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filePerm)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		os.Remove(tmp)
	}
	return werr
}
