// Command dyrokol is a single binary with two roles:
//
//	dyrokol server    run the relay server
//	dyrokol client    run the agent (machine side, binds ingress ports)
//	dyrokol service   manage the Windows service (install, uninstall, ...)
//
// Both roles must be given explicitly; running dyrokol with no arguments
// prints the help.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"

	"github.com/dnech/DyroKol/internal/agent"
	"github.com/dnech/DyroKol/internal/config"
	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/server"
)

func main() {
	// Started by the Windows service manager: hand over to the service
	// wrapper, which calls run() with the arguments from the service's
	// command line. A no-op on other platforms.
	if runAsService(os.Args[1:]) {
		return
	}
	run(os.Args[1:])
}

func run(args []string) {
	if len(args) < 1 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch args[0] {
	case "server":
		serverCmd(args[1:])
	case "client":
		clientCmd(args[1:])
	case "service":
		serviceCmd(args[1:])
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "dyrokol: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		os.Exit(2)
	}
}

// svcStop is closed by the Windows service wrapper when the service manager
// asks the process to stop; it plays the role of SIGTERM there.
var svcStop = make(chan struct{})

// notifyContext returns a context cancelled by SIGINT/SIGTERM or by svcStop.
func notifyContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-svcStop:
			stop()
		case <-ctx.Done():
		}
	}()
	return ctx, stop
}

func usage(w *os.File) {
	fmt.Fprint(w, `dyrokol — ingress ports, routes, egress ports

Usage:
  dyrokol server [flags]            run the relay server
  dyrokol client [flags]            run the agent (machine side)
  dyrokol service <install|uninstall|start|stop|status>   (Windows service)
  dyrokol help

Both roles must be given explicitly. Files (config, database, certificate,
pins) live in ~/.config/dyrokol, or $XDG_CONFIG_HOME/dyrokol, unless --dir is
set; pass --config to place the config elsewhere, or override individual paths
with their own flags.

Run "dyrokol <command> --help" for the full flag list.
Environment variables use the DYROKOL_ prefix (e.g. DYROKOL_SERVER).
`)
}

// --- flag helpers ---------------------------------------------------------

// flagSet registers every option under both a short (-x) and a long (--xxx)
// name pointing at the same variable.
type flagSet struct{ *flag.FlagSet }

func newFlagSet(name string, usage func()) *flagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = usage
	return &flagSet{fs}
}

func (f *flagSet) str(p *string, short, long, def, usage string) {
	f.StringVar(p, long, def, usage)
	if short != "" {
		f.StringVar(p, short, def, usage)
	}
}

func (f *flagSet) boolean(p *bool, short, long string, def bool, usage string) {
	f.BoolVar(p, long, def, usage)
	if short != "" {
		f.BoolVar(p, short, def, usage)
	}
}

func defaultDir() string {
	if d, err := config.Dir(); err == nil {
		return d
	}
	return "."
}

// baseDirAndConfig computes the working directory and the config file path
// from the --dir / --config flags. When --config points somewhere and --dir is
// absent, the config file's own directory becomes the base for the other
// default files (db, cert, key, pins).
func baseDirAndConfig(dirFlag, cfgFlag, defaultName, envConfig string) (dir, cfgPath string) {
	dir = config.Resolve(dirFlag, "DIR", "")
	cfgPath = config.Resolve(cfgFlag, envConfig, "")
	switch {
	case cfgPath == "":
		if dir == "" {
			dir = defaultDir()
		}
		cfgPath = filepath.Join(dir, defaultName)
	case dir == "":
		dir = filepath.Dir(cfgPath)
	}
	return dir, cfgPath
}

// resolveRel turns a relative path into one anchored at dir; absolute paths
// and empty values are returned unchanged.
func resolveRel(dir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// --- server ---------------------------------------------------------------

type fileConfig struct {
	Listen           string `yaml:"listen"`
	Admin            string `yaml:"admin,omitempty"`
	DB               string `yaml:"db"`
	Cert             string `yaml:"cert"`
	Key              string `yaml:"key"`
	AdminToken       string `yaml:"admin_token,omitempty"`
	AdminTokenSHA256 string `yaml:"admin_token_sha256,omitempty"`
}

// loadServerConfig reads the server config file, writing one with the
// defaults when it is missing. Relative defaults keep the config portable:
// they resolve against the directory that holds the config file.
func loadServerConfig(path string) (fileConfig, error) {
	cfg := fileConfig{
		Listen: server.DefaultListen,
		DB:     "dyrokol.db.enc",
		Cert:   "dyrokol.crt",
		Key:    "dyrokol.key",
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
		return cfg, nil
	case !os.IsNotExist(err):
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if data, err = yaml.Marshal(cfg); err != nil {
		return cfg, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return cfg, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return cfg, err
	}
	log.Printf("server: created default config %s", path)
	return cfg, nil
}

func serverUsage(w *os.File) {
	fmt.Fprint(w, `dyrokol server — run the relay server

Usage:
  dyrokol server [flags]

Flags:
  -d, --dir DIR            working directory for defaults (env DYROKOL_DIR)
  -c, --config PATH        server config file, created when missing
                           (default <dir>/dyrokol-server.yaml; env DYROKOL_CONFIG)
  -l, --listen ADDR        tunnel channel listen address (default :4443; env DYROKOL_LISTEN)
  -a, --admin ADDR         optional local TCP entrance to the admin ui, e.g. 127.0.0.1:9180
                           (default none: the ui is reachable only through an admin
                           machine's tunnel; env DYROKOL_ADMIN)
  -b, --db PATH            encrypted database file (default <dir>/dyrokol.db.enc; env DYROKOL_DB)
  -e, --cert PATH          tls certificate file (default <dir>/dyrokol.crt; env DYROKOL_CERT)
  -k, --key PATH           tls key file (default <dir>/dyrokol.key; env DYROKOL_KEY)
  -t, --token TOKEN        admin api token (env DYROKOL_ADMIN_TOKEN; if unset, one is
                           generated once and kept in the encrypted database)
  -f, --token-file PATH    read the admin api token from a file (env DYROKOL_ADMIN_TOKEN_FILE)
  -x, --token-sha256 HEX   hex sha256 of the admin api token (env DYROKOL_ADMIN_TOKEN_SHA256)
  -h, --help               show this help

On first run the config file, the TLS certificate/key and the database are
created automatically in --dir. Flags override the config file, which in turn
overrides the built-in defaults.
`)
}

func serverCmd(args []string) {
	var (
		dirFlag, cfgFlag             string
		listen, admin, db, cert, key string
		token, tokenFile, tokenHash  string
		help                         bool
	)
	fs := newFlagSet("dyrokol server", func() { serverUsage(os.Stderr) })
	fs.str(&dirFlag, "d", "dir", "", "working directory for config, db and tls files (env DYROKOL_DIR)")
	fs.str(&cfgFlag, "c", "config", "", "server config file, created when missing (default <dir>/dyrokol-server.yaml; env DYROKOL_CONFIG)")
	fs.str(&listen, "l", "listen", "", "tunnel channel listen address (default :4443; env DYROKOL_LISTEN)")
	fs.str(&admin, "a", "admin", "", "optional local TCP entrance to the admin ui (default none: only through an admin machine; env DYROKOL_ADMIN)")
	fs.str(&db, "b", "db", "", "encrypted database file (default <dir>/dyrokol.db.enc; env DYROKOL_DB)")
	fs.str(&cert, "e", "cert", "", "tls certificate file (default <dir>/dyrokol.crt; env DYROKOL_CERT)")
	fs.str(&key, "k", "key", "", "tls key file (default <dir>/dyrokol.key; env DYROKOL_KEY)")
	fs.str(&token, "t", "token", "", "admin api token (env DYROKOL_ADMIN_TOKEN; auto-generated if unset)")
	fs.str(&tokenFile, "f", "token-file", "", "read the admin api token from this file (env DYROKOL_ADMIN_TOKEN_FILE)")
	fs.str(&tokenHash, "x", "token-sha256", "", "hex sha256 of the admin api token (env DYROKOL_ADMIN_TOKEN_SHA256)")
	fs.boolean(&help, "h", "help", false, "show this help and exit")
	fs.Parse(args)
	if help {
		serverUsage(os.Stdout)
		return
	}

	dir, cfgPath := baseDirAndConfig(dirFlag, cfgFlag, "dyrokol-server.yaml", "CONFIG")

	// Relative defaults keep the config portable: they resolve against the
	// directory that holds the config file at load time.
	cfg, err := loadServerConfig(cfgPath)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	cfg.Listen = config.Resolve(listen, "LISTEN", cfg.Listen)
	cfg.Admin = config.Resolve(admin, "ADMIN", cfg.Admin)
	cfg.DB = config.Resolve(db, "DB", cfg.DB)
	cfg.Cert = config.Resolve(cert, "CERT", cfg.Cert)
	cfg.Key = config.Resolve(key, "KEY", cfg.Key)
	cfg.AdminToken = config.Resolve(token, "ADMIN_TOKEN", cfg.AdminToken)
	cfg.AdminTokenSHA256 = config.Resolve(tokenHash, "ADMIN_TOKEN_SHA256", cfg.AdminTokenSHA256)

	// Paths from the config file are relative to the directory that holds it.
	cfg.DB = resolveRel(dir, cfg.DB)
	cfg.Cert = resolveRel(dir, cfg.Cert)
	cfg.Key = resolveRel(dir, cfg.Key)

	if tf := config.Resolve(tokenFile, "ADMIN_TOKEN_FILE", ""); tf != "" {
		data, err := os.ReadFile(tf)
		if err != nil {
			log.Fatalf("server: read admin token file: %v", err)
		}
		cfg.AdminToken = strings.TrimSpace(string(data))
	}

	// Without an explicit token the relay generates one once, keeps it inside
	// the encrypted database and hands it to admin machines when they connect.

	if cfg.Admin != "" {
		log.Printf("server: admin ui also on %s (optional local entrance; remove `admin:` from %s to serve it only through an admin machine)", cfg.Admin, cfgPath)
	}

	srv := server.New(server.Config{
		Listen:           cfg.Listen,
		AdminAddr:        cfg.Admin,
		DBPath:           cfg.DB,
		CertFile:         cfg.Cert,
		KeyFile:          cfg.Key,
		AdminToken:       cfg.AdminToken,
		AdminTokenSHA256: cfg.AdminTokenSHA256,
	})

	ctx, stop := notifyContext()
	defer stop()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("server: %v", err)
	}
	log.Println("server: stopped")
}

// --- client ---------------------------------------------------------------

func clientUsage(w *os.File) {
	fmt.Fprint(w, `dyrokol client — run the agent (machine side)

Usage:
  dyrokol client [flags]

Flags:
  -d, --dir DIR            working directory for defaults (env DYROKOL_DIR)
  -c, --config PATH        agent config file, created when missing
                           (default <dir>/dyrokol.yaml; env DYROKOL_CONFIG)
  -s, --server ADDR        relay address: a domain or an ip, with an optional port (default 4443), e.g. vps.example.com (env DYROKOL_SERVER)
  -t, --token TOKEN        tunnel token (NOT the admin UI token); with --server
                           only, one is registered on the fly (env DYROKOL_TOKEN)
  -p, --db-password PASS   database password for auto-unlock (env DYROKOL_DB_PASSWORD)
  -a, --admin-listen ADDR  local admin ui address; default 127.0.0.1:9181 when
                           this machine holds the admin role (env DYROKOL_ADMIN_LISTEN)
      --no-admin           do not expose the local admin ui (env DYROKOL_NO_ADMIN)
      --init               create the config file with placeholders to fill in
                           (relay address, token, ...) unless it exists, then exit
  -i, --tls-pin PIN        expected server certificate pin (env DYROKOL_TLS_PIN)
  -n, --pins PATH          tls pin store file (default <dir>/pins.json; env DYROKOL_PINS)
  -h, --help               show this help

The tunnel token authenticates this machine to the relay; it is never used to
log into the admin UI. When this machine holds the admin role the relay sends
the admin UI token (X-Dyrokol-Api) over the tunnel and the client prints it at
startup as "admin ui token: ...".

Modes:
  dyrokol client                       run from the config file
  dyrokol client -s host:port          register this machine on the relay, save
                                       the config and run
  dyrokol client -s host:port -t TOKEN -p PASS
                                       run a single server without touching any
                                       config file
`)
}

func clientCmd(args []string) {
	var (
		dirFlag, cfgFlag          string
		serverAddr, token, dbPass string
		adminListen, tlsPin, pins string
		noAdmin                   bool
		initOnly                  bool
		help                      bool
	)
	fs := newFlagSet("dyrokol client", func() { clientUsage(os.Stderr) })
	fs.str(&dirFlag, "d", "dir", "", "working directory for config and pins (env DYROKOL_DIR)")
	fs.str(&cfgFlag, "c", "config", "", "agent config file, created when missing (default <dir>/dyrokol.yaml; env DYROKOL_CONFIG)")
	fs.str(&serverAddr, "s", "server", "", "relay address: a domain or an ip, with an optional port (default 4443), e.g. vps.example.com (env DYROKOL_SERVER)")
	fs.str(&token, "t", "token", "", "tunnel token; with --server only, one is registered on the fly (env DYROKOL_TOKEN)")
	fs.str(&dbPass, "p", "db-password", "", "database password for auto-unlock (env DYROKOL_DB_PASSWORD)")
	fs.str(&adminListen, "a", "admin-listen", "", "local admin ui address; default 127.0.0.1:9181 for the admin role (env DYROKOL_ADMIN_LISTEN)")
	fs.boolean(&noAdmin, "", "no-admin", false, "do not expose the local admin ui (env DYROKOL_NO_ADMIN)")
	fs.str(&tlsPin, "i", "tls-pin", "", "expected server certificate pin (env DYROKOL_TLS_PIN)")
	fs.str(&pins, "n", "pins", "", "tls pin store file (default <dir>/pins.json; env DYROKOL_PINS)")
	fs.boolean(&initOnly, "", "init", false, "create the config file with placeholders unless it exists, then exit")
	fs.boolean(&help, "h", "help", false, "show this help and exit")
	fs.Parse(args)
	if help {
		clientUsage(os.Stdout)
		return
	}

	_, cfgPath := baseDirAndConfig(dirFlag, cfgFlag, "dyrokol.yaml", "CONFIG")
	pinsPath := config.Resolve(pins, "PINS", filepath.Join(filepath.Dir(cfgPath), "pins.json"))

	if initOnly {
		created, err := agent.WriteTemplate(cfgPath)
		if err != nil {
			log.Fatalf("client: %v", err)
		}
		if created {
			fmt.Printf("Created %s — fill in the relay address and the machine token, then start the agent.\n", cfgPath)
		} else {
			fmt.Printf("%s already exists, left unchanged.\n", cfgPath)
		}
		return
	}

	serverAddr = agent.NormalizeAddr(config.Resolve(serverAddr, "SERVER", ""))
	token = config.Resolve(token, "TOKEN", "")
	dbPass = config.Resolve(dbPass, "DB_PASSWORD", "")
	adminListen = config.Resolve(adminListen, "ADMIN_LISTEN", "")
	tlsPin = config.Resolve(tlsPin, "TLS_PIN", "")
	noAdmin = noAdmin || config.EnvSet("NO_ADMIN")

	// A relay this machine is already registered on is run from the saved
	// config: registering twice would fail ("database file already exists").
	registered := false
	if serverAddr != "" && token == "" {
		if saved, err := agent.LoadConfig(cfgPath); err == nil {
			for _, e := range saved.Servers {
				if e.Addr == serverAddr {
					registered = true
				}
			}
		}
	}

	var cfg agent.Config
	switch {
	case registered:
		var err error
		if cfg, err = agent.LoadConfig(cfgPath); err != nil {
			log.Fatalf("client: load config: %v", err)
		}
		for i := range cfg.Servers {
			if cfg.Servers[i].Addr == serverAddr && adminListen != "" {
				cfg.Servers[i].AdminListen = adminListen
			}
		}
		log.Printf("client: %s is already registered in %s; using the saved settings", serverAddr, cfgPath)

	case serverAddr != "" && token == "":
		tok, pass, err := agent.Bootstrap(serverAddr, pinsPath)
		var perr *protocol.Error
		if errors.As(err, &perr) && perr.Code == protocol.CodeDBExists {
			log.Fatalf("client: %s is already initialized and %s has no entry for it; pass --token and --db-password, or use the config this machine was registered with", serverAddr, cfgPath)
		}
		if err != nil {
			log.Fatalf("client: %v", err)
		}
		entry := agent.ServerEntry{
			Addr:         serverAddr,
			Token:        tok,
			TLSPin:       tlsPin,
			AdminListen:  adminListen,
			DisableAdmin: noAdmin,
			DBPassword:   pass,
		}
		if err := agent.SaveServer(cfgPath, entry); err != nil {
			log.Fatalf("client: update config: %v", err)
		}
		fmt.Printf("Machine registered on %s.\n\n", serverAddr)
		fmt.Printf("  tunnel token:  %s\n", tok)
		fmt.Printf("  db password:   %s\n", pass)
		fmt.Printf("  config:        %s\n", cfgPath)
		if !noAdmin {
			ui := config.ListenAddr(adminListen)
			if ui == "" {
				ui = agent.DefaultAdminAddr
			}
			fmt.Printf("  admin ui:      http://%s (this machine holds the admin role)\n", ui)
			fmt.Printf("\nThe relay sends the admin ui token to this machine right after it\n")
			fmt.Printf("connects; it is printed below as \"admin ui token: ...\". Log in with\n")
			fmt.Printf("that, not the tunnel token above.\n")
		}
		fmt.Printf("\nTunnel token and db password are shown once and saved to the config — keep it safe.\n\n")
		cfg, err = agent.LoadConfig(cfgPath)
		if err != nil {
			log.Fatalf("client: load config: %v", err)
		}

	case serverAddr != "":
		cfg = agent.Config{Servers: []agent.ServerEntry{{
			Addr:         serverAddr,
			Token:        token,
			TLSPin:       tlsPin,
			AdminListen:  adminListen,
			DisableAdmin: noAdmin,
			DBPassword:   dbPass,
		}}}

	default:
		var err error
		cfg, err = agent.LoadConfig(cfgPath)
		if err != nil {
			if os.IsNotExist(err) {
				if created, werr := agent.WriteTemplate(cfgPath); werr == nil && created {
					log.Fatalf("client: there was no config; created %s with placeholders. Fill in the relay address and this machine's token (admin UI: Machines → Add machine) and start again. To register the very first machine instead, run `dyrokol client --server host:port`", cfgPath)
				}
				log.Fatalf("client: config %s not found — run `dyrokol client --server host:port` to register, or pass --server/--token", cfgPath)
			}
			log.Fatalf("client: load config: %v", err)
		}
		if err := agent.CheckConfig(cfg); err != nil {
			log.Fatalf("client: %s: %v", cfgPath, err)
		}
	}

	a := agent.New(cfg, agent.Options{PinsPath: pinsPath})

	ctx, stop := notifyContext()
	defer stop()

	log.Printf("dyrokol: %d server(s)", len(cfg.Servers))
	a.Run(ctx)
	log.Println("dyrokol: stopped")
}
