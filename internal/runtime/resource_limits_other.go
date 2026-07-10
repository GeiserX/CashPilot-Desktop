//go:build !linux && !windows && !darwin

package runtime

import (
	"os/exec"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// applyNativeResourceLimits is a no-op on platforms without a resource-limit implementation
// (the three supported OSes are linux/windows/darwin), so the package builds everywhere.
func applyNativeResourceLimits(cmd *exec.Cmd, res catalog.ResourceLimits) {
	_ = cmd
	_ = res
}
