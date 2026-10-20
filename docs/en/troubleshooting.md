# Troubleshooting

Where to look: the **relay log** (stdout or `journalctl -u dyrokol-server`), the **agent log** and the
**Logs** page in the admin UI (relay events for 7 days).

## The agent does not connect

The agent log has a line like `[address] session: …`:

| Message | Cause and solution |
| --- | --- |
| `dial: … connection refused` / `i/o timeout` | The relay is unreachable: not running, the port is closed in a firewall, or the address/port is wrong. Check with `nc -vz vps.example.com 7443`. |
| `dial: transport: certificate pin mismatch for … got … want …` | The relay certificate changed (it was recreated or you connected to a different server). If the change is **expected**, delete the address entry in `pins.json` (or fix `tls_pin`). If not, it may be a substitution: check the fingerprint in the relay log. |
| `server error (locked): database is locked` | The relay was restarted and the connecting agent did not send the database password. Connect an agent with `db_password` (usually the admin machine); the rest will follow after the unlock. |
| `server error (locked): no database; register a machine first …` | The relay has no database yet. Register the first machine: `dyrokol client --server host:7443`. |
| `server error (unauthorized): bad database password` | The database password in the agent config is wrong (for example, after [moving the relay](deployment.md#moving-the-relay-to-another-server)). |
| `server error (unauthorized): unknown token` | The machine was deleted or its token reissued. Get the current token (**Machines → Regenerate token**). |
| `client: …dyrokol.yaml: server #1 still has the placeholder values` | The agent config was created with placeholders (`dyrokol client --init`, the installer, `service install client`). Put in the relay address and this machine's token (**Machines → Add machine**). |
| `server error (version): protocol version … not supported` | Agent and relay versions are incompatible. Update both. |
| `server error (db_exists): database already initialized` (with `client -s host:port`) | Registration without a token works only on a relay with no database. If this machine is already registered, run `client -s host:port` with the same `--dir`/`--config`: the saved settings are used. Create the machine in the admin UI and start the agent with its token. |
| `connection closed by peer` in a loop | The relay drops the session: the machine's role/token was changed or a backup was restored; a one-time event. If **permanent**, probably **two agents use one token**: a new session displaces the previous one and they keep kicking each other out. Every machine needs its own token. |

## Admin UI

| Symptom | Solution |
| --- | --- |
| The admin UI does not open | The relay opens no admin port: the UI is available only on an admin machine (`http://127.0.0.1:9181` by default) and only after the database is unlocked, so connect the admin machine first. |
| `admin ui: cannot listen on …` in Logs | The optional `--admin` address is busy on the relay host. Set a different one or drop `--admin`. Tunnels keep working. |
| The admin machine prints `admin ui: cannot listen on …` | The admin port is busy. Free it or set another in the client config's `admin_listen` (`--admin-listen`); a random port is not chosen. With several relays in one client config each admin machine gets its own default port (9181, 9182, … in config order). The admin address and token are written to the log on every (re)connect. |
| "This token is not valid for this relay" | This is not an admin API token. You need the admin machine's `admin ui token` (in its log), not a tunnel token (`adm_…`/`cl_…`). |
| `admin ui token: unknown` | The relay runs with `--token-sha256` and does not know the token itself. Enter it manually or restart the relay with `--token`/`--token-file`. |
| The token changed after a restart | The generated token lives in the relay's encrypted database. It changes only if the database was recreated, or if a `--token` is now passed. Check the relay log for `admin api token:` errors. |
| `403 cross-origin request rejected` | The request came with a different `Origin` than the admin address: usually because of a proxy that rewrites `Host`. Connect directly or preserve `Host`. |
| "Relay unreachable · retry" | The UI could not reach the admin service: the admin machine's tunnel is down or the relay is stopped. |

## Ports and forwarding

| Symptom | Solution |
| --- | --- |
| Creating a relay port: `cannot bind port: …` (409) | The port is busy on the relay host or there are no permissions (ports below 1024: run with `CAP_NET_BIND_SERVICE` or as root). The port is rolled back. |
| Port status on an agent: `bind failed` | The port is busy on the agent's host. The message is under the status (`cannot bind port 8080: address already in use`). Free the port: the relay retries every 15 seconds. |
| `port N is already bound by ingress M of …` | The same number is already open by another relay connection of the same agent (one host, one port). |
| Status `not routed` | Connect the port to an egress port on the **Routes** page. An unrouted port on the relay closes connections; on an agent it is not opened. |
| Status `target offline` | The machine executing the egress port is not online. See **Machines**. |
| The connection opens and closes immediately | Check the logs: `egress: destination … denied by filter` (destination filter), `… blocked by source filter` (source filter), `dial … connection refused` (nothing listens on that host/port **from the executing machine's point of view**), `machine … is offline`. |
| Egress port `127.0.0.1:5000` "does not work" | `127.0.0.1` is the executing machine's address, not the relay's. Pick the machine where the service runs in *Exits on*. |
| Creating an egress port: `host must be a plain hostname or ip without port` | In *Target host* give only a name/address, no port and no slashes. IPv6 literals are not accepted; use a name that resolves to IPv6. |
| Error `source filters cannot match ports` | A source filter cannot contain a port (the visitor's port is random). Remove `:port`; an entry with a port is valid only in a destination filter. |

## Windows service

| Symptom | Solution |
| --- | --- |
| `access denied: run from an elevated (Administrator) prompt` | Open PowerShell via "Run as administrator". |
| `service "DyroKol" already exists` | Remove it (`dyrokol service uninstall`) or pick another `--name`. |
| The service starts and stops immediately | Read `dyrokol-service.log` in the data directory (`C:\ProgramData\DyroKol` by default). Typically: the agent is not registered yet (no `dyrokol.yaml`) or the port is busy. |
| The agent works from the console but not as a service | The service runs as `LocalSystem`; its config is in `C:\ProgramData\DyroKol`, not in your `%USERPROFILE%`. Register with the same `--dir`. |

## Data and configuration

| Symptom | Solution |
| --- | --- |
| Charts and "Seen addresses" are empty | Data appears 5–10 seconds after the first traffic; the chart is built per minute. If you deleted `dyrokol.data.db.enc`, history starts over (this is safe). |
| `store: cannot decrypt database: bad password` at start | Wrong password, or the file is corrupt. |
| `store: unsupported schema version …` | The database was created by a different application version. There is no compatibility with old schemas: rebuild the network from a backup. |
| Restore: `snapshot … is not supported` or `unknown field` | The backup file is from a different version or was edited by hand. |
| Restore: `snapshot must contain at least one admin machine` | The file has no admin. Do not delete machine records by hand. |

## Access lost

- **Forgot the admin token.** Read `admin ui token` in the log of an admin machine (restart the client to print it again), or restart the relay with `--token`/`--token-file`. Agents connect as soon
  as the database is unlocked.
- **Lost the admin machine's config** (and no admin tokens are left). There is no standard reset
  command. If you have a `dyrokol-config.json` backup: stop the relay, move `dyrokol.db.enc` and
  `dyrokol.data.db.enc` away, start it, register a new machine (`client --server …`) and restore the
  backup. Agents keep working with their tokens, but they will need the new database password. Without
  a backup you must create the configuration again: the database cannot be decrypted without the password.
- **Lost the database password** but the admin machine is alive: the configuration can be exported as a
  backup from the running relay until it is restarted.

## What to include with a question

The version (`Settings → About this relay`), OS and architecture, an excerpt of the relay and agent
logs (**without tokens and passwords**), and how the "ingress → route → egress" chain is set up.

## FAQ

**Is UDP supported?** No, TCP only.

**Can an agent connect to several relays?** Yes: several entries in the config's `servers`; each has its
own session, identity and set of ports.

**Do agents need a public IP?** No: the agent connects to the relay itself. Only the relay needs a public
IP (or visitors need access to it).

**What happens if the relay restarts?** Agents reconnect on their own (a 1–15 s pause); the relay unlocks
when an agent with the database password connects; ports and routes come back from the database. Active
connections are dropped.

**What happens if the connection to an agent is lost?** Its ingress ports on the agent keep listening but
connections are dropped (the relay is unreachable); the machine's egress ports show as offline.

**Can the same port be on different machines?** Yes: the number is unique within a machine, not globally.
