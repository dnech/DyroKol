package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const prefix = "DYROKOL_"

// Dir returns the application config directory:
// $XDG_CONFIG_HOME/dyrokol when XDG_CONFIG_HOME is set,
// otherwise ~/.config/dyrokol.
func Dir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "dyrokol"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "dyrokol"), nil
}

// Resolve picks the flag value first, then the DYROKOL_<key> environment
// variable, then def (typically a config-file value or built-in default).
func Resolve(flagVal, envKey, def string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv(prefix + envKey); v != "" {
		return v
	}
	return def
}

// EnvSet reports whether DYROKOL_<key> is set to a non-empty value.
func EnvSet(envKey string) bool {
	return os.Getenv(prefix+envKey) != ""
}

// ListenAddr normalizes a listen address: a bare port ("9181") becomes
// "127.0.0.1:9181", the loopback interface, so a forgotten host never exposes a
// local-only service. Any other value (":9181", "0.0.0.0:80", "[::1]:80") is
// returned as typed, trimmed.
func ListenAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if n, err := strconv.Atoi(addr); err == nil && n > 0 && n < 65536 {
		return "127.0.0.1:" + addr
	}
	return addr
}
