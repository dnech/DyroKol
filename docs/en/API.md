# DyroKol Admin HTTP API

By default the relay opens **no port** for the admin API: it is served only through the tunnel of an
admin machine (see below). A local TCP entrance such as `127.0.0.1:9180` exists only if you set
`--admin ADDR`. It is the same surface the bundled web UI uses, so a desktop tray, an Android
WebView wrapper, or any automation can drive the relay through it.

Base URL: the admin machine's local entrance, `http://127.0.0.1:9181` by default (or the relay's
optional `--admin` address).

## Authentication

Every request must carry the admin token in the `X-Dyrokol-Api` header:

```http
X-Dyrokol-Api: <admin-ui-token>
```

The admin UI token can be set on the relay with `dyrokol server --token`
(or `DYROKOL_ADMIN_TOKEN`). When unset, the relay generates it once and keeps it **inside the
encrypted database** (it is never written to the config file or the log), so it does not change on
restart. Admin machines receive it when they connect and print it as `admin ui token`. It is **not** the same
as a client's tunnel token — tunnel tokens never open the admin UI.

* **401 Unauthorized** — header missing or token mismatch (constant-time compare).
* **403 Forbidden** — request came from a browser-like cross-origin context
  (`Origin` header present that does not match the admin origin). Defense in
  depth against CSRF; send no `Origin` header from a native client.
* Static assets (`/`, JS, CSS, HTML) are **not** protected so the login page can
  render before a token is known.

The web UI stores the token in `sessionStorage` under the key `dyrokol_token`.

## Discovery: `GET /api/config`

Returns the runtime contract a shell needs to talk to the relay. **Never**
contains secrets.

```json
{
  "version": "1.0.0",
  "protocol": 4,
  "admin_addr": "",            // the optional local TCP entrance; empty by default
  "listen": ":7443",
  "fingerprint": "sha256:…",
  "api_header": "X-Dyrokol-Api",
  "db_file": "/root/.config/dyrokol/dyrokol.db.enc",
  "autostart": false,
  "capabilities": ["backup", "restore", "live", "logs", "settings"]
}
```

`api_header` tells you the header name; `capabilities` lists optional endpoints
that may be absent on older builds.

## Error shape

All error responses are:

```json
{ "error": "message" }
```

Common status codes: `400` bad request (validation error, malformed JSON or an
unknown field), `401` unauthenticated, `403` forbidden, `404` not found, `409`
conflict (port in use, already routed, identical egress exists, bind failure).

The admin listener only starts once the database is unlocked (by the first
agent that brings the database password), so every endpoint below always
has a store to work with.

Request bodies are decoded strictly: unknown fields are rejected with `400`.
Successful mutations return `204 No Content` unless a body is specified below.
Every mutation is applied to agents immediately (config push), no restart
required.

## Resources

### `GET /api/status`

Full topology snapshot. This is what the web UI renders.

```json
{
  "fingerprint": "sha256:…",
  "now": 1730000000,
  "relay_label": "relay",
  "version": "1.0.0",
  "config_version": 12,
  "autostart": false,
  "name": "Frankfurt VPS",     // display name of the relay ("" when not set)
  "public_addr": "relay.example.com",   // domain or ip agents connect to ("" when not set)
  "totals": { "bytes_in": 0, "bytes_out": 0, "conns_total": 0, "active": 0 },
  "clients": [ /* clientStatus */ ],
  "inports": [ /* inportStatus */ ],
  "endpoints": [ /* endpointStatus */ ],
  "routes": [ /* routeStatus */ ]
}
```

`clientStatus`:

```json
{
  "key_hash": "2f62dc633cde02b6…",
  "role": "client",            // "client" | "admin"
  "label": "nas",
  "created_at": 1730000000,
  "last_seen": 1730000000,
  "last_ip": "203.0.113.7",
  "prefix": "2f62dc63",
  "online": true,
  "inport_count": 1,
  "endpoint_count": 1,
  "agent_version": "1.0.0"
}
```

`inportStatus` (embeds the `InPort` object):

```json
{
  "id": 1,
  "machine": "relay",          // "relay" or a key_hash
  "port": 9000,
  "label": "web",
  "allow": [], "deny": ["127.0.0.0/8"],
  "created_at": 1730000000,
  "bytes_in": 0, "bytes_out": 0, "conns_total": 0,
  "machine_label": "relay",
  "route_id": 1,               // omitted when not routed
  "route_enabled": true,       // false when the route is switched off
  "endpoint_id": 1,
  "target": "relay → example.com:443",
  "endpoint_online": true,
  "active": 2,
  "bind_error": "listen tcp :9000: bind: address already in use"
}
```

`bind_error` is present only when the port could not be bound; treat a non-empty
value as a warning badge. `active` is the live connection count (for
agent-side ports, as last reported by the agent). Counters are runtime data:
they live in the data database and are not part of backups.

`endpointStatus` (embeds the `Endpoint` object) adds `machine_label`, `online`,
`route_count`, and a human-readable `display`.

`routeStatus` (embeds `Route`) adds resolved `inport` (port number),
`inport_label`, `endpoint` (display string), `endpoint_label`.

### Machines (clients)

| Method | Path | Body | Result |
| --- | --- | --- | --- |
| `POST` | `/api/clients` | `{"label":"nas","role"?:"client"}` | `200` `{"token":"cl_…","client":{…}}` |
| `PATCH` | `/api/clients/{key_hash}` | `{"label"?,"role"?}` | `204` |
| `POST` | `/api/clients/{key_hash}/regenerate` | — | `200` `{"token":"cl_…","client":{…}}` |
| `DELETE` | `/api/clients/{key_hash}` | — | `204` |

* `role` is `"client"` or `"admin"`. A role change drops the live session so the
  machine reconnects with its new privileges.
* The last admin machine cannot be demoted or deleted (`400`): the relay would
  become unmanageable.
* `regenerate` rotates the token and kicks the session; the new token is shown
  once. Ports and endpoints owned by the machine follow the new key.
* Deleting a machine deletes its ingress ports, egress endpoints and their
  routes.
* Label length limit is 200 characters (all entities).

### Ingress ports

| Method | Path | Body | Result |
| --- | --- | --- | --- |
| `POST` | `/api/inports` | `{"machine"?,"port","label"?,"allow"?,"deny"?}` | `200` `InPort` |
| `PATCH` | `/api/inports/{id}` | `{"port"?,"label"?,"allow"?,"deny"?}` | `204` |
| `DELETE` | `/api/inports/{id}` | — | `204` |

* `machine` defaults to `"relay"`. Omit it to bind on the relay host; otherwise
  it must be a known machine `key_hash` and the port is pushed to that agent.
* `port` must be 1-65535. Creating a relay-owned port binds it immediately; a
  bind failure is a `409` and the port is rolled back.
* A port number is unique **per machine**: the relay and every machine may each
  have their own `8080`; a second `8080` on the same machine is a `409`. Ports
  on one host (a machine connected to several relays) still conflict when bound.
* `allow`/`deny` are source filters. In PATCH an omitted field means "leave
  unchanged"; pass `[]` to clear. One PATCH is one config change.
* PATCH can change the **port number** (the machine that owns the port cannot
  change). A taken number on that machine is a `409`. For a relay-owned port the
  relay rebinds immediately: if the new number cannot be bound the change is
  rolled back and the answer is `409` (the port keeps working on the old number).
  Agent-owned ports are rebound by the agent with the next config push.

### Egress endpoints

| Method | Path | Body | Result |
| --- | --- | --- | --- |
| `POST` | `/api/endpoints` | see below | `200` `Endpoint` |
| `PATCH` | `/api/endpoints/{id}` | `{"host"?,"port"?,"socks5_user"?,"socks5_pass"?,"label"?,"allow"?,"deny"?}` | `204` |
| `DELETE` | `/api/endpoints/{id}` | — | `204` |

PATCH changes the target (`host`, `port`, direct endpoints only), the SOCKS5 login
(`socks5_user`/`socks5_pass`, socks5 endpoints only; sending them for the wrong type is a
`400`), the label and the filters. The machine and the type cannot change. Making an
endpoint identical to another one (same machine, type and target) is a `409`.

Create body:

```json
{
  "machine": "relay",
  "type": "direct",            // "direct" | "socks5"
  "host": "example.com",
  "port": 443,
  "socks5_user": "", "socks5_pass": "",  // only for type "socks5", set together
  "label": "web",
  "allow": [], "deny": []
}
```

* `machine` defaults to `"relay"`, `type` to `"direct"`, `host` to `127.0.0.1`.
* Creating an endpoint identical to an existing one (same machine, type,
  target) is a `409`. A machine has at most one socks5 endpoint.
* `type: "direct"` dials `host:port` from that machine.
* `type: "socks5"` has no `host`/`port`: the machine runs a built-in SOCKS5 server for
  the routed ingress port and connects wherever the SOCKS5 client asks (CONNECT only).
* `allow`/`deny` are **destination** filters checked before every dial.
* Deleting an endpoint also drops live connections of ingress ports that were
  routed through it (agent-side ports close with their listener).

### Routes

| Method | Path | Body | Result |
| --- | --- | --- | --- |
| `POST` | `/api/routes` | `{"inport_id":1,"endpoint_id":2}` | `200` `Route` |
| `PATCH` | `/api/routes/{id}` | `{"inport_id"?,"endpoint_id"?,"enabled"?}` | `204` |
| `DELETE` | `/api/routes/{id}` | — | `204` |

Each ingress port has exactly one route (a second route for the same ingress is a
`409`), but any number of ingress ports may route to the same endpoint.
Deleting a route drops the live connections of that ingress port.

`Route` has `enabled` (new routes are on). With `{"enabled": false}` the route is kept but carries
no traffic: its ingress port behaves as if it had no route, its live connections are dropped and
agents stop binding the port. Backups keep the state (a backup without it restores as on).

`PATCH /api/routes/{id}` re-points one route atomically to another ingress port
and/or another endpoint. `409` when the new ingress port already has a route, `404`
for an unknown route/ingress/endpoint. Live connections of the ingress port the
route left are dropped.

#### Set all ingress ports of an endpoint

`PUT /api/endpoints/{id}/routes` — body `{"inport_ids":[1,4,5]}` — `204`.

Makes the listed ingress ports **exactly** the set routed to the endpoint, in one
transaction: listed ports are routed to it (moved if they were routed to another
endpoint), ports currently routed to it but not listed lose their route. An empty list
disconnects them all. Ports whose previous route went away have their live
connections dropped. `404` when the endpoint or any listed ingress port does not exist
(nothing changes). A call that changes nothing does not bump the config version.

### Map layout

The route map in the UI is arranged by the operator; the arrangement is stored on the
relay and shared by every admin. It is not sent to agents, so saving it does not
change the config version. It is part of backups.

| Method | Path | Body | Result |
| --- | --- | --- | --- |
| `GET` | `/api/map` | — | `200` `MapLayout` |
| `PUT` | `/api/map` | `MapLayout` | `204` |

```json
{
  "nodes": { "in:3": {"x": 24, "y": 24}, "eg:1": {"x": 700, "y": 24} },
  "items": [
    {"id": "f1", "kind": "frame", "x": 300, "y": 40, "w": 320, "h": 200, "text": "Home", "color": "blue"},
    {"id": "t1", "kind": "text",  "x": 320, "y": 260, "text": "Public side", "color": "neutral", "size": "M"}
  ]
}
```

* `nodes` is keyed by `in:<ingress id>` and `eg:<endpoint id>`; coordinates are map pixels.
* `items` are annotations drawn **under** the links and the ports: `kind` is `frame`
  (needs `w`, `h`, 40-20000) or `text` (`size` `S`, `M` or `L`). `color` is one of `neutral`,
  `blue`, `green`, `orange`, `red`, `violet`. Text is at most 200 characters.
* Limits: 300 positioned ports, 100 annotations, unique annotation ids. A layout that
  breaks them is rejected with `400` and the stored one stays.
* `PUT` replaces the whole layout (last write wins).

### Observability

| Method | Path | Result |
| --- | --- | --- |
| `GET` | `/api/stats?since=<unix>` | `{"since":…,"samples":[Sample]}` (default last hour) |
| `GET` | `/api/peers?limit=200` | `{"peers":[Peer]}` |
| `GET` | `/api/live` | `{"live":[LiveConn]}` |
| `GET` | `/api/logs?limit=200` | `{"logs":[LogEntry]}` |

`Sample`: `{"ts","inport_id","bytes_in","bytes_out","active"}`
`Peer`: `{"ip","inport_id","conns","bytes_in","bytes_out","first_seen","last_seen"}`
`LiveConn`: `{"inport","inport_id","inport_name","machine","remote","since","where"}`
  — `where` is `"relay"` or a machine label.
`LogEntry`: `{"ts","level","source","message"}` — `level` is `info`, `warn` or
`error`. Relay events (database unlocked, machines connecting/disconnecting,
bind failures, rejected handshakes and configs) are recorded and kept 7 days.

### Backup and restore

`GET /api/backup[?secrets=0]` downloads a JSON snapshot as
`dyrokol-config.json`:

```json
{
  "format": "dyrokol-config", "schema": 2, "config_version": 12,
  "exported_at": 1730000000, "secrets": true,
  "settings": {"autostart": false, "name": "", "public_addr": ""},
  "map": {"nodes": {}, "items": []},
  "clients": [], "inports": [], "endpoints": [], "routes": []
}
```

The snapshot is config only (no counters or traffic history; the map layout is included) and references
machines by `key_hash` — access tokens and the database password are never
included. SOCKS5 passwords are exported by default; pass `secrets=0` to blank
them so the file is safe to share.

`POST /api/restore` accepts a snapshot as the raw request body (limit 8 MB),
validates it completely and replaces the whole configuration atomically.
Response:

```json
{ "ok": true, "bind_errors": { "<inport_id>": "message" } }
```

After a restore the relay resyncs its listeners and kicks every agent session so
machines re-authenticate against the restored client set.

### Settings and lifecycle

`PUT /api/settings` — body `{"autostart"?, "name"?, "public_addr"?}` — returns the stored settings.
Omitted fields are left unchanged. `name` is up to 200 characters; `public_addr` is a domain or an ip
with an optional port (a leading `https://` and a trailing `/` are dropped); anything else is a `400`.

`POST /api/shutdown` — returns `{"ok":true}` then stops the relay after a short
delay. Useful for a tray's "quit" action.

## Filter syntax (`allow` / `deny`)

Each entry is one of:

| Form | Matches |
| --- | --- |
| `example.com` | that exact host |
| `*.example.com` | any subdomain of `example.com` |
| `api.example.com:443` | that host on that exact port |
| `example.com:*` | that host on any port |
| `10.0.0.0/8` | any address in that CIDR |
| `192.168.1.20:8080` | that address on that port |
| `10.0.0.0/8:5432` | that CIDR on that port |
| `*:25` | any host on that port |

Semantics:

* Empty `allow` and empty `deny` ⇒ everything is allowed.
* Non-empty `allow` ⇒ only listed hosts are allowed; everything else is denied.
* **`deny` always wins** over `allow`.
* An unparsable list fails closed (traffic is dropped).

On ingress ports these filters apply to the **source** address of the connecting
visitor. On egress endpoints they apply to the **destination** being dialed.

A visitor's source port is random, so **source filters (ingress ports) cannot
contain ports**: a pattern such as `*:25` or `10.0.0.0/8:22` is rejected with
`400`. Port forms are valid only in destination filters (egress endpoints).

## Reaching the admin UI from a client machine

The relay opens no admin port by default: the admin HTTP server accepts only the streams that admin
machines open through their tunnel. Access is tunnelled instead of published.

When a `dyrokol client` connects with the **admin** role, it opens a local
loopback listener and forwards every connection over the tunnel to the relay's
admin HTTP server:

```
  browser ──▶ 127.0.0.1:9181 (on the client machine)
                 │  (admin listener on the client)
                 ▼
             yamux "admin" stream over the existing tunnel
                 ▼
             admin HTTP server inside the relay (in memory, no port) ──▶ admin API
```

* The listener appears automatically for the admin role; the address defaults
  to `127.0.0.1:9181`. Configure it with `dyrokol client --admin-listen ADDR`,
  or disable it with `--no-admin`. The port is never chosen randomly: if it is
  busy the agent logs an error. The address and the admin token are logged on
  every (re)connect.
* Only sessions authenticated with the admin role may open an admin stream; a
  regular client is refused server-side.
* The tunnel is authenticated by the **tunnel token**, but the admin UI still
  requires the relay's **admin UI token** in `X-Dyrokol-Api` — the two are
  distinct. When the relay knows the plaintext token it sends it to admin-role
  machines during the handshake, and the client prints it at startup as
  `admin ui token: ...`. If the relay is configured with `--token-sha256` only,
  the plaintext is unavailable and the operator must supply it.

## Integration sketch (tray / Android WebView)

1. `GET /api/config` → read `api_header`, `admin_addr`, `capabilities`.
2. Ask the user for (or load a stored) admin token; persist it however you like.
3. Call `GET /api/status` with `X-Dyrokol-Api` to render machines and ports.
4. Poll `GET /api/status` (and optionally `/api/live`) on a timer for the
   dashboard; debounce against the `now` field.
5. Mutate with the POST/PATCH/DELETE endpoints above; every change is pushed to
   agents automatically, no restart required.
6. Offer backup/restore through `/api/backup` and `/api/restore`; offer
   "quit" through `/api/shutdown`.
