# What's new in 1.0

CashPilot Desktop 1.0 is the first stable release. It follows about 130 releases
since v0.1.0 in May 2026. Nothing breaks on upgrade: your config, database,
saved credentials and deployed services carry over as they are. The version
number says the app is ready for daily use, with the limits listed at the end
of this page.

This page covers what Desktop does today, grouped by what you would want to
know first. Each part links to the page with the details.

## Earners can't reach your network

Earner images are closed source. Desktop assumes they might misbehave and
limits what they can reach.

- **LAN fence.** On a new install, bridge-networked earners run on CashPilot's
  own network, `cashpilot-isolated`. One nftables table drops traffic to
  10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, tailnets at 100.64.0.0/10, cloud
  metadata at 169.254.0.0/16, subnets directly connected to this computer, the
  computer itself and its public IPv4. That last one stops NAT loopback from
  reaching a port your router forwards. The app learns that address with a
  lookup, so until the first lookup after a start succeeds the address is open,
  and only one public IPv4 is covered. With a second WAN, block the others on
  the router. NAT and the drops live in the same table, so if the table goes
  missing the earners lose the internet over TCP instead of gaining your LAN.
  Three things still get through while it is missing: traffic to this computer
  itself, one-way UDP to a LAN device, and everything when the host has a
  masquerade rule of its own that covers the bridge. The app writes the table
  again at startup, before every deploy and every five
  minutes, and refuses a deploy it can't fence before it touches the running
  container. An upgraded install gets a **Turn on isolation** button under Needs
  attention. On Podman the fence can't be written yet: with `auto`, earners
  deploy unfenced on Podman's default network and Needs attention says so.
  See [LAN isolation](configuration.md#lan-isolation).
- **Least privilege for every container.** Every capability is dropped and only
  the ones the catalog entry declares are added back, with `no-new-privileges`,
  a process limit, host devices only from a fixed allow-list, and privileged
  mode only where the entry asks for it. The fence, tunnel and Activity helper
  images that Desktop runs itself are pinned by digest.
- **Data that can't be replaced is protected.** Removing a service keeps its
  volumes. Deleting the data is a separate question that names each volume and
  what it holds. A volume the catalog marks unrecoverable, such as a Storj identity
  or a Mysterium keystore, is never deleted without a yes to that question. The
  refusal comes before anything is removed, proxied copies included.
- **Encrypted backups of node identities.** A **Back these up** card lists the
  services whose identity, keystore or wallet exists only on this disk.
  **Download backup** reads those volumes with the container paused and
  encrypts them with ChaCha20-Poly1305 to a passphrase or an X25519 public key.
  CashPilot keeps nothing that could open the file, and has no upload. The
  format is the web app's, so either app's tool opens either app's backups.
  See [Backup](backup.md).
- **Credentials encrypted at rest** with AES-256-GCM, with the master key in the
  OS keychain. Where no keychain is available the key falls back to a
  `.credential_key` file with `0600` permissions in the app's data folder, so a
  backup of that folder holds the key too. A native, non-Docker service's
  environment, which can include its credentials, sits unencrypted in
  `native/state.json` with `0600` permissions.
- **Mysterium on Windows** runs in userspace mode, which blocks its VPN
  customers from your LAN, plus a Windows Firewall rule for 100.64.0.0/10 and
  169.254.0.0/16. The node does not start without the rule. A node deployed
  before this change keeps its old launch, which lets VPN customers reach your
  LAN and tailnet, until you deploy it again.
- **Signed macOS builds.** The macOS app is signed with a Developer ID
  certificate, notarized and stapled. The release job fails if a signing secret
  is missing, so it never ships unsigned. Releases are built with Go 1.26.10,
  which distrusts the eight revoked eMudhra intermediates.
- **Activity, opt-in.** See which destinations each earner talks to: countries,
  networks, ports, failed connections, and alerts for mail ports, LAN attempts
  and odd hours. It never reads packet contents. Remote addresses are kept 24
  hours and deleted the moment you turn it off. See [Activity](activity.md).

## Earn on more than one IP

**Proxied copies** run an extra copy of a deployed service through a SOCKS5
proxy, an HTTP CONNECT proxy or a Tailscale exit node, so one computer earns on
its own IP plus one per proxy. Add a proxy under Fleet, Proxies, test it to see
its exit IP and country, then run a copy through it.

- Each copy is a tunnel container plus the earner in the tunnel's network
  namespace. The tunnel's firewall goes in first, and the earner starts only
  after the tunnel sees an exit IP within 45 seconds. When the app knows this
  computer's public IP, that exit must also differ from it.
- It fails closed. If the proxy or the tunnel dies, the earner can't send a
  packet anywhere, not from your own IP and not to your LAN.
- DNS goes through the proxy too, including plain HTTP CONNECT proxies.
- A SOCKS5 or HTTP proxy gives the earner whatever the proxy itself can reach,
  private networks on its side included. Pick a proxy that refuses private
  destinations. Tailscale tunnels drop private and tailnet ranges.
- An optional traffic cap stops every copy behind a proxy at a percentage you
  pick. A reset day, or Reset usage, starts only the copies the cap stopped,
  and a copy that fails to start then is tried again.
- Proxy logins and auth keys are stored encrypted.

There are limits. The app counts the cap only while it runs. Services that need host
networking, ports, volumes, devices or raw network capabilities can't run
through a proxy, and nor can services whose terms forbid proxies. With today's
catalog that rules out eight services. SOCKS5 and HTTP carry TCP only. See
[Proxied copies](proxies.md).

## The catalog

- **41 active services**, plus 11 retired entries the app hides and refuses to
  deploy. They are vendored from the CashPilot web catalog byte for byte, with a weekly
  check that fails when the two drift apart.
- **Letter grades** for every provider, computed from published criteria such
  as payout record, account safety, container fit and privacy, with the top
  cons and their sources. Missing evidence counts against a provider.
  [How we grade](https://geiserx.github.io/CashPilot/ratings/).
- **A check before you deploy** that says what will stop a service earning on
  this machine: a provider that forbids containers, no build for your CPU, or
  another machine in the fleet already running a service the provider allows
  once per connection. It never blocks.
- **A "not earning" flag** when a service's logs match a known failure or the
  container keeps restarting. There is no green "earning" badge, because nothing
  a container shows proves money moved.
- **Custom services** run an image the catalog doesn't list, under the same
  restrictions. See [Custom services](custom-services.md).
- **Several instances** of a service on one machine, where the provider pays per
  device and allows it.
- **Compose export**: save a `docker-compose.yml` with the same container
  hardening and no credentials, and keep earning without CashPilot. The file
  does not carry the LAN fence, so exported earners can reach your LAN and this
  computer unless your own firewall stops them.
- **Import** service logins from money4band, CashFactory or InternetIncome, after
  a dry run. See [money4band](migrate/money4band.md).

## Earnings and payouts

- 17 earnings collectors read balances straight from the providers. A platform
  with no reading shows a dash, never 0.00.
- Earnings per day over 7 or 30 days, in any of 44 display currencies.
- Each service shows how far its balance is from the provider's cash-out
  minimum, how and how often it pays, and what it does with your connection.
- Mysterium's balance is read from each node's own API, not the 30-day cloud
  figure.
- Per-container CPU, memory and network charts, kept for 35 days.

## Fleet

- **Pair with a CashPilot server** as a worker. The dashboard then shows what
  your provider accounts earned across the whole fleet, and marks a platform as
  shared when more than one machine runs it. The first pairing copies this
  machine's earnings history to the server, so its past days count too.
  Unlinking leaves your local history as it was. Desktop 1.0 works alongside
  [CashPilot web 2.0](https://geiserx.github.io/CashPilot/whats-new-2.0/),
  which keeps the worker heartbeat, enrollment and history import routes
  compatible.
- **Use Desktop as the hub instead.** Turn it on in Settings, and workers and
  the Android companion report to this Desktop. Phones report every
  15 minutes, so Desktop gives a phone 45 minutes before it shows offline,
  against 3 minutes for a worker.
- **The Android companion reaches 1.0 at the same time.** It reports the
  earning apps on a phone to your CashPilot server or to this Desktop, and
  sends nothing anywhere else. It needs CashPilot web 1.73.2 or newer, or
  Desktop 0.39.6 or newer, so Desktop 1.0 works with it. Get it
  from https://github.com/GeiserX/CashPilot-android/releases. An F-Droid
  listing is submitted and waiting for review.

## Platforms

| Platform | Build | Signed |
|---|---|---|
| macOS (Apple Silicon) | `.zip` | Yes, Developer ID, notarized |
| Windows (x64) | `.exe` | Not yet |
| Linux (x64) | `.tar.gz` | No |

Earners run in Docker or Podman; the setup wizard helps if neither is
installed. Packages for winget, a Homebrew tap and the AUR are written and
tested against the release assets, but not submitted yet. See
[App stores](app-stores.md).

## Upgrading from 0.x

Your settings, data and services carry over. A few older deployments stay as
they were until you act. The first three matter for security:

- An install from before v0.37.0 keeps its earners on Docker's default bridge.
  Press **Turn on isolation** under Needs attention to fence them.
- A proxied copy created before v0.39.1 keeps its tunnel on the default bridge
  until you deploy that copy again.
- A Windows Mysterium node deployed before the userspace change keeps its old
  launch, which lets its VPN customers reach your LAN and tailnet. Deploy it
  again.
- A ProxyBase service deployed before v0.10.1 runs the retired client and earns
  nothing. Deploy it again with a fresh Access Token.
- Copying your history to a server needs CashPilot v1.16.0 or newer.

## Known limits

- The tray icon is macOS only.
- Podman is not fenced yet. With `auto`, Needs attention says so; with `on`,
  container deploys are refused.
- A native (no-Docker) Mysterium node is not behind the fence. It keeps its
  customers off private ranges itself. Mysterium's container runs only behind
  the fence.
- The fence is tested on colima. It is not tested on Docker Desktop itself,
  rootless Docker or Windows.
- The proxy traffic cap is only counted while the app runs.
- No auto-update yet. Download new releases from the releases page.

## Still in progress

- Auto-updates.
- Windows code signing.
- Submitting the winget, Homebrew and AUR packages.
- A tray icon on Windows and Linux.
- A LAN fence that works on Podman.
- Remote deploy from a CashPilot server to this Desktop.

The full list of changes is in the [changelog](../CHANGELOG.md) and on the
[releases page](https://github.com/GeiserX/CashPilot-Desktop/releases).
