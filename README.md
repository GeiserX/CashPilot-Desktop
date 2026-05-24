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

CashPilot Desktop is a native desktop application for managing your [CashPilot](https://github.com/GeiserX/CashPilot) passive income fleet. It can run as a **full dashboard** (managing services, tracking earnings, and connecting workers) or as a **worker node** (reporting back to an existing CashPilot instance). Built with [Tauri v2](https://tauri.app) for a lightweight, cross-platform experience with native performance.

## Features

- **Dual mode** -- Full CashPilot dashboard or lightweight Worker Node
- **System tray** -- Runs quietly in the background with quick-access status
- **Real-time monitoring** -- Live earnings, service status, and node health
- **Multi-node fleet** -- Aggregate view across your entire CashPilot fleet
- **Auto-updater** -- Seamless in-app updates with ed25519 signature verification
- **Cross-platform** -- Native builds for macOS (ARM64), Windows, and Linux
- **Lightweight** -- ~5 MB installer, minimal resource usage thanks to Tauri

## Installation

Download the latest release for your platform:

| Platform | Download |
|----------|----------|
| macOS (Apple Silicon) | [`.dmg`](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) |
| Windows | [`.exe` / `.msi`](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) |
| Linux (Debian/Ubuntu) | [`.deb`](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) |
| Linux (AppImage) | [`.AppImage`](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) |

After installation, the app auto-updates itself when new versions are available.

## Quick Start

1. Download and install CashPilot Desktop for your platform
2. Launch the app -- the setup wizard will guide you
3. Choose **CashPilot** mode (full dashboard) or **Worker Node** mode
4. If Worker Node, enter your CashPilot instance address and fleet key
5. Monitor your earnings from the system tray

## Supported Services

CashPilot manages 40+ passive income services across multiple categories. Below is the full catalog.

### Docker-Deployable Services

Services CashPilot can deploy and manage automatically via Docker containers.

| Service | Residential IP | VPS IP | Devices / Acct | Devices / IP | Payout |
|---------|:-:|:-:|:-:|:-:|--------|
| [Anyone Protocol](https://anyone.io) | ✅ | ✅ | Unlimited | 1 | Crypto (ANYONE) |
| [Bitping](https://app.bitping.com) | ✅ | ✅ | Unlimited | 1 | Crypto (SOL) |
| [Earn.fm](https://earn.fm) | ✅ | ✅ | Unlimited | 1 | Crypto |
| [EarnApp](https://earnapp.com) | ✅ | ❌ | 15 | 1 | PayPal, Gift Cards, Wise |
| [Honeygain](https://dashboard.honeygain.com) | ✅ | ❌ | 10 | 1 | PayPal, Crypto |
| [IPRoyal Pawns](https://pawns.app) | ✅ | ❌ | Unlimited | 1 | PayPal, Crypto, Bank Transfer |
| [MystNodes](https://mystnodes.co) | ✅ | ✅ | Unlimited | Unlimited | Crypto (MYST) |
| [PacketStream](https://packetstream.io) | ✅ | ❌ | Unlimited | 1 | PayPal |
| [Presearch](https://presearch.com) | ✅ | ✅ | Unlimited | 1 | Crypto (PRE) |
| [ProxyBase](https://peer.proxybase.org) | ✅ | ❌ | Unlimited | 1 | Crypto |
| [ProxyLite](https://proxylite.ru) | ✅ | ✅ | Unlimited | 1 | Crypto, PayPal |
| [ProxyRack](https://peer.proxyrack.com) | ✅ | ✅ | 500 | 1 | PayPal, Crypto |
| [Repocket](https://repocket.com) | ✅ | ❌ | 5 | 2 | PayPal, Crypto |
| [Storj](https://www.storj.io/node) | ✅ | ✅ | Unlimited | 1 | Crypto (STORJ) |
| [Traffmonetizer](https://traffmonetizer.com) | ✅ | ✅ | Unlimited | Unlimited | Crypto (USDT), PayPal |
| [URnetwork](https://ur.io) | ✅ | ✅ | Unlimited | 1 | Crypto |

### Browser Extension / Desktop Only

These services have no Docker image. CashPilot lists them in the catalog with signup links and earning estimates.

| Service | Residential IP | VPS IP | Devices / Acct | Devices / IP | Payout |
|---------|:-:|:-:|:-:|:-:|--------|
| [Bytelixir](https://bytelixir.com) | ✅ | ❌ | Unlimited | 1 | Crypto |
| [Dawn Internet](https://dawninternet.com) | ✅ | ❌ | Unlimited | 1 | Crypto (DAWN) |
| [Deeper Network](https://deeper.network) | ✅ | ❌ | Unlimited | 1 | Crypto (DPR) |
| [Ebesucher](https://www.ebesucher.com) | ✅ | ✅ | Unlimited | 1 | PayPal |
| [Gradient Network](https://app.gradient.network) | ✅ | ❌ | Unlimited | 1 | Crypto (GRADIENT) |
| [Grass](https://app.grass.io) | ✅ | ❌ | Unlimited | 1 | Crypto (GRASS) |
| [Helium](https://helium.com) | ✅ | ❌ | Unlimited | 1 | Crypto (HNT) |
| [Nodepay](https://app.nodepay.ai) | ✅ | ❌ | Unlimited | 1 | Crypto (NC) |
| [Nodle](https://nodle.com) | ✅ | ✅ | Unlimited | 1 | Crypto (NODL) |
| [PassiveApp](https://passiveapp.com) | ✅ | ❌ | Unlimited | 1 | Crypto, PayPal |
| [Sentinel dVPN](https://sentinel.co) | ✅ | ✅ | Unlimited | 1 | Crypto (DVPN) |
| [Spide](https://spide.network) | ✅ | ❌ | Unlimited | 1 | Crypto |
| [Teneo Protocol](https://dashboard.teneo.pro) | ✅ | ❌ | Unlimited | 1 | Crypto (TENEO) |
| [Theta Edge Node](https://thetatoken.org) | ✅ | ✅ | Unlimited | 1 | Crypto (TFUEL) |
| [Titan Network](https://edge.titannet.info) | ✅ | ❌ | Unlimited | 1 | Crypto (TNT) |
| [Uprock](https://link.uprock.com) | ✅ | ❌ | Unlimited | 1 | Crypto |

### GPU Compute

GPU-intensive computing services. Requires compatible hardware.

| Service | Residential IP | GPU Required | Min Storage | Payout |
|---------|:-:|:-:|:-:|--------|
| [Flux](https://runonflux.io) | ✅ | ❌ | 220GB | Crypto (FLUX) |
| [Golem Network](https://golem.network) | ✅ | ❌ | 20GB | Crypto (GLM) |
| [io.net](https://io.net) | ✅ | ✅ | N/A | Crypto (IO) |
| [Nosana](https://nosana.io) | ✅ | ✅ | 50GB | Crypto (NOS) |
| [Salad](https://salad.io) | ✅ | ✅ | N/A | PayPal, Gift Cards |
| [Vast.ai](https://cloud.vast.ai) | ✅ | ✅ | 100GB | Crypto, Bank Transfer |

## Architecture

```
CashPilot Desktop (Tauri v2)
├── Frontend (HTML/CSS/JS) — setup wizard, status popup
├── Rust Core — system tray, auto-updater, IPC, Docker management
└── Python Sidecar (PyInstaller) — CashPilot backend communication
    └── Connects to CashPilot API for earnings & fleet data
```

## Development

### Prerequisites

- [Rust](https://rustup.rs/) (stable)
- [Node.js](https://nodejs.org/) 20+
- [Python](https://python.org/) 3.12+ (for sidecar development)

### Build from Source

```bash
git clone https://github.com/GeiserX/CashPilot-Desktop.git
cd CashPilot-Desktop
npm install
npx tauri dev
```

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

[GPL-3.0](LICENSE)
