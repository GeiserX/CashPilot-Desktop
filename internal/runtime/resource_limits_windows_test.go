//go:build windows

package runtime

import (
	"os/exec"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// TestAssignProcessMemoryLimitInvalidPidErrors exercises the Job Object path on a real
// Windows host. This file is windows-only, so it does not run on the Linux PR CI, but
// desktop-release.yml runs `go test ./...` on windows-latest at every tag — so this
// executes for real there. CreateJobObject + SetInformationJobObject succeed, then
// OpenProcess fails on a pid no process owns, so the helper returns that error. This
// proves the Job-Object API wiring compiles + runs without capping any real process.
func TestAssignProcessMemoryLimitInvalidPidErrors(t *testing.T) {
	// 0x7FFFFFFF is not a live pid; OpenProcess should fail and the error propagate.
	if err := assignProcessMemoryLimit(0x7FFFFFFF, 64<<20); err == nil {
		t.Fatal("assignProcessMemoryLimit(invalid pid) = nil, want an error from OpenProcess")
	}
}

// TestApplyNativeResourceLimitsNilCmdNoPanic proves the best-effort guard: a nil cmd, or a
// not-yet-started cmd (nil Process) with a MemLimit set, is a safe no-op and never panics
// — a limit we cannot apply must never take down the caller.
func TestApplyNativeResourceLimitsNilCmdNoPanic(t *testing.T) {
	applyNativeResourceLimits(nil, catalog.ResourceLimits{})
	applyNativeResourceLimits(&exec.Cmd{}, catalog.ResourceLimits{MemLimit: "256m"})
}
