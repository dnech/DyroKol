# Architecture

A document for those who want to understand how DyroKol works or change the code. The user-facing
notions are described in [Concepts](concepts.md).

## Overview

```
                    ┌──────────────────────── relay (dyrokol server) ───────────────────────┐
 visitor ───TCP────▶│ relay ports ─┐                                                         │
                    │              ├─▶ route cache ─▶ executeEgress ─┬─▶ egress.Dial (local) │
 agent ─TLS 1.3─────▶ handshake ─▶ Session ── yamux ─▶ streams ──────┘   or a dial stream    │
                    │                  │  push (config) ◀── Store (2 encrypted SQLite)       │
                    │                  │  report ──▶ statistics ──▶ flush every 5 s          │
                    │ admin HTTP (in memory) ◀── admin stream from the admin agent           │
                    └─────────────────────────────────────────────────────────────────────────┘
```

One binary, three layers:

1. **Transport**: TLS 1.3 + [yamux](https://github.com/hashicorp/yamux), many independent streams over
   one TCP connection of the agent.
2. **Control plane**: handshake, config push, traffic reports, the admin API.
3. **Data plane**: forwarding bytes between the visitor and the target.

## Package map

| Package | Responsibility |
| --- | --- |
| `cmd/dyrokol` | CLI: flag parsing, configs, starting `server`/`client`, Windows service management (`service_windows.go`). |
| `internal/server` | Relay: handshake, sessions, relay ports, push, statistics, the admin API and the embedded UI. |
| `internal/agent` | Agent: sessions with the relay, opening ingress ports, executing egress ports, reports, the local admin entrance. |
| `internal/store` | Storage: encrypted databases, entities, transactions, backup. |
| `internal/protocol` | Message and frame format, handshake, tokens. |
| `internal/transport` | TLS certificate, pinning/TOFU, yamux settings, socket tuning. |
| `internal/traffic` | Traffic accounting: counters, visitor addresses, live connections, `Port.Relay`. |
| `internal/copyio` | Byte forwarding with a buffer pool, the bidirectional `Pipe`. |
| `internal/egress` | Executing an egress port: direct, or the built-in SOCKS5 server, with the destination filter check. |
| `internal/socks5` | A minimal SOCKS5 client and the server used by the SOCKS5 exit (RFC 1928/1929, CONNECT only). |
| `internal/filter` | Parsing and applying allow/deny lists. |
| `internal/config` | The data directory and the "flag → variable → default" precedence. |

Dependencies point downward: `server` and `agent` use `protocol`, `transport`, `traffic`, `egress`,
`filter`; `store` knows only `filter` and `protocol`; the leaf packages hardly depend on each other.

## Protocol

After the TLS handshake the exchange goes in **frames**: 4 bytes of length (big-endian) + JSON, at most 1 MiB.

### Handshake

The agent sends `Hello`, the relay answers with `Reply` (both are frames, 15-second timeout):

```jsonc
// agent → relay
{ "version": 4, "purpose": "connect",       // or "init"
  "token": "cl_…", "db_password": "…",       // purpose=connect
  "admin_token": "adm_…",                    // purpose=init
  "agent_version": "1.0.0" }

// relay → agent
{ "role": "admin", "version": "1.0.0", "admin_token": "api_…" }     // success
{ "error": { "code": "unauthorized", "message": "unknown token" } } // rejection
```

- `connect` is an ordinary connection. If the database is locked, the relay opens it with the password
  from Hello.
- `init` registers the first machine: the relay creates both databases, and the machine with the token
  `admin_token` becomes admin. After the reply the relay closes this connection, and the agent
  reconnects as `connect`.
- Error codes: `locked`, `unauthorized`, `db_exists`, `bad_init`, `version`, `internal`.
- `admin_token` in the reply goes only to an admin machine and only if the relay knows it in the clear. A generated token is stored in the config database's `meta` (encrypted with the rest), not in a file.
- The protocol version must match exactly.

### yamux streams

After a successful handshake yamux starts over the connection (the relay is the server, the agent the
client, keep-alive 10 s, stream window 2 MiB). **The first frame of every stream** is a `StreamHeader`
with the type:

| Type | Direction | Content after the header |
| --- | --- | --- |
| `report` | agent → relay | A series of `Report` frames every 2.5 s: `stats` (deltas and the number of active connections per port), `peers` (deltas per address), `live` (open connections). One writer per stream. |
| `push` | relay → agent | A `ConfigPush` frame, answered by a `ConfigAck` frame (15 s timeout). |
| `dial` | relay → agent | `EgressSpec` in the header; then raw bytes: the agent connects to the target and forwards data. |
| `route` | agent → relay | `inport_id` in the header; then raw bytes. The relay finds the route, executes the exit and forwards data. |
| `admin` | agent → relay | Raw bytes: the relay hands the stream to its admin HTTP server through an in-memory listener (no port is opened). Only for the `admin` role. |

### Configuration (`ConfigPush`)

```jsonc
{ "version": 12,
  "in":  [ { "inport_id": 3, "port": 8080, "label": "web",
             "allow": [], "deny": ["10.0.0.0/8"],
             "egress": { "machine": "relay|<hash>", "type": "direct", "host": "…", "port": 5000, … } } ],
  "out": [ { "endpoint_id": 2, "type": "direct", "target": "127.0.0.1:5000", "from": ["relay"] } ] }
```

`in` lists the ports the agent must open (only those with a route). `out` is informational, for the
agent's log. A push is the **full state** for the machine, not a diff: the agent brings its listeners
to what is described (closes extra ones, opens missing ones, updates the config of existing ones).

`ConfigAck` contains `error` (the whole push is rejected: a bad port, a duplicate, a broken filter) or
`bind_errors` (some ports did not open, the rest was applied).

## Relay lifecycle

1. `Run`: load/create the certificate, the tunnel listener. **The database is locked**, `store == nil`;
   there is no admin UI and no relay ports.
2. The first successful `connect` with a password (or `init`) → `activateLocked`: stores the store,
   resets the route cache, opens relay ports, starts the admin HTTP and two background workers (`flush`
   every 5 s, `repush` every 15 s).
3. Each session: `register` → config push → the stream `Accept` loop → `unregister`. A machine's new
   session displaces the old one.
4. Shutdown (`Close`): close listeners and sessions, wait for workers, **final flush**, seal the databases.

### The configuration change flow

```
API handler ─▶ store.mutate: transaction + config_version++ + seal to disk
            ─▶ server.configChanged: reset the route cache
                                     sync relay listeners with the database
                                     broadcastConfig → pushConfig to every session
```

`pushConfig` is serialized per session (`pushMu`) and skips a machine that already applied the current
version. If the ack contained bind errors or the link broke, `repush` tries again, every 15 seconds.

### Route cache

Accepting a connection must not go to the database. `routeCache` keeps the resolved route of a port
(the port, the egress port, the compiled filter) and is reset on any change. So that a load that began
before a reset does not return a stale value, the cache has a **generation counter**. A load is stored
only if the generation has not changed.

## Agent

- `runServer` holds a session with each relay: the pause between attempts grows 1 → 15 s and resets
  after 30 s of stable operation.
- Ports are keyed by **(relay, port id)**: identifiers are unique only within a relay, and a given port
  number on a host can be taken only once (a conflict is reported as a bind error).
- For a port whose egress port is on the same machine, traffic does not go through the relay
  (`Egress.Machine ==` our `key_hash`); otherwise a `route` stream is opened.
- Each port is a `traffic.Port` (counters + live connections) and a set of visitor addresses; the
  report every 2.5 s **drains** the accumulated deltas, so nothing is lost or duplicated.
- Closing a port also closes its connections (`ConnSet.Shut`).

## Storage

Two independent databases, each an **in-memory SQLite**, sealed to disk as a whole (`sealed.go`):

```
file:  "DYRK1" | salt(16) | nonce(12) | AES-256-GCM( serialized SQLite database )
key:   scrypt(password, salt, N=2¹⁵, r=8, p=1) → 32 bytes
```

The `modernc.org/sqlite` driver can `Serialize`/`Deserialize` a database image, so plaintext never
reaches the disk at all. Saving means encrypting the image with a new nonce and atomically replacing the
file (`.tmp` → `rename` with `fsync`). One dedicated connection serves the database.

| Database | Content | When it is written |
| --- | --- | --- |
| **config** (`*.db.enc`) | machines, ingress ports, egress ports, routes, settings, configuration version | on every configuration change |
| **data** (`*.data.db.enc`) | totals, per-minute points, visitor addresses, machine presence, the log | on `flush`: every 5 s while there is traffic or online machines (presence is written too) |

The split exists so that frequent statistics writes do not re-encrypt the configuration, and so that
losing the data database is safe.

Rules for working with the database:

- Any configuration change is **one transaction**, inside which `config_version` also grows
  (`store.mutate`), followed by sealing. The operation is applied entirely or not at all.
- One aggregate snapshot: `Topology()` reads all four tables under one lock, so status replies and
  configurations sent to agents are consistent.
- Integrity constraints live in the schema: `UNIQUE(machine, port)` for ingress ports,
  `UNIQUE(machine, type, host, port, socks5_host, socks5_port)` for egress ports, `UNIQUE(inport_id)` for
  routes, foreign keys with cascading route deletion.
- A route has an `enabled` flag (schema 3; a schema 2 database is migrated on open). A switched-off route
  does not resolve (`Resolve` returns no endpoint), is left out of the configuration pushed to agents and
  of the "served from" lists; backups keep the flag, and a backup without it restores as on.
- Routes: an ingress port has at most one (`UNIQUE(inport_id)`), and any number can lead to one egress
  port. `SetEndpointRoutes` atomically sets the whole set of an egress port's ingress ports (moves ports
  from other egress ports, removes extra ones) and returns the ports that lost their previous route; a
  call without changes does not spend a configuration version.
- The route diagram layout (`MapLayout`: port positions and labels/frames) lives in the config
  database's `meta` as one JSON value. It is shared by all administrators, is part of the backup (node
  keys `in:<id>`/`eg:<id>` are reassigned on restore, since identifiers are created anew) and does
  **not** increase the configuration version: agents do not need it.
- Changing an ingress port's number (`UpdateInPort`) and an egress port's target (`UpdateEndpoint`) are
  ordinary transactions with a uniqueness check; `UpdateRoute` redirects one route atomically.
- Deleting an entity also cleans its runtime data (`dropRuntimeLocked`).
- The schema version is stored in `meta`; there is no compatibility with old schemas (a mismatch is an
  error), except the one built-in step 2 → 3 (adds `routes.enabled`).
- Storage errors are typed (`Error{Kind, Msg}`: `ErrInvalid`, `ErrNotFound`, `ErrConflict`); the HTTP
  layer translates them into statuses 400/404/409 in one place.

## Traffic accounting

`traffic.Port` combines `Counters` (atomic bytes, connections, active), `ConnSet` (live connections;
`Shut` closes them and blocks new ones) and the `Relay` method, which forwards bytes through
`copyio.Pipe` and charges traffic to the port and to the visitor's address.

- `Counters.Drain` returns the deltas since the previous call and the current gauge value.
- On the relay, the deltas of relay ports and the received agent reports accumulate in memory and are
  flushed to the data database in `flush`. Agent reports are accepted only for that machine's ports.
- Totals (`totals`) are summed in the database; the chart is built from per-minute points, while the
  total lives long.

## Admin API and UI

- `adminHandler` is created once at unlock and receives a ready `*store.Store`, so handlers need no
  "database is locked" checks.
- All endpoints go through `guard`: token, `Origin` check.
- The status (`/api/status`) is assembled from one `Topology()` plus a runtime slice of the server.
- The UI is static files embedded in the binary (`go:embed web`).

### UI structure

No build step: native ES modules and an import map, Preact + htm live in `web/vendor/`.

```
js/main.js          entry point: login or shell, dialogs, toasts
js/shell.js         frame: sidebar, top bar, router, theme
js/state.js         app state: token, status snapshot (5 s polling), mutate/useAction
js/lib/             api (API client), store (observable stores, usePolled),
                    router (hash router), format, html (Preact+htm)
js/ui/              kit: buttons, fields, statuses, table wrappers, menus, dialogs (promise-based),
                    toasts, filter editor, icons
js/pages/           pages: overview, machines, ingress, egress, activity, logs, settings, login
js/charts/          traffic chart (SVG) and the routemap.js diagram (nodes and annotations are HTML, lines are SVG)
css/app.css         tokens (dark/light themes), layout and components
```

Principles:

- The app state is one observable store; pages read it with the `useApp()` hook and change it only
  through `mutate`/`useAction`, which refresh the snapshot and show the result.
- The Activity and Logs pages poll their endpoints themselves (`usePolled`); polling pauses in a
  background tab.
- Dialogs are promises: `openDialog(render)` returns the close result, `confirmDialog` returns `true/false`.
- Drop-down menus are positioned by window coordinates so tables do not clip them.
- The route diagram (`charts/routemap.js`) is built from the same status snapshot and the shared layout
  (`lib/maplayout.js`: loaded when the page opens, saved with a 0.5 s delay). It is a free canvas: ports
  and annotations are positioned absolutely and dragged with pointer events (mouse, pen, finger), with a
  10-pixel grid step. The layers from bottom to top: frames, labels, lines (SVG), ports. Ports without an
  assigned place fall into columns (ingress on the left, egress on the right) below those already placed;
  on the first drag the positions of **all** ports are fixed, so adding new ones does not shift the
  others. Lines leave the side of the port facing the target. A port's state is always labelled with
  text, not only a colour.
- The diagram is seen through a viewport with a transform (`translate` + `scale` on the "world"): pan by
  dragging the background, zoom by buttons, Ctrl + wheel, pinch, and the Fit command. Dragging ports and
  annotations converts screen pixels to diagram coordinates by dividing by the scale. Full-screen mode is
  a `position: fixed` layer (z-index below dialogs) plus the Fullscreen API on the document, so dialogs
  work in it too. Default port positions do not depend on the window size, otherwise expanding to full
  screen would shift the unplaced ports. The view (pan, zoom) is not saved.
- The Routes page is a "one row, one route" table (`pages/routes.js`): cells are edited in place through
  `PATCH /api/routes/{id}`, and the last row creates a route.
- Status colours are fixed and always accompanied by text; the chart uses a palette verified for
  distinguishability by colour-blind users in both themes.

## Constants

| What | Value |
| --- | --- |
| Connect and handshake timeout | 15 s |
| yamux keep-alive | 10 s |
| yamux stream window | 2 MiB |
| Maximum protocol frame | 1 MiB |
| Agent report | 2.5 s |
| Statistics flush to the database | 5 s |
| Push retry | 15 s |
| Agent reconnect pause | 1 → 15 s (reset after 30 s of stable operation) |
| Egress dial / SOCKS5 timeout | 10 s |
| Retention of per-minute points and addresses | 24 hours |
| Log retention | 7 days |
| Live connections from one agent | up to 500 |
| Forwarding buffer size | 128 KiB (from a pool) |
| API request body / restore snapshot limit | 64 KiB / 8 MiB |
| Maximum label length | 200 characters |

## Invariants that must not be broken

1. **There is no plaintext on disk.** Databases live in memory; only the `sealed` image goes to disk.
2. **Every configuration change goes through `store.mutate`** (transaction, version, save), and
   `server.configChanged()` is called after it.
3. **The agent does not choose the route:** the relay verifies port ownership and picks the exit itself.
4. **Lock order: `Server.mu` → `Store.mu`**, never the reverse (the store knows nothing about the
   server). The route cache (which goes to the store) is not accessed under `Server.mu`.
5. **Filters fail closed:** an unrecognized filter blocks traffic.
6. **The protocol and schema versions** are bumped on incompatible changes (`protocol.Version`,
   `store.schemaVersion`); there are no migrations except 2 → 3.
7. **Ingress port identifiers are unique only within a relay**, port numbers only within a machine. State
   must not be keyed by the port number or ID alone.
