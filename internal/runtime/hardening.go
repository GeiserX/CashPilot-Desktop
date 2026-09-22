package runtime

import (
	"os"
	"strconv"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/container"
)

// Container hardening, matching the CashPilot worker's policy (web app/orchestrator.py
// deploy_raw) so a service behaves the same whether it runs from the server or from
// this app.
//
// These images are third-party and closed-source, and they run whatever their vendor
// ships. Without this they ran as in-container root with Docker's full default
// capability set, which includes NET_RAW (ARP and DNS spoofing on the bridge network),
// MKNOD, SETUID and SYS_CHROOT. The policy is:
//
//   - every capability dropped, then only the ones the service's own catalog entry
//     declares added back (most earners are plain outbound TCP clients and add none);
//   - no-new-privileges, so a setuid binary inside the image cannot escalate;
//   - a PID ceiling, so a runaway or hostile image cannot exhaust the host's PID space;
//   - host devices only from the runtime's own allow-list (see buildDevices);
//   - privileged only when the catalog entry asks for it, which no entry does today.

// noNewPrivileges is the security option that blocks privilege escalation through
// setuid binaries. Both dockerd and Podman parse this exact spelling, which is also
// what docker compose writes, so it works on Docker Desktop, Docker Engine and Podman
// alike.
const noNewPrivileges = "no-new-privileges:true"

// defaultPidsLimit is generous for a real earner — these run a single process tree —
// while still bounding a fork bomb. Raise it with CASHPILOT_PIDS_LIMIT if a service
// legitimately needs more. Same default and same env var as the web worker.
const defaultPidsLimit = 512

// pidsLimitEnv is the environment variable that overrides defaultPidsLimit.
const pidsLimitEnv = "CASHPILOT_PIDS_LIMIT"

// pidsLimit reads CASHPILOT_PIDS_LIMIT, falling back to the default on anything it
// cannot use. A typo must never stop a deploy, so a non-numeric value is ignored
// rather than raised. A non-positive value is ignored too: Docker reads 0 and -1 as
// "unlimited", which would silently switch off the very limit this exists to set.
func pidsLimit() int64 {
	raw := os.Getenv(pidsLimitEnv)
	if raw == "" {
		return defaultPidsLimit
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return defaultPidsLimit
	}
	return value
}

// applyHardening puts the capability, privilege-escalation and PID policy on a host
// config.
//
// osType is the daemon's own OS, not this process's. Docker Desktop on Windows can be
// switched to Windows containers, and that daemon REFUSES a create carrying cap_add or
// cap_drop with "adding or dropping kernel capabilities is not supported on Windows" —
// so on a non-Linux daemon the Linux-only knobs are left off instead of failing every
// deploy. Every catalog image is a Linux image, so this is about keeping a Windows-mode
// daemon usable, not about weakening the policy where it applies.
func applyHardening(hostConfig *container.HostConfig, svc catalog.Service, osType string) {
	// The catalog decides privileged, and no entry sets it. It is applied on every
	// daemon because a Windows daemon rejects it the same way it rejects capabilities,
	// and an entry asking for it must not be quietly downgraded.
	hostConfig.Privileged = svc.Docker.Privileged
	if osType != "" && osType != "linux" {
		return
	}
	hostConfig.CapDrop = []string{"ALL"}
	hostConfig.CapAdd = svc.Docker.CapAdd
	hostConfig.SecurityOpt = []string{noNewPrivileges}
	limit := pidsLimit()
	hostConfig.PidsLimit = &limit
}
