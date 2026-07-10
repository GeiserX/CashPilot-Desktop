//go:build windows

package runtime

import (
	"os/exec"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// applyNativeResourceLimits on Windows will cap a native earner's memory by assigning it to
// a Job Object with JOBOBJECT_EXTENDED_LIMIT_INFORMATION.ProcessMemoryLimit (US-D3),
// best-effort and post-Start. Until that slice lands it is a no-op so the native provider
// builds and runs on Windows unchanged.
func applyNativeResourceLimits(cmd *exec.Cmd, res catalog.ResourceLimits) {
	_ = cmd
	_ = res
}
