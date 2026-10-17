# Security

This document describes **what exactly** is protected, from whom, and what remains your responsibility.

## Who and what the system trusts

| Party | Trust |
| --- | --- |
| **Relay** | Full. It sees the contents of all TCP traffic passing through it and tells agents where to connect. Compromising the relay means compromising the whole network. |
| **Agent (`client`)** | Limited. It can use only its own ports and its own egress ports and cannot manage anything. |
| **Agent (`admin`)** | Full control: it receives the admin API token. Treat an admin machine like the relay itself. |
| **Visitors** | Untrusted. Source filters apply to them. |

### What the relay checks on an agent

- The agent authenticates with a token; the relay stores only the token's SHA-256. Tokens are random
  (192 bits), so brute force is unrealistic.
- An agent can open a `route` stream **only for an ingress port that belongs to it**. A request for
  someone else's port is rejected and logged. So an agent cannot exit through someone else's egress port.
- An agent can send statistics, visitor addresses and the connection list **only for its own
  ports**: foreign IDs are ignored and other people's counters cannot be falsified.
- The route and the egress port are chosen by the relay from its database, not by the agent.
- Only sessions with the `admin` role can reach the admin stream.
- When a machine is deleted, its role changed, its token reissued, or a backup restored, the sessions
  of the affected machines are dropped, so new rights take effect immediately.

### What the agent trusts

An agent does what the relay tells it: it connects to any `host:port` given in an egress port (subject
to that port's destination filter, which also comes from the relay). If the relay is compromised, it
can point the agent at any network the agent can reach. Therefore:

- run agents as a separate unprivileged user;
- on machines inside an internal network, do not grant the agent more than needed at the OS and
  network level;
- set a destination filter on egress ports: it is additionally checked on the agent's side.

## The agent ↔ relay channel

- **TLS 1.3** (earlier versions are rejected). The certificate is a self-signed ECDSA P-256 valid for 5 years.
- **Fingerprint verification** (SHA-256 of the public key) rather than a trust chain: either the
  fingerprint is set explicitly (`tls_pin`) or it is remembered on first connection (**TOFU**) in
  `pins.json`. A mismatch aborts the connection (`certificate pin mismatch`).
- **The weak spot of TOFU is the first connection:** an active attacker between the agent and the
  relay at that moment can substitute their own certificate. If this matters, compare the fingerprint
  from the relay log (`tls pin …`) and pass it explicitly (`--tls-pin`).
- Timeouts: 15 seconds for connect and handshake; 10 seconds for session keep-alive.
- The protocol frame size is limited to 1 MiB.

## Admin UI and API

- It listens **only** on `127.0.0.1` (by default). Do not expose it to the network. Reach it through
  the admin machine's tunnel or an SSH forward.
- Every request to `/api/*` requires a token in `X-Dyrokol-Api` (or `Authorization: Bearer`). The
  token is compared by SHA-256 in constant time.
- **CSRF protection:** besides the token in a non-standard header, requests with an `Origin` header
  that does not match the admin address are rejected (`403`). The match is by host; the loopback
  addresses (`localhost`, `127.0.0.1`, `::1`) are treated as equal.
- Request bodies are limited (64 KB; the restore snapshot 8 MB), and unknown JSON fields are rejected.
- In the browser the token lives in the tab's `sessionStorage` (not `localStorage`, not a cookie).
- Static UI files are served without a token (the sign-in screen needs them); they contain no secrets.
- **The admin API token is stored only inside the encrypted database** (unless you pass `--token`), so a copy of the relay's files does not reveal it. The relay opens no admin port by default.
- **The admin UI token ends up in the admin machine's log** (`admin ui token: …`). Watch where logs
  go (journald, files) and do not publish them.

## Data storage

- The configuration and the statistics are stored in two SQLite databases that exist on disk **only in
  encrypted form**: AES-256-GCM, key derived from the password via scrypt (N=2¹⁵, r=8, p=1), a random
  salt per file and a fresh random nonce on every write. The format identifier is part of the
  authenticated data. A wrong password and a corrupted file produce the same error.
- While running, the database lives **in the memory** of the relay process and is never written to
  disk in plaintext (neither to temporary files nor to the SQLite journal). Writes to disk are atomic.
- Files are created with mode `0600` (key and configs); `dyrokol.crt` is the public part.
- Secrets stay in the clear in process memory (including SOCKS5 passwords and the admin API token).
  If protection against memory dumps and swap matters, encrypt swap and restrict access to the process.
- SOCKS5 login and password (of the built-in SOCKS5 exit) are stored inside the encrypted database, are passed to the agent in the egress
  command, and end up in the backup (unless you use the *Without proxy passwords* button).

### Database password

The database password (256 bits of randomness) is generated by the agent when it registers the first
machine. It is needed **only to unlock** the relay after it starts, because the relay does not store
the password itself. Consequences:

- the password sits in plaintext in the agent config (`db_password`, mode `0600`). Database encryption
  protects against theft of the **relay's disk** (a backup, a VM image), but not against someone who
  has both the relay's disk and the agent's config;
- until the relay is unlocked it knows neither machines nor ports: the admin UI and ports are off and
  agents get `database is locked`;
- **once unlocked, the password is no longer checked**: any machine that passed token authentication
  works regardless of what it sent;
- the password cannot be recovered. If you lose every copy, you lose access to `*.db.enc` and can
  only rebuild the network from the `dyrokol-config.json` backup.

Putting `db_password` into every agent's config or only into the admin machine's is a trade-off:
everywhere means the relay comes up after a restart as soon as any agent returns; admin only means
fewer copies of the secret.

## Filters

- The source filter drops visitors before anything is opened for them. The destination filter is
  checked before every outgoing connection on the executing machine.
- `deny` always beats `allow`. A corrupt stored filter blocks traffic (fail closed).
- Do not rely on host-name filtering for sources: the address of the connecting peer (IP) is compared,
  not its reverse DNS record.

## Checklist

- [ ] Only the relay's tunnel port and the needed ingress ports are open; the relay has no admin port.
- [ ] Agents and the relay do not run as root (for ports below 1024 use `CAP_NET_BIND_SERVICE`).
- [ ] Ingress ports not meant for the whole Internet have `allow` set (especially on agents: they
      listen on all interfaces).
- [ ] Egress ports have a destination filter.
- [ ] The relay certificate fingerprint was verified on first connection or set through `tls_pin`.
- [ ] Permissions on `dyrokol.yaml` and the data directory are `0600`/`0700`; logs containing the
      admin token are not published.
- [ ] A configuration backup was made, and the certificate, key and database password were saved.
- [ ] The admin token is permanent and kept in a file/secret manager, not in shell history.

## What the system does not do

- **It does not encrypt the "visitor → ingress port" leg**: your ordinary TCP travels there. If visitors
  need TLS, terminate it in the service itself.
- **It does not limit speed or the number of connections** on ingress ports and does not protect against
  brute force at the handshake level (tokens are long enough, but connection floods are not cut off).
- **It does not keep a full audit trail** of administrator actions: relay events (connections, errors,
  restore) go to the log, but not every configuration change.
- **It does not rotate tokens** by itself and they do not expire: a token is valid until you reissue it.
- **It does not support several administrators with different rights**: the admin role is all or nothing.

## Reporting security issues

If you found a vulnerability, do not publish it in the open tracker. Report it privately through
[GitHub Security Advisories](https://github.com/dnech/DyroKol/security/advisories/new), including the
version (`Settings → About`), reproduction steps and a configuration without secrets.
