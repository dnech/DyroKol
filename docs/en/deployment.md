# Deployment

## Network and firewall

| Port | Where | Must it be opened |
| --- | --- | --- |
| `4443/tcp` (or your `--listen`) | relay | **Yes**: agents connect here |
| ingress port numbers on the relay (for example, `8080`) | relay | Yes, if visitors come from outside |
| the admin UI | relay | **No port at all** by default: it is served only through the admin machine's tunnel (an optional `--admin` entrance, if you enable it, must stay on loopback) |
| ingress port numbers on agents | agent | Only if they are needed from outside; by default they listen on all interfaces |

Agents need no incoming connections: they connect to the relay themselves. The tunnel port can be moved
to `443`: `--listen :443` (this needs the right to bind ports below 1024, see below).

> Agent ingress ports listen on **all** interfaces (`:port`). If a port is needed only by the local PC,
> restrict the source with the filter `allow: 127.0.0.1`.

## Linux

### One-line install (Linux)

The script [`scripts/install.sh`](../../scripts/install.sh) downloads the binary for your architecture
(`amd64`, `arm64`, `arm`) from the [Releases](https://github.com/dnech/DyroKol/releases) page, verifies
its SHA-256 against `SHA256SUMS` and puts it in `/usr/local/bin/dyrokol`. With `--role` it also creates
the `dyrokol` user, the `/var/lib/dyrokol` directory and a systemd unit.

```bash
# the binary only
curl -fsSL https://raw.githubusercontent.com/dnech/DyroKol/main/scripts/install.sh | sudo bash

# the relay as a systemd service (starts immediately)
curl -fsSL https://raw.githubusercontent.com/dnech/DyroKol/main/scripts/install.sh | sudo bash -s -- --role server

# the agent as a systemd service (enabled; start it after registration, see below)
curl -fsSL https://raw.githubusercontent.com/dnech/DyroKol/main/scripts/install.sh | sudo bash -s -- --role client
```

| Option | Description |
| --- | --- |
| `--role server\|client` | also install and enable the `dyrokol-server` / `dyrokol-client` unit |
| `--version vX.Y.Z` | a specific version (the latest by default) |
| `--prefix DIR` | where to put the binary (`/usr/local/bin` by default) |
| `--data-dir DIR` | the service data directory (`/var/lib/dyrokol` by default) |
| `--uninstall` | remove the binary and units (data is kept); with `--purge` the data directory is deleted too |

You can download the script, read it and run it locally: `sudo bash install.sh --role server`. It
installs only what is described above and verifies the checksum; on a mismatch the installation aborts.
For a mirror or an offline check set `DYROKOL_BASE_URL` (a directory holding the files
`dyrokol-linux-<arch>` and `SHA256SUMS`). In Termux the script puts the binary in `$PREFIX/bin` without
systemd (see [Android](#android-termux)).

The script creates `/var/lib/dyrokol/dyrokol.yaml` with **placeholders** and enables the agent service
without starting it. Two cases:

- **A machine of a relay that already runs** (the usual case): create the machine in the admin UI
  (**Machines → Add machine**), open `/var/lib/dyrokol/dyrokol.yaml`, put in the relay address (a domain or
  an IP) and the machine's token, then `sudo systemctl start dyrokol-client`.
- **The very first machine of a new relay** (it initializes the database, once, as the service user):

```bash
sudo -u dyrokol dyrokol client --dir /var/lib/dyrokol --server vps.example.com
# …press Ctrl+C when you see "connected", then:
sudo systemctl start dyrokol-client
```

### Manual install

```bash
sudo install -m 0755 dyrokol /usr/local/bin/dyrokol
sudo useradd --system --home /var/lib/dyrokol --create-home --shell /usr/sbin/nologin dyrokol
```

### Making DyroKol a daemon (service)

DyroKol is an ordinary foreground process: it writes its log to stderr, shuts down cleanly on
`SIGTERM`/`SIGINT` and reconnects by itself when the link drops. So any process manager will do; a
built-in `fork`/`daemonize` is not needed (and not supported). Below are working options, from the
recommended one to fallbacks. In all examples the binary is at `/usr/local/bin/dyrokol` and the data in
`/var/lib/dyrokol`; for the agent replace `server` with `client`.

#### systemd: the relay

`/etc/systemd/system/dyrokol-server.service`:

```ini
[Unit]
Description=DyroKol relay
After=network-online.target
Wants=network-online.target

[Service]
User=dyrokol
ExecStart=/usr/local/bin/dyrokol server --dir /var/lib/dyrokol
# permanent admin token (optional): a file with the token, mode 0600, owned by dyrokol
Environment=DYROKOL_ADMIN_TOKEN_FILE=/etc/dyrokol/admin-token
# only needed for ports below 1024 (443, 80, …)
AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=on-failure
RestartSec=3
# basic hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/dyrokol
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now dyrokol-server
journalctl -u dyrokol-server -f
```

The log shows that the admin UI is reachable only through an admin machine; the admin token (generated once, kept in the encrypted database) is printed by the admin machine as `admin ui token`. Port changes in the admin UI apply
without restarting the service.

#### systemd: the agent

`/etc/systemd/system/dyrokol-client.service`:

```ini
[Unit]
Description=DyroKol agent
After=network-online.target
Wants=network-online.target

[Service]
User=dyrokol
ExecStart=/usr/local/bin/dyrokol client --dir /var/lib/dyrokol
AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=always
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/dyrokol

[Install]
WantedBy=multi-user.target
```

Before the first start of the service register the machine (see above) or prepare the config
`/var/lib/dyrokol/dyrokol.yaml` (see [CLI](cli.md#the-agent-configuration-file)) with mode `0600` owned
by `dyrokol`. The agent reconnects by itself: a network loss or a relay restart needs no intervention.

#### systemd without root (a user service)

Suited to a home PC or Raspberry Pi where you have no access to `/etc`. The file
`~/.config/systemd/user/dyrokol-client.service`:

```ini
[Unit]
Description=DyroKol agent
After=network-online.target

[Service]
ExecStart=%h/bin/dyrokol client
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now dyrokol-client
loginctl enable-linger "$USER"      # run without a login session, from boot
journalctl --user -u dyrokol-client -f
```

Ports below 1024 are not available here without root.

#### OpenRC (Alpine, Gentoo)

`/etc/init.d/dyrokol-client`:

```sh
#!/sbin/openrc-run
description="DyroKol agent"
command="/usr/local/bin/dyrokol"
command_args="client --dir /var/lib/dyrokol"
command_user="dyrokol:dyrokol"
command_background=true
pidfile="/run/${RC_SVCNAME}.pid"
output_log="/var/log/dyrokol-client.log"
error_log="/var/log/dyrokol-client.log"
supervisor=supervise-daemon     # restart on crash

depend() {
    need net
}
```

```bash
sudo chmod +x /etc/init.d/dyrokol-client
sudo rc-update add dyrokol-client default
sudo rc-service dyrokol-client start
```

#### SysV init (old systems without systemd)

`/etc/init.d/dyrokol-client`:

```sh
#!/bin/sh
### BEGIN INIT INFO
# Provides:          dyrokol-client
# Required-Start:    $network $remote_fs
# Required-Stop:     $network $remote_fs
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Description:       DyroKol agent
### END INIT INFO
BIN=/usr/local/bin/dyrokol
ARGS="client --dir /var/lib/dyrokol"
PIDFILE=/var/run/dyrokol-client.pid
LOG=/var/log/dyrokol-client.log

case "$1" in
  start)
    start-stop-daemon --start --background --make-pidfile --pidfile "$PIDFILE" \
      --chuid dyrokol --startas /bin/sh -- -c "exec $BIN $ARGS >>$LOG 2>&1" ;;
  stop)
    start-stop-daemon --stop --pidfile "$PIDFILE" --retry TERM/20/KILL/5 && rm -f "$PIDFILE" ;;
  restart) "$0" stop; "$0" start ;;
  status)
    start-stop-daemon --status --pidfile "$PIDFILE" && echo running || echo stopped ;;
  *) echo "usage: $0 {start|stop|restart|status}"; exit 1 ;;
esac
```

```bash
sudo chmod +x /etc/init.d/dyrokol-client
sudo update-rc.d dyrokol-client defaults      # Debian/Ubuntu
sudo service dyrokol-client start
```

Such a script does not restart the process after a crash; if that matters, use `supervisor` (below).

#### supervisor

`/etc/supervisor/conf.d/dyrokol-client.conf`:

```ini
[program:dyrokol-client]
command=/usr/local/bin/dyrokol client --dir /var/lib/dyrokol
user=dyrokol
autostart=true
autorestart=true
stopsignal=TERM
stdout_logfile=/var/log/dyrokol-client.log
redirect_stderr=true
```

```bash
sudo supervisorctl reread && sudo supervisorctl update
```

#### Fallback: `@reboot` and tmux/nohup

If you have none of the managers above (for example, shared hosting with cron), the agent can be started
from `crontab -e`:

```cron
@reboot nohup /home/me/bin/dyrokol client >>/home/me/.dyrokol.log 2>&1 &
```

This has no restart after a crash and no log rotation, so it is fine only as a stopgap.

## Windows

### Running manually

1. Put `dyrokol.exe` in a directory, for example `C:\dyrokol`.
2. Run in PowerShell: `.\dyrokol.exe client --server vps.example.com:4443`
   (or `.\dyrokol.exe server` for the relay).

Files (config, `pins.json`, the relay database) end up in `%USERPROFILE%\.config\dyrokol` or in the folder
given by `--dir`. The first relay start may trigger a Windows Firewall prompt: allow incoming connections
on the ports you need.

### Windows service

`dyrokol.exe` can register itself as a Windows service. Run the commands in PowerShell **started as
administrator**:

```powershell
# Install and start at once
.\dyrokol.exe service install client --server vps.example.com:4443
.\dyrokol.exe service install server                 # or the relay

# Management
.\dyrokol.exe service status
.\dyrokol.exe service stop
.\dyrokol.exe service start
.\dyrokol.exe service restart

# Unregister (data is kept)
.\dyrokol.exe service uninstall
```

After `install` you give the role (`server` or `client`) and any of its flags: they are written into the
service command line as is.

| `service install` option | Description |
| --- | --- |
| `--name NAME` | the service name (`DyroKol` by default); lets you install the relay and an agent side by side, for example `--name DyroKolServer` |
| `--manual` | start manually instead of automatically at boot |
| `--no-start` | do not start the service right after installation |

What the installation does:

- remembers the **full path** to `dyrokol.exe`: do not move the file after installing (or reinstall the service);
- if the role flags have no `--dir`/`--config`, adds `--dir %ProgramData%\DyroKol`: the service runs as
  `LocalSystem`, which has no ordinary home folder;
- creates that directory and leaves access only to `SYSTEM` and administrators (tokens and keys live there);
- sets a delayed automatic start after boot and **restart on failure** (after 5, 5 and 30 seconds);
- writes the service log to `<data directory>\dyrokol-service.log`.

`service install client` creates `<data directory>\dyrokol.yaml` with **placeholders** (relay address, token)
and does not start the service until they are filled in:

```powershell
.\dyrokol.exe service install client
# edit C:\ProgramData\DyroKol\dyrokol.yaml: the relay address and this machine's token
# (admin UI: Machines → Add machine), then:
.\dyrokol.exe service start
```

The very first machine of a new relay is registered instead (it initializes the database, works only
once, and must use the same data directory):

```powershell
.\dyrokol.exe client --dir C:\ProgramData\DyroKol --server vps.example.com
# …press Ctrl+C when you see "connected"
.\dyrokol.exe service install client --dir C:\ProgramData\DyroKol
```

On stop the service manager asks the process to finish, and it closes as cleanly as with Ctrl+C: the relay
finishes the statistics and seals the database.

> The service can also be managed with the standard tools: `services.msc`, `sc.exe query DyroKol`,
> `Get-Service DyroKol`.

### Task Scheduler (fallback)

If a service does not suit you, create a task in **Task Scheduler**: trigger "At log on" or "At startup",
action: run `C:\dyrokol\dyrokol.exe` with the argument `client` and "Start in" `C:\dyrokol`; enable "Run
whether user is logged on or not" and "Restart if the task fails".

## Android (Termux)

Use the `dyrokol-termux-arm64` build (or `dyrokol-termux-arm` for 32-bit devices) from the
[Releases](https://github.com/dnech/DyroKol/releases) page, or the install script:
`curl -fsSL https://raw.githubusercontent.com/dnech/DyroKol/main/scripts/install.sh | bash`.

```bash
cp dyrokol-termux-arm64 ~/dyrokol && chmod +x ~/dyrokol
~/dyrokol client --server vps.example.com:4443
```

To keep the agent from sleeping with the screen off, enable `termux-wake-lock` and disable battery
optimization for Termux. For autostart at device boot use the **Termux:Boot** app (a script in
`~/.termux/boot/` that runs `dyrokol client`) or `tmux`/`termux-services`.

> Android builds have only been checked for executable format conformity (ELF ARM/AArch64); you should
> test them on a real device yourself.

## Upgrading

1. Make a backup (**Settings → Download backup**) and copy the relay's data directory.
2. Stop the service, replace the binary, start it. On Linux, running `install.sh` again replaces the
   binary (then restart the service: `systemctl restart dyrokol-server`). On Windows: `dyrokol service
   stop`, replace `dyrokol.exe`, `dyrokol service start`.
3. The database and protocol are versioned: the relay refuses agents with a different protocol version
   (`protocol version … not supported`), and rejects a database of a different schema version with a
   clear error. Update the relay and agents together while the protocol version is changing.

The protocol and application version is shown on **Settings → About this relay**.

## Backups

There are two levels:

- **Configuration backup** (`dyrokol-config.json`): machines (as hashes), ports, egress ports, routes, the
  diagram layout, settings. Good for moving to another relay. After a restore agents need **their own
  tokens** (the backup stores only hashes), and the new relay's database password is set by its initialization.
- **A copy of the relay data directory** (`*.db.enc`, certificate and key): keeps everything, including
  statistics and the TLS fingerprint. Copy while the relay is stopped or right after a write: files are
  replaced atomically, so the copy stays intact at any moment.

Losing `dyrokol.crt`/`dyrokol.key` means a new fingerprint: every agent will have to delete the entry from
`pins.json`. Losing the database password means losing access to `*.db.enc`: the password cannot be
recovered, and only the `dyrokol-config.json` backup remains.

## Moving the relay to another server

Machine tokens stay the same during a move, because the backup stores their hashes. Only the relay address
and the database password change.

1. On the old relay download a backup (**Settings → Download backup**).
2. Move `dyrokol.crt` and `dyrokol.key` into the new relay's data directory so that the fingerprint does
   not change. Otherwise every agent will have to delete the entry from `pins.json`.
3. Start the relay on the new server and register a temporary admin machine:
   `dyrokol client --server new-address:4443`. This creates a **new database with a new password**; the
   password is in that machine's config (`db_password`).
4. Open that machine's admin UI and restore the backup (**Settings → Restore**). The temporary machine
   disappears along with the rest of the old configuration, and all agents are disconnected. Stop the
   temporary agent: it will keep failing to reconnect (`unknown token` in the relay log).
5. On each agent (including the former admin machine) change `addr` in the config to the new address and,
   if the config has a `db_password`, replace it with the **new password from step 3**. The old password
   does not fit the new database, and the relay will not unlock after a restart until somebody presents
   the right one.
6. Start the agents: they connect with their old tokens.

## Running behind a reverse proxy

The tunnel uses its own TLS over TCP, so no HTTP proxy is needed in front of it. If the port does pass
through a load balancer, it must work as a pure **TCP passthrough** without TLS termination. The PROXY
protocol is not supported, and the relay will see the balancer's address instead of the agent's.

Exposing the admin UI is not recommended. If you do put a proxy in front of it: the `Origin` check compares
it with the `Host` header, so the proxy must pass the original `Host`. Network and TLS protection are then
your responsibility.
