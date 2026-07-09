# Branch 2 — Native Runtime + Trust Design

> **What this is.** The design + roadmap for **Branch 2**: turning CashPilot-Desktop into a **zero-install,
> native macOS/Linux/Windows app for the mass market** (non-technical "beermoney" users). The sibling
> **`CashPilot` (web) stays the homelabber tool**; the desktop app is the native, complementary product.
> This doc synthesizes a four-researcher study (native-client landscape, runtime-bundling options,
> signing/AV/notarization, and how it maps onto the existing `internal/runtime` code). Companion:
> [`managed-runtime.md`](./managed-runtime.md) (the earlier VM-first sketch, now scoped as a *later, lazy*
> tier), [`MARKET-FIT.md`](./MARKET-FIT.md).

## TL;DR — the decision

1. **Go native-first, hybrid.** Run earners as **supervised native child processes (no Docker)** wherever an
   official native client exists — which is **most of the catalog**. Keep a **lazy bundled runtime** only for
   the few services that structurally need a container, spun up **only** when the user deploys one.
2. **This is far easier than it looks.** The earnings/collector layer is **already runtime-independent**
   (balances come from each provider's cloud API over HTTP, not from the local container), and most current
   "Docker images" are **thin wrappers around an official headless CLI** — so porting is mostly *deleting the
   container layer*, not rewriting earning logic.
3. **The Docker assumption lives in one layer** (`runtime.Provider` + its wiring), not smeared across the
   codebase. The `Deployment.Runtime` discriminator column already exists. The smallest first step is a
   **behavior-preserving provider-resolver refactor**.
4. **Signing is now mandatory** (unsigned = uninstallable for non-technical users). ~$99/yr Apple + ~$120/yr
   Windows. **This needs the maintainer's enrollment + spend — a real go/no-go decision.**
5. **Some antivirus PUA flags are unavoidable** for *any* proxyware launcher, even signed and open-source —
   so the **honesty/consent design is the core trust lever**, not optional polish.
6. **Be honest in messaging:** "zero-install" means **"zero manual Docker setup,"** not literally zero
   first-run prompts (even Salad, a funded company solving this exact problem, still shows admin/reboot
   friction).

---

## Why native-first is the right call (and cheap)

Three findings make this an unusually clean pivot:

- **The earnings layer doesn't care how a service runs.** `internal/collectors` dispatches by slug to pure
  HTTP calls against each provider's *account API* (e.g. Honeygain logs into `dashboard.honeygain.com`). It
  never touches a container. This is *why the app already ships 30 of 49 services with no image at all* —
  they're tracked purely by credentials. **A native-process service is architecturally just one of those
  "credential-only" services that additionally has a local lifecycle.**
- **Most "Docker images" are thin CLI wrappers.** The community EarnApp image just runs Bright Data's own
  official Linux daemon (installed via `install.sh`) inside a container. Dropping the container and running
  the same official binary directly is *removing* a layer, not adding one.
- **The container assumption is localized.** It lives behind the 8-method `runtime.Provider` interface
  (`internal/runtime/runtime.go`). The shell-safe argv builder (`tokenizeCommand`/`substitute`, already
  CWE-78-hardened) is **reusable verbatim** for launching native binaries, and `store.Deployment.Runtime`
  already exists as a per-deployment provider discriminator.

**The one hard physical constraint:** macOS and Windows kernels *cannot* run unmodified Linux containers —
a VM (or WSL2, which *is* a VM) is the only way. So "zero-VM, full Docker catalog" on mac/Windows is
**impossible, not just hard**. That's exactly why the model is hybrid: native where we can, a lazy VM only
where a service genuinely needs a container.

---

## Execution model — options considered

| Option | Friction (non-technical user) | Catalog coverage | Verdict |
|---|---|---|---|
| **A. Native-process** (supervised binary per earner) | **Lowest** — no VM/container concept (Grass's real-world pattern) | Only services with an official native client | **Primary path** |
| **B. Bundled VM for everything** (Lima/krunkit + WSL2) | Medium–High, on *every* install (VT-x/BIOS, UAC, sometimes reboot) | Full — any Docker image "just works" | Too heavy as the default; "a second product" |
| **C. Embedded rootless engine** (podman/containerd) | Low on Linux; N/A on mac/Windows (still needs B) | Full on Linux only | Use on **Linux** inside the hybrid |
| **D. Hybrid** — native where possible, **lazy** VM only for Docker-only services | Native path = zero; VM cost deferred to first Docker-only deploy | Full | **Chosen (end state)** |

**Chosen: D, reached incrementally via A.** Native-first now (covers the mass-market bandwidth earners with
zero VM); the lazy VM is a later, opt-in tier built from **krunkit/vfkit + libkrun** (macOS), a bundled
**`wsl --import`** rootfs (Windows), and **rootless containerd** (Linux). Budget **Colima-class** cost
(~350 MB idle RAM, ~6 s boot) — *not* Docker-Desktop-class (1.5–2.5 GB). Docker Desktop itself **cannot be
bundled** (proprietary EULA); the engines (Moby/containerd/nerdctl) are Apache-2.0 and GPL-3.0-compatible as
subprocesses.

---

## Native-client landscape (what runs how)

Of the 16 core earners studied — **~12 have official native binaries (≥ Windows), ~10 are headless/CLI-capable**:

| Runs natively today (supervise as a child process) | Needs a container runtime | Browser-extension only | Dead / drop |
|---|---|---|---|
| **Mysterium** (GPLv3, official headless CLI, all 3 OSes — *best MVP candidate*), **Storj** (open source, official CLI), **ProxyBase** (official server CLI), EarnApp (official daemon, incl. Linux/ARM), Honeygain (mac/Win native), IPRoyal Pawns, Traffmonetizer (mac/Win), PacketStream, URnetwork (headless-first), Repocket*, Salad (GUI-only native, no headless) | **Vast.ai** (renting out running *others'* Docker containers on your GPU — Docker *is* the product) | **Nodepay** (extension + mobile only) | **Gradient** (Sentry Node earning program ended Aug 2025), **Peer2Profit** (site dead) |

\* Repocket's native matrix is medium-confidence (vendor page 404'd on direct fetch) — re-verify.

**Implications:**
- **~10–12 services** become native supervised processes → the mass-market zero-install core.
- **Vast.ai** waits for the lazy VM tier (it's a GPU-host power-user feature anyway).
- **Nodepay** stays **collector-only** (track earnings from credentials; no local run) unless/until a bundled
  headless-browser path is justified — heavy; the Wails WebView can't load Chrome extensions, so it's not a
  free win. Defer.
- **Catalog corrections needed** (stale data): Gradient `status: active → dead`; Salad `platforms` add
  `macos`/`linux` (graduated from beta); PacketStream add `macos`; ProxyBase image ref
  (`ghcr.io/proxybase-org-company/peer-cli`). Peer2Profit already `dead`.
- **Business-model note:** Honeygain/Bright Data run official partner-SDK programs that *pay* to bundle their
  client — but they require exclusivity ("no competing tech") that conflicts with the aggregator model.
  **Stay on the current model** (run the *retail* client with the *user's own* account credentials) — it's a
  simpler, unconflicted relationship. Don't pursue SDK partnership.

---

## The trust minefield (this is first-order for Branch 2)

Two separate problems — one fully fixable, one only mitigable:

### 1. Signing/notarization — a HARD BLOCKER you can fully fix (but it costs money + your enrollment)
- **macOS:** unsigned/un-notarized apps show "app is damaged," and **macOS 15 Sequoia removed the old
  right-click-to-open bypass** — so for a non-technical user an un-notarized app is effectively
  **uninstallable**. Needs Apple Developer Program (**$99/yr**) + Developer ID + hardened runtime + notarize
  + staple, and **every** bundled Mach-O signed. Native binaries keep this surface small; a bundled VM/JIT
  hypervisor enlarges it (extra entitlements, possibly a DTS request).
- **Windows:** unsigned = the SmartScreen "unrecognized app" wall. Needs Authenticode signing (**~$120–220/yr**
  — Azure Trusted Signing ~$120/yr *if available in Spain*, else an OV cert; **skip EV**, it no longer buys
  instant trust) plus a **multi-week reputation ramp you can't shortcut**.
- **→ Decision for the maintainer:** this is a real recurring spend + identity enrollment I can't do
  autonomously. **I can build the CI signing pipeline the moment the certs exist.**

### 2. Proxyware AV/PUA flagging — NOT fully fixable (the honest tax)
Even perfectly signed and open-source, a bandwidth-sharing launcher **will still be PUA/"riskware"-flagged by
some engines by design** (Talos, Trend Micro, Kaspersky classify the whole category; Microsoft's *own*
guidance says *"do not sign potentially unwanted applications,"* which can even poison your cert; VirusTotal's
trusted-source whitelist is **explicitly closed** to proxyware). A subset of users *will* see a flag no
matter what. **This can only be mitigated — and the mitigation is the honesty/consent design below.**

### The mitigation = core Branch-2 product design (genuinely good, and our differentiator)
1. **Per-service opt-in, nothing running by default** (directly counters the #1 PUA trigger: "silently
   installs/runs other software").
2. **No silent background start; a persistent, honest status of what's running.**
3. **Trivially easy, complete uninstall** (proxyware's "hard to remove" reputation is itself a trust signal;
   EarnApp is notorious for it — beating that is a real differentiator).
4. **Visible, user-set resource caps + a kill switch.**
5. **Lean on GPL-3.0 source-auditability as a headline trust claim** — "read the code yourself,"
   reproducible builds + published checksums — something closed-source Honeygain/EarnApp cannot offer.
6. **Plain-language IP-risk disclosure** (already added to the README) at the point of consent.
7. **Don't trip *accidental* heuristics:** no packing/obfuscation, no anti-VM, no auto-download of unsigned
   second-stage binaries (download native clients over HTTPS with **pinned SHA-256 checksums**).

---

## Phased implementation plan

Each phase is independently shippable and (except where noted) behavior-preserving.

- **Phase 0 — catalog + honesty groundwork (no runtime change).** Correct the stale catalog entries
  (Gradient/Salad/PacketStream/ProxyBase); the README honesty pass is already in flight (PR #75). *Small.*
- **Phase 1 — provider-resolver refactor** (`internal/services/manager.go`). Replace the single
  `runtime.Provider` field with a **resolver/registry keyed by runtime kind**; register `DockerProvider` as
  `"existing-docker"`; record the resolved kind into `Deployment.Runtime`; make `Refresh`/`sampleHealth`
  union `List()` across registered providers. **Behavior-preserving today** (only Docker registered), fully
  unit-testable. Also parameterize `dockerClient()` to accept an optional socket host (the seam a future VM
  provider needs). *This is the true pivot from "Docker is the runtime" to "Docker is a runtime."*
- **Phase 2 — `NativeProcessProvider` MVP on Mysterium** (the cleanest case: GPLv3, official headless CLI,
  all 3 OSes). Add a `Native NativeConfig` block to the catalog schema (per-OS/arch download URL + **SHA-256**,
  archive/binary name, launch-args template — reuse `tokenizeCommand`/`substitute`). Implement:
  download+verify+extract, `exec.Cmd` with built argv, stdout→ring-buffer for `Logs`, a PID/state registry for
  `List`/`Start`/`Stop`/`Remove`, **app-level respawn** for the "restart policy," and per-PID stats via
  `gopsutil`. Ship resource-limits as **best-effort v1** (don't block on cross-OS cgroups). **Relax the
  onboarding gate** (`frontend/src/main.ts` disables "Open Dashboard" unless a Docker runtime is available —
  *the #1 Branch-2 UX blocker*).
- **Phase 3 — broaden native coverage** to the other ~9–11 native-viable services (Storj/ProxyBase next —
  server-CLI-clean; then the host-dir/host-network/cap_add outliers individually).
- **Phase 4 — trust/consent UX + signing.** Per-service opt-in default-off, status surface, one-click
  uninstall, resource caps, checksum display. Wire the CI signing/notarization pipeline once the maintainer
  provides certs.
- **Phase 5 — lazy bundled runtime** (`cashpilot-vm-docker`) *only if still wanted*, for Vast.ai and future
  Docker-only services (e.g. Akash). Reuses ~90% of `DockerProvider` against a private socket; the new work is
  the VM/WSL2 appliance manager. Second product — defer.
- **Cross-cutting:** OS-native sandboxing for the native processes where cheap (Job Objects/AppContainer on
  Windows, `sandbox-exec`/App Sandbox on macOS, rootless+cgroups on Linux) to recover some of the isolation a
  container gave for free, without a full VM.

---

## Open decisions for the maintainer

1. **Signing spend + enrollment** (~$99/yr Apple + ~$120–220/yr Windows). Go/no-go — Branch 2 is
   uninstallable for the mass market unsigned. I'll wire CI the moment certs exist.
2. **Isolation tolerance.** Native execution runs third-party (often closed-source) proxy binaries directly on
   the user's bare OS — lighter, but sheds the container's isolation. OK to start native for reputable
   bandwidth earners + add OS-native sandboxing, keeping the VM as a later isolation tier? (Recommended.)
3. **Nodepay / browser-extension services** — leave collector-only (recommended) or invest later in a bundled
   headless-browser (heavy)?
4. **Build the lazy VM tier at all?** Vast.ai is the only structural need today (a GPU power-user feature). Fine
   to defer indefinitely and stay native-only for the mass market? (Recommended: defer.)

---

## Sources & confidence
Synthesized from a 4-researcher panel (2026-07-09): the native-client landscape (per-service, vendor-sourced,
some vendor pages self-reported/time-sensitive — Salad/Gradient/Repocket flagged for re-verification), the
runtime-bundling comparison (Rancher/Podman/Colima/OrbStack/Salad/WSL2 precedents; container engines
Apache-2.0/GPL-compatible, Docker Desktop excluded by EULA), the signing/AV study (Apple/Microsoft primary
docs, Cisco Talos/Trend/Kaspersky proxyware-PUA classification, VirusTotal-whitelist-closed), and a code-level
architecture map of `internal/runtime`, `internal/collectors`, `internal/catalog`, `internal/services`,
`store.Deployment.Runtime`, and the `frontend/src/main.ts` onboarding gate. High confidence on the
architecture map and the "native-first is cheap" conclusion; medium on time-sensitive vendor facts.
