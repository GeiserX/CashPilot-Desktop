# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **Fleet server — per-worker keys.** The fleet heartbeat API (`/api/workers/heartbeat`) now issues each device its own key on first contact — returned once as `worker_key` — and requires it thereafter. The shared fleet token (`CASHPILOT_API_KEY`) becomes an enrollment-only bootstrap credential and is rejected for a device once it has confirmed its own key, so a leaked device key is scoped to that device and no device can impersonate another. A fresh key is re-delivered on each heartbeat until the device confirms it, so a dropped response can't lock a device out. Interoperates with the CashPilot web UI (v1.0.0) and the CashPilot-android client. A forward-only SQLite migration adds the per-device key columns.

  Enrollment is trust-on-first-use: the shared token still lets its holder enroll a device identity, so keep the shared token secret and keep the fleet API on loopback (the default `FleetBindAddress`) unless you deliberately expose it to a trusted LAN.
