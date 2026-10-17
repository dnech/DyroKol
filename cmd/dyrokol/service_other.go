//go:build !windows

package main

import "fmt"

func runAsService([]string) bool { return false }

func manageService(action string, _ []string) error {
	switch action {
	case "install", "uninstall", "remove", "start", "stop", "restart", "status":
		return fmt.Errorf("only supported on Windows; on Linux use systemd (scripts/install.sh, docs/en/deployment.md), on Termux use Termux:Boot")
	}
	return usageError(fmt.Sprintf("unknown action %q (want install|uninstall|start|stop|restart|status)", action))
}
