<p align="center">
  <img src="docs/banner.svg" alt="CashPilot Desktop" width="100%">
</p>

<p align="center">
  <a href="https://github.com/GeiserX/CashPilot-Desktop/releases/latest"><img src="https://img.shields.io/github/v/release/GeiserX/CashPilot-Desktop?style=flat-square&logo=github" alt="Release"></a>
  <a href="https://github.com/GeiserX/CashPilot-Desktop/releases"><img src="https://img.shields.io/github/downloads/GeiserX/CashPilot-Desktop/total?style=flat-square&logo=github" alt="Downloads"></a>
  <a href="https://github.com/GeiserX/CashPilot-Desktop/stargazers"><img src="https://img.shields.io/github/stars/GeiserX/CashPilot-Desktop?style=flat-square&logo=github" alt="Stars"></a>
  <a href="https://github.com/GeiserX/CashPilot-Desktop/blob/main/LICENSE"><img src="https://img.shields.io/github/license/GeiserX/CashPilot-Desktop?style=flat-square" alt="License"></a>
</p>

---

## What is CashPilot Desktop?

CashPilot Desktop is a native desktop application for managing your [CashPilot](https://github.com/GeiserX/CashPilot) passive income fleet. Instead of running CashPilot as a Docker container and accessing it via browser, CashPilot Desktop bundles everything into a single installable app with system tray integration, auto-updates, and a guided setup wizard.

It can run in two modes:

- **CashPilot mode** -- Full dashboard with service management, earnings tracking, container deployment, and fleet orchestration
- **Worker Node mode** -- Lightweight agent that connects to an existing CashPilot instance to run services on this machine

Built with [Tauri v2](https://tauri.app) for a lightweight, cross-platform experience with native performance.

## Features

- **One-click install** -- No Docker knowledge required; the app handles container setup for you
- **System tray** -- Runs quietly in the background with quick-access status and earnings summary
- **Real-time monitoring** -- Live earnings, service health, container stats, and node uptime
- **Multi-node fleet** -- Aggregate view across your entire CashPilot fleet from a single window
- **Auto-updater** -- Seamless in-app updates with ed25519 signature verification
- **Guided setup wizard** -- Step-by-step onboarding with Docker detection and installation guidance
- **Cross-platform** -- Native builds for macOS (ARM64), Windows (x64), and Linux (x64)
- **Lightweight** -- ~45 MB installer, minimal resource usage thanks to Tauri's native webview
- **Secure** -- macOS code-signed and notarized, Windows code-signed, encrypted credential storage

## Installation

Download the latest release for your platform:

| Platform | Format | Download | Notes |
|----------|--------|----------|-------|
| macOS (Apple Silicon) | `.dmg` | [Download](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) | Signed and notarized |
| Windows (x64) | `.exe` (NSIS) | [Download](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) | Code-signed |
| Linux (Debian/Ubuntu) | `.deb` | [Download](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) | systemd integration |

After installation, the app auto-updates itself when new versions are available.

### System Requirements

| Requirement | Minimum |
|-------------|---------|
| **Docker** | Docker Desktop (macOS/Windows) or Docker Engine (Linux) |
| **RAM** | 4 GB (8 GB recommended for multiple services) |
| **Disk** | 2 GB free (more for service containers) |
| **Network** | Residential IP recommended for most services |

## Quick Start

1. **Download and install** CashPilot Desktop for your platform
2. **Launch the app** -- the setup wizard detects Docker and guides you through installation if needed
3. **Choose your mode** -- CashPilot (full dashboard) or Worker Node (connect to existing instance)
4. **If Worker Node** -- enter your CashPilot instance address and fleet key
5. **Start earning** -- browse the service catalog, deploy containers, and monitor earnings from the system tray

## Supported Services

CashPilot manages 40+ passive income services across multiple categories. Below is the full catalog.

### Docker-Deployable Services

Services CashPilot can deploy and manage automatically via Docker containers.

| Service | Residential IP | VPS IP | Devices / Acct | Devices / IP | Payout |
|---------|:-:|:-:|:-:|:-:|--------|
| [Anyone Protocol](https://anyone.io) | ✅ | ✅ | Unlimited | 1 | Crypto (ANYONE) |
| [Bitping](https://app.bitping.com) | ✅ | ✅ | Unlimited | 1 | Crypto (SOL) |
| [Earn.fm](https://earn.fm/ref/GEISYB91) | ✅ | ✅ | Unlimited | 1 | Crypto |
| [EarnApp](https://earnapp.com/i/TSMD9wSm) | ✅ | ❌ | 15 | 1 | PayPal, Gift Cards, Wise |
| [Honeygain](https://dashboard.honeygain.com/ref/SERGIB4014) | ✅ | ❌ | 10 | 1 | PayPal, Crypto |
| [IPRoyal Pawns](https://pawns.app?r=19266874) | ✅ | ❌ | Unlimited | 1 | PayPal, Crypto, Bank Transfer |
| [MystNodes](https://mystnodes.co/?referral_code=do7v7YOoBBpbOstKQovX2pUvZYKia4ZhH3QIdNtE) | ✅ | ✅ | Unlimited | Unlimited | Crypto (MYST) |
| [PacketStream](https://packetstream.io/?psr=7xgZ) | ✅ | ❌ | Unlimited | 1 | PayPal |
| [Presearch](https://presearch.com/signup?rid=4872322) | ✅ | ✅ | Unlimited | 1 | Crypto (PRE) |
| [ProxyBase](https://peer.proxybase.org?referral=nXzS3c6iTO) | ✅ | ❌ | Unlimited | 1 | Crypto |
| [ProxyLite](https://proxylite.ru/?r=KMUPRZIZ) | ✅ | ✅ | Unlimited | 1 | Crypto, PayPal |
| [ProxyRack](https://peer.proxyrack.com/ref/mpwiok3xlaxeycnn5znqlg7ipjeutxyxr6xl7vmn) | ✅ | ✅ | 500 | 1 | PayPal, Crypto |
| [Repocket](https://repocket.com/) | ✅ | ❌ | 5 | 2 | PayPal, Crypto |
| [Storj](https://www.storj.io/node) | ✅ | ✅ | Unlimited | 1 \* | Crypto (STORJ) |
| [Traffmonetizer](https://traffmonetizer.com/?aff=2111758) | ✅ | ✅ \*\* | Unlimited | Unlimited | Crypto (USDT), PayPal |
| [URnetwork](https://ur.io/?referral_code=1Q3G19) | ✅ | ✅ | Unlimited | 1 | Crypto |

> \* Storj nodes on the same /24 subnet share data allocation, reducing per-node earnings.
>
> \*\* Traffmonetizer ToS requires residential IP, but VPS nodes are accepted in practice.

### Browser Extension / Desktop Only

These services have no Docker image. CashPilot lists them in the catalog with signup links and earning estimates.

| Service | Residential IP | VPS IP | Devices / Acct | Devices / IP | Payout | Status |
|---------|:-:|:-:|:-:|:-:|--------|--------|
| [Bytelixir](https://bytelixir.com/r/OYEIRE0VSZBZ) | ✅ | ❌ | Unlimited | 1 | Crypto | Active |
| [Dawn Internet](https://dawninternet.com/?code=2QLQV97F) | ✅ | ❌ | Unlimited | 1 | Crypto (DAWN) | Active |
| [Deeper Network](https://deeper.network) | ✅ | ❌ | Unlimited | 1 | Crypto (DPR) | Active |
| [Ebesucher](https://www.ebesucher.com/?ref=geiserx) | ✅ | ✅ | Unlimited | 1 | PayPal | Active |
| [Gradient Network](https://app.gradient.network/signup?referralCode=YSKMY7) | ✅ | ❌ | Unlimited | 1 | Crypto (GRADIENT) | Active |
| [Grass](https://app.grass.io/register?referralCode=kn8FNEPnUr2tMqE) | ✅ | ❌ | Unlimited | 1 | Crypto (GRASS) | Active |
| [Helium](https://helium.com) | ✅ | ❌ | Unlimited | 1 | Crypto (HNT) | Active |
| [Nodepay](https://app.nodepay.ai/register?ref=0wzzyznen64j9zx) | ✅ | ❌ | Unlimited | 1 | Crypto (NC) | Active |
| [Nodle](https://nodle.com) | ✅ | ✅ | Unlimited | 1 | Crypto (NODL) | Active |
| [PassiveApp](https://passiveapp.com/i/bqpC4M) | ✅ | ❌ | Unlimited | 1 | Crypto, PayPal | Active |
| [Sentinel dVPN](https://sentinel.co) | ✅ | ✅ | Unlimited | 1 | Crypto (DVPN) | Active |
| [Spide](https://spide.network/register.html?f3bc51) | ✅ | ❌ | Unlimited | 1 | Crypto | Active |
| [Teneo Protocol](https://dashboard.teneo.pro/?code=CAqef) | ✅ | ❌ | Unlimited | 1 | Crypto (TENEO) | Active |
| [Theta Edge Node](https://thetatoken.org) | ✅ | ✅ | Unlimited | 1 | Crypto (TFUEL) | Active |
| [Titan Network](https://edge.titannet.info/signup?inviteCode=2GKKJ495) | ✅ | ❌ | Unlimited | 1 | Crypto (TNT) | Active |
| [Uprock](https://link.uprock.com/i/33e8492e) | ✅ | ❌ | Unlimited | 1 | Crypto | Active |

### GPU Compute

GPU-intensive computing services. Requires compatible hardware.

| Service | Residential IP | GPU Required | Min Storage | Payout | Status |
|---------|:-:|:-:|:-:|--------|--------|
| [Flux](https://runonflux.io) | ✅ | ❌ | 220GB | Crypto (FLUX) | Active |
| [Golem Network](https://golem.network) | ✅ | ❌ | 20GB | Crypto (GLM) | Active |
| [io.net](https://io.net) | ✅ | ✅ | N/A | Crypto (IO) | Active |
| [Nosana](https://nosana.io) | ✅ | ✅ | 50GB | Crypto (NOS) | Active |
| [Salad](https://salad.io) | ✅ | ✅ | N/A | PayPal, Gift Cards | Active |
| [Vast.ai](https://cloud.vast.ai/?ref_id=452772) | ✅ | ✅ | 100GB | Crypto, Bank Transfer | Active |

> **Note:** Earnings vary widely by location, hardware, and demand.

## CashPilot Desktop vs Web

| Feature | Desktop App | Web (Docker) |
|---------|:-----------:|:------------:|
| Installation | One-click installer | `docker compose up -d` |
| Docker management | Built-in (auto-detects, guides install) | Requires Docker pre-installed |
| System tray integration | **Yes** | No |
| Auto-updates | **Yes** (in-app) | Manual image pull |
| Background operation | Native OS service | Container must stay running |
| Fleet management | **Yes** | **Yes** |
| Earnings dashboard | **Yes** | **Yes** |
| Target audience | End users, non-technical | Self-hosters, sysadmins |
| Resource usage | ~110 MB RAM | ~80 MB RAM (container only) |

## Architecture

```
CashPilot Desktop
├── Tauri Shell (Rust)       — Window management, system tray, auto-updater, IPC
├── Frontend (HTML/CSS/JS)   — Setup wizard, loading states, status display
└── Python Sidecar           — Full CashPilot backend (FastAPI + Docker SDK)
    ├── Service orchestration — Deploy/stop/restart containers
    ├── Earnings collection  — Polls service APIs on schedule
    ├── Fleet management     — Multi-node coordination via HTTP
    └── SQLite database      — Config, credentials (encrypted), earnings history
```

The Tauri shell spawns the Python sidecar as a subprocess, communicates via localhost HTTP, and displays the CashPilot web UI in a native window. The sidecar is the same codebase as the [CashPilot](https://github.com/GeiserX/CashPilot) Docker container, packaged with PyInstaller for standalone execution.

## Development

### Prerequisites

- [Rust](https://rustup.rs/) (stable)
- [Node.js](https://nodejs.org/) 20+
- [Python](https://python.org/) 3.12+ (for sidecar development)
- [CashPilot](https://github.com/GeiserX/CashPilot) repo cloned alongside this one

### Dev Workflow

```bash
# Terminal 1: Start the Python backend
make dev-backend

# Terminal 2: Start the Tauri app (hot-reload on Rust changes)
make dev-tauri
```

The dev backend runs on port 8765. The Tauri app in dev mode automatically connects to it without needing a PyInstaller build.

### Build from Source

```bash
git clone https://github.com/GeiserX/CashPilot-Desktop.git
cd CashPilot-Desktop
npm install
npx tauri build
```

### Running Tests

```bash
make test
```

## FAQ

**How is this different from the CashPilot Docker container?**

It's the same CashPilot backend, but packaged as a desktop app instead of a Docker container. You get system tray integration, auto-updates, a guided Docker installation wizard, and a native window -- no need to manage Docker yourself or access a web UI via browser.

**Do I still need Docker installed?**

Yes. CashPilot Desktop manages Docker containers for you, but Docker itself must be installed. The setup wizard detects if Docker is missing and guides you through installing Docker Desktop (macOS/Windows) or Docker Engine (Linux).

**How much can I earn?**

A single residential machine running 10-15 services typically earns **$30-$100/month**. Earnings depend on location, network speed, uptime, and which services you choose. The dashboard tracks your actual earnings over time.

**Is it safe?**

All service containers run isolated via Docker. Credentials are encrypted at rest in a local SQLite database. The app communicates only with localhost (the sidecar) and the services you choose to deploy. No telemetry, no analytics, no data leaves your machine unless a service requires it.

**What happens if the app crashes?**

The sidecar has a built-in watchdog that auto-restarts up to 3 times if it dies unexpectedly. Docker containers continue running independently -- they don't stop when CashPilot Desktop is closed. Reopening the app reconnects to your running containers.

**Can I run CashPilot Desktop on multiple machines?**

Yes. Use **Worker Node** mode on additional machines -- they connect to your main CashPilot instance (either Desktop or Docker) and appear in the fleet dashboard. Each worker runs its own set of services and reports status back.

**How do auto-updates work?**

The app checks for updates on launch using Tauri's built-in updater. Updates are signed with ed25519 keys and verified before installation. You can disable auto-updates in settings.

## Disclosure

> This project contains affiliate/referral links in the service catalog. If you sign up through these links, the project maintainer may earn a small commission at no extra cost to you. This helps support development. You can replace referral codes with your own in the Settings page.

## Ecosystem

| Project | Type | Description |
|---------|------|-------------|
| [CashPilot](https://github.com/GeiserX/CashPilot) | Backend | Multi-service passive income aggregator and fleet manager |
| [CashPilot-Desktop](https://github.com/GeiserX/CashPilot-Desktop) | Desktop App | Native desktop dashboard (this repo) |
| [CashPilot-android](https://github.com/GeiserX/CashPilot-android) | Android Agent | Monitoring agent for passive income apps on Android |
| [cashpilot-mcp](https://github.com/GeiserX/cashpilot-mcp) | MCP Server | Monitor earnings from AI assistants via Model Context Protocol |
| [cashpilot-ha](https://github.com/GeiserX/cashpilot-ha) | Home Assistant | Earnings and service status sensors for your smart home |
| [n8n-nodes-cashpilot](https://github.com/GeiserX/n8n-nodes-cashpilot) | n8n Node | Automate earnings workflows in n8n |

## License

[GPL-3.0](LICENSE) -- Sergio Fernandez, 2026
