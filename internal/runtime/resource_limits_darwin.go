//go:build darwin

package runtime

import (
	"os/exec"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// applyNativeResourceLimits on macOS is best-effort only, and deliberately does NOT enforce
// a hard memory cap. Unlike Linux cgroups or Windows Job Objects, macOS exposes no
// per-process hard RSS ceiling to an unprivileged app: setrlimit(RLIMIT_AS) bounds virtual
// address space (not resident memory) and breaks runtimes that reserve large virtual
// regions — notably the Go runtime — so applying it would crash earners rather than cap
// them. This is the honest position recorded in docs/NATIVE-SUPERVISION.md; on macOS we
// rely on the earner's own resource behaviour plus the supervisor's crash accounting.
func applyNativeResourceLimits(cmd *exec.Cmd, res catalog.ResourceLimits) {
	_ = cmd
	_ = res
}
