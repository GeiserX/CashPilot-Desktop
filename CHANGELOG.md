# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **The service catalog was months out of date, and some of it was earning nothing.** Desktop shipped its own hand-edited copy of the catalog with no way back to the CashPilot web repository it came from, so corrections made there never arrived. What that cost, concretely:

  **Mysterium was deployed without SETUID, SETGID or `/dev/net/tun`.** Every container runs with `cap_drop: ALL`, and the node configures its interface and firewall through `sudo`, which switches uid and gid on the way. Without those two capabilities the call fails with `sudo: PERM_SUDOERS: setresuid(...): Operation not permitted`, every session dies at setup — and the node still registers, still appears in discovery, and still looks healthy. Without the TUN device it advertises itself to the network and carries no traffic. Both failures earn nothing while showing green.

  **Bitping was deployed with no credentials.** Its `BITPING_EMAIL` and `BITPING_PASSWORD` were dropped from an earlier catalog rewrite while the image kept reading them, so the container sat at "No active session" indefinitely. It also now requests `NET_RAW`, which `cap_drop: ALL` removes and its network probes need.

  **Presearch was offered as an active service.** The programme is gone; the entry is `dead` and it no longer appears, along with its signup link.

  **Repocket's container used the wrong environment contract** (`REPOCKET_EMAIL`/`REPOCKET_PASSWORD` rather than `RP_EMAIL`/`RP_API_KEY`, which is an API key from the dashboard, not the account password).

  **Ten entries listed the wrong architectures.** Nine were narrower than the registry actually publishes (EarnApp, Earn.fm, Honeygain, IPRoyal Pawns, Mysterium, PacketStream, Storj, Traffmonetizer and SpeedShare all gained 32-bit ARM or armv5); ProxyLite was wider, claiming an arm64 build that is not published. Traffmonetizer also gains the per-architecture image overrides for its two real ARM builds — Docker Hub labels every tag of that image `linux/amd64`, so Docker cannot pick them itself.

  **ProxyBase Markets is new**, and Storj now declares a 300-second stop timeout — a node SIGKILLed after the default 30 seconds loses in-flight pieces and audit score.

### Added

- **`services/` is now vendored from the web catalog, with a weekly drift check.** `make catalog-sync` fetches `GeiserX/CashPilot@main`, copies each entry byte for byte, and deletes anything upstream retired. The differences Desktop keeps are declared in `catalog-overlay/` and nowhere else: the immutable image digest pins, and the `native:` block that lets Desktop run a service as a supervised process with no container runtime. The sync refuses to run when a pin names a different repository or tag from the web entry, which is what stops a provider's image move leaving Desktop on a retired build that looks healthy and earns nothing.

  A scheduled weekly workflow runs the same check and fails loudly when the two have parted. It deliberately does not run on pull requests: a change in the web repository is not a reason to block an unrelated Desktop PR.

- **The catalog loader now reads the whole web schema.** Capabilities, host devices, per-architecture images, critical volumes, health signals, the advertised-address variable, stop timeouts, referral codes, payout and disclosure blocks, credential hints, and the preflight fields (`container_prohibited`, per-IP limits) all parse. Nothing new acts on them yet; this is what lets the next slice use them without another round of catalog surgery.

  Two of them are deliberately nullable, because absent and zero are different answers: a per-IP device limit of `null` means nobody has documented one and a second instance needs checking against the provider's terms, while `0` means the provider states it imposes none. `referral.program` is the same shape — `null` is "unchecked", not a verified "no".

- **A paired Desktop shows the account-wide picture, and an unlinked one goes back to its own.** While paired, the dashboard gains an "Across your CashPilot account" panel with what the platforms this machine runs earned on your provider accounts over the server's reporting window, straight from the heartbeat response. Unlink and it disappears, leaving exactly the local numbers as before — which works because pairing COPIES this machine's history upstream rather than moving it.

  Two things it is careful about. A platform the server has no reading for renders as **—**, never `0.00`: no reading usually means a collector that does not exist yet or credentials nobody entered, and showing zero would report a loss that did not happen. And a platform running on more than one machine is marked **shared**, because earnings are collected per platform from the provider — if two machines run the same service the provider reports one balance and nothing can split it, so the figure is the account's rather than this machine's.

- **Pairing hands the server the history collected before it.** A Desktop that ran standalone for months and was then paired used to appear on the fleet page starting from the day of pairing — every earlier day it had recorded was simply absent from the total, with no way to get it there. The first time a CashPilot server confirms this worker, Desktop now uploads its recorded daily balances to `POST /api/workers/earnings-import` (requires CashPilot v1.16.0 or newer).

  It is a **copy, not a migration**: the local rows are read and left exactly where they are, so unlinking leaves this machine still showing precisely what it earned on its own. The server files the readings under this client's own source rather than merging them into its own series, because earnings are clamped deltas between consecutive balance readings — interleaving two samplers of one provider account makes every apparent drop clamp to zero and understates the total. Separate series are differenced separately and then summed.

  Sent once per server, recorded in `upstreamHistoryPushedTo`; pairing with a different server hands it the history too. A failed or partial upload is retried on the next heartbeat rather than recorded as done, and the import is idempotent so a retry costs nothing. The upload waits until this worker is fully enrolled — a client still presenting the shared enrollment key is refused by the server, since every worker holds that key and it cannot prove who is writing. Historical readings carry no exchange rate: Desktop does not record what a currency was worth on a past day, and stamping today's rate onto a year-old reading would misprice it confidently.

### Changed

- **`dropped` services are hidden alongside `dead` and `broken`.** A service that was evaluated and then removed is not on offer, for the same reason a dead one is not: listing it invites a signup that will never pay.

## [0.10.1] - 2026-07-17

### Fixed

- **ProxyBase — migrated to the current client.** ProxyBase retired its Docker Hub image and old GHCR org and moved to `proxybase.org`, so the catalog entry no longer worked. The image is now `ghcr.io/proxybaseorg/peer-cli` (digest-pinned, multi-arch amd64/arm64/armv7 — arm64/Raspberry Pi now supported), the credentials are the client's current `ID` (relabelled **Access Token**, masked) and `NAME` env vars (the retired `USER_ID`/`DEVICE_NAME` are ignored by the new client), every URL points at `proxybase.org`, and datacenter IPs are now marked as accepted (residential still earns most). Existing ProxyBase services must be re-deployed with a fresh Access Token.

## [0.10.0] - 2026-07-11

### Changed

- **Fleet server — per-worker keys.** The fleet heartbeat API (`/api/workers/heartbeat`) now issues each device its own key on first contact — returned once as `worker_key` — and requires it thereafter. The shared fleet token (`CASHPILOT_API_KEY`) becomes an enrollment-only bootstrap credential and is rejected for a device once it has confirmed its own key, so a leaked device key is scoped to that device and no device can impersonate another. A fresh key is re-delivered on each heartbeat until the device confirms it, so a dropped response can't lock a device out. Interoperates with the CashPilot web UI (v1.0.0) and the CashPilot-android client. A forward-only SQLite migration adds the per-device key columns.

  Enrollment is trust-on-first-use: the shared token still lets its holder enroll a device identity, so keep the shared token secret and keep the fleet API on loopback (the default `FleetBindAddress`) unless you deliberately expose it to a trusted LAN. The heartbeat endpoint is per-IP rate-limited, and the key state machine is serialized so concurrent/retried heartbeats and the stale-device reaper cannot race a device's key. The API speaks plain HTTP — do not bind it beyond loopback/a trusted LAN without a TLS-terminating reverse proxy in front (full built-in TLS is a planned follow-up).
