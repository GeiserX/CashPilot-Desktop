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
- A native (no-Docker) Mysterium node is not on any bridge, so the fence does not cover it. The app starts it with `--firewall.protected.networks` set to six fixed ranges: 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 127.0.0.0/8, 100.64.0.0/10 and 169.254.0.0/16. That list is smaller than the fence: it leaves out other directly connected subnets and this computer's public IP. What the flag does depends on the OS. On Linux the node drops customer traffic to those ranges. On macOS it only skips NAT for them, so it does not block that traffic. On Windows the node also runs with `--userspace` and a Windows Firewall rule. Creating that rule needs a Windows admin prompt. Before it, the setup card shows CashPilot's own screen: what the rule blocks, what Windows asks and how to remove the rule. Windows shows the prompt only after Continue there. After a No at the prompt the node does not run, and the card says so. Removing the service deletes the rule.
- Published ports keep working: replies to inbound connections are allowed.
- A proxied copy's tunnel joins the fenced network too, so a SOCKS5 or HTTP proxy at a LAN or tailnet address cannot be reached. A tunnel created before v0.39.1 stays on the default bridge until its copy is deployed again, so until then such a proxy is still reachable. Inside the tunnel, a SOCKS5 or HTTP proxy still gives the earner whatever that proxy can reach ([proxied copies](proxies.md#lan-isolation)).
- Most home routers send a connection to their own public address back into the LAN (NAT loopback), so the table also drops this computer's public IPv4. The app reads it from api.ipify.org once an hour, every five minutes until the first answer, and keeps the last address when a lookup fails. Until the first lookup succeeds after a start, a port your router forwards into the LAN is reachable through that address.
- The table also drops the public IPv4 of every other box in your fleet, so an earner here cannot reach the port forwards of your other internet lines. When this app reports to a CashPilot server (CashPilot 2.10.0 or newer), it sends its own public IP in each heartbeat and drops the addresses the server sends back. When workers report to this app, it drops their addresses and sends each of them the full list. Phones are left out, because a phone on mobile data reports its carrier's address. Until the server first answers after a start, the table keeps the drops it already has. A line with no CashPilot box on it is not covered: block it on the router or put the machine on a VLAN of its own.
- A proxied copy whose proxy or Tailscale exit node sits on one of those lines keeps its way out. Its tunnel gets an exception for its own address only: TCP to the proxy's port, or UDP for Tailscale. The earner in the copy shares the tunnel's network, where the tunnel's own firewall lets out only the tunnel's traffic.
- Tested on colima (Docker 29.5.2) with nftables. Not tested on Docker Desktop itself, rootless Docker or Windows. A runtime that ignores the interface name is refused, because no rule would match it. On Docker Desktop, Enhanced Container Isolation may block the helper; the deploy is then refused and the message gives the opt-out.

## Join my CashPilot

`mode` in `config.json` is `lead` (Run everything here) or `follower` (Join my CashPilot). The first run asks. An install from before this setting gets it once from what it has: a saved server address means `follower`, none means `lead`, and it never sees the first run again. To switch later, use the Settings card **How this computer runs**. Each direction shows what happens before anything changes:

- Joining: the earning apps keep running; this computer's earnings history is copied to the server once; this computer stops collecting earnings, because the server logs in to the provider accounts; and the hub (**Accept workers**) turns off, with a list of the workers and phones to point at the server instead.
- Leaving: the earning apps keep running; the address and both keys are cleared; local collection starts again at once. The server keeps this computer's row until you remove it on its Fleet page.

While joined, the dashboard says the earnings are on the server, and shows the last heartbeat and the last error in plain words. Collector errors from before the join are not shown, because this computer no longer collects.

### The server starts and stops services here

While joined, your CashPilot can deploy, stop, start, restart and remove services on this computer, as it does on a CashPilot Docker worker. The dashboard card **<server> can start and stop services on this computer** has the switch and shows the last command the server sent. It is on when you join. When you join again, the switch keeps what you chose. `serverCommandsOff` in `config.json` holds it.

A computer that was already joined in Desktop 1.x starts with the switch off after the upgrade, because an upgrade is not a choice to let the server run commands here. Turn it on with the same switch. Once you use the switch, `serverCommandsChosen` in `config.json` is true, and the upgrade never changes the switch again. The switch stays off, also when you leave and join again, until you turn it on. A computer that joins for the first time in Desktop 2.0 or later starts with the switch on.

- The server reaches this computer on TCP port 8086. CashPilot listens there only on this computer's private network and Tailscale addresses (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, fc00::/7). It never listens on 0.0.0.0, a public address or the bridges the earning apps' containers use. It refuses a request from any other source address.
- It needs CashPilot 3.0.0 or newer. An older server would deploy here without the checks 3.0.0 added, so until the server's heartbeat answer says it can send commands, this computer does not report its command address and the server only sees what runs here. The card then says the server is older than CashPilot 3.0.0.
- It takes only the key the server gave this computer when it joined. The shared key every box joins with is refused, and so is a request with no key. The key is never logged or shown. If your CashPilot refuses it with HTTP 401, Desktop suspends server commands until you join again.
- A deploy runs the service of this computer's own catalog, with the image CashPilot pins here. The server's request sets only the variables. Every local rule still holds: a retired or desktop-only service, a missing required field, a service routed through a proxy, a computer set to run services only through a proxy. A refusal goes back to the server in plain words.
- The server cannot delete data that cannot be recovered, such as a node identity or a wallet. Do that on this computer.
- It deploys one copy of a service. Add more copies on this computer. The server does not scale a service here and does not remove copies you added here. It has no routes for copies that run through a proxy (`/api/proxied/...`) or for one extra copy (`/api/containers/<service>/instances/<n>`); manage those on this computer.
- The server can also use the checked deploy route (`/api/containers/<service>/deploy-checked`), as on a CashPilot worker. Its lookup names the container a deploy would replace, by its full Docker ID, or `none`. The deploy then replaces only that container. If another container is there, or none, nothing changes and the answer is 409 with `deploy_target_changed`. The checked route also answers 409 while more copies of the service run on this computer, and for a service that runs here as a program, not in a container, once that program is here. Redeploy those on this computer. The older route (`/api/containers/<service>/deploy`) still replaces whatever runs.
- A start, stop or restart answers as a CashPilot worker does: 404 for a service or copy that is not here, 503 when Docker is not reachable, 403 with a code when the proxy route rule refuses it, and 409 for any other refusal.
- On Windows, CashPilot does not listen until Windows Firewall lets it in, so Windows never shows its own firewall prompt for it. The card says it is waiting for the rule. Its **Allow through Windows Firewall** button says what it opens, then asks Windows for an administrator: inbound TCP 8086, for CashPilot only, on networks Windows calls private, from the ranges above. If Windows already blocks CashPilot (Windows makes such a rule when someone presses Cancel on its own firewall prompt), the same step removes that block, because a Block rule wins over an Allow rule. It removes only the inbound Block rules for CashPilot that cover TCP 8086 (TCP or any protocol, port 8086 or any port) and keeps every other Block rule. The card says so before you press it.
- Turning the switch off closes the port at once. Switching to **Run everything here**, or clearing the server's address in Settings, closes it and removes the firewall rule. CashPilot checks Windows for the rule after removal. If the rule remains or Windows cannot report its status, open **How this computer runs** in Settings and select **Retry firewall removal**. This option stays available after restart. The rule is named `CashPilot: server commands (TCP 8086)`. If you uninstall CashPilot while joined, delete that rule in Windows Defender Firewall.

The window and the background helper share the port: whichever holds it serves the server, and the other one reports the same address.

An install that was upgraded as a `follower` keeps **Accept workers** as it was, so its workers keep reporting. The Settings card lists them until you point them at the server and turn the switch off.

While joined to CashPilot 2.10.0 or newer, the heartbeat carries each copy's traffic counters, as a CashPilot worker's does, and the server raises the traffic stall alerts for this computer's own copies; the dashboard says so. This holds only while the server answers and the heartbeat interval is under 3 minutes, because the server marks a box offline after 3 minutes of silence and does not judge it then. Otherwise, and always for copies behind a proxy, this computer judges its copies itself.

The background helper reads the same pairing revision as the window. It checks the current revision and key for every server command. Within a minute of a switch in the window, the helper also changes how it collects and reports.

The join screen takes the block that the server's Fleet page copies under Add a box, or the address and key in two fields. It sends one heartbeat and saves nothing unless the server accepts it. The advanced way is to enter the address (`upstreamUrl`) and the pairing key, which is that server's `CASHPILOT_API_KEY`, in the settings list; the mode follows the address. The app uses it once to enrol and then keeps the per-worker key the server issues. The app sends a heartbeat every minute by default (`upstreamIntervalMinutes`), the same interval a CashPilot worker uses. Leaving the address empty keeps the app standalone. The address must be https, or plain http to this machine, a private or Tailscale address, a single-label name or a `.local`, `.lan`, `.home.arpa`, `.internal` or `.ts.net` name, the same rule the Android app uses; anything else is refused when you save it, because the pairing key would cross the internet in clear text. An address saved before this rule existed is not checked again when you save other settings; the app logs the refusal on each heartbeat instead.

If the server refuses this machine's own key (the owner removed it from the fleet page, or the server lost its keys), the app enrols again once with the pairing key, as the Android app does. To take a machine out of the fleet for good, clear its pairing address or key here, or change the server's `CASHPILOT_API_KEY`.

## Secrets

Service credentials saved in the database are encrypted with AES-256-GCM. A native (non-Docker) service's environment, which can include its credentials, is recorded in `native/state.json` with `0600` permissions but is not encrypted.

The master key, the fleet key and the upstream keys live in the OS keychain; when no keychain is available they fall back to `0600` files in the data directory. If the keychain is present but locked or denies access, the app stops with an error instead of creating a new master key, because a new key would make every saved credential unreadable. `config.json` never holds a token.
