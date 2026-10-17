package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultServiceName = "DyroKol"
	serviceUsageText   = `dyrokol service — manage DyroKol as a Windows service

Usage:
  dyrokol service install [--name NAME] [--manual] [--no-start] <server|client> [role flags]
  dyrokol service uninstall [--name NAME]
  dyrokol service start|stop|restart|status [--name NAME]

install registers the current executable as a service that runs the given
role with the given flags, starts it (unless --no-start) and sets it to start
at boot (unless --manual). When the role flags carry neither --dir nor
--config, --dir %ProgramData%\DyroKol is added, because the service runs as
LocalSystem and has no useful home directory. Run these commands from an
elevated (Administrator) prompt.

On Linux and Termux use systemd or Termux:Boot instead (see docs/en/deployment.md).
`
)

// serviceDataDir returns the directory a service started with args keeps its
// files in, so the wrapper can place its log file there.
func serviceDataDir(args []string) string {
	for i, a := range args {
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") {
			continue
		}
		if !hasVal && i+1 < len(args) {
			val = args[i+1]
		}
		switch name {
		case "d", "dir":
			return val
		case "c", "config":
			return filepath.Dir(val)
		}
	}
	return defaultServiceDir()
}

// serviceConfigPath returns the agent config file a service started with args
// reads: --config when given, otherwise dyrokol.yaml in the data directory.
func serviceConfigPath(args []string) string {
	for i, a := range args {
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || (name != "c" && name != "config") {
			continue
		}
		if !hasVal && i+1 < len(args) {
			val = args[i+1]
		}
		return val
	}
	return filepath.Join(serviceDataDir(args), "dyrokol.yaml")
}

// hasDataFlag reports whether args already pin the data directory or config.
func hasDataFlag(args []string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch name {
		case "d", "dir", "c", "config":
			return true
		}
	}
	return false
}

func defaultServiceDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "DyroKol")
}

// serviceCmd implements "dyrokol service ...". The platform work lives in
// service_windows.go / service_other.go.
func serviceCmd(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(os.Stderr, serviceUsageText)
		if len(args) == 0 {
			os.Exit(2)
		}
		return
	}
	if err := manageService(args[0], args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "dyrokol service %s: %v\n", args[0], err)
		if _, ok := err.(usageError); ok {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// usageError marks a mistake in the command line (exit code 2).
type usageError string

func (e usageError) Error() string { return string(e) }
