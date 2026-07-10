//go:build windows

package runtime

import (
	"os/exec"
	"unsafe"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"golang.org/x/sys/windows"
)

// applyNativeResourceLimits on Windows caps a native earner's memory via a Job Object. It is
// called POST-Start (it needs the pid) and is best-effort: any failure is ignored so a limit
// we cannot set never takes down a running earner. Only the memory cap is implemented (from
// res.MemLimit) — oom_score_adj is a Linux concept with no Windows equivalent.
//
// NOTE: runtime enforcement is exercised only on a Windows host. This repo's CI is Linux, so
// this path is compile-verified (GOOS=windows go build) but not runtime-tested in CI; the
// design is fail-safe (any error leaves the earner running, uncapped).
func applyNativeResourceLimits(cmd *exec.Cmd, res catalog.ResourceLimits) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if maxBytes, ok := parseMemBytes(res.MemLimit); ok {
		_ = assignProcessMemoryLimit(cmd.Process.Pid, maxBytes)
	}
}

// assignProcessMemoryLimit creates a Job Object with a per-process memory limit and assigns
// pid to it. The job (and its limit) persists for the process's lifetime even after the
// creating handle is closed, because a job is not destroyed while it still has a member
// process. Best-effort: it returns an error the caller ignores.
func assignProcessMemoryLimit(pid int, maxBytes int64) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
	info.ProcessMemoryLimit = uintptr(maxBytes)
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return err
	}

	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	return windows.AssignProcessToJobObject(job, h)
}
