# Installation

CashPilot Desktop is a local-first, cross-platform desktop application for deploying and monitoring passive-income and DePIN services. Instead of running CashPilot as a Docker container and accessing it via browser, CashPilot Desktop bundles everything into a single installable app with system tray integration and a guided setup wizard.

It can run in two modes:

- **CashPilot mode** -- Full dashboard with service management, earnings tracking, container deployment, and fleet orchestration
- **Worker Node mode** -- Lightweight agent that connects to an existing CashPilot instance to run services on this machine

Built with [Wails](https://wails.io) (Go + vanilla TypeScript) for a lightweight, cross-platform experience with native performance.

## Download

Download the latest release for your platform:

| Platform | Format | Download | Notes |
|----------|--------|----------|-------|
| macOS (Apple Silicon) | `.dmg` | [Download](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) | Unsigned (right-click → Open to bypass Gatekeeper) |
| Windows (x64) | `.exe` (NSIS) | [Download](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) | Unsigned unless a signing cert is configured in CI |
| Linux (Debian/Ubuntu) | `.deb` | [Download](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) | Raw binary (.deb packaging planned) |

## System requirements

| Requirement | Minimum |
|-------------|---------|
| **Docker** | Docker Desktop (macOS/Windows) or Docker Engine / Podman (Linux) |
| **RAM** | 4 GB (8 GB recommended for multiple services) |
| **Disk** | 2 GB free (more for service containers) |
| **Network** | Residential IP recommended for most services |

## Quick start

1. **Download and install** CashPilot Desktop for your platform
2. **Launch the app** -- the setup wizard detects Docker/Podman and guides you through installation if needed
3. **Choose your mode** -- CashPilot (full dashboard) or Worker Node (connect to existing instance)
4. **If Worker Node** -- enter your CashPilot instance address and fleet key
5. **Start earning** -- browse the service catalog, deploy containers, and monitor earnings from the system tray

## CashPilot Desktop vs Web

| Feature | Desktop App | Web (Docker) |
|---------|:-----------:|:------------:|
| Installation | One-click installer | `docker compose up -d` |
| Docker management | Built-in (auto-detects, guides install) | Requires Docker pre-installed |
| System tray integration | macOS only | No |
| Auto-updates | Planned | Manual image pull |
| Background operation | Native OS service | Container must stay running |
| Fleet management | **Yes** | **Yes** |
| Earnings dashboard | **Yes** | **Yes** |
| Target audience | End users, non-technical | Self-hosters, sysadmins |
| Resource usage | ~80 MB RAM | ~80 MB RAM (container only) |
