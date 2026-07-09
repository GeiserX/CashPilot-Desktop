# Remote Deploy — How the Desktop Reaches Your Servers (Transport)

> Companion to [`REMOTE-DEPLOY-DESIGN.md`](./REMOTE-DEPLOY-DESIGN.md). This doc answers the one
> question that design left open: **when the desktop app deploys a container to a remote
> machine, how does it actually reach that machine across the internet — securely, and without
> asking the user to configure anything?** It's written to be read by a human making the call,
> grounded in real docs (sources at the bottom).

## TL;DR

1. **This is mostly *not* a NAT-hole-punching problem.** The desktop is the one clicking "deploy",
   and your servers are always-on. The desktop dials **out** to a server that's already reachable.
   And the worker **pulls its own image** (digest-pinned) — so only tiny commands cross the wire,
   never image layers.
2. **Default = the desktop dials the worker over an address it can already reach** (same LAN, a
   Tailscale tailnet, or a public-IP VPS) using plain **HTTP + a per-worker bearer token**, with an
   optional **mTLS** upgrade. This is what's already designed, and your 3 Tailscale servers work with
   it today — the SSRF validator already trusts the Tailscale range.
3. **For users with no VPN and a server behind NAT**, the best *zero-config* answer is **embedded
   `tsnet`** (a Tailscale node inside the app — pure Go, no account shown to the user), **not iroh**.
4. **Reject iroh.** Great tech, but it has **no official Go binding**, so it would drag Rust + cgo
   into a pure-Go app and break the clean cross-platform build — for NAT traversal that `tsnet`
   already gives you in pure Go.
5. **Use per-worker keys** (each server its own identity), not one shared key. You were right.

---

## The reframe (read this first)

The instinct is "users have wildly different networks → I need magic P2P NAT traversal like iroh."
Mostly false, for four concrete reasons:

- **The desktop initiates.** You click "deploy" on your laptop; it connects *out* to the server.
  Outbound connections sail through almost any home NAT. The hard direction (something dialing *into*
  a home machine) is only needed if you tried to make the **worker** call the **desktop** — and you
  shouldn't (see below).
- **Servers are the reachable end.** A "remote server" is usually a VPS with a public IP, or a home
  box you've already put on Tailscale, or a machine on your own LAN. In all three the desktop can
  just dial it.
- **The worker pulls its own image.** Deploys don't ship container layers over the link — the worker
  runs `docker pull <digest>` itself. Only small JSON commands cross the wire. So even a slow/relayed
  path is fine; the "P2P for big transfers" argument doesn't apply.
- **"Worker phones home to the desktop" is broken.** A desktop is often closed and behind its *own*
  NAT — there's nothing for the worker to phone. Every reverse-tunnel tool proves this by requiring
  an always-on public broker. Don't build that.

So the real question shrinks to: **"how does the desktop reach a worker that isn't on the same LAN
and isn't on a VPN?"** — a minority case, with a clean answer (`tsnet`).

---

## The options, plainly

### A. Over a mesh VPN you already run (Tailscale / WireGuard) — **the default**
- **What:** both machines are on the same tailnet; the desktop dials the worker's tailnet address
  over plain HTTP. WireGuard encrypts it.
- **Pros:** rock-solid, encrypted, mature; **zero extra code** — it's how your 3 servers work today,
  and the SSRF validator already allow-lists the Tailscale range (`100.64.0.0/10`, `*.ts.net`).
- **Cons:** only helps people who *already* run Tailscale (i.e. you, not the average downloader).
- **Verdict:** keep as the default for you + any Tailscale user.

### B. mTLS + the user opens a port (port-forward) — **advanced opt-in only**
- **What:** the worker listens on a public port; the desktop dials it with a client certificate.
- **Pros:** pure-Go stdlib (`crypto/tls`), strong channel security, no third party.
- **Cons:** **impossible behind CGNAT** (no public IP to forward — normal on mobile, common on home
  ISPs); router config is beyond most users; exposing a deploy port to the internet is a standing
  attack surface; cert expiry is a silent-outage footgun.
- **Verdict:** offer as an explicit "advanced" mode for a reachable worker (VPS you already have),
  never as the default. Note: **mTLS to a server's *existing* public endpoint** (no user port-forward)
  is great — the landmine is specifically asking users to forward ports.

### C. iroh (P2P, dial-by-key) — **reject (for now)**
- **What:** Rust library; dial a machine by its public key over QUIC; ~90–95% direct, relay fallback.
- **Pros:** the only option needing **no account from the user** while still piercing NAT; excellent tech.
- **Cons (disqualifying):** **no official Go binding** — Swift/Kotlin/Python/JS only, and n0 has *paused*
  the whole FFI effort. Your Go options are (i) a self-maintained cgo wrapper (breaks the clean
  `go build` cross-compile, adds a Rust toolchain to CI, extra macOS notarization), (ii) bundle a Rust
  sidecar binary (you lose "single binary" + ship/sign a second exe per platform), or (iii) a 16-star,
  single-author, unofficial pure-Go reimplementation forking quic-go (too risky for a deploy control
  plane). Hard-NAT users also land on n0's **rate-limited** relays.
- **Verdict:** **no.** Revisit only if n0 ships a maintained Go binding. `tsnet` gives you the same
  NAT-traversal class in pure Go.

### D. Reverse tunnel / "agent phones home" — **park**
- **What:** worker + desktop both dial out to a rendezvous relay that forwards between them.
- **Pros:** outbound-only, works behind almost anything.
- **Cons:** *someone has to run the always-on relay 24/7* (you, for all your users — perpetual ops +
  a central attack surface brokering remote deploys). It also relays everything (no direct-P2P
  upgrade unless you also build hole-punching — which is most of what `tsnet` gives you for free), and
  it doesn't fit the existing "dial the worker's URL" model.
- **Verdict:** only if you want zero dependency on the Tailscale ecosystem for its own sake — and even
  then `tsnet` is less work for a better result.

### E. Embedded `tsnet` (a Tailscale node *inside* the app) — **the zero-config default for non-VPN users**
- **What:** the app embeds a pure-Go Tailscale node (`tailscale.com/tsnet`). Desktop and worker each
  become a node and talk over the tailnet — real hole-punching, DERP relay fallback, WireGuard crypto.
- **Pros:** **pure Go, no cgo, cross-compiles clean** (`tsnet` is Tailscale's headline library for
  exactly this); iroh-class NAT traversal (>90% direct); **plugs into the code you already shipped with
  zero changes** — a `tsnet` node's address is in `100.64.0.0/10`, which your SSRF validator already
  trusts, so it's just another way to fill in a worker's endpoint. The user never sees "Tailscale" or
  makes an account — they see a **pairing code**; a small maintainer-run endpoint mints a tagged,
  ephemeral auth key behind the scenes.
- **Cons:** needs a control plane. Options: Tailscale's SaaS free tier (careful — 50 *tagged* resources
  / 1,000 ephemeral-minutes-per-month ceiling), or **self-hosted Headscale** (you run it — trivial on
  your existing servers, and removes the ceiling). Heavier dependency (+~15–25 MB, embeds WireGuard).
- **Verdict:** the strongest replacement for the "no-VPN user" gap. BSD-3 licensed (GPL-3.0 compatible).

---

## Side-by-side

| | A. Existing Tailscale | B. mTLS + port-forward | C. iroh | D. Reverse tunnel | E. Embedded `tsnet` |
|---|---|---|---|---|---|
| Works behind NAT/CGNAT, zero-config | only if already on tailnet | ❌ (CGNAT impossible) | ✅ ~90–95% direct | ✅ (needs relay) | ✅ >90% direct + relay |
| Pure-Go / clean cross-compile | ✅ | ✅ (cleanest) | ❌ cgo/Rust or sidecar | ✅ | ✅ |
| No third-party/always-on dependency | needs tailnet | mostly | relay self-hostable; **Rust toolchain** | **you run a relay** | needs control plane (Headscale = you) |
| Fits the shipped SSRF/dial model | ✅ | ✅ | ❌ (bypasses it) | ❌ (rework) | ✅ (range already trusted) |
| Security | excellent (WireGuard) | strong if done right | strong (QUIC/TLS1.3) | your job to get right | excellent (WireGuard) |
| Effort in this repo | **0 (shipped)** | S–M | **L + Rust tax** | M–L (+ new relay) | **M (no security-code change)** |

---

## Auth: per-worker keys (you were right — and today's code isn't there yet)

**Use a distinct identity/key per worker, not one shared token.**

| | Single shared key | **Per-worker keys** |
|---|---|---|
| One key leaks | **whole fleet compromised** | only that one worker |
| Revoke one worker | must re-key everyone | revoke it alone; others untouched |
| Audit | can't tell workers apart | per-worker identity + trail |

### Why this is urgent, not theoretical

The **current shipped design has the worst version of the shared-key problem.** The single
`config.FleetAPIKey` authenticates **both directions at once** — master→worker *and* worker→master — and
the same secret is handed to **every** worker. Consequences:

- **One leaked worker = command over the entire fleet (fleet-wide RCE).** Any server that runs a worker
  holds the master's own credential, so compromising one cheap VPS lets an attacker drive a deploy on
  every other machine.
- **No revocation.** You cannot cut off one worker without re-keying all of them.
- **No per-worker audit.** Every request looks identical; you can't tell which server did what.

This is a **Medium/High** gap to close *before* the remote command channel opens to more than one machine.

### The fix, in two steps

- **Model P — ship first (small, no new transport):** mint a **distinct bearer token per worker**. Store
  only a **hash** of each (bcrypt/argon2) on the master; revoke = delete one row. This alone kills the
  fleet-wide-RCE and revocation problems with the code you already have.
- **Model A — target:** per-worker **Ed25519 keypairs**, ideally realized *through* the transport
  (`tsnet` node identity / mTLS client cert) so **identity and encryption are one mechanism** instead of a
  bearer bolted on top. This is what Tailscale (per-node keys), SSH (per-host keys), mTLS (per-client
  certs), **Portainer Edge** (Edge ID + Edge Key) and **Komodo** (per-server Ed25519, auto-rotated — they
  *replaced* a shared "passkey" with exactly this) all converge on.

### Pairing UX (secure *and* the same paste-in ergonomics)

The desktop shows a **single-use, short-lived pairing code** (QR for mobile) that carries the **master's
fingerprint**. The worker presents it once, gets an **"Approve this device?"** prompt on the desktop, and
swaps it for its own durable per-worker identity — after which the bootstrap secret is never reused. The
secret in flight is *bounded* (one worker, minutes-long), never the fleet master key. With `tsnet` this is
literally how tagged ephemeral auth keys already work.

### Adjacent hardening surfaced with this (do alongside)

- **Keychain-back the fleet key.** Today `FleetAPIKey` sits **plaintext in `config.json`**, while *service*
  credentials correctly get AES-256-GCM + OS keychain. Bring it under the same protection. (Actionable now,
  independent of remote deploy.)
- **Master-pin + replay protection.** Pin the master's public key into each worker; sign requests with
  `timestamp + nonce + HMAC` so a captured request can't be replayed.
- **Rate-limit + audit-log** the fleet endpoint; add **`govulncheck`** to CI.

> Note: whichever transport wins, the deploy commands are **still** gated by the app-level bearer +
> the deploy-spec validator (blocks `privileged`, dangerous caps, `docker.sock` mounts) already shipped
> in Phase 0/1. "Reachable over the transport" is never treated as authorization by itself.

---

## The real lever (bigger than the transport choice)

The "pure-Go single static binary" property that makes exotic transports look scary **is already lost
for the GUI** — Wails forces cgo on macOS/Linux (your CI already builds natively per-OS). It only
matters for the **worker**. So the decision that actually matters is: **build the worker as a separate,
Wails-free, `CGO_ENABLED=0` target that shares the deploy code** (`internal/runtime`,
`internal/services`, `internal/fleetnet`). That gives a tiny static binary that runs on a bare Linux
server / Raspberry Pi and cross-compiles trivially — which a Wails GUI binary *cannot* do (it needs
GTK/WebKit libs a server doesn't have). This resolves design open-question #1 and is what unlocks every
transport above.

---

## Recommendation (layered)

- **Tier 1 — default, already built:** desktop dials the worker over an address it can reach (LAN /
  Tailscale) with **per-worker bearer + optional mTLS**. Auto-detect an existing Tailscale route and
  use it (fast path). This is correct and effort-free for you and any Tailscale user.
- **Tier 2 — the zero-config default for everyone else:** **embedded `tsnet`**, joined via a pairing
  code (no Tailscale account shown), on **self-hosted Headscale** to avoid the free-tier ceiling.
  Slots into the shipped SSRF/dial model with no security-code changes.
- **Tier 2 opt-in:** **mTLS + manual address** as an explicit "advanced" mode for a reachable worker
  (public-IP VPS). Documented as "you own the reachability."
- **Parked:** a custom reverse-tunnel relay (D) and iroh (C) — both cost more for an equal-or-worse
  outcome today.
- **Cross-cutting:** per-worker keys; the Wails-free `CGO_ENABLED=0` worker binary; keep the bearer +
  deploy-spec validator gating every action.

---

## Open questions for the operator

- Are you OK running a small **Headscale + pairing endpoint** so the zero-config `tsnet` default works
  for non-Tailscale users? (Trivial on your infra; it's the one always-on piece Tier 2 needs.)
- Ship Tier 1 (Tailscale/LAN + mTLS) first and add Tier 2 (`tsnet`) later, or design both up front?
- Confirm **per-worker keys** as the model (recorded) — the pairing-code flow above.

---

## Sources & confidence

High confidence (primary docs) on: iroh has no official Go binding + FFI paused
([iroh-ffi](https://github.com/n0-computer/iroh-ffi), [language bindings](https://docs.iroh.computer/deployment/other-languages),
[FFI updates](https://www.iroh.computer/blog/ffi-updates)); `tsnet` is pure-Go, embeddable, BSD-3, with
ephemeral tagged auth keys ([tsnet](https://pkg.go.dev/tailscale.com/tsnet),
[auth keys](https://tailscale.com/docs/features/access-control/auth-keys)); Headscale self-hosts the
control plane ([Headscale](https://github.com/juanfont/headscale)); Tailscale NAT-traversal >90% direct
+ DERP fallback ([how NAT traversal works](https://tailscale.com/blog/how-nat-traversal-works)); CGNAT
breaks port-forwarding ([CGNAT](https://en.wikipedia.org/wiki/Carrier-grade_NAT)); Docker daemon
exposure = host takeover without TLS ([Docker security](https://docs.docker.com/engine/security/protect-access/));
Wails forces cgo on macOS/Linux (this repo's own release CI); per-agent-key norm
([Portainer Edge](https://docs.portainer.io/advanced/edge-agent),
[Komodo](https://komo.do/docs/setup/connect-servers)); ZeroTier is BSL-licensed (avoid for GPL-3.0).
Medium confidence on exact NAT-success percentages (vendor self-reported) and Tailscale free-tier
numbers (as of April 2026 repricing).

_Synthesized from a 7-researcher panel (docs, landscape, comparativist, security, Go-architect, critic,
iroh/P2P deep-dive), 2026-07-09. Drives the transport decision in REMOTE-DEPLOY-DESIGN.md §7 (open
question #4)._
