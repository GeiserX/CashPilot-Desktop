<p align="center">
  <img src="docs/images/banner.svg" alt="CashPilot Desktop" width="100%">
</p>

<p align="center">
  <a href="https://github.com/GeiserX/CashPilot-Desktop/releases/latest"><img src="https://img.shields.io/github/v/release/GeiserX/CashPilot-Desktop?style=flat-square&logo=github" alt="Release"></a>
  <a href="https://github.com/GeiserX/CashPilot-Desktop/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/GeiserX/CashPilot-Desktop/ci.yml?style=flat-square&label=CI" alt="CI"></a>
  <a href="https://github.com/GeiserX/CashPilot-Desktop/blob/main/LICENSE"><img src="https://img.shields.io/github/license/GeiserX/CashPilot-Desktop?style=flat-square" alt="License"></a>
  <a href="https://github.com/GeiserX/CashPilot-Desktop/releases"><img src="https://img.shields.io/github/downloads/GeiserX/CashPilot-Desktop/total?style=flat-square&logo=github" alt="Downloads"></a>
  <a href="https://github.com/GeiserX/CashPilot-Desktop/stargazers"><img src="https://img.shields.io/github/stars/GeiserX/CashPilot-Desktop?style=flat-square&logo=github" alt="Stars"></a>
</p>

CashPilot Desktop is a local-first desktop app for macOS (Apple Silicon), Windows and Linux that deploys and monitors passive-income and DePIN services. It packs CashPilot into one app with a system tray and a setup wizard, so you open an app instead of running the CashPilot container and a browser; the earning services themselves still run in Docker or Podman. It runs either as the full CashPilot dashboard (Run everything here) or as a box that joins a CashPilot server you already have (Join my CashPilot).

<p align="center">
  <img src="docs/images/screenshots/02-dashboard.png" alt="CashPilot Desktop dashboard" width="80%">
</p>

CashPilot Desktop is at 2.0 and does not update itself: get new versions from the [releases page](https://github.com/GeiserX/CashPilot-Desktop/releases). What changed in each major version, and the steps to upgrade: [UPGRADING.md](UPGRADING.md).

## Features

- **One-click install** -- No Docker knowledge required; the app handles container setup for you
- **System tray (macOS)** -- Runs quietly in the background with quick-access status and earnings summary; menu-bar icon is macOS-only today (Windows/Linux planned)
- **Real-time monitoring** -- Live earnings, service health, container stats, and node uptime
- **Multi-node fleet** -- Aggregate view across your entire CashPilot fleet from a single window
- **Server commands** -- A CashPilot server that this computer joined can deploy, stop, start, restart and remove services here. It needs CashPilot 3.0.0 or newer on the server, and a switch on the dashboard card turns it on or off ([docs](docs/configuration.md#the-server-starts-and-stops-services-here))
- **Hub for workers and phones** -- Desktop can also be the hub: workers and the [Android companion](https://github.com/GeiserX/CashPilot-android/releases) report to it instead of to a CashPilot server
- **Activity (opt-in)** -- Which destinations each earner talks to, whether it reaches into your network or sends mail; off by default, remote addresses kept 24 hours ([details](docs/activity.md))
- **Guided setup wizard** -- Step-by-step onboarding with Docker detection and installation guidance
- **Custom services** -- Run any image from Docker Hub or another registry that the catalog does not list, with the same restrictions as catalog services. A custom service gets no GPU and may not set an `NVIDIA_` variable ([docs](docs/custom-services.md))
- **Proxied copies** -- Run extra copies of a deployed service through a SOCKS5, HTTP CONNECT or Tailscale exit-node proxy, so one computer earns on several IPs; a dead proxy stops the copy instead of leaking ([docs](docs/proxies.md))
- **LAN isolation** -- New installs fence bridge-networked container earners off private ranges, tailnets, cloud metadata, this computer, its public IP and the public IPs of your other CashPilot boxes. If the fence can't be written, the deploy is refused, and the app rewrites it every five minutes. Mysterium's container runs only behind the fence, and its deploy is refused where the fence can't be applied. An upgraded install turns it on with one button. Not covered: Podman, a native (no-Docker) Mysterium node, a public IP with no CashPilot box on it, the minutes before the first public-IP lookup, and the gap after a firewall reload until the next rewrite ([docs](docs/configuration.md#lan-isolation))
- **Node identities kept safe** -- Removing a service keeps its data, an unrecoverable volume is never deleted without a separate yes, and encrypted backups take one click ([docs](docs/backup.md))
- **Provider grades** -- Every catalog provider gets a letter grade from the same published criteria as the web app, with its cons and their sources ([how we grade](https://geiserx.github.io/CashPilot/ratings/))
- **GPU miners** -- A catalog miner gets the GPU that its catalog entry declares, and only this computer's catalog decides that. GPU support is in beta
- **Check before deploy** -- Before a deploy, the app says what would stop a service earning on this computer
- **Import from another tool** -- Bring service logins over from money4band, CashFactory or InternetIncome after a dry run ([docs](docs/migrate/money4band.md))
- **Cross-platform** -- Native builds for macOS (ARM64), Windows (x64), and Linux (x64)
- **Lightweight** -- Minimal resource usage thanks to native Go backend with webview frontend
- **Secure** -- Credentials encrypted at rest with AES-256-GCM; master key in the OS keychain. From the first release after v0.20.5 the macOS build is signed with a Developer ID certificate and notarized by Apple; Windows signing is planned.

## Quick start

1. Download the app from the [latest release](https://github.com/GeiserX/CashPilot-Desktop/releases/latest). Docker or Podman must be installed; the wizard helps if it is missing.
   - **CashPilot Desktop for Windows**: the installer, `CashPilot-Desktop-windows-amd64-setup.exe`. It installs for your account with no admin prompt and adds an uninstaller to Settings > Apps. The portable `CashPilot-Desktop-windows-amd64.exe` runs from any folder and installs nothing. Both use the same data.
   - **CashPilot Desktop for macOS**: `CashPilot-Desktop-darwin-arm64.zip`, for Apple silicon.
   - **CashPilot Desktop for Linux**: `CashPilot-Desktop-linux-amd64.tar.gz`, for x64.
2. Launch the app and pick how it runs: **Run everything here** (it collects your earnings and shows the dashboard) or **Join my CashPilot** (paste the block from your CashPilot's Fleet page, Add a box; the app tests the connection before it saves anything).
3. Browse the service catalog, deploy containers, and watch earnings from the dashboard or the tray.

Platform notes, system requirements and the full walkthrough are in [Getting started](docs/getting-started.md).

## Supported services

CashPilot bundles a catalog of 49 active passive-income services across multiple categories (the catalog also carries 11 retired entries, which the app hides and refuses to deploy). Some links below are affiliate/referral links -- see the [disclosure](docs/services.md#disclosure). Read [how bandwidth-sharing works](docs/services.md#before-you-start-how-bandwidth-sharing-works) before you opt in; per-service limits, payouts and caveats are in [docs/services.md](docs/services.md).

- **Docker-deployable:** [Anyone Protocol](https://anyone.io), [Bitping](https://app.bitping.com), [Earn.fm](https://earn.fm/ref/GEISYB91), [EarnApp](https://earnapp.com/i/TSMD9wSm), [Honeygain](https://dashboard.honeygain.com/ref/SERGIB4014), [IPRoyal Pawns](https://pawns.app?r=19266874), [MystNodes](https://mystnodes.co/?referral_code=do7v7YOoBBpbOstKQovX2pUvZYKia4ZhH3QIdNtE), [PacketStream](https://packetstream.io/?psr=7xgZ), [ProxyBase](https://peer.proxybase.org/dashboard/signup?referral=nXzS3c6iTO), [ProxyBase Markets](https://proxybase.xyz?referral=nXzS3c6iTO), [ProxyLite](https://proxylite.ru/?r=KMUPRZIZ), [ProxyRack](https://peer.proxyrack.com/ref/mpwiok3xlaxeycnn5znqlg7ipjeutxyxr6xl7vmn), [Repocket](https://link.repocket.com/b1zM), [Sia hostd](https://docs.sia.tech/provide-storage/setting-up-hostd/docker), [Storj](https://storj.dev/node/get-started/setup), [Traffmonetizer](https://traffmonetizer.io/?aff=2111758), [URnetwork](https://ur.io/c?bonus=1Q3G19)
- **Browser extension / desktop only:** [Bytebenefit](https://bytebenefit.io/invited?ref=Brl4z3), [Bytelixir](https://bytelixir.com/r/OYEIRE0VSZBZ), [Dawn Internet](https://infrastructure.finance/validator-extension?code=2QLQV97F), [Deeper Network](https://shop.deeper.network?sca_ref=12020100.q8Vxm3SliA), [Ebesucher](https://www.ebesucher.com/?ref=geiserx), [Grass](https://app.grass.io/register?referralCode=kn8FNEPnUr2tMqE), [Helium](https://helium.com), [Nodepay](https://app.nodepay.ai/register?ref=0wzzyznen64j9zx), [Noderr Micronodes](https://micronodes.noderr.xyz/), [Nodle](https://nodle.com), [PassiveApp](https://passiveapp.com/i/bqpC4M), [Sentinel dVPN](https://sentinel.co), [Spide](https://spide.network/register.html?f3bc51), [Teneo Protocol](https://dashboard.teneo.pro/auth/signup?referralCode=CAqef), [Theta Edge Node](https://thetatoken.org), [Titan Network](https://edge.titannet.info/signup?inviteCode=2GKKJ495), [Uprock](https://link.uprock.com/i/33e8492e), [Wipter](https://wipter.com/register?via=FB1373FBDA)
- **GPU compute:** [Flux](https://runonflux.io), [Golem Network](https://golem.network), [io.net](https://io.net), [Nosana](https://nosana.io), [Salad](https://salad.io), [Vast.ai](https://cloud.vast.ai/?ref_id=452772)
- **Crypto mining:** [2Miners ETC](https://etc.2miners.com/help) (unpaid ETC balance), [HeroMiners Kaspa](https://kaspa.herominers.com/?lang=en) (unpaid KAS balance), [Kryptex Pool (Pearl)](https://pool.kryptex.com/?ref=2444cca8) (NVIDIA GPU, balance kept in your Kryptex account), [OCEAN](https://ocean.xyz/getstarted) (unpaid BTC balance), [SRBMiner-Multi (GPU mining)](https://github.com/doktor83/SRBMiner-Multi/releases) (NVIDIA GPU, fee shown before deploy), [SupportXMR](https://supportxmr.com) (unpaid XMR balance), [unMineable (CPU mining, paid in your coin)](https://unmineable.com/?ref=awk6-j1z0) (unpaid balance in your coin), [XMRig (Monero CPU mining)](https://xmrig.com) (fee shown before deploy)

> Dawn Internet's link does not pass the code on. Create the account in the DAWN Validator extension and enter referral code `2QLQV97F` when it asks.

## Documentation

- [Getting started](docs/getting-started.md): modes, downloads, system requirements, Desktop vs Web
- [Configuration](docs/configuration.md): every setting and its default, joining a CashPilot server, where secrets live
- [Usage](docs/usage.md): a tour of every screen
- [Activity](docs/activity.md): what the opt-in Activity page reads, keeps and alerts on
- [Supported services](docs/services.md): bandwidth-sharing explained, per-service limits and payouts, disclosure
- [Custom services](docs/custom-services.md): run an image the catalog does not list, and what it may do
- [Proxied copies](docs/proxies.md): run extra copies through a proxy, what cannot, the traffic cap, Tailscale exit nodes
- Moving from another tool: [money4band](docs/migrate/money4band.md), [CashFactory](docs/migrate/cashfactory.md), [InternetIncome](docs/migrate/internetincome.md)
- [How it works](docs/how-it-works.md): the full design, grounded in the code
- [FAQ](docs/faq.md): earnings, safety, Docker, crashes, several machines
- [Development](docs/development.md): prerequisites, dev workflow, build, tests
- [For AI agents](https://geiserx.github.io/CashPilot/for-ai-agents/): how an AI assistant can offer CashPilot's referral sign-ups to its user

## Related projects

| Project | Type | Description |
|---------|------|-------------|
| [CashPilot](https://github.com/GeiserX/CashPilot) | Backend | Multi-service passive income aggregator and fleet manager |
| [CashPilot-android](https://github.com/GeiserX/CashPilot-android) | Android Agent | Monitoring agent for passive income apps on Android |
| [cashpilot-mcp](https://github.com/GeiserX/cashpilot-mcp) | MCP Server | Monitor earnings from AI assistants via Model Context Protocol |
| [cashpilot-ha](https://github.com/GeiserX/cashpilot-ha) | Home Assistant | Earnings and service status sensors for your smart home |

## License

[GPL-3.0-or-later](LICENSE). Sergio Fernandez, 2026.
