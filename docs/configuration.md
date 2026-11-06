# Configuration

Most settings are on the **Settings** screen. The app stores them in `config.json` in its data directory, and fills in the defaults below for any value that is missing.

## Where the data lives

| Platform | Directory |
|----------|-----------|
| macOS | `~/Library/Application Support/com.cashpilot.desktop` |
| Windows | `%APPDATA%\CashPilot Desktop` |
| Linux | `$XDG_DATA_HOME/cashpilot-desktop` (default `~/.local/share/cashpilot-desktop`) |

Set `CASHPILOT_DESKTOP_DATA_DIR` to use another directory. The SQLite database sits in `data/` inside it.

## Settings

| Key in `config.json` | Default | What it does |
|----------------------|---------|--------------|
| `displayCurrency` | `USD` | Currency the dashboard shows earnings in |
| `collectIntervalMinutes` | `60` | How often the app collects earnings from each service |
| `retentionDays` | `400` | How long earnings history is kept |
| `timezone` | `UTC` | Time zone passed to managed workers and phone sync events |
| `hostnamePrefix` | `cashpilot` | Containers are named `<prefix>-<service>` where the service allows it |
| `runtimeProvider` | `existing-docker` | The Docker-compatible runtime in use (read-only) |
| `fleetServerEnabled` | `false` | Lets this Desktop accept worker and phone heartbeats. Off by default: the CashPilot server is normally the hub |
| `fleetBindAddress` | `127.0.0.1` | Address the fleet API listens on; loopback unless you change it |
| `fleetPort` | `8085` | Port of the fleet API |
| `metricsEnabled` | `false` | Opt-in Prometheus `/metrics` endpoint on the fleet API, without authentication |
| `workerUrlPolicy` | `private` | Which worker addresses the app will call: `strict` (only the allowlist), `private` (private ranges, Tailscale and the allowlist) or `public` |
| `workerAllowedHosts` | empty | Allowlist of worker hosts: exact names, `*.suffix`, CIDRs or IPs |
| `workerAllowMetadata` | `false` | Allows cloud metadata addresses such as `169.254.169.254`; leave it off |
| `isolation` | empty (`auto`) | LAN isolation for container earners: `auto`, `on` or `off`. See [LAN isolation](#lan-isolation) |

## LAN isolation

Earner images are closed source and run whatever their vendor ships. On Docker's default bridge they can reach your LAN, this computer, a tailnet and cloud metadata. A new install puts them on a bridge of CashPilot's own instead, the same mechanism the CashPilot worker uses:

- The network `cashpilot-isolated` (interface `cp-isolated`), with Docker's own masquerade and traffic between containers turned off.
- One nftables table, `ip cashpilot`, written into the daemon's network namespace by a one-shot helper container (`cashpilot-fence`, host networking, `NET_ADMIN` and `NET_RAW` only, read-only root, pinned `drumsergio/cashpilot-worker` image). It holds the earners' NAT plus drops for 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16, subnets directly connected to the host and the host itself. On a host with IPv6 a second table drops all IPv6 from the bridge. Kernels without nf_tables get the same rules through `iptables-legacy`.
- Earners on that network use 1.1.1.1 and 9.9.9.9 for DNS, because a LAN resolver is behind the fence.

NAT and drops share one table, so if something deletes it the earners lose the internet instead of gaining your LAN over TCP. Three things still get through while the table is missing: traffic to this computer itself, which needs no NAT; everything, if the host has a masquerade rule of its own that covers the bridge (a WireGuard `PostUp`, a UFW or firewalld recipe); and one-way UDP to a LAN device. The app writes the table again at startup, before every deploy and every five minutes. A deploy it cannot fence is refused before the running container is touched, and the message says how to opt out.

On Docker Desktop, Podman Desktop and colima the daemon's namespace is the Linux VM's. That is where the containers' traffic leaves from, so the fence sits in the right place there too.

| Setting | What happens |
|---------|--------------|
| `auto` | A new install is fenced. An install whose earners already sit on the default bridge (an upgrade) is left as it is: the dashboard names the exposed services under Needs attention, with a **Turn on isolation** button that redeploys each running one onto the fenced network with its settings and data. Stopped services move on their next deploy. Once our network exists the install stays fenced. |
| `on` | Always fenced. |
| `off` | Docker's default bridge, as before. If our network still exists, the table keeps only its NAT, so services still on it keep their internet until you redeploy them. |

Limits:

- Host-networked services (Mysterium) are not on any bridge, so the fence does not cover them. Mysterium's catalog entry starts the node with `--firewall.protected.networks` for the same ranges, which blocks its VPN customers from them.
- Published ports keep working: replies to inbound connections are allowed.
- A proxied copy's tunnel joins the fenced network too, so a SOCKS5 or HTTP proxy at a LAN or tailnet address cannot be reached. A tunnel created before v0.39.1 stays on the default bridge until its copy is deployed again, so until then such a proxy is still reachable. Inside the tunnel, a SOCKS5 or HTTP proxy still gives the earner whatever that proxy can reach ([proxied copies](proxies.md#lan-isolation)).
- Most home routers send a connection to their own public address back into the LAN (NAT loopback), so the table also drops this computer's public IPv4. The app reads it from api.ipify.org once an hour, every five minutes until the first answer, and keeps the last address when a lookup fails. Until the first lookup succeeds after a start, a port your router forwards into the LAN is reachable through that address.
- Only that one address is dropped. With a second WAN or more public IPs, the others can still loop back; block them on the router or put the machine on a VLAN of its own.
- Tested on colima (Docker 29.5.2) with nftables. Not tested on Docker Desktop itself, Podman, rootless Docker or Windows. A runtime that ignores the interface name is refused, because no rule would match it. On Docker Desktop, Enhanced Container Isolation may block the helper; the deploy is then refused and the message gives the opt-out.

## Worker Node mode

To report to an existing CashPilot server, enter its address (`upstreamUrl`) and the pairing key, which is that server's `CASHPILOT_API_KEY`. The app uses it once to enrol and then keeps the per-worker key the server issues. The app sends a heartbeat every minute by default (`upstreamIntervalMinutes`), the same interval a CashPilot worker uses. Leaving the address empty keeps the app standalone.

## Secrets

Service credentials saved in the database are encrypted with AES-256-GCM. A native (non-Docker) service's environment, which can include its credentials, is recorded in `native/state.json` with `0600` permissions but is not encrypted.

The master key, the fleet key and the upstream keys live in the OS keychain; when no keychain is available they fall back to `0600` files in the data directory. If the keychain is present but locked or denies access, the app stops with an error instead of creating a new master key, because a new key would make every saved credential unreadable. `config.json` never holds a token.
