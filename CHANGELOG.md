# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Pairing hands the server the history collected before it.** A Desktop that ran standalone for months and was then paired used to appear on the fleet page starting from the day of pairing — every earlier day it had recorded was simply absent from the total, with no way to get it there. The first time a CashPilot server confirms this worker, Desktop now uploads its recorded daily balances to `POST /api/workers/earnings-import` (requires CashPilot v1.16.0 or newer).

  It is a **copy, not a migration**: the local rows are read and left exactly where they are, so unlinking leaves this machine still showing precisely what it earned on its own. The server files the readings under this client's own source rather than merging them into its own series, because earnings are clamped deltas between consecutive balance readings — interleaving two samplers of one provider account makes every apparent drop clamp to zero and understates the total. Separate series are differenced separately and then summed.

  Sent once per server, recorded in `upstreamHistoryPushedTo`; pairing with a different server hands it the history too. A failed or partial upload is retried on the next heartbeat rather than recorded as done, and the import is idempotent so a retry costs nothing. The upload waits until this worker is fully enrolled — a client still presenting the shared enrollment key is refused by the server, since every worker holds that key and it cannot prove who is writing. Historical readings carry no exchange rate: Desktop does not record what a currency was worth on a past day, and stamping today's rate onto a year-old reading would misprice it confidently.

## [0.10.1] - 2026-07-17

### Fixed

- **ProxyBase — migrated to the current client.** ProxyBase retired its Docker Hub image and old GHCR org and moved to `proxybase.org`, so the catalog entry no longer worked. The image is now `ghcr.io/proxybaseorg/peer-cli` (digest-pinned, multi-arch amd64/arm64/armv7 — arm64/Raspberry Pi now supported), the credentials are the client's current `ID` (relabelled **Access Token**, masked) and `NAME` env vars (the retired `USER_ID`/`DEVICE_NAME` are ignored by the new client), every URL points at `proxybase.org`, and datacenter IPs are now marked as accepted (residential still earns most). Existing ProxyBase services must be re-deployed with a fresh Access Token.

## [0.10.0] - 2026-07-11

### Changed

- **Fleet server — per-worker keys.** The fleet heartbeat API (`/api/workers/heartbeat`) now issues each device its own key on first contact — returned once as `worker_key` — and requires it thereafter. The shared fleet token (`CASHPILOT_API_KEY`) becomes an enrollment-only bootstrap credential and is rejected for a device once it has confirmed its own key, so a leaked device key is scoped to that device and no device can impersonate another. A fresh key is re-delivered on each heartbeat until the device confirms it, so a dropped response can't lock a device out. Interoperates with the CashPilot web UI (v1.0.0) and the CashPilot-android client. A forward-only SQLite migration adds the per-device key columns.

  Enrollment is trust-on-first-use: the shared token still lets its holder enroll a device identity, so keep the shared token secret and keep the fleet API on loopback (the default `FleetBindAddress`) unless you deliberately expose it to a trusted LAN. The heartbeat endpoint is per-IP rate-limited, and the key state machine is serialized so concurrent/retried heartbeats and the stale-device reaper cannot race a device's key. The API speaks plain HTTP — do not bind it beyond loopback/a trusted LAN without a TLS-terminating reverse proxy in front (full built-in TLS is a planned follow-up).
