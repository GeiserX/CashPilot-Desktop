# Native Supervision — Keeping Track of Native Earners (Docker-parity without Docker)

> **The question this answers:** with Docker, tracking an earner is easy — the daemon keeps containers
> running, restarts them, starts them at boot, and reports status/stats/logs. **How do you get the same for a
> native process that CashPilot runs directly?** This doc is the design, synthesized from a 3-researcher study
> (OS service managers, supervision architecture + prior art, and tracking/resource-limits). Companion:
> [`BRANCH-2-RUNTIME.md`](./BRANCH-2-RUNTIME.md) (the native-first pivot this extends).

## TL;DR — the model

1. **Hand supervision to the OS, not to a detached child.** A process started by the Wails GUI is its *child*;
   when the app closes it's orphaned (keeps running but **never restarts on crash, never starts on boot**).
   Docker-parity means an **OS-level supervisor** becomes the parent.
2. **Run one CashPilot "helper," kept alive by the OS, that supervises the earners** — the same two-level model
   as Docker (the OS keeps `dockerd` alive; `dockerd` applies per-container restart policies). The OS keeps
   **one small CashPilot process** alive; that helper keeps the earners alive.
3. **This is a *relocation*, not a rewrite.** `internal/runtime/native.go` is already a full supervisor
   (backoff restart, a persistent registry, PID-identity-guarded orphan re-adoption), and `fleet_server.go` is
   already a loopback API a thin UI queries. Option C just moves the supervisor into a helper the OS keeps
   alive.
4. **Zero extra binaries to sign** — the *same signed* CashPilot executable runs as the UI when you launch it
   and as the daemon when the OS launches it (a one-flag "dual role"). The OS only ever launches *our* signed
   binary; the unsigned third-party earners are its children.
5. **Tracking (status/CPU/mem/logs/crash-loop) is a solved, cheap, cross-platform problem.** **Hard resource
   limits split by OS** (Linux/Windows: real caps; macOS: best-effort only — say so honestly). **Per-process
   network bytes + bandwidth caps largely can't match Docker natively** — use the earner's own bandwidth setting.
6. **Signing is now doubly load-bearing:** a background-launched *downloaded* earner is blocked by Gatekeeper /
   SmartScreen unless we strip its quarantine/mark-of-the-web **and** sign our launcher. So signing isn't just
   for the installer — it's what makes the always-on background earner work at all on macOS/Windows.

---

## The architecture: one OS-kept-alive helper (Option C)

```
        ┌─ OS service manager (launchd / systemd --user / Task Scheduler) ─┐
        │  keeps ONE process alive: restart-on-crash + start-at-login       │
        └───────────────────────────────┬──────────────────────────────────┘
                                         │ launches (our SIGNED binary, --daemon)
                                ┌────────▼─────────┐        loopback API (fleet_server.go)
                                │  CashPilot HELPER │◀───────────────────────────────────┐
                                │  = native.go      │                                     │
                                │  supervisor       │  spawns + backoff-restarts          │
                                └───┬───────┬───────┘                                     │
                          child ┌───▼──┐ ┌──▼───┐ child                          ┌────────┴────────┐
                                │earner│ │earner│  … (unsigned 3rd-party bins)   │  CashPilot UI   │
                                └──────┘ └──────┘                                │ (thin dashboard)│
                                                                                 └─────────────────┘
```

- **The helper is a per-user login agent, not a root system daemon** (macOS `LaunchAgent`, ideally via
  `SMAppService` on Ventura+ — no admin, shows in Login Items; Linux `systemd --user`; Windows per-user logon
  task). No admin/elevation at install → a clean install *and uninstall*.
- **Dual-role binary:** `kardianos/service`'s `Interactive()` (false under a service manager, true when
  double-clicked) lets one executable be both. So **0 or 1 extra signed binaries**, and only *our* code is ever
  launched by the OS.
- **Why one helper beats one-service-per-earner (Option A):** one background item (not N scary "background item
  added" prompts), **one-click clean uninstall** (Option A's N leftover services is exactly the proxyware
  "impossible to remove" reputation), one status pane, one signed binary — and it reuses the tested `native.go`
  supervisor instead of throwing it away. **Every comparable app agrees:** Docker Desktop, Tailscale
  (`tailscaled` + thin GUI over a LocalAPI), Ollama, Syncthing, and the direct competitors EarnApp / Pawns /
  Grass each ship **one** background engine + a thin UI, auto-started at login — never one service per work-item.
- **The honest trade-off:** if the helper crashes, all earners pause. But the OS revives the helper in seconds
  (`KeepAlive`/`Restart=always`), the helper is the *stablest* piece (a tiny first-party babysitter), and the
  flaky third-party earners are still caught by the existing per-earner backoff. Net = Docker's own model.

---

## OS persistence — how the OS keeps the helper alive

| | macOS | Linux | Windows |
|---|---|---|---|
| **Mechanism (no admin)** | `LaunchAgent` (`SMAppService` or `~/Library/LaunchAgents/*.plist`), `KeepAlive`+`RunAtLoad` | `systemd --user` unit, `Restart=always` + `loginctl enable-linger` | per-user **Scheduled Task** (LogonTrigger, `RestartOnFailure`, `ExecutionTimeLimit=PT0S`) |
| **Survives app-close** | ✅ | ✅ | ✅ |
| **Auto-restart on crash** | ✅ `KeepAlive` (mind `ThrottleInterval`) | ✅ `Restart=always` (set `StartLimitIntervalSec=0` or crash-loops go permanently `failed`) | ✅ `RestartOnFailure` Count+Interval |
| **Starts after reboot** | at **login** | **pre-login, headless** (linger) | at **login** |
| **Needs admin?** | No | No | No |
| **Drive from Go** | template plist + `launchctl bootstrap/bootout/kickstart/print` | `systemctl --user …` (structured `show`) | generate task XML + `schtasks` |

**The one honest limit:** on macOS + Windows a *non-admin* helper starts **at login**, not before it; only Linux
(linger) runs pre-login headless without root. **This is exactly how Docker Desktop behaves** (a login item), so
it's honest parity — not a shortcut. A true pre-login/headless tier (macOS `LaunchDaemon` / Windows Service) is an
optional "advanced" install behind one admin prompt, reserved for headless-server users.

**Go:** `golang.org/x/sys/windows/svc` is the only official lib (for the admin Service tier). For the no-admin
path, **template the plist / unit / task-XML and shell out** (`launchctl` / `systemctl --user` / `schtasks`).
`kardianos/service` is a good cross-platform first cut for the *login-agent* helper on macOS/Linux; its Windows
path is admin-SCM only, so the no-admin Windows logon-task is a non-`kardianos` code path.

---

## Tracking — Docker-`ps`/`stats`/`logs` parity (the cheap, solved part)

CashPilot already has most of the machinery (a registry keyed by slug, PID-identity via exe-path+createTime,
bounded rotating logs, a health-score model). The service manager supplies the rest:

| Signal | How | Rating |
|---|---|---|
| **running / stopped / uptime / last-exit / restart-count** | Query the manager — Linux `systemctl --user show -p ActiveState,SubState,NRestarts,ExecMainStatus,ExecMainStartTimestamp,Result` (machine-readable, best); macOS `launchctl list <label>` → `PID`+`LastExitStatus`; Windows `Get-ScheduledTaskInfo` → `LastTaskResult` — plus the existing registry + gopsutil live PID for uptime | **Easy** |
| **CPU% + RSS** | gopsutil (already wired). **Fix:** the native path samples CPU once → first reading is 0/lifetime-avg; use the Docker path's two-sample-1s-apart approach | **Easy** |
| **logs** | Capture stdout/stderr to a file in the per-slug dir (reuse the existing bounded `readLogTail`) on all OSes; `journalctl --user -u <unit>` as a Linux bonus | **Easy** |
| **crash-loop + health** | Portable respawn-window counter (≥K distinct PIDs / createTime-changes in window T — the registry already tracks this), enriched on Linux by `Result=start-limit-hit`; feed the existing `runtime_events` → `store.HealthScores` vocabulary (`restarted`/`*_error`/`health_down`) — **zero schema change** | **Easy** |

---

## Resource limits — the real gap, and it's OS-divergent (be honest)

Phase 2's `applyNativeResourceLimits` is a documented no-op. There is **no portable limit knob** (that's the
point of a container). Ranked by feasibility:

| | Linux | Windows | macOS |
|---|---|---|---|
| **Hard memory cap** | ✅ cgroup `MemoryMax`/`MemoryHigh` | ✅ Job Object `ProcessMemoryLimit` | ❌ (only `RLIMIT_AS` on virtual mem — misfires for Go/Node) |
| **Hard CPU cap** | ✅ `CPUQuota=50%` | ✅ Job Object `CpuRate` hard-cap | ❌ (only `taskpolicy -b`/`Nice`/`LowPriorityIO` = priority, not a cap) |
| **Bandwidth cap** | ❌ native (tc/eBPF = hard) | ⚠️ egress-only (Job Object `MaxBandwidth`) | ❌ |
| **Verdict** | **Docker-parity, near-zero code** (render unit directives) | **Good, pure-Go** (`x/sys/windows`) | **best-effort only — label it "not a hard cap"** |

**Reconciling supervision + limits** (the one tension between the researchers): keep **one helper** (the trust/UX
win), and let the helper apply *per-earner* OS-native limits to its children — on **Linux** by placing each earner
in its own cgroup scope (`systemd-run --user --scope -p MemoryMax=… -p CPUQuota=… -p IPAccounting=yes`, which also
yields **free per-earner network bytes**); on **Windows** by owning a **Job Object per earner** (the *persistent
helper* must own it, not the GUI, or the earner dies on app-close); on **macOS** best-effort priority only. Wire
from the existing `catalog.ResourceLimits` (add a CPU field alongside `MemLimit`/`MemReservation`/`OomScoreAdj`;
reuse `parseMemoryBytes`).

---

## Honest native-vs-Docker capability matrix

| Capability | Docker | Native (this design) |
|---|---|---|
| Keep running when the app is closed | ✅ | ✅ (OS-supervised helper) |
| Auto-restart on crash | ✅ | ✅ (OS keeps helper alive; helper backoff-restarts earners) |
| Auto-start after reboot | ✅ | ✅ Linux (pre-login) / macOS+Windows (**at login** — like Docker Desktop) |
| Status / uptime / last-exit / restart-count | ✅ | ✅ (service manager + registry) |
| Per-process CPU% + memory | ✅ | ✅ (gopsutil) |
| Hard CPU + memory caps | ✅ | ✅ Linux + Windows / ⚠️ **best-effort on macOS** |
| Logs | ✅ | ✅ (file capture + tail) |
| **Per-process network bytes** | ✅ | ⚠️ Linux only (systemd IP-accounting); else **use the earner's self-reported bandwidth** |
| **Per-process bandwidth cap** | ✅ | ❌ mostly — **prefer the earner's own max-bandwidth setting** |
| No container-image download | n/a | ✅ (native binary, digest-pinned) |
| Isolation (namespaces/fs/net) | ✅ | ❌ runs on the bare OS — mitigate with OS sandboxing later; keep a Docker/VM tier for untrusted images |

**The two honest "can'ts":** per-process **network bytes** and **bandwidth caps** don't have a good cross-platform
native answer. That's fine — every consumer earner already exposes its own bandwidth setting + reports earnings via
its dashboard API (which CashPilot's collectors already read), so we surface *that* instead of trying to out-instrument
the OS.

---

## Phased implementation plan

- **Phase A — dual-role helper skeleton.** Add a `--daemon` mode to the CashPilot binary (`Interactive()` split):
  headless, runs the `native.go` supervisor + the loopback API (`fleet_server.go`), no window. UI unchanged when
  launched normally. *Behavior-preserving; nothing auto-registers yet.*
- **Phase B — OS-persistence registration.** Install/remove the helper as a per-user login agent
  (macOS LaunchAgent via `SMAppService`, Linux `systemd --user` + linger, Windows Scheduled Task), driven from Go.
  A "Keep earning in the background" toggle in Settings installs/removes it. Quarantine/MotW stripping on downloaded
  earners so the OS launch isn't Gatekeeper/SmartScreen-blocked.
- **Phase C — tracking surface.** Relocate status/stats/logs reads to go through the helper's API; fix two-sample
  CPU; add uptime/restart-count/last-exit + a crash-loop badge; feed the existing health score.
- **Phase D — resource limits.** Linux cgroup scope (`MemoryMax`/`CPUQuota` + IP-accounting) + Windows Job Object
  (helper-owned) + macOS best-effort, from `catalog.ResourceLimits`. Honest per-OS labelling in the UI.
- **Phase E — signing** (gated on the maintainer's go/no-go): sign+notarize the (dual-role) binary; this is what
  makes the background earner launch at all on macOS/Windows.
- **Cross-cutting:** clean one-click uninstall (unregister the single helper, stop all earners first); the
  optional admin "pre-login / headless-server" tier (LaunchDaemon / Windows Service) later.

---

## Open decisions for the maintainer

1. **At-login vs pre-login persistence.** All consumer prior art (incl. Docker Desktop) is **at-login**, no admin
   (recommended default). A pre-login/headless tier needs an admin prompt — offer it later as "advanced," or skip?
2. **Signing** (the recurring gate) — now *doubly* important, since it's required for the background earner to launch
   on macOS/Windows, not just for the installer. Go/no-go + ~$220/yr + enrollment; I wire the CI + the
   quarantine/MotW handling once decided.
3. **macOS limits honesty** — ship best-effort priority only and label it clearly ("best-effort, not a hard cap"), or
   hold macOS resource-capping entirely until there's a better answer? (Recommend: ship best-effort + label.)
4. **Bandwidth** — rely on each earner's own max-bandwidth setting (recommended) rather than OS shaping we mostly
   can't do?

---

## Sources & confidence
Synthesized from a 3-researcher panel (2026-07-09): OS service managers (Apple `launchd.plist(5)`, freedesktop
`systemd.service`/`resource-control`/`loginctl`, Microsoft Task Scheduler/SCM/Job-Objects, `x/sys/windows/svc`);
supervision architecture + prior art (Docker Desktop, Tailscale `tailscaled`, Ollama, Syncthing, EarnApp/Pawns/Grass,
`kardianos/service`); and tracking/limits (gopsutil, systemd cgroup-v2 directives, Windows Job Objects, macOS
`taskpolicy`/`setrlimit`, per-process-network feasibility). **High confidence** on the mechanisms, the "engine + thin
UI at login" pattern, and that it maps onto CashPilot's existing `native.go`/`fleet_server.go`. **Medium** on exact
Gatekeeper/SmartScreen behavior for a *background-launched* downloaded binary (the safe answer — strip quarantine/MotW
+ sign — holds regardless) and on macOS `launchctl print` field stability (use `launchctl list`).
