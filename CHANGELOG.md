# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **The service catalog was months out of date, and some of it was earning nothing.** Desktop shipped its own hand-edited copy of the catalog with no way back to the CashPilot web repository it came from, so corrections made there never arrived. What that cost, concretely:

  **Mysterium was deployed without SETUID, SETGID or `/dev/net/tun`.** The node configures its interface and firewall through `sudo`, which switches uid and gid on the way (`setresuid`, `setgroups`), so it needs both capabilities wherever they are not granted by default; the entry now asks for them. It also needs a TUN device for wireguard, and Desktop was never mapping one: the catalog declared `docker.devices`, the loader parsed it and the runtime built its container without it. The device is now mapped, against a fixed allow-list that a catalog entry cannot widen on its own — anything outside it fails the deploy instead of producing a container that cannot work. Both failures used to earn nothing while showing green: the node registers, appears in discovery, and carries no traffic.

  **Bitping was deployed with no credentials.** Its `BITPING_EMAIL` and `BITPING_PASSWORD` were dropped from an earlier catalog rewrite while the image kept reading them, so the container sat at "No active session" indefinitely. It also now requests `NET_RAW`, which its network probes need wherever the default capability set does not include it.

  **Presearch was offered as an active service.** The programme is gone; the entry is `dead` and it no longer appears, along with its signup link.

  **Repocket's container used the wrong environment contract** (`REPOCKET_EMAIL`/`REPOCKET_PASSWORD` rather than `RP_EMAIL`/`RP_API_KEY`, which is an API key from the dashboard, not the account password).

  **Ten entries listed the wrong architectures.** Nine were narrower than the registry actually publishes (EarnApp, Earn.fm, Honeygain, IPRoyal Pawns, Mysterium, PacketStream, Storj, Traffmonetizer and SpeedShare all gained 32-bit ARM or armv5); ProxyLite was wider, claiming an arm64 build that is not published. Traffmonetizer also gains the per-architecture image overrides for its two real ARM builds — Docker Hub labels every tag of that image `linux/amd64`, so Docker cannot pick them itself.

  **ProxyBase Markets is new**, and Storj now declares a 300-second stop timeout — a node SIGKILLed after the default 30 seconds loses in-flight pieces and audit score.

- **Repocket could not collect earnings at all.** The web catalog renamed the container's environment to `RP_EMAIL`/`RP_API_KEY`, which is the right contract for the container — but the earnings collector signs in to Firebase with the ACCOUNT password, and the form stopped asking for it, so every collection answered "Repocket email and password are required". The collector-only credential fields now live in the backend next to the collectors that read them, and a test fills each collector from the form's own keys and fails if it cannot get as far as its request.

- **A retired service could still be deployed.** `dead`, `dropped` and `broken` hide a service's card, and nothing on the deploy path looked at status, so a stale wizard selection or a direct call would still pull the image and start a container for a programme that is gone. Deploy and credential validation now refuse, before the pull.

- **The weekly drift check could not tell a network failure from real drift.** Every failure exited 1 and the workflow printed "services/ no longer matches the CashPilot web catalog", so a codeload outage on a Monday looked like a catalog that had changed, naming files nobody had touched. `-check` now exits 1 only for real drift and 2 when the comparison could not be made, a transient fetch failure is retried twice before it counts, and `-ref` takes a tag or a commit SHA rather than 404ing on anything that is not a branch.

- **Bytebenefit's referral link was missing from the README.** The service is live in the catalog with a referral code; the README listed neither, so anyone reading it signed up through a bare link that pays nothing. It is in the Desktop-only table now, and a test fails when any live entry's signup link is absent from the README or has had its code stripped. The README also said 50 services when 11 of those are retired entries the app hides.

- **`make catalog-check` reported the whole catalog as drifted on Windows.** Git for Windows checks files out with CRLF by default, and the comparison was byte for byte. A `.gitattributes` keeps these paths LF on every platform, and the comparison no longer counts a line ending as a change.

### Added

- **Export a compose file and keep earning without CashPilot.** A service's card now saves a `docker-compose.yml` you can run with Docker, Podman, Portainer or a box's own GitOps. It is written for the machine you pick — x86-64, 64-bit ARM or 32-bit ARM — which matters for the two images whose ARM builds Docker cannot choose from the manifest. It carries no credentials: every value you own is a `${VAR}` placeholder Compose fills from a `.env` file beside it, so the file is safe to keep in a repository or send to another machine. It is also no weaker than a CashPilot deploy — same capability set, same PID and memory ceilings, same devices, same stop grace period — and it keeps the `cashpilot.*` labels, so CashPilot still finds and watches those containers even though it did not start them.

- **The card now says what the catalog already knew.** Where a provider hides its token, the minimum before you can cash out and the unit it is counted in, how and how often you get paid, and what the service does with your connection and your account. A value nobody has documented shows as an em dash rather than a zero, because "no minimum documented" and "the minimum is zero" are different facts and one of them says you can cash out today. A balance is never counted down against a minimum in another unit either: Storj reports dollars while its minimum is in STORJ, and the two are not the same number.

- **`services/` is now vendored from the web catalog, with a weekly drift check.** `make catalog-sync` fetches `GeiserX/CashPilot@main`, copies each entry byte for byte, and deletes anything upstream retired. The differences Desktop keeps are declared in `catalog-overlay/` and nowhere else: the immutable image digest pins, and the `native:` block that lets Desktop run a service as a supervised process with no container runtime. The sync refuses to run when a pin names a different repository or tag from the web entry, which is what stops a provider's image move leaving Desktop on a retired build that looks healthy and earns nothing.

  A scheduled weekly workflow runs the same check and fails loudly when the two have parted. It deliberately does not run on pull requests: a change in the web repository is not a reason to block an unrelated Desktop PR.

- **The catalog loader now reads the whole web schema.** Capabilities, host devices, per-architecture images, critical volumes, health signals, the advertised-address variable, stop timeouts, referral codes, payout and disclosure blocks, credential hints, and the preflight fields (`container_prohibited`, per-IP limits) all parse. Host devices and capabilities now reach the container; the rest arrive and wait for the slice that uses them, which is what lets that slice happen without another round of catalog surgery. A test strict-decodes every vendored entry, so a key the web catalog adds or renames fails on the sync PR rather than being dropped in silence.

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
