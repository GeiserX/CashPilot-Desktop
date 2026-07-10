//go:build linux

package runtime

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// cgroupV2Mount is the cgroup v2 filesystem mount point. A var (not a const) so tests can
// point the writer at a temp dir instead of the real hierarchy.
var cgroupV2Mount = "/sys/fs/cgroup"

// applyNativeResourceLimits applies Linux resource limits to a just-started child. It is
// called POST-Start (it needs cmd.Process.Pid) and is best-effort: any failure is ignored
// so a limit we could not set never takes down an earner that is already running.
//
//   - OOM-killer priority via /proc/<pid>/oom_score_adj (res.OomScoreAdj), matching the knob
//     the Docker path sets.
//   - A hard memory cap via cgroup v2 memory.max (res.MemLimit), where the process runs in a
//     delegated, writable cgroup subtree (a normal systemd user session). Where cgroup v2
//     delegation is unavailable it degrades to a no-op and the earner runs uncapped.
func applyNativeResourceLimits(cmd *exec.Cmd, res catalog.ResourceLimits) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if res.OomScoreAdj != nil {
		_ = writeOomScoreAdj(pid, *res.OomScoreAdj)
	}
	if maxBytes, ok := parseMemBytes(res.MemLimit); ok {
		_ = applyCgroupMemoryMax(pid, maxBytes)
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

// applyCgroupMemoryMax best-effort caps pid's memory via cgroup v2 memory.max: it creates a
// child cgroup under the process's current cgroup, sets memory.max, and moves the process
// in. It requires cgroup v2 with a delegated, writable subtree (a normal systemd user
// session provides this). Where that is unavailable (no delegation, cgroup v1) it returns an
// error and the caller no-ops — the earner keeps running, just uncapped.
func applyCgroupMemoryMax(pid int, maxBytes int64) error {
	rel, err := currentCgroupV2Path(pid)
	if err != nil {
		return err
	}
	return cgroupMemoryMaxAt(filepath.Join(cgroupV2Mount, rel), pid, maxBytes)
}

// cgroupMemoryMaxAt creates <parentDir>/cashpilot-<pid>/, writes memory.max, and moves pid
// into it. The parent dir is injected so tests can exercise the write logic against a temp
// directory without a real delegated cgroup.
func cgroupMemoryMaxAt(parentDir string, pid int, maxBytes int64) error {
	// Best-effort: ask the parent to delegate the memory controller to its children. This
	// is a no-op (or benign error, ignored) where already enabled.
	_ = os.WriteFile(filepath.Join(parentDir, "cgroup.subtree_control"), []byte("+memory"), 0o644)
	child := filepath.Join(parentDir, "cashpilot-"+strconv.Itoa(pid))
	if err := os.Mkdir(child, 0o755); err != nil && !os.IsExist(err) {
		return err
	}
	if err := os.WriteFile(filepath.Join(child, "memory.max"), []byte(strconv.FormatInt(maxBytes, 10)), 0o644); err != nil {
		return err
	}
	// Moving the pid into the leaf cgroup is what actually subjects it to memory.max.
	return os.WriteFile(filepath.Join(child, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644)
}

// currentCgroupV2Path returns pid's cgroup v2 path relative to the cgroup mount, read from
// /proc/<pid>/cgroup (the unified-hierarchy "0::<path>" line).
func currentCgroupV2Path(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return "", err
	}
	return parseCgroupV2Path(string(data))
}

// parseCgroupV2Path extracts the unified-hierarchy path from /proc/<pid>/cgroup content. The
// cgroup v2 line is "0::<path>"; it returns an error when there is none (a cgroup v1-only
// host), so the caller degrades to no cap.
func parseCgroupV2Path(content string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", errors.New("no cgroup v2 (unified) hierarchy for process")
}
