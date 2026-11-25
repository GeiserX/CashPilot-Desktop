//go:build !windows

package winvm

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func fakePowerShellCommand(t *testing.T, ctx context.Context, args ...string) *exec.Cmd {
	t.Helper()
	ps := os.Getenv("CPD_TEST_POWERSHELL")
	if ps == "" {
		var err error
		ps, err = exec.LookPath("pwsh")
		if err != nil {
			t.Skip("PowerShell is unavailable")
		}
	}
	return exec.CommandContext(ctx, ps, append([]string{"-NoProfile", "-NonInteractive"}, args...)...)
}
