# CashPilot Desktop Roadmap

> Living document. Tracks planned features for the desktop app specifically. Server-side features live in the [CashPilot roadmap](https://github.com/GeiserX/CashPilot/blob/main/ROADMAP.md).

---

## Milestone 1 — Core Desktop Experience (in progress)

- [x] Wails 2.x shell with Go backend
- [x] Local SQLite with AES-GCM encrypted credentials (OS keychain master key)
- [x] Docker-compatible runtime detection (Docker Desktop, Engine, Colima, Lima, Podman)
- [x] Guided runtime setup suggestions
- [x] Service catalog loader (vendored subset + dev sibling tree)
- [x] Container deploy/stop/restart/remove/logs
- [x] Honeygain and Earn.fm collectors ported
- [x] Synthwave onboarding UI
- [x] All remaining collectors ported (15 total — parity with CashPilot server)
- [x] Earnings dashboard (custom inline-SVG chart)
- [x] System tray integration with status indicator (macOS native; Windows/Linux stubbed)
- [x] Settings view (display currency + preferences)
- [ ] Auto-updates (Wails native update channel)

## Milestone 2 — Earnings Intelligence

- [x] All 15 collectors ported from CashPilot server
- [x] Daily earnings chart (custom inline-SVG)
- [ ] Weekly/monthly earnings charts
- [x] Per-service progress-to-payout bars
- [x] Aggregate FX-normalized portfolio view (total / today / month + change-%)
- [x] Multi-currency display (44 currencies)
- [ ] **Auto-claim daily rewards** — automated daily reward collection for services that support it (Honeygain lucky pot, Grass daily check-in, etc.); per-service opt-in with schedule configuration
- [ ] Collector health alerts (in-app notification when a collector fails)

## Milestone 3 — Fleet & Multi-Node

- [x] Fleet heartbeat API (token-authenticated worker/mobile heartbeats; desktop-as-master, loopback by default)
- [x] Fleet status display (nodes, services, earnings)
- [ ] Remote deploy from master to this Desktop node
- [ ] Flight sheets — pre-built service bundles deployable with one click

## Milestone 4 — Smart Features

- [ ] IP type detection (residential vs datacenter)
- [ ] Earnings estimator by location/ISP/hardware
- [ ] Resource-aware scheduling (pause during heavy usage, idle-only mode)
- [ ] Bandwidth-app detection (throttle during video calls)
- [ ] Service health scoring with death-signal monitoring

## Milestone 5 — Managed Runtime

- [ ] macOS: Lima-style VM using Apple Virtualization
- [ ] Windows: WSL2 distro appliance
- [ ] Linux: rootless runtime, rootful opt-in for privileged services
- [ ] Full container isolation without requiring Docker Desktop

## Future Ideas

- GPU compute detection and deployment (Vast.ai, Salad, Nosana)
- Storj guided setup with disk allocation UI
- DePIN browser automation (headless containers for extension-only services)
- Plugin system for community collectors
- Earnings export (CSV/JSON for tax reporting)
