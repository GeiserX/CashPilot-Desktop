# Getting started

CashPilot Desktop is a local-first, cross-platform desktop application for deploying and monitoring passive-income and DePIN services. Instead of running CashPilot as a Docker container and accessing it via browser, CashPilot Desktop bundles everything into a single installable app with system tray integration and a guided setup wizard.

It runs in one of two modes, and the first run asks which:

- **Run everything here** -- this computer collects your earnings, shows the dashboard and runs its earning apps. No CashPilot server is needed. Pick this if you do not run CashPilot anywhere else.
- **Join my CashPilot** -- this computer runs earning apps for a CashPilot server you already have, and that server collects and shows the earnings. Pick this if you already run the CashPilot dashboard on a server or another computer.

Built with [Wails](https://wails.io) (Go + vanilla TypeScript) for a lightweight, cross-platform experience with native performance.

## Download

Download the latest release for your platform:

| Platform | File on the [latest release](https://github.com/GeiserX/CashPilot-Desktop/releases/latest) | How to open it |
|----------|------|-------|
| macOS (Apple Silicon) | `CashPilot-Desktop-darwin-arm64.zip` | Unzip it and double-click `cashpilot-desktop.app`. It is signed and notarized, so Gatekeeper lets it open; macOS only asks once to confirm an app downloaded from the internet |
| Windows (x64) | `CashPilot-Desktop-windows-amd64.exe` | The app itself, not an installer; run it. It is unsigned unless a signing certificate is configured in CI, so SmartScreen may warn |
| Linux (x64) | `CashPilot-Desktop-linux-amd64.tar.gz` | Extract it and run `CashPilot Desktop`; it needs GTK 3 and WebKitGTK 4.1 |

## System requirements

| Requirement | Minimum |
|-------------|---------|
| **Docker** | Docker Desktop (macOS/Windows) or Docker Engine / Podman (Linux) |
| **RAM** | 4 GB (8 GB recommended for multiple services) |
| **Disk** | 2 GB free (more for service containers) |
| **Network** | Residential IP recommended for most services |

## Quick start

1. **Download and open** CashPilot Desktop for your platform (table above)
2. **Launch the app** -- the setup wizard detects Docker/Podman and guides you through installation if needed
3. **Choose how it runs** -- Run everything here, or Join my CashPilot
4. **If you join** -- on your CashPilot, open Fleet, then Add a box, press Reveal API Key, then Copy, and paste the block into the app (or type the address and key). The app sends one heartbeat and saves the link only if your CashPilot accepts it. The computer then appears on your CashPilot's Fleet page
5. **Start earning** -- browse the service catalog, deploy containers, and monitor earnings from the system tray

## CashPilot Desktop vs Web

| Feature | Desktop App | Web (Docker) |
|---------|:-----------:|:------------:|
| Installation | Download and open the app | `docker compose up -d` |
| Docker management | Built-in (auto-detects, guides install) | Requires Docker pre-installed |
| System tray integration | macOS only | No |
| Auto-updates | Planned | Manual image pull |
| Background operation | Native OS service | Container must stay running |
| Fleet management | **Yes** | **Yes** |
| Earnings dashboard | **Yes** | **Yes** |
| Target audience | End users, non-technical | Self-hosters, sysadmins |
| Resource usage | ~80 MB RAM | ~80 MB RAM (container only) |
