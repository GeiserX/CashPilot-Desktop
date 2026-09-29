# CashPilot Desktop Edge-Case Audit

Date: 2026-05-25

## Scope

This pass compared the Wails desktop app against the original CashPilot web app workflows for:

- onboarding and runtime readiness
- dashboard navigation and empty states
- setup wizard and catalog browsing
- service lifecycle actions
- logs, collectors, and credentials
- destructive actions, especially Docker volumes
- macOS packaging, dock icon, tray icon, and window recovery

## Findings Fixed In This Pass

### macOS app identity and icon confusion

The built app used Wails' default `com.wails.<name>` bundle identifier, which makes LaunchServices and Dock icon caching easy to confuse across old builds. The app now ships explicit macOS bundle identifiers:

- production: `com.geiserx.cashpilot.desktop`
- dev: `com.geiserx.cashpilot.desktop.dev`

The app bundle was verified to contain `Contents/Resources/iconfile.icns` and `CFBundleIconFile=iconfile`.

### Window can launch off-screen

After previous monitor/layout changes, the built app process was running but its only window was at an off-screen coordinate (`2136,-1048`). Wails' own centering still centered relative to that stale display space. The app now uses a small native macOS recovery helper on DOM ready to move its window onto the visible screen containing global origin, then activates it.

### Menubar tray icon installation timing

The tray icon was installed during startup, before the UI was reliably ready. Installation now happens on DOM ready. The native status item is reused instead of recreated, uses a template image, and includes a visible sun fallback label.

### Sidebar branding

The desktop sidebar had a hand-built CSS sun. It now uses the same SVG mark as the original CashPilot logo instead of reinventing the asset.

### Delete did not clean Docker volumes

Desktop removal previously called Docker with `RemoveVolumes: false`, so anonymous volumes were left behind and named volumes were never cleaned. The original app has an explicit `delete_volumes` removal path. Desktop removal now:

- verifies the target container is CashPilot-managed
- collects Docker volume mounts from the container
- removes the container with anonymous volume cleanup enabled
- removes named Docker volumes attached to that managed container
- leaves host bind mounts alone

Redeploy still preserves volumes, which is the right behavior for stateful services.

### Stale deployments after manual Docker deletion

Refreshing deployments upserted containers that still existed, but did not remove store rows for containers deleted outside CashPilot. Refresh now removes stale deployment records when the runtime is reachable and the managed container is gone.

### Missing Start action

The original app exposes start/stop/restart paths. Desktop only had stop/restart. Desktop now has `StartService` in the Go backend and shows `Start` for non-running deployed services.

### Destructive actions lacked clear confirmation

Removing a service now prompts and explicitly says it deletes the managed container and Docker volumes while leaving host bind-mount folders untouched. Redeploying an already-deployed service now prompts and says the container will be replaced while volumes are kept.

## Remaining Workflow Gaps

These are still not fully equivalent to the original web app:

- Dashboard table does not yet have sortable columns, health score, payout progress, claim modal, credential update modal, or expandable multi-instance rows.
- Logs are still dumped into a page output block instead of a modal/panel with polling and close behavior.
- Credentials can be saved and overwritten, but there is no first-class "clear credentials" action in the desktop UI.
- Earnings summary is still simple latest-balance aggregation; it does not yet match the original app's today/month deltas, exchange-rate conversion, promo offset notes, or native currency display.
- Catalog cards are closer to the original but still lack all original badges and rich provider metadata.
- Manual-only services can be tracked/collected, but their UX is not separated enough from Docker-managed services.
- Runtime support still primarily assumes Docker API compatibility; Podman-specific socket/context discovery should get targeted testing.

## Regression Matrix To Keep

- Fresh launch after monitor changes: app window visible on the current screen.
- Installed app identity: bundle ID, product version, iconfile, and app name all match.
- Menubar: sun status item appears, opens a CashPilot menu, and Quit works.
- Onboarding runtime guides: links open externally; macOS guides do not mention WSL2.
- Dashboard empty state: no services, no earnings, runtime ready/offline states.
- Catalog: category filters, search, signup links, and setup buttons.
- Wizard: cannot advance without categories/services; credentials hydrate after save; manual-only deploy stays disabled.
- Deploy: required fields block deploy; redeploy prompts; volumes are preserved on redeploy.
- Stop/start/restart: state updates and refresh reconciles with Docker.
- Logs: missing container errors are surfaced; long logs remain scrollable.
- Remove: confirmation appears; managed container is removed; Docker volumes are removed; bind-mount folders remain.
- External deletion: deleting a managed container in Docker and refreshing removes stale desktop state.
- Collectors: missing credentials report actionable errors and do not poison balances.

## Verification Run

- `go test ./...`
- `npm run build` in `frontend/`
- `wails build -clean`
- Built app `Info.plist` inspected for `com.geiserx.cashpilot.desktop` and `CFBundleIconFile=iconfile`
- Built app launched and native window position verified at `80,113`
