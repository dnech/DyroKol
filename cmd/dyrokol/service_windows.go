//go:build windows

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dnech/DyroKol/internal/agent"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// --- running as a service -------------------------------------------------

type handler struct{ args []string }

func (h handler) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(h.args)
	}()
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	st <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending, WaitHint: 20000}
				close(svcStop)
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
				return false, 0
			}
		case <-done:
			// The role exited on its own (fatal error): report failure so
			// the recovery actions restart the service.
			return false, 1
		}
	}
}

// runAsService reports whether the process was started by the service
// manager and, if so, runs the service to completion.
func runAsService(args []string) bool {
	isSvc, err := svc.IsWindowsService()
	if err != nil || !isSvc {
		return false
	}
	dir := serviceDataDir(args)
	if err := os.MkdirAll(dir, 0o755); err == nil {
		if f, err := os.OpenFile(filepath.Join(dir, "dyrokol-service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			log.SetOutput(f)
		}
	}
	if err := svc.Run(defaultServiceName, handler{args}); err != nil {
		log.Printf("service: %v", err)
		os.Exit(1)
	}
	return true
}

// --- managing the service -------------------------------------------------

func manageService(action string, args []string) error {
	fs := flag.NewFlagSet("dyrokol service "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", defaultServiceName, "service name")
	manual := fs.Bool("manual", false, "start manually instead of at boot")
	noStart := fs.Bool("no-start", false, "do not start the service after install")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	rest := fs.Args()

	switch action {
	case "install":
		return installService(*name, *manual, *noStart, rest)
	case "uninstall", "remove":
		return withService(*name, uninstallService)
	case "start":
		return withService(*name, func(s *mgr.Service) error { return startService(s) })
	case "stop":
		return withService(*name, func(s *mgr.Service) error { return stopService(s) })
	case "restart":
		return withService(*name, func(s *mgr.Service) error {
			if err := stopService(s); err != nil {
				return err
			}
			return startService(s)
		})
	case "status":
		return withService(*name, func(s *mgr.Service) error {
			st, err := s.Query()
			if err != nil {
				return err
			}
			fmt.Println(stateName(st.State))
			return nil
		})
	}
	return usageError(fmt.Sprintf("unknown action %q (want install|uninstall|start|stop|restart|status)", action))
}

func connect() (*mgr.Mgr, error) {
	m, err := mgr.Connect()
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, errors.New("access denied: run from an elevated (Administrator) prompt")
		}
		return nil, err
	}
	return m, nil
}

func withService(name string, fn func(*mgr.Service) error) error {
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %q: %w", name, err)
	}
	defer s.Close()
	return fn(s)
}

func installService(name string, manual, noStart bool, rest []string) error {
	if len(rest) == 0 || (rest[0] != "server" && rest[0] != "client") {
		return usageError("install needs a role: dyrokol service install <server|client> [role flags]")
	}
	roleArgs := rest
	if !hasDataFlag(roleArgs) {
		roleArgs = append(append([]string{}, roleArgs...), "--dir", defaultServiceDir())
	}
	dir := serviceDataDir(roleArgs)

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return err
	}

	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(name); err == nil {
		s.Close()
		return fmt.Errorf("service %q already exists (uninstall it first, or pick another --name)", name)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lockDown(dir)

	cfg := mgr.Config{
		DisplayName:      "DyroKol " + rest[0],
		Description:      "DyroKol " + rest[0] + " — TCP port forwarding through a relay",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}
	if manual {
		cfg.StartType = mgr.StartManual
		cfg.DelayedAutoStart = false
	}
	s, err := m.CreateService(name, exe, cfg, roleArgs...)
	if err != nil {
		return err
	}
	defer s.Close()
	// Restart on crash: 5 s, 5 s, then 30 s; reset the counter after a day.
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400)

	fmt.Printf("service %q installed: %s %s\n", name, exe, strings.Join(roleArgs, " "))
	fmt.Printf("data and log directory: %s (log: dyrokol-service.log)\n", dir)
	if rest[0] == "client" {
		// A machine of an already activated relay needs its token: leave a
		// config with placeholders instead of starting a service that cannot work.
		cfgPath := serviceConfigPath(roleArgs)
		if _, err := agent.WriteTemplate(cfgPath); err != nil {
			return fmt.Errorf("create config: %w", err)
		}
		if cur, err := agent.LoadConfig(cfgPath); err != nil || agent.CheckConfig(cur) != nil {
			fmt.Printf("config with placeholders: %s\n", cfgPath)
			fmt.Println("Fill in the relay address and this machine's token, then run: dyrokol service start")
			return nil
		}
	}
	if noStart {
		return nil
	}
	if err := startService(s); err != nil {
		return err
	}
	fmt.Println("started")
	return nil
}

func uninstallService(s *mgr.Service) error {
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		if err := stopService(s); err != nil {
			return err
		}
	}
	if err := s.Delete(); err != nil {
		return err
	}
	fmt.Println("service removed (data files were left in place)")
	return nil
}

func startService(s *mgr.Service) error {
	if err := s.Start(); err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return nil
		}
		return err
	}
	return waitState(s, svc.Running)
}

func stopService(s *mgr.Service) error {
	_, err := s.Control(svc.Stop)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return nil
		}
		return err
	}
	return waitState(s, svc.Stopped)
}

func waitState(s *mgr.Service, want svc.State) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for state %q", stateName(want))
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start-pending"
	case svc.StopPending:
		return "stop-pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue-pending"
	case svc.PausePending:
		return "pause-pending"
	case svc.Paused:
		return "paused"
	}
	return fmt.Sprintf("unknown(%d)", s)
}

// lockDown restricts the data directory (secrets: tokens, keys, sealed
// databases) to SYSTEM and Administrators. Best effort.
func lockDown(dir string) {
	out, err := exec.Command("icacls", dir, "/inheritance:r",
		"/grant:r", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not restrict permissions on %s: %v\n%s", dir, err, out)
	}
}
