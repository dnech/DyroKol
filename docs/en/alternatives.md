# Alternatives and comparison

DyroKol is a self-hosted TCP port forwarder with a web UI (similar to a reverse proxy / tunnel / NAT traversal tool). Related projects, so you can choose what fits:

| Tool | What it is | How DyroKol differs |
| --- | --- | --- |
| [frp](https://github.com/fatedier/frp) | Reverse proxy / port forwarding with a server (`frps`) and clients (`frpc`) configured by files | DyroKol is managed from a web UI with a live route map, and the relay pushes changes to agents instantly; ports are unique per machine |
| [rathole](https://github.com/rapiz1/rathole) | Lightweight, fast reverse proxy for NAT traversal | DyroKol trades some minimalism for a UI, filters, statistics and an encrypted config database |
| [ngrok](https://ngrok.com/) | Hosted tunnels with public URLs | DyroKol is self-hosted: your VPS, your certificate, no third-party service in the path |
| [Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) | Outbound tunnels published through Cloudflare | No account or third party; plain TCP ports, not only HTTP(S) |
| [Tailscale](https://tailscale.com/) / [WireGuard](https://www.wireguard.com/) | Mesh VPN / VPN | DyroKol is not a VPN: it forwards individual TCP ports, nothing to install at the OS level, no virtual network adapters |
| [chisel](https://github.com/jpillora/chisel) | TCP/UDP tunnel over HTTP with SSH encryption | DyroKol adds central routing, source/destination filters and a UI |
| [bore](https://github.com/ekzhang/bore) | Minimal TCP tunnel to a public server | DyroKol manages many ports and machines from one place |
| [sshuttle](https://github.com/sshuttle/sshuttle) | "Poor man's VPN" over SSH: transparently routes whole subnets through an SSH server | sshuttle needs SSH access and root on the client and routes networks from the client outward; DyroKol forwards chosen TCP ports, agents connect out on their own and need no SSH or admin rights |
| `ssh -R` / autossh | Reverse port forwarding over SSH | No hand-written commands or restarts per port; routes are edited in the UI |

Pick DyroKol if you want to **expose or route TCP ports between your own machines through one
public server, manage it in a browser, and run a built-in SOCKS5 exit** without a VPN. Pick another
tool if you need UDP, HTTP hostnames with automatic certificates, or a full mesh network.

This comparison is a short summary from public descriptions; check each project for its current features.
