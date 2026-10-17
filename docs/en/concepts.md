# Concepts

All of DyroKol's configuration is described by five notions: **machine**, **ingress port**,
**egress port**, **route** and **filter**.

## Machines

A *machine* is a device that runs an agent (`dyrokol client`), or the relay itself.

| Machine | Identified by | Roles |
| --- | --- | --- |
| **relay** | Always the name `relay` | — |
| **Agent** | The SHA-256 hash of its token (`key_hash`) | `client` or `admin` |

- The **tunnel token** (`cl_…` / `adm_…`) is the machine's password. The relay stores only its hash;
  the token itself is shown once, at creation. The token cannot be used to sign in to the web UI.
- The **`client` role** is an ordinary machine: it can have ingress and egress ports.
- The **`admin` role** is the same plus: the machine receives the admin UI token from the relay and
  opens a local entrance to the admin UI on its own `127.0.0.1` through the tunnel. The first machine
  (the one that initialized the relay) becomes admin; an administrator promotes the others.
- There is always at least one admin: the last one cannot be demoted or deleted.

One computer can connect to several relays at once (several `servers` entries in the agent config),
and has a separate identity for each relay.

## Ingress port

A port where traffic **enters** the network. It is opened (`listen`) on the machine that owns it:

- on the **relay**: visitors come from the Internet;
- on an **agent**: for example, to bring a remote service to your laptop's `localhost:5432`.

Properties: port number, label (optional), source filter, owner machine.

**The port number is unique within a machine.** `8080` can exist on the relay and on two agents at
the same time, but a second `8080` on the same machine cannot be created. If two ports live on the
same physical host (for example, the relay and an agent on one server), the real `bind` succeeds only
for the first one; the second gets a "bind failed" error.

A route can be **switched off** (the green/red button in the Routes table): it stays configured but
carries no traffic, and the ingress port behaves like a port without a route.

A port without a route (or with a switched-off one):
- on the relay: it listens but closes connections immediately;
- on an agent: it is not opened at all (an agent binds only ports that have somewhere to send traffic).

## Egress port

A place where traffic **leaves** the network: the machine opens an outgoing connection to the target.

| Type | What it does |
| --- | --- |
| `direct` | The machine connects to `host:port` itself. |
| `socks5` | A SOCKS5 server **built into the exit machine**. It has no host or port: an ingress port is routed to it, an external SOCKS5 client is pointed at that ingress port, and the machine opens the outgoing connections the client asks for (optional login and password). |

- For `direct`, `host` defaults to `127.0.0.1` and is understood **from the executing machine's point of view**: for
  an egress port on a NAS, host `127.0.0.1` is the NAS itself.
- An egress port can be on the relay or on any agent.
- Properties: machine, type, target, label, destination filter.
- Two identical egress ports (same machine, type, target, proxy) cannot be created.

## Route

A link "ingress port → egress port". The rule is:

- **each ingress port has exactly one target**: one TCP connection must arrive in one place;
- **any number of ingress ports can lead to one egress port**. For example, `80`, `443` and `8080`
  on the relay can all lead to the same service on the NAS.

```
relay:80   ─┐
relay:443  ─┼──▶  home-nas → 127.0.0.1:5000
laptop:8080 ┘
```

Routes are configured on the **Routes** page: a table in which every row is one "ingress port →
egress port" link. Under the table a diagram shows everything at once; you can arrange and label it.
An ingress port without a route is highlighted on the diagram and on the overview.

## Filters

An ingress port has a **source** filter (who may connect); an egress port has a **destination**
filter (where it may connect). Each consists of two lists:

- `allow`: if the list is not empty, **only** the listed entries are allowed;
- `deny`: the listed entries are always forbidden; `deny` beats `allow`;
- both empty: everything is allowed.

Entry format:

| Entry | Matches |
| --- | --- |
| `example.com` | this host |
| `*.example.com` | any subdomain |
| `10.0.0.5` | this address |
| `10.0.0.0/8` | any address in the network |
| `api.example.com:443` | the host on this port *(destination filter only)* |
| `example.com:*` | the host on any port *(destination filter only)* |
| `10.0.0.0/8:5432` | the network on this port *(destination filter only)* |
| `*:25` | any host on port 25 *(destination filter only)* |

Notes:

- **The source filter contains no ports.** A visitor's port is random, and a rule like `*:25` would
  silently match everyone. Such entries are therefore rejected with an error.
- An invalid list is not applied. If one nevertheless gets corrupted, traffic is blocked (fail closed).
- The source filter is checked where the port is open (on the relay or on an agent). The destination
  filter is checked on the machine that executes the egress port, before every connection.
- Host names are compared case-insensitively; patterns use `*` and `?`.

## How traffic flows

There are four combinations depending on where the entrance and the exit are. In all cases the leg
between machines goes through the TLS tunnel.

**1. Ingress on the relay, egress on the relay.** Visitor → the relay connects to the target itself.
No tunnel is needed (handy for exposing a service running on the VPS itself).

**2. Ingress on the relay, egress on agent A.**

```
visitor ─▶ relay :8080 ══(agent A tunnel, dial stream)══▶ agent A ─▶ target
```

**3. Ingress on agent A, egress on the same agent A.** The agent connects to the target directly; the
relay is not involved in the traffic (it only receives statistics).

**4. Ingress on agent A, egress on another machine B (or on the relay).**

```
visitor ─▶ agent A :5432 ══(route stream)══▶ relay ══(dial stream)══▶ agent B ─▶ target
```

The relay itself verifies that the port really belongs to agent A and looks up the route: an agent
cannot pick an arbitrary exit.

## Configuration version and delivery to agents

Every change (machine, port, egress port, route, filter) increments the **configuration version**.
The relay sends each agent only its own part: the ports it must open and the egress ports it serves.
The agent applies everything it can and returns an acknowledgement with bind errors (for example,
"port in use"). If something failed, the relay resends every 15 seconds, so the port opens as soon as
it becomes free.

## Statistics

- Counters are kept where the port is open (on the relay or on an agent). Agents report every
  2.5 seconds; the relay flushes the accumulated data to the database every 5 seconds.
- Stored: totals per port (bytes, connections), per-minute points for the chart (24 hours), visitor
  addresses (24 hours), machine presence (when and from where they were last online).
- Relay events (connections, errors, rejected logins) are written to a 7-day log.
- Statistics are runtime data: they are stored separately from the configuration and are not part of
  the backup.
