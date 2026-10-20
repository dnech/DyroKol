# Getting started

This guide takes you from empty machines to a working port forward and walks through three typical
scenarios. The terms ("machine", "ingress port", "egress port") are explained in
[Concepts](concepts.md).

## What you need

- **The relay**: a machine with a public IP or domain name where you can open TCP port `7443`.
  The cheapest VPS will do.
- **At least one agent machine**: any computer or device with Internet access.
- The `dyrokol` binary for your platform (download it from the
  [Releases page](https://github.com/dnech/DyroKol/releases), see the
  [table in the README](../../README.md#builds)) or the [one-line installer](deployment.md#one-line-install-linux).

The rest of this guide assumes the relay is reachable at `vps.example.com`.

## 1. Start the relay

On the VPS:

```bash
chmod +x dyrokol
./dyrokol server
```

You will see:

```
server: created default config /root/.config/dyrokol/dyrokol-server.yaml
dyrokol server listening on :7443 (tls pin sha256:454c8c0b…); database locked, waiting for an agent
```

What happened:

- a config, a self-signed TLS certificate and its key were created (in `~/.config/dyrokol`);
- the relay listens on the tunnel port `:7443`;
- **the database does not exist yet and is locked**: the admin UI and the ports appear when the
  first machine connects;
- an admin API token was generated (unless you set your own, see [CLI](cli.md#the-relay-dyrokol-server)).

Open TCP port `7443` on the VPS (the provider's firewall and `ufw`/`iptables`). You do **not**
need to expose the admin UI: it listens on `127.0.0.1` only.

For permanent operation set up a service, see [Deployment](deployment.md#systemd-the-relay).

## 2. The first machine: registration

On the machine you will manage the network from (usually a laptop or a home server):

```bash
./dyrokol client --server vps.example.com:7443
```

Output:

```
Machine registered on vps.example.com:7443.

  tunnel token:  adm_…
  db password:   …
  config:        /home/me/.config/dyrokol/dyrokol.yaml
  admin ui:      http://127.0.0.1:9181 (this machine holds the admin role)
…
2026/10/17 03:09:45 [vps.example.com:7443] admin ui available at http://127.0.0.1:9181
2026/10/17 03:09:45 [vps.example.com:7443] admin ui token: api_…
2026/10/17 03:09:45 [vps.example.com:7443] connected, role=admin
```

What happened:

1. The agent generated a token and a database password and sent them to the relay.
2. The relay created an encrypted database with one machine, this one, holding the **admin** role.
3. The token and password were saved to the agent config (`dyrokol.yaml`, mode `0600`).
   **The token and password are shown once**, but they stay in the config.
4. The agent connected and opened the admin UI entry point on its own `127.0.0.1:9181`.
5. The relay certificate fingerprint was remembered in `pins.json` (trust-on-first-use): from now on
   the agent will refuse to connect to an impostor server.

> **Verify the fingerprint.** The first connection trusts the certificate blindly. Compare the
> `tls pin` line in the relay log with `pins.json` on the agent if the network between you is not
> trustworthy. Or pass the fingerprint up front: `--tls-pin sha256:…`.

Next time just run `./dyrokol client`: the config already exists.

## 3. Sign in to the admin UI

Open `http://127.0.0.1:9181` **on the same machine** and paste the `admin ui token` from the agent
log (it starts with `api_`).

This is the admin API token, not the tunnel token. The relay generates it once, on the first start, and
keeps it inside its encrypted database (not in a config file or the log), so it does not change on
restarts; the admin machine receives it on every connect and prints it again.

![Overview](../img/overview.png)

The **Overview** page shows what needs attention at a glance: "Needs attention" suggests the next
steps (a port without a route, an offline machine, a bind error).

## Scenario A. Publish a service from a home machine to the Internet

*Goal:* a web service on a NAS (`127.0.0.1:5000`) should open at `http://vps.example.com:8080`.

### A1. Add the NAS as a machine

**Machines → Add machine**, label `home-nas`, role `Client`. A dialog with the token opens:

![New machine token](../img/token-dialog.png)

Copy the token (it will not be shown again) and the launch command. On the NAS:

```bash
./dyrokol client --server vps.example.com:7443 --token cl_…
```

The database password is not needed here: it only unlocks the relay after a restart, and the first machine
that connects with it does that for everyone. Give it (`--db-password <password>`) to the admin machine and,
if you like, to one always-on machine; other machines wait until the relay is unlocked and then connect.
The command in the **Add machine** dialog includes `--db-password` for the `Admin` role only.

This form of launch saves nothing. To make the agent start simply as `./dyrokol client`, put a
config in place:

```yaml
# ~/.config/dyrokol/dyrokol.yaml
servers:
  - addr: vps.example.com:7443
    token: cl_…
    db_password: …      # only needed if the relay may be locked, see below
```

> **Why the database password.** After a relay restart the database is locked until somebody presents
> the password. The admin machine holds it (it is saved in its config), so the relay unlocks as soon
> as that machine connects. If the admin machine may be switched off, add the password to the other
> agents' configs too. Details in [Security](security.md#database-password).

`home-nas` becomes **online** in the machine list.

### A2. An egress port on the NAS

**Egress ports → Add egress port**:

![Adding an egress port](../img/egress-dialog.png)

- *Exits on*: `home-nas`
- *Type*: Direct
- *Target host*: `127.0.0.1` (this address is **from the NAS's point of view**), *Port*: `5000`
- *Label*: `nas dashboard`

### A3. An ingress port on the relay

**Ingress ports → Add ingress port**:

![Adding an ingress port](../img/ingress-dialog.png)

- *Bound on*: `relay (this server)`, *Port*: `8080`

Click **Add port**. The port is created but leads nowhere yet: on the Ingress page its status is
`not routed`, and the overview flags it as a problem.

### A4. The route

Open **Routes**. In the last table row (**new route**) pick the ingress port `relay:8080` on the
left, the egress port `home-nas → 127.0.0.1:5000` on the right and click **Add route**.

![Routes](../img/routes.png)

A `relay:8080 → home-nas` row appears in the table, and the diagram below draws a line between those
ports. Open `http://vps.example.com:8080`: you land on the NAS. Open port `8080` in the VPS firewall
if it is closed.

The port row on the Ingress page shows the `forwarding` status, counters and the target.

![Ingress ports](../img/ingress.png)

**Need more entrances to the same service?** Add ports (say, `80` and `443`), then add a row in the
Routes table for each with the same egress port: any number of ingress ports can lead to one egress
port.

**You can arrange the diagram under the table as you like:** drag ports, add a "Home" frame around
the home machines and an "Internet" label: the layout is remembered and is the same for every
administrator.

### Restrict who can connect

In the port dialog (**⋮ → Edit**), in the *Source filter* block add, for example,
`allow: 203.0.113.0/24`. Other visitors will be dropped. Ports are not allowed in the source filter.

## Scenario B. Reach a remote service from your laptop

*Goal:* open `localhost:5432` on the laptop and get to the PostgreSQL running on the NAS.

1. Connect the laptop as a machine `laptop` (as in A1) or use the admin machine.
2. Egress port: machine `home-nas`, host `127.0.0.1`, port `5432`.
3. Ingress port: *Bound on* `laptop`, port `5432`. Then on the **Routes** page connect it to that egress port.

The agent on the laptop opens port `5432`; connections go through the relay to the NAS
(case 4 in [Concepts](concepts.md#how-traffic-flows)). To make the port reachable only from the
laptop itself and not from the local network, add `allow: 127.0.0.1` to the source filter.

## Scenario C. Reach the network through a SOCKS5 exit

*Goal:* an external program that speaks SOCKS5 should reach the Internet from the home machine.

1. Egress port: machine `home-nas`, type **SOCKS5 exit** (no host or port; optionally a login and
   password).
2. An ingress port on the relay; on the **Routes** page connect it to that egress port.
3. Point any SOCKS5 client at the ingress port (`relay:PORT`).

The SOCKS5 server is built into the `home-nas` agent: it opens every outgoing connection the client
asks for from `home-nas`. Limit the targets with a destination filter (`allow: example.com`) if the
port must not go just anywhere.

## What next

- All UI pages: [Admin UI](admin-ui.md).
- Running as a service, firewall, upgrades: [Deployment](deployment.md).
- If something does not work: [Troubleshooting](troubleshooting.md).
