# Configuration

Most settings are on the **Settings** screen. The app stores them in `config.json` in its data directory, and fills in the defaults below for any value that is missing.

## Where the data lives

| Platform | Directory |
|----------|-----------|
| macOS | `~/Library/Application Support/com.cashpilot.desktop` |
| Windows | `%APPDATA%\CashPilot Desktop` |
| Linux | `$XDG_DATA_HOME/cashpilot-desktop` (default `~/.local/share/cashpilot-desktop`) |

Set `CASHPILOT_DESKTOP_DATA_DIR` to use another directory. The SQLite database sits in `data/` inside it.

Set `CASHPILOT_PROXIED_ONLY=true` to run services only through a proxy: the app then refuses any deploy, Start or Restart from this computer's own IP, and stops a service already running that way when it starts and every 30 seconds after. See [Route a service at deploy](proxies.md#route-a-service-at-deploy).

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
| `isolation` | empty (`auto`) | LAN isolation for container earners: `auto`, `on` or `off`. See [LAN isolation](#lan-isolation) |

## LAN isolation

Earner images are closed source and run whatever their vendor ships. On Docker's default bridge they can reach your LAN, this computer, a tailnet and cloud metadata. A new install puts them on a bridge of CashPilot's own instead, the same mechanism the CashPilot worker uses:

- The network `cashpilot-isolated` (interface `cp-isolated`), with Docker's own masquerade and traffic between containers turned off.
- One nftables table, `ip cashpilot`, written into the daemon's network namespace by a one-shot helper container (`cashpilot-fence`, host networking, `NET_ADMIN` and `NET_RAW` only, read-only root, `drumsergio/cashpilot-worker` image pinned by digest). It holds the earners' NAT plus drops for 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16, subnets directly connected to the host and the host itself. On a host with IPv6 a second table drops all IPv6 from the bridge. Kernels without nf_tables get the same rules through `iptables-legacy`.
- Earners on that network use 1.1.1.1 and 9.9.9.9 for DNS, because a LAN resolver is behind the fence.

NAT and drops share one table, so if something deletes it the earners lose the internet instead of gaining your LAN over TCP. Three things still get through while the table is missing: traffic to this computer itself, which needs no NAT; everything, if the host has a masquerade rule of its own that covers the bridge (a WireGuard `PostUp`, a UFW or firewalld recipe); and one-way UDP to a LAN device. The app writes the table again at startup, before every deploy and every five minutes. A deploy it cannot fence is refused before the running container is touched, and the message says how to opt out.

On Docker Desktop and colima the daemon's namespace is the Linux VM's. That is where the containers' traffic leaves from, so the fence sits in the right place there too.

Podman is not fenced yet. Its Docker-compatible API does not keep the network options the fence relies on (the interface name, masquerade off, traffic between containers off), and netavark adds a masquerade of its own. On Podman, `auto` deploys earners on Podman's default network and Needs attention says they are not fenced; `on` refuses container deploys, because the fence it asks for cannot be given; `off` is the default network without the warning.

A proxied copy's tunnel container is never named as exposed. Its own firewall drops every packet except those to the proxy and through the tunnel, private ranges included.

| Setting | What happens |
|---------|--------------|
| `auto` | A new install is fenced. An install whose earners already sit on the default bridge (an upgrade) is left as it is: the dashboard names the exposed services under Needs attention, with a **Turn on isolation** button that redeploys each running one onto the fenced network with its settings and data. The redeploy is per service: a service with a running copy is redeployed whole, and its stopped copies come back running. A service with no running copy is left alone and moves on its next deploy. Once our network exists the install stays fenced, and the app's config also remembers that isolation was on, so the install stays fenced after a prune or a runtime reset removes the network and every fenced earner. Setting `off` clears that memory. |
| `on` | Always fenced. |
| `off` | Docker's default bridge, as before. If our network still exists, the table keeps only its NAT, so services still on it keep their internet until you redeploy them. |

Limits:

- Mysterium's container runs only behind the fence. With isolation off, on Podman or with Windows containers, its deploy and start are refused. Docker does not restart it on its own: the app starts it after it has applied the fence.
- A native (no-Docker) Mysterium node is not on any bridge, so the fence does not cover it. The app starts it with `--firewall.protected.networks` set to six fixed ranges: 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 127.0.0.0/8, 100.64.0.0/10 and 169.254.0.0/16. That list is smaller than the fence: it leaves out other directly connected subnets and this computer's public IP. What the flag does depends on the OS. On Linux the node drops customer traffic to those ranges. On macOS it only skips NAT for them, so it does not block that traffic. On Windows the node also runs with `--userspace` and a Windows Firewall rule.
- Published ports keep working: replies to inbound connections are allowed.
- A proxied copy's tunnel joins the fenced network too, so a SOCKS5 or HTTP proxy at a LAN or tailnet address cannot be reached. A tunnel created before v0.39.1 stays on the default bridge until its copy is deployed again, so until then such a proxy is still reachable. Inside the tunnel, a SOCKS5 or HTTP proxy still gives the earner whatever that proxy can reach ([proxied copies](proxies.md#lan-isolation)).
- Most home routers send a connection to their own public address back into the LAN (NAT loopback), so the table also drops this computer's public IPv4. The app reads it from api.ipify.org once an hour, every five minutes until the first answer, and keeps the last address when a lookup fails. Until the first lookup succeeds after a start, a port your router forwards into the LAN is reachable through that address.
- Only that one address is dropped. With a second WAN or more public IPs, the others can still loop back; block them on the router or put the machine on a VLAN of its own.
- Tested on colima (Docker 29.5.2) with nftables. Not tested on Docker Desktop itself, rootless Docker or Windows. A runtime that ignores the interface name is refused, because no rule would match it. On Docker Desktop, Enhanced Container Isolation may block the helper; the deploy is then refused and the message gives the opt-out.

## Worker Node mode

To report to an existing CashPilot server, enter its address (`upstreamUrl`) and the pairing key, which is that server's `CASHPILOT_API_KEY`. The app uses it once to enrol and then keeps the per-worker key the server issues. The app sends a heartbeat every minute by default (`upstreamIntervalMinutes`), the same interval a CashPilot worker uses. Leaving the address empty keeps the app standalone. The address must be https, or plain http to this machine, a private or Tailscale address, a single-label name or a `.local`, `.lan`, `.home.arpa`, `.internal` or `.ts.net` name, the same rule the Android app uses; anything else is refused when you save it, because the pairing key would cross the internet in clear text. An address saved before this rule existed is not checked again when you save other settings; the app logs the refusal on each heartbeat instead.

If the server refuses this machine's own key (the owner removed it from the fleet page, or the server lost its keys), the app enrols again once with the pairing key, as the Android app does. To take a machine out of the fleet for good, clear its pairing address or key here, or change the server's `CASHPILOT_API_KEY`.

## Secrets

Service credentials saved in the database are encrypted with AES-256-GCM. A native (non-Docker) service's environment, which can include its credentials, is recorded in `native/state.json` with `0600` permissions but is not encrypted.

The master key, the fleet key and the upstream keys live in the OS keychain; when no keychain is available they fall back to `0600` files in the data directory. If the keychain is present but locked or denies access, the app stops with an error instead of creating a new master key, because a new key would make every saved credential unreadable. `config.json` never holds a token.
