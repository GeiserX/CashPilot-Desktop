# Upgrading CashPilot Desktop

Each major release has a section here, newest first: what changed, then the steps to upgrade. Every change is in the [changelog](CHANGELOG.md).

## 2.0

In CashPilot Desktop 2.0, a CashPilot server that this computer joined can
deploy, stop, start, restart and remove services here. We call that server
commands. Server commands need CashPilot 3.0.0 or newer on the server.

2.0 is a major release because some changes need a step from you. Read
[Before you upgrade to 2.0](#before-you-upgrade-to-20) and
[After you upgrade to 2.0](#after-you-upgrade-to-20) first.

### What changed in 2.0

#### Server commands

While this computer is joined and the switch is on, your CashPilot deploys,
stops, starts, restarts and removes services here. It uses the same routes as
on a CashPilot Docker worker.

- CashPilot listens on TCP 8086, on this computer's private network and
  Tailscale addresses only (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16,
  100.64.0.0/10, fc00::/7). It refuses requests from other addresses.
- Only the key that the server gave this computer at the join works. The
  shared key is refused.
- A deploy runs this computer's own catalog entry and pinned image. Every
  local deploy rule still applies.
- The server cannot delete data that cannot be recovered, such as a node
  identity or a wallet.
- The server can use the checked deploy route. That route replaces only the
  container that the owner approved. CashPilot 3.0.0 still sends Desktops down
  the older route, which replaces whatever runs.
- The dashboard card shows which server can send commands and the last command
  that it sent.

The full rules are in
[The server starts and stops services here](docs/configuration.md#the-server-starts-and-stops-services-here).

#### GPU miners

A catalog miner gets the GPU that its catalog entry declares, as on a CashPilot
worker. Only this computer's catalog decides that. The deploy form, a joined
CashPilot, a custom service and a proxied copy cannot ask for a GPU.

- Every container without an NVIDIA grant gets
  `NVIDIA_VISIBLE_DEVICES=void`. A CUDA image then cannot take the GPU on a
  host whose default Docker runtime is nvidia.
- No catalog entry, custom service or request may set an `NVIDIA_` variable.
- As a lead, Desktop refuses to send a GPU miner to a worker whose GPU grant
  for that miner differs from this computer's catalog. A worker that reports
  no grants is told to update.
- GPU support is in beta until it runs on a real GPU host.

#### New in the catalog

- Pool balances for SupportXMR, HeroMiners Kaspa and 2Miners ETC, read from
  your payout address.
- XMRig, a Monero CPU miner. The deploy card shows its 1% developer fee before
  you deploy. Its Max CPU threads (%) setting caps the threads that it starts,
  on arm64, on x86 and on virtual machines.
- unMineable, which mines with XMRig and pays out in the coin that you pick.
  The balance shows in that coin.
- Dawn and Gradient do not read the referral code from the sign-up link, so
  their cards now show the code to type, with a Copy button.
- SRBMiner-Multi, a GPU miner for Pearl, Zano or Ravencoin on the pool that
  you choose, and KRig, which mines Pearl on Kryptex Pool.

#### Fixes that you can notice

- On Windows, a stop, restart, redeploy or remove of a native earner
  (Mysterium) no longer waits 30 seconds before the earner stops.
- Docker actions are off while Docker is stopped, and the page says how to
  fix it.
- As a lead, a Redeploy yes counts only for the worker that it names. If
  another worker takes over the row with the same name, Desktop asks again.
- When you remove a copy that is already gone, Desktop shows the worker's own
  message.

### How to upgrade to 2.0

#### Which server 2.0 needs

| Server | Desktop 1.7.0 | Desktop 2.0 |
|---|---|---|
| CashPilot 2.x | Reports only | Reports only |
| CashPilot 3.0.0 or newer | Reports only | Reports, and takes server commands while the switch is on |

"Reports only" means that the server shows what runs here, but cannot run
commands here. Under CashPilot 2.x, Desktop 2.0 still listens on TCP 8086
while the switch is on, but a server that never listed `desktop_commands`
never learns the address. A server that stops listing it, for example after
a downgrade to 2.x, can still get the address in one more heartbeat. The
heartbeat after that leaves it out. The window and the background helper
each send their own heartbeats, so each can send that one. To stop server
commands before a downgrade, turn the switch off first.

- Desktop 2.0 sends its command address only when the server's previous
  heartbeat answer said that it can command a Desktop safely. CashPilot 3.0.0
  sends that answer. An older server never gets the address, except in the one
  heartbeat after a downgrade described above. While the switch is on, the
  dashboard card says that the server is older than CashPilot 3.0.0.
- Desktop 1.7.0 has no server commands. It stays report-only under CashPilot
  3.0.0, so you can upgrade the server first.
- Upgrade the server first, then Desktop.

#### Before you upgrade to 2.0

1. Upgrade your CashPilot server to 3.0.0 or newer. If you only run Desktop
   (Run everything here), skip this step.
2. Remove every `NVIDIA_` variable from your custom services. Desktop 2.0
   refuses an `NVIDIA_` variable when you save a service and when you deploy
   it. A custom service from 1.x that sets one keeps running, but you cannot
   deploy it again until you remove the variable.
3. Move GPU work out of your custom services. In 2.0, a custom service gets
   no GPU. A custom service that uses an NVIDIA GPU through Docker's nvidia
   default runtime runs without the GPU after its next deploy. Run that
   workload outside CashPilot, or ask for a catalog entry.
4. Know what happens if you go back to 1.x. Desktop 2.0 moves this computer's
   pairing with your CashPilot into a new file, `pairing.json`, and deletes the
   old keys. After that, a return to Desktop 1.x needs a new join. When you
   upgrade to 2.0 again, the window applies the choice you made in 1.x when
   it first starts:
   - You joined a server in 1.x, the same one or another one: 2.0 moves that
     pairing over, as on a first upgrade. The keychain must be unlocked and the
     server must answer when the window starts. If not, unlock the keychain or
     restore the connection, then quit CashPilot from the tray and start it
     again.
   - You left the server in 1.x: 2.0 leaves it too.
   - You did not change the pairing in 1.x: 2.0 keeps its own pairing.
   - 1.x has a server address but no key for it: 2.0 shows that address and
     waits. Join again or leave in Settings.

   When the pairing changes, the key that 2.0 held before stops working.
   Server commands start off again, because 1.x drops the saved switch. Turn
   them on with the switch on the dashboard card. Until the window has applied
   the 1.x choice, the window does not report to any server and takes no
   commands. A background helper that still runs 1.x reports until the window
   replaces it. The window does that as soon as it opens ("After you
   upgrade to 2.0", step 2).
5. Download 2.0 from the
   [releases page](https://github.com/GeiserX/CashPilot-Desktop/releases).
   Desktop does not update itself and does not tell you about a new version.

#### After you upgrade to 2.0

These steps apply to a computer that was joined to a CashPilot server in 1.x.

1. Start CashPilot while your server is reachable. When the window starts,
   it moves the pairing to `pairing.json` if the server answers. The
   background helper cannot do it. If the server was down, quit CashPilot
   from the tray and start it again. On a first upgrade from 1.x, this
   computer keeps reporting with its old key until the move. After a return
   to 1.x and back, the window does not report until it applies the 1.x
   choice (step 4 of "Before you upgrade to 2.0"). A helper that still runs
   1.x can report until step 2.
2. If the background helper is on, the window replaces it with 2.0 as soon
   as it opens. Until then, a helper that still runs 1.x sends its own
   heartbeats, so the server's Fleet page can show 1.x for this computer. On
   macOS and Linux, native earners that the old helper started stop with it,
   as at a sign-out. Start them again in CashPilot.
3. Turn on server commands if you want them. They start off after an upgrade
   from 1.x, because an upgrade is not a choice to allow them. Use the switch
   on the dashboard card. A computer that joins for the first time in 2.0
   starts with server commands on, and the same switch turns them off.
4. On Windows, also press **Allow through Windows Firewall** on the same card.
   CashPilot does not listen until Windows Firewall lets it in.

The details are in [Join my CashPilot](docs/configuration.md#join-my-cashpilot).

#### If your CashPilot refuses this computer's key

The dashboard then says "Your CashPilot refused this computer's key." This
happens when the server answers HTTP 401, for example after the owner removed
this computer on the server's Fleet page. Desktop 1.x joined again by itself
with the shared key. Desktop 2.0 does not. After the move to `pairing.json`,
it stops the heartbeat and the server commands until you join again by hand.
A pairing that is not moved yet keeps sending the heartbeat with its old key
and shows the same message. In both cases, join again:

1. In Settings, open **How this computer runs** and switch to
   **Run everything here**.
2. Switch back to **Join my CashPilot** and join again.

#### Change the server address in Settings

After the move to `pairing.json`, an edit of `upstreamUrl` in `config.json`
does not change the server. Use Settings, or leave and join again.

## 1.0

CashPilot Desktop 1.0 is the first stable release. It follows about 130 releases
since v0.1.0 in May 2026. Nothing breaks on upgrade: your config, database,
saved credentials and deployed services carry over as they are. The version
number says the app is ready for daily use, with the limits listed under
[Known limits in 1.0](#known-limits-in-10).

This section covers what Desktop 1.0 does, grouped by what you would want to
know first. Each part links to the page with the details.

### What changed in 1.0

#### Earners can't reach your network

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
  See [LAN isolation](docs/configuration.md#lan-isolation).
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
  See [Backup](docs/backup.md).
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
  hours and deleted the moment you turn it off. See [Activity](docs/activity.md).

#### Earn on more than one IP

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
[Proxied copies](docs/proxies.md).

#### The catalog

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
  restrictions. See [Custom services](docs/custom-services.md).
- **Several instances** of a service on one machine, where the provider pays per
  device and allows it.
- **Compose export**: save a `docker-compose.yml` with the same container
  hardening and no credentials, and keep earning without CashPilot. The file
  does not carry the LAN fence, so exported earners can reach your LAN and this
  computer unless your own firewall stops them.
- **Import** service logins from money4band, CashFactory or InternetIncome, after
  a dry run. See [money4band](docs/migrate/money4band.md).

#### Earnings and payouts

- 17 earnings collectors read balances straight from the providers. A platform
  with no reading shows a dash, never 0.00.
- Earnings per day over 7 or 30 days, in any of 44 display currencies.
- Each service shows how far its balance is from the provider's cash-out
  minimum, how and how often it pays, and what it does with your connection.
- Mysterium's balance is read from each node's own API, not the 30-day cloud
  figure.
- Per-container CPU, memory and network charts, kept for 35 days.

#### Fleet

- **Pair with a CashPilot server** as a worker. The dashboard then shows what
  your provider accounts earned across the whole fleet, and marks a platform as
  shared when more than one machine runs it. The first pairing copies this
  machine's earnings history to the server, so its past days count too.
  Unlinking leaves your local history as it was. Desktop 1.0 works alongside
  [CashPilot web 2.0](https://github.com/GeiserX/CashPilot/blob/main/UPGRADING.md#20-change-the-image-tags),
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

#### Platforms

| Platform | Build | Signed |
|---|---|---|
| macOS (Apple Silicon) | `.zip` | Yes, Developer ID, notarized |
| Windows (x64) | `.exe` | Not yet |
| Linux (x64) | `.tar.gz` | No |

Earners run in Docker or Podman; the setup wizard helps if neither is
installed. Packages for winget, a Homebrew tap and the AUR are written and
tested against the release assets, but not submitted yet. See
[App stores](docs/app-stores.md).

#### Known limits in 1.0

- The tray icon is macOS only.
- Podman is not fenced yet. With `auto`, Needs attention says so; with `on`,
  container deploys are refused.
- A native (no-Docker) Mysterium node is not behind the fence. On Linux and
  Windows it keeps its customers off private ranges itself. On macOS it does
  not block that traffic; run the container instead if that matters.
  Mysterium's container runs only behind the fence.
- The fence is tested on colima. It is not tested on Docker Desktop itself,
  rootless Docker or Windows.
- The proxy traffic cap is only counted while the app runs.
- No auto-update yet. Download new releases from the releases page.

#### Still in progress in 1.0

- Auto-updates.
- Windows code signing.
- Submitting the winget, Homebrew and AUR packages.
- A tray icon on Windows and Linux.
- A LAN fence that works on Podman.
- Remote deploy from a CashPilot server to this Desktop.

### How to upgrade to 1.0

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

The full list of changes is in the [changelog](CHANGELOG.md) and on the
[releases page](https://github.com/GeiserX/CashPilot-Desktop/releases).
