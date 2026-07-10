//go:build linux

package runtime

import (
	"os"
	"os/exec"
	"strconv"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// applyNativeResourceLimits applies Linux resource limits to a just-started child. It is
// called POST-Start (it needs cmd.Process.Pid) and is best-effort: any failure is ignored
// so a limit we could not set never takes down an earner that is already running.
//
// Implemented: OOM-killer priority via /proc/<pid>/oom_score_adj, matching the OomScoreAdj
// knob the Docker path sets — so a memory-hungry earner can be made the first (or last)
// thing the kernel reclaims under pressure, instead of a random process.
// A hard memory cap (cgroup v2 memory.max) is added in a later slice (US-D2).
func applyNativeResourceLimits(cmd *exec.Cmd, res catalog.ResourceLimits) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if res.OomScoreAdj != nil {
		_ = writeOomScoreAdj(cmd.Process.Pid, *res.OomScoreAdj)
	}
}

// clampOomScoreAdj bounds an OOM score adjustment to the kernel's valid range
// [-1000, 1000] (see proc(5)). -1000 makes a process effectively OOM-immune; +1000 makes
// it the first victim.
func clampOomScoreAdj(adj int) int {
	if adj < -1000 {
		return -1000
	}
	if adj > 1000 {
		return 1000
	}
	return adj
}

// writeOomScoreAdj sets pid's OOM-killer bias by writing /proc/<pid>/oom_score_adj. Raising
// the value (more killable) is always permitted for a same-uid child; lowering it below the
// parent's may require CAP_SYS_RESOURCE, so the write is best-effort and the caller ignores
// the returned error.
func writeOomScoreAdj(pid, adj int) error {
	path := "/proc/" + strconv.Itoa(pid) + "/oom_score_adj"
	return os.WriteFile(path, []byte(strconv.Itoa(clampOomScoreAdj(adj))), 0o644)
}
