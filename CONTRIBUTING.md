# Contributing to CashPilot Desktop

Thanks for your interest in improving CashPilot Desktop! This is a cross-platform
desktop app ([Wails](https://wails.io) + Go + vanilla TypeScript) for deploying and
monitoring passive-income and DePIN services. This guide gets you from a clone to a
merged pull request.

For a map of how the app is put together, read [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
first — it explains the backend packages, the collector dispatch pattern, the data
model, and the earnings/FX pipeline.

## Prerequisites

- **Go 1.26+**
- **Node.js 20+** and npm (for the Vite frontend)
- **Wails CLI v2**: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- A container runtime for manual testing of deploys: **Docker** or **Podman**
  (the app talks to either through the Docker-compatible API).

Check your environment with `wails doctor`.

## Getting started

```bash
git clone https://github.com/GeiserX/CashPilot-Desktop.git
cd CashPilot-Desktop
wails dev            # hot-reload dev build (Go backend + Vite frontend)
```

`wails dev` also serves the app at <http://localhost:34115> so you can open it in a
normal browser with devtools while the Go backend runs live.

Useful commands:

```bash
wails build                              # production build into build/bin
go test -race ./...                      # run the Go test suite (as CI does)
go test -race -coverprofile=coverage.out ./... && go tool cover -html=coverage.out
npm --prefix frontend run build          # tsc typecheck + Vite production build
```

## Project layout

| Path | What lives there |
|------|------------------|
| `main.go`, `app.go` | Wails bootstrap and the bound `App` (the frontend API, scheduler, earnings summary) |
| `internal/catalog` | Loads the `services/**/*.yml` catalog |
| `internal/collectors` | Per-service earnings collectors + the `collectorDispatch` map |
| `internal/store` | SQLite persistence + AES-256-GCM credential encryption |
| `internal/config` | App config + OS-keychain master key |
| `internal/exchange` | Crypto/fiat FX rate cache used by the earnings summary |
| `internal/runtime` | Docker/Podman container abstraction |
| `internal/services` | Deployment lifecycle orchestration |
| `fleet_server.go` | Loopback worker/mobile heartbeat API |
| `services/` | The service catalog (one YAML per provider) |
| `frontend/src/main.ts` | The whole vanilla-TS SPA |

## How to make common changes

### Add or update a service in the catalog

1. Add `services/<category>/<slug>.yml` (categories: `bandwidth`, `depin`, `storage`,
   `compute`). Copy an existing entry for the shape; `services/_schema.yml` documents
   the fields.
2. **Pin the container image to an immutable digest** — `image: repo/name:tag@sha256:…`,
   never a bare tag and never `:latest`. This is enforced by a fail-closed test
   (`internal/catalog/image_pin_test.go`); an unpinned live image fails CI. Services
   with no `docker.image` are treated as *manual-only* (tracked, never containerized).
3. If the service exposes an API/dashboard you can read a balance from, add a collector
   (below) so its earnings refresh automatically.

### Add an earnings collector

1. Implement `func (r *Registry) collect<Name>(ctx, creds) (Result, error)` in
   `internal/collectors/collectors.go`. Return a balance in the provider's **native
   currency** (USD, a crypto token, or reward points) — do not convert to fiat; the
   exchange layer does that at read time.
2. Register it in the `collectorDispatch` map (the single source of truth for
   `Supports`/`Collect`).
3. Route all HTTP through the shared `doJSON`/`doRaw` helpers.
4. The parity test (`TestCatalogCollectorParity`) fails if an automatable catalog
   service has neither a collector nor an entry in the `knownUnported` allowlist —
   add your collector (preferred) or, if intentionally deferred, the allowlist entry.
5. Add a test. HTTP collectors can be tested offline against a stub `http.RoundTripper`
   — see `internal/collectors/collectors_http_test.go` for the pattern.

### Change the database schema

Migrations are **forward-only**: extend the idempotent `CREATE TABLE IF NOT EXISTS`
batch in `internal/store/store.go:migrate`. There are no down migrations.

### Change the frontend

The SPA is a single vanilla-TypeScript file (`frontend/src/main.ts`) that builds HTML
strings and assigns them to `innerHTML`. Always run interpolated values through
`escapeHtml`. **Never hand-edit `frontend/wailsjs/`** — those bindings are generated
from the Go types; change the Go signature/struct and rebuild.

## Coding standards

- **Go**: `gofmt`/`go vet` clean. Match the surrounding style; keep changes surgical.
- **TypeScript**: the build runs `tsc` in strict mode — no type errors.
- **Never hardcode credentials or secrets.** Credentials are encrypted at rest; the
  master key lives in the OS keychain (`internal/config`).
- Prefer the simplest change that fully solves the problem.

## Commits and pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`,
  `docs:`, `test:`, `chore:`, `refactor:` … Scope where useful, e.g. `fix(scheduler): …`.
- Keep each PR focused. Open it against `main`.
- **Every PR must pass CI** (`.github/workflows/ci.yml`): `go build`, `go vet`, and
  `go test -race` with coverage. The parity and image-pin gates run here too.
- Add tests for new behavior and bug fixes.
- PRs are reviewed (CodeRabbit runs automatically); please address its findings.
- Do not commit generated artifacts or secrets.

## Reporting bugs and requesting features

Open a GitHub issue with clear reproduction steps (OS, runtime, what you expected vs.
what happened). For anything security-sensitive, follow [`SECURITY.md`](SECURITY.md)
instead of filing a public issue.

## Affiliate links

Some service definitions include the maintainer's referral/affiliate codes. This helps
support development at no cost to you. You are free to fork and substitute your own.

## License

By contributing, you agree that your contributions are licensed under the project's
**GPL-3.0** license (see [`LICENSE`](LICENSE)).
