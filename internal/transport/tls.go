// Package transport owns the tunnel channel: the relay's self-signed TLS
// certificate, certificate pinning on the agent side (explicit pin or
// trust-on-first-use) and the yamux session settings shared by both ends.
package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// PinPrefix prefixes every certificate fingerprint.
const PinPrefix = "sha256:"

// DialTimeout bounds the TCP connect and the TLS handshake of a tunnel.
const DialTimeout = 15 * time.Second

// Fingerprint is the pin of a certificate: sha256 of its public key info.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return PinPrefix + hex.EncodeToString(sum[:])
}

// EnsureCertificate loads the relay certificate, generating a self-signed
// P-256 one on first run.
func EnsureCertificate(certFile, keyFile string) (tls.Certificate, string, error) {
	certOK, keyOK := fileExists(certFile), fileExists(keyFile)
	switch {
	case certOK && keyOK:
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return tls.Certificate{}, "", fmt.Errorf("transport: load cert: %w", err)
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return tls.Certificate{}, "", err
		}
		return cert, Fingerprint(leaf), nil
	case certOK != keyOK:
		return tls.Certificate{}, "", fmt.Errorf("transport: cert/key pair incomplete: %s, %s", certFile, keyFile)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "dyrokol-relay"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o755); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o755); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, "", err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, Fingerprint(leaf), nil
}

// ServerConfig is the relay's TLS configuration.
func ServerConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
}

// MuxConfig is the yamux configuration used by both ends of a tunnel.
func MuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.EnableKeepAlive = true
	c.KeepAliveInterval = 10 * time.Second
	c.MaxStreamWindowSize = 2 * 1024 * 1024
	return c
}

// TuneSocket raises the kernel socket buffers of a (possibly TLS) TCP
// connection so long fat tunnels are not limited by the default windows.
// Best effort: a failure never breaks the connection.
func TuneSocket(c net.Conn) {
	if t, ok := c.(*tls.Conn); ok {
		c = t.NetConn()
	}
	if t, ok := c.(*net.TCPConn); ok {
		t.SetReadBuffer(1 << 20)
		t.SetWriteBuffer(1 << 20)
	}
}

// --- pinning ---

// pinsMu serialises read-modify-write cycles of pin files: an agent dials
// several relays concurrently and each may record a first-use pin.
var pinsMu sync.Mutex

func loadPins(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	p := map[string]string{}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("transport: parse pin file %s: %w", path, err)
	}
	return p, nil
}

func savePin(path, addr, pin string) error {
	pinsMu.Lock()
	defer pinsMu.Unlock()
	p, err := loadPins(path)
	if err != nil {
		return err
	}
	p[addr] = pin
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func storedPin(path, addr string) (string, error) {
	pinsMu.Lock()
	defer pinsMu.Unlock()
	p, err := loadPins(path)
	if err != nil {
		return "", err
	}
	return p[addr], nil
}

// DialTLS dials addr over TLS verifying the server certificate against pin.
// If pin is empty, the fingerprint stored in pinsPath is used; when the
// address is unknown it is trusted and stored (trust-on-first-use). An empty
// pinsPath disables the store.
func DialTLS(addr, pin, pinsPath string) (net.Conn, error) {
	expected := pin
	if expected == "" && pinsPath != "" {
		var err error
		if expected, err = storedPin(pinsPath, addr); err != nil {
			return nil, err
		}
	}

	var got string
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// The relay certificate is self-signed: it is verified by pin below.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("transport: no certificate")
			}
			cert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			got = Fingerprint(cert)
			if expected != "" && got != expected {
				return fmt.Errorf("transport: certificate pin mismatch for %s: got %s want %s", addr, got, expected)
			}
			return nil
		},
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: DialTimeout}, "tcp", addr, cfg)
	if err != nil {
		return nil, err
	}
	if expected == "" && pinsPath != "" {
		if err := savePin(pinsPath, addr, got); err != nil {
			conn.Close()
			return nil, fmt.Errorf("transport: save pin: %w", err)
		}
	}
	TuneSocket(conn)
	return conn, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
