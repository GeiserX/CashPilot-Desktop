# Getting started

CashPilot Desktop is a local-first, cross-platform desktop application for deploying and monitoring passive-income and DePIN services. Instead of running CashPilot as a Docker container and accessing it via browser, CashPilot Desktop bundles everything into a single installable app with system tray integration and a guided setup wizard.

It runs in one of two modes, and the first run asks which:

- **Run everything here** -- this computer collects your earnings, shows the dashboard and runs its earning apps. No CashPilot server is needed. Pick this if you do not run CashPilot anywhere else.
- **Join my CashPilot** -- this computer runs earning apps for a CashPilot server you already have, and that server collects and shows the earnings. Pick this if you already run the CashPilot dashboard on a server or another computer.

Built with [Wails](https://wails.io) (Go + vanilla TypeScript) for a lightweight, cross-platform experience with native performance.

## Download

Download the latest release for your system from https://github.com/GeiserX/CashPilot-Desktop/releases/latest

| Download | File | How to open it |
|----------|------|-------|
| **CashPilot Desktop for Windows**, installer (x64) | `CashPilot-Desktop-windows-amd64-setup.exe` | Run it. It installs into your own account, with no admin prompt, and adds a Start menu shortcut, a desktop shortcut and an entry in Settings > Apps > Installed apps. It is not code-signed yet, so SmartScreen may warn: click More info, then Run anyway |
| **CashPilot Desktop for Windows**, portable (x64) | `CashPilot-Desktop-windows-amd64.exe` | The app itself; run it from any folder. It installs nothing and has no uninstaller. The same SmartScreen warning applies |
| **CashPilot Desktop for macOS** (Apple silicon) | `CashPilot-Desktop-darwin-arm64.zip` | Unzip it and double-click `cashpilot-desktop.app`. It is signed and notarized, so Gatekeeper lets it open; macOS only asks once to confirm an app downloaded from the internet |
| **CashPilot Desktop for Linux** (x64) | `CashPilot-Desktop-linux-amd64.tar.gz` | Extract it and run `CashPilot Desktop`; it needs GTK 3 and WebKitGTK 4.1 |

### Windows: installer or portable

Both copies keep their data in the same folder, `%APPDATA%\CashPilot Desktop`. So you can move from the portable exe to the installer, or back, and keep your settings, earnings history and saved logins.

The background helper (Settings > Background Earning) starts the copy you opened last. When you open the installed app after the portable exe, the helper moves to the installed app, and the other way round. You can delete the portable exe once you have opened the installed app.

### Uninstall on Windows

Open Settings > Apps > Installed apps, find CashPilot Desktop and click Uninstall. The uninstaller always removes the app, its shortcuts, its entry in Installed apps, the background helper's scheduled task and the reminder that reopens the Docker setup after a restart. It asks about two more things, and both start unticked:

- **Stop and remove the earning apps CashPilot Desktop started.** This removes only the containers CashPilot Desktop made, and its native earning apps with their Windows Firewall rules. It never touches other containers, such as those of a CashPilot worker. Docker volumes stay. Unticked, the containers keep running in Docker.
- **Delete my CashPilot data.** This deletes the settings, the earnings database, the logs and the saved logins, with their keys in the Windows Credential Manager. Unticked, they stay, and a new install or the portable exe finds them again.

The uninstaller does not remove the Secure VM (Windows 11 Pro); it says so when one exists. For a silent uninstall, run `uninstall.exe /S` from the install folder, `%LOCALAPPDATA%\Programs\CashPilot Desktop`. Add `/REMOVEEARNERS` and `/DELETEDATA` to answer yes to the two questions.

The portable exe has no uninstaller. To remove it, turn off Background Earning in Settings, then delete the exe.

## System requirements

| Requirement | Minimum |
|-------------|---------|
| **Docker** | Docker Desktop (macOS/Windows) or Docker Engine / Podman (Linux) |
| **RAM** | 4 GB (8 GB recommended for multiple services) |
| **Disk** | 2 GB free (more for service containers) |
| **Network** | Residential IP recommended for most services |

## Quick start

1. **Download and open** CashPilot Desktop for your platform (table above)
2. **Choose how it runs** -- after Welcome, pick Run everything here, or Join my CashPilot
3. **If you join** -- on your CashPilot, open Fleet, then Add a box, press Reveal API Key, then Copy, and paste the block into the app (or type the address and key). The app sends one heartbeat and saves the link only if your CashPilot accepts it. The computer then appears on your CashPilot's Fleet page
4. **Set up the runtime** -- the setup wizard detects Docker/Podman and guides you through installation if needed. On Windows it shows a checklist of what Docker Desktop needs ([Windows setup](windows-setup.md))
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
