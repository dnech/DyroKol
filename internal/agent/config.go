package agent

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ServerEntry is one relay this machine connects to.
type ServerEntry struct {
	Addr       string `yaml:"addr"`
	Token      string `yaml:"token"`
	TLSPin     string `yaml:"tls_pin,omitempty"`
	DBPassword string `yaml:"db_password,omitempty"`

	// AdminListen is the local address where this machine exposes the relay
	// admin UI when it connects with the admin role (default
	// DefaultAdminAddr).
	AdminListen string `yaml:"admin_listen,omitempty"`
	// DisableAdmin turns off the local admin UI even for the admin role.
	DisableAdmin bool `yaml:"disable_admin,omitempty"`
}

// DefaultPort is the relay tunnel port used when an address has none.
const DefaultPort = "7443"

// NormalizeAddr turns what an operator typed into host:port: a domain or an ip
// without a port gets DefaultPort, and a leading scheme (https://, tcp://, ...)
// and a trailing slash are dropped.
func NormalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	for _, scheme := range []string{"https://", "http://", "tls://", "tcp://"} {
		addr = strings.TrimPrefix(addr, scheme)
	}
	addr = strings.TrimRight(addr, "/")
	if addr == "" {
		return ""
	}
	if host, port, err := net.SplitHostPort(addr); err == nil && host != "" && port != "" {
		return addr
	}
	return net.JoinHostPort(strings.Trim(strings.TrimSuffix(addr, ":"), "[]"), DefaultPort)
}

// Placeholders written by WriteTemplate; a config that still has them is not
// ready to run.
const (
	placeholderAddr  = "YOUR-RELAY-HOST:7443"
	placeholderToken = "PASTE_TOKEN_HERE"
)

// isPlaceholder reports whether e is a stub entry that was never filled in.
func (e ServerEntry) isPlaceholder() bool {
	return strings.Contains(e.Addr, "YOUR-RELAY-HOST") || strings.HasPrefix(e.Token, "PASTE_")
}

const configTemplate = `# DyroKol agent configuration.
# Fill in the values below, then start the agent (or its service).
#
#   addr         the relay: a domain or an IP, the port is optional (default 7443)
#   token        this machine's tunnel token (admin UI: Machines -> Add machine)
#   db_password  only for a machine that should unlock the relay after a restart
#                (normally the admin machine); ordinary machines leave it out
servers:
  - addr: ` + placeholderAddr + `
    token: ` + placeholderToken + `
    # db_password: PASTE_DB_PASSWORD_HERE
    # admin_listen: 9181      # admin machines: where the admin UI opens locally
    # tls_pin: sha256:...     # optional: pin the relay certificate explicitly
`

// WriteTemplate creates a config file with placeholders at path unless a file
// is already there. It reports whether it wrote one.
func WriteTemplate(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(configTemplate), 0o600)
}

// CheckConfig returns an error naming what still has to be filled in.
func CheckConfig(cfg Config) error {
	if len(cfg.Servers) == 0 {
		return fmt.Errorf("no servers configured")
	}
	for i, e := range cfg.Servers {
		switch {
		case e.isPlaceholder():
			return fmt.Errorf("server #%d still has the placeholder values (addr %q, token %q): fill them in", i+1, e.Addr, e.Token)
		case e.Addr == "":
			return fmt.Errorf("server #%d has no addr", i+1)
		case e.Token == "":
			return fmt.Errorf("server #%d (%s) has no token", i+1, e.Addr)
		}
	}
	return nil
}

// Config is the agent config file.
type Config struct {
	Servers []ServerEntry `yaml:"servers"`
}

// LoadConfig reads the agent config file.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	for i := range cfg.Servers {
		cfg.Servers[i].Addr = NormalizeAddr(cfg.Servers[i].Addr)
	}
	return cfg, nil
}

// SaveServer adds entry to the config file, replacing an entry with the same
// address. The file is created when missing.
func SaveServer(path string, entry ServerEntry) error {
	entry.Addr = NormalizeAddr(entry.Addr)
	cfg, err := LoadConfig(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// stub entries from the template are dropped: a real one replaces them
	kept := cfg.Servers[:0]
	for _, e := range cfg.Servers {
		if !e.isPlaceholder() {
			kept = append(kept, e)
		}
	}
	cfg.Servers = kept
	replaced := false
	for i := range cfg.Servers {
		if cfg.Servers[i].Addr == entry.Addr {
			cfg.Servers[i] = entry
			replaced = true
		}
	}
	if !replaced {
		cfg.Servers = append(cfg.Servers, entry)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
