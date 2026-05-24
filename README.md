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

CashPilot Desktop is a native desktop application for monitoring your [CashPilot](https://github.com/GeiserX/CashPilot) passive income fleet. It connects to your CashPilot backend and provides real-time earnings visibility, service health monitoring, and historical analytics — all from your system tray.

Built with [Tauri v2](https://tauri.app) for a lightweight, cross-platform experience with native performance.

## Features

- **Real-time dashboard** — Live earnings, service status, and node health at a glance
- **System tray** — Runs quietly in the background with quick-access menu
- **Multi-node monitoring** — Aggregate view across your entire CashPilot fleet
- **Auto-updater** — Seamless in-app updates with ed25519 signature verification
- **Cross-platform** — Native builds for macOS (ARM64 + Intel), Windows, and Linux
- **Lightweight** — ~5 MB installer, minimal resource usage thanks to Tauri

## Installation

Download the latest release for your platform:

| Platform | Download |
|----------|----------|
| macOS (Apple Silicon) | [`.dmg`](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) |
| macOS (Intel) | [`.dmg`](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) |
| Windows | [`.exe`](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) |
| Linux (Debian/Ubuntu) | [`.deb`](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) |

After installation, the app auto-updates itself when new versions are available.

## Quick Start

1. Install [CashPilot](https://github.com/GeiserX/CashPilot) on your server(s)
2. Download and install CashPilot Desktop for your platform
3. Connect to your CashPilot instance
4. Monitor your earnings from the system tray

## Architecture

```
CashPilot Desktop (Tauri v2)
├── Frontend (HTML/CSS/JS)
├── Rust Core (system tray, updater, IPC)
└── Python Sidecar (CashPilot backend communication)
    └── Connects to CashPilot API
```

The application bundles a Python sidecar binary (built with PyInstaller) that handles all communication with the CashPilot backend, keeping the frontend lightweight and responsive.

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
