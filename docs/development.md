# Development

## Prerequisites

- [Go](https://go.dev/) 1.26.x
- [Node.js](https://nodejs.org/) 26+
- [Wails CLI](https://wails.io/docs/gettingstarted/installation) v2

## Dev Workflow

```bash
wails dev              # Hot-reload dev mode (Go + TypeScript)
go test -race ./...    # Run Go tests
```

## Build from Source

```bash
git clone https://github.com/GeiserX/CashPilot-Desktop.git
cd CashPilot-Desktop
wails build
```

## Running Tests

```bash
make test
```

## Architecture overview

The full design, grounded in the code, is in [ARCHITECTURE.md](ARCHITECTURE.md).

```
CashPilot Desktop (Wails 2.x)
├── Go Backend (app.go, internal/)
│   ├── Container runtime    — Docker/Podman detection, deploy/stop/restart
│   ├── Earnings collectors  — Polls service APIs for earnings
│   ├── internal/exchange    — FX rates (crypto + fiat → display currency)
│   ├── Fleet management     — Multi-node coordination via HTTP
│   ├── fleet_server.go      — Token-auth worker/mobile heartbeat API
│   └── SQLite database      — Config, credentials (OS keychain), earnings history
├── Frontend (vanilla TypeScript + Vite)
│   ├── Dashboard            — Real-time earnings and service status
│   ├── Setup wizard         — Onboarding flow with runtime detection
│   ├── Service catalog      — Browse and deploy services
│   ├── Settings             — Display currency and preferences
│   └── Fleet                — Connected worker/node status
└── Wails Runtime            — Window management, system tray, native bindings
```

The Go backend handles all business logic, container orchestration, and data collection. The TypeScript frontend communicates via Wails bindings (direct Go function calls, no HTTP). State is persisted in a local SQLite database, with credentials encrypted at rest (AES-256-GCM) under a master key held in the OS keychain.
