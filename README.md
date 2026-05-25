# CashPilot Desktop

CashPilot Desktop is a local-first desktop manager for passive-income and DePIN services. The app is being migrated to **Wails 2.x + Go** so the desktop product can manage Docker-compatible runtimes directly instead of wrapping the official CashPilot FastAPI app as a Python sidecar.

## Current Scope

This branch implements the first Wails milestone:

- Wails 2.x desktop shell with a Go backend.
- Local SQLite state for encrypted credentials, deployments, earnings, and runtime events.
- Vendored CashPilot service catalog subset with a loader that can also read the sibling `../CashPilot/services` tree during development.
- Existing Docker-compatible runtime detection.
- Guided runtime setup suggestions for Docker Desktop, Docker Engine, Colima, Lima, and Podman.
- Docker deploy/stop/restart/remove/logs for simple catalog services.
- First collector ports for Honeygain and Earn.fm.
- A synthwave onboarding/dashboard frontend that preserves the previous visual direction.

## Runtime Strategy

The first production milestone uses an external Docker-compatible runtime if one is already installed:

- Docker Desktop on macOS/Windows.
- Docker Engine on Linux.
- Colima or Lima Docker contexts.
- Podman where its Docker-compatible API works for the service.

The app explicitly guides users through those choices instead of failing with a generic “Docker missing” error.

The later managed-runtime milestone is planned as a CashPilot-controlled VM appliance:

- macOS: Lima-style VM using Apple Virtualization where possible.
- Windows: WSL2 distro appliance.
- Linux: rootless runtime first, rootful opt-in for privileged services.

Docker Desktop itself is not bundled.

## Development

Prerequisites:

- Go 1.26.x or newer.
- Node.js 26.x or newer.
- Wails 2.x: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- A Docker-compatible runtime for deploy tests.

Optional collector configuration:

- `CASHPILOT_EARNFM_SUPABASE_ANON_KEY` enables Earn.fm earnings collection without baking the platform API key into the app binary.

Commands:

```bash
make dev
make test
make build
```

## Architecture

```text
frontend/              Wails web UI
app.go                 Wails app bindings
internal/catalog       CashPilot YAML catalog loader
internal/config        App paths, config, credential key management
internal/store         SQLite schema and encrypted credential storage
internal/runtime       Runtime abstraction, existing Docker provider, install guides
internal/services      Deploy/stop/restart/remove/logs orchestration
internal/collectors    Go earnings collectors
services/              Vendored service catalog subset
```

## Security Notes

CashPilot treats third-party passive-income containers as untrusted. The external-runtime MVP cannot fully isolate them from the host runtime, so the UI highlights runtime choice and deployment state. The managed VM runtime is the long-term isolation boundary for services that need more control, privileged containers, or unusual networking.

Credentials are encrypted before being stored in SQLite. The master key is kept in the OS keychain where possible, with a local file fallback restricted to the app data directory.
