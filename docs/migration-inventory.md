# Wails Migration Inventory

## Keep

- `docs/banner.svg` and `docs/mockups/onboarding-reference.svg`: visual identity and onboarding direction.
- `.github` issue/PR templates, funding metadata, dependabot settings.
- `LICENSE`.
- Service catalog semantics from `CashPilot/services`, initially vendored as a subset under `services/`.

## Replace

- Tauri shell under `src-tauri/` with Wails 2.x files: `go.mod`, `main.go`, `app.go`, `wails.json`.
- Python sidecar under `sidecar/` with native Go packages in `internal/`.
- Static Tauri frontend under `src/` with the Vite frontend under `frontend/`.
- Tauri release workflow with Wails build workflow.
- Root `package-lock.json` with `frontend/package-lock.json`.

## Defer

- Full catalog sync automation from the server repo.
- Auto-update manifest/signature parity.
- Production notarization and Windows EV/OV signing hardening.
- Managed VM appliance bundling.
- Advanced collector coverage beyond Earn.fm and Honeygain.
- System tray parity after the Wails MVP is stable.
