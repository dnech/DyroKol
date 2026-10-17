# Command line and configuration

```
dyrokol server [flags]                                       run the relay
dyrokol client [flags]                                       run the agent
dyrokol service <install|uninstall|start|stop|restart|status>   Windows service
dyrokol help
```

The role is always given explicitly: running without arguments prints the help and exits with code 2.
`dyrokol <command> --help` gives detailed help on the flags.

Both roles shut down gracefully on `SIGINT`/`SIGTERM` (Ctrl+C): the relay finishes the statistics and
seals the database to disk, the agent closes its ports.

## Setting precedence

For every parameter the first value found is used:

1. the command-line **flag**;
2. the **environment variable** `DYROKOL_<NAME>`;
3. the value from the **configuration file** (for the relay);
4. the **default value**.

An empty environment variable counts as unset.

## The data directory

By default all files live in **`~/.config/dyrokol`** (or `$XDG_CONFIG_HOME/dyrokol` if set). Change the
directory with `--dir` / `DYROKOL_DIR`. If `--config` is given and `--dir` is not, the data directory
becomes the directory of the config file. Relative paths (`db`, `cert`, `key`) are taken from the data
directory; absolute ones are used as is.

| File | Created by | Contents |
| --- | --- | --- |
| `dyrokol-server.yaml` | relay | the relay configuration (created on first run) |
| `dyrokol.crt`, `dyrokol.key` | relay | a self-signed TLS certificate (ECDSA P-256, 5 years) and its key (`0600`) |
| `dyrokol.db.enc` | relay | the encrypted configuration: machines, ports, routes, settings |
| `dyrokol.data.db.enc` | relay | encrypted statistics: counters, chart, addresses, log. Can be deleted while the relay is stopped; it will create an empty one |
| `dyrokol.yaml` | agent | the agent configuration: the list of relays and tokens (`0600`) |
| `pins.json` | agent | remembered relay certificate fingerprints (next to the agent config) |

The name of the second database file is derived from the first: `name.db.enc` → `name.data.db.enc`.
During a write a temporary `*.tmp` appears next to it; it replaces the main file by an atomic rename, so a
power cut never leaves a half-written database.

---

## The relay: `dyrokol server`

```
dyrokol server [flags]
```

| Flag | Variable | Default | Description |
| --- | --- | --- | --- |
| `-d`, `--dir DIR` | `DYROKOL_DIR` | `~/.config/dyrokol` | data directory |
| `-c`, `--config PATH` | `DYROKOL_CONFIG` | `<dir>/dyrokol-server.yaml` | configuration file; created if missing |
| `-l`, `--listen ADDR` | `DYROKOL_LISTEN` | `:4443` | address of the tunnel port (TLS) used by agents |
| `-a`, `--admin ADDR` | `DYROKOL_ADMIN` | none | optional local TCP entrance to the admin UI, e.g. `127.0.0.1:9180`. By default no admin port is opened: the UI is reachable only through an admin machine |
| `-b`, `--db PATH` | `DYROKOL_DB` | `<dir>/dyrokol.db.enc` | encrypted database |
| `-e`, `--cert PATH` | `DYROKOL_CERT` | `<dir>/dyrokol.crt` | TLS certificate |
| `-k`, `--key PATH` | `DYROKOL_KEY` | `<dir>/dyrokol.key` | TLS key |
| `-t`, `--token TOKEN` | `DYROKOL_ADMIN_TOKEN` | generated | admin API token |
| `-f`, `--token-file PATH` | `DYROKOL_ADMIN_TOKEN_FILE` | — | read the admin API token from a file (surrounding whitespace is dropped) |
| `-x`, `--token-sha256 HEX` | `DYROKOL_ADMIN_TOKEN_SHA256` | — | store only the SHA-256 of the token, not the token itself |

### The admin API token

The token protects the admin API and the web UI (the `X-Dyrokol-Api` header). Options:

- **Not set**: one is generated on the first start and kept **inside the encrypted database** (not in
  the config file, not in the log). It stays the same after restarts. Admin machines receive it on
  connect and print it as `admin ui token`.
- **`--token` / `--token-file`**: a permanent token. Passing it by file is safer than by flag (flags
  are visible in the process list).
- **`--token-sha256`**: the relay knows only the token's hash. Acceptable if you keep the token
  yourself; but then the relay **cannot hand the token to admin machines**, and they print "admin ui
  token: unknown": you have to enter it manually. Get the hash with: `printf %s 'my-token' | sha256sum`.

Precedence if several are set: `--token-file` > `--token`, and `--token-sha256` takes precedence over
both for verification (the plain token is then not distributed).

### The relay configuration file

Created on first run with default values; flags and environment variables do **not** modify it, they
only override it for the duration of the run.

```yaml
listen: :4443                 # tunnel port for agents
# admin: 127.0.0.1:9180       # optional local entrance to the admin UI (off by default; keep it on loopback)
db: dyrokol.db.enc            # relative paths are taken from the data directory
cert: dyrokol.crt
key: dyrokol.key
# admin_token: ""             # optional fixed admin API token (otherwise generated once, kept in the database)
# admin_token_sha256: ""
```

### What happens at start

1. The TLS certificate is loaded or created (if only half of the pair exists, it is an error).
2. The tunnel port opens. **The database stays locked.**
3. The admin UI, relay ports and background tasks start when the first machine unlocks (or initializes)
   the database; see [Architecture](architecture.md#relay-lifecycle).

---

## The agent: `dyrokol client`

```
dyrokol client [flags]
```

| Flag | Variable | Default | Description |
| --- | --- | --- | --- |
| `-d`, `--dir DIR` | `DYROKOL_DIR` | `~/.config/dyrokol` | data directory |
| `-c`, `--config PATH` | `DYROKOL_CONFIG` | `<dir>/dyrokol.yaml` | agent configuration |
| `-s`, `--server ADDR` | `DYROKOL_SERVER` | — | relay address: a domain or an IP, with an optional port (default `4443`), for example `vps.example.com` or `203.0.113.5:4443`; a leading `https://` is ignored |
| `-t`, `--token TOKEN` | `DYROKOL_TOKEN` | — | this machine's **tunnel** token (not the admin token) |
| `-p`, `--db-password PASS` | `DYROKOL_DB_PASSWORD` | — | database password for automatic relay unlock; needed only by a machine that should unlock the relay after a restart (typically the admin machine) |
| `-a`, `--admin-listen ADDR` | `DYROKOL_ADMIN_LISTEN` | `127.0.0.1:9181` | local admin UI address (admin role only). A bare port (`9182`) means `127.0.0.1:9182` |
| `--no-admin` | `DYROKOL_NO_ADMIN` | off | do not open the local admin UI |
| `-i`, `--tls-pin PIN` | `DYROKOL_TLS_PIN` | — | expected relay certificate fingerprint (`sha256:…`) |
| `-n`, `--pins PATH` | `DYROKOL_PINS` | next to the config | file of remembered fingerprints |

### Three run modes

| Command | What it does |
| --- | --- |
| `dyrokol client` | Runs the agent from the config. No config gives a clear error. |
| `dyrokol client -s host:port` | **Registers the first machine.** Initializes the database on the relay (the machine becomes admin), saves the token and password to the config and runs the agent. Works only on a relay without a database: otherwise the answer is `db_exists`. |
| `dyrokol client --init` | Creates the config file with **placeholders** (relay address, token) unless it exists, and exits. The same happens when `dyrokol client` finds no config: the file is created and the agent asks you to fill it in. |
| `dyrokol client -s host:port -t TOKEN [-p PASS]` | Runs the agent for one relay **without** reading or writing a config. |

Other machines are created in the admin UI (**Machines → Add machine**) and started with the third form
or written into the agent config by hand.

### The agent configuration file

```yaml
servers:
  - addr: vps.example.com:4443       # relay address
    token: cl_…                      # this machine's tunnel token
    db_password: …                   # database password (optional, see "Security")
    tls_pin: sha256:…                # optional: an explicit fingerprint instead of TOFU
    admin_listen: 127.0.0.1:9181     # optional: where to open the admin UI (admin role); `9182` alone also works.
                                     # Default: 9181 for the first relay, 9182 for the second, and so on
    disable_admin: false             # optional: do not open the admin UI
  - addr: second-relay.example.org:4443   # several relays are possible
    token: cl_…
```

For each relay the agent keeps a separate session and reconnects on its own. The pause between attempts
grows from 1 to 15 seconds and resets if a connection lasted longer than 30 seconds.

The file is written with mode `0600`: it holds secrets.

### Certificate fingerprint verification

- By default (**TOFU**): the first connection remembers the fingerprint in `pins.json`; any difference
  afterwards is a `certificate pin mismatch` error.
- `tls_pin` / `--tls-pin` sets the fingerprint explicitly and takes precedence over `pins.json`.
- If you deliberately recreated the relay certificate, delete its entry in `pins.json`.

### The local admin UI

An admin machine listens on `admin_listen` on **loopback**, and each connection is forwarded through the
tunnel to the relay's admin port. If the chosen port is busy, the agent takes a free loopback port and
writes it to the log. The admin token is printed to the log on every connect (`admin ui token: …`).

---

## The Windows service: `dyrokol service`

```
dyrokol service install [--name NAME] [--manual] [--no-start] <server|client> [role flags]
dyrokol service uninstall [--name NAME]
dyrokol service start|stop|restart|status [--name NAME]
```

Registers `dyrokol.exe` as a Windows service that runs the chosen role with the given flags. The commands
need administrator rights. If the role has no `--dir`/`--config`, `--dir %ProgramData%\DyroKol` is added.
Details, examples and the order of registering an agent are in
[Deployment](deployment.md#windows-service).

`status` prints the state (`running`, `stopped`, …). On Linux and in Termux these commands report that they
are not supported on this platform (exit code 1): use systemd and the other methods from
[Deployment](deployment.md#making-dyrokol-a-daemon-service).

## Exit codes

| Code | When |
| --- | --- |
| `0` | normal termination, help display |
| `1` | startup error (config, port, certificate, registration) |
| `2` | wrong command or flags |
