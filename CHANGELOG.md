# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.10.1] - 2026-07-17

### Fixed

- **ProxyBase — migrated to the current client.** ProxyBase retired its Docker Hub image and old GHCR org and moved to `proxybase.org`, so the catalog entry no longer worked. The image is now `ghcr.io/proxybaseorg/peer-cli` (digest-pinned, multi-arch amd64/arm64/armv7 — arm64/Raspberry Pi now supported), the credentials are the client's current `ID` (relabelled **Access Token**, masked) and `NAME` env vars (the retired `USER_ID`/`DEVICE_NAME` are ignored by the new client), every URL points at `proxybase.org`, and datacenter IPs are now marked as accepted (residential still earns most). Existing ProxyBase services must be re-deployed with a fresh Access Token.

## [0.10.0] - 2026-07-11

### Changed

- **Fleet server — per-worker keys.** The fleet heartbeat API (`/api/workers/heartbeat`) now issues each device its own key on first contact — returned once as `worker_key` — and requires it thereafter. The shared fleet token (`CASHPILOT_API_KEY`) becomes an enrollment-only bootstrap credential and is rejected for a device once it has confirmed its own key, so a leaked device key is scoped to that device and no device can impersonate another. A fresh key is re-delivered on each heartbeat until the device confirms it, so a dropped response can't lock a device out. Interoperates with the CashPilot web UI (v1.0.0) and the CashPilot-android client. A forward-only SQLite migration adds the per-device key columns.

  Enrollment is trust-on-first-use: the shared token still lets its holder enroll a device identity, so keep the shared token secret and keep the fleet API on loopback (the default `FleetBindAddress`) unless you deliberately expose it to a trusted LAN. The heartbeat endpoint is per-IP rate-limited, and the key state machine is serialized so concurrent/retried heartbeats and the stale-device reaper cannot race a device's key. The API speaks plain HTTP — do not bind it beyond loopback/a trusted LAN without a TLS-terminating reverse proxy in front (full built-in TLS is a planned follow-up).
