//go:build windows

package runtime

import (
	"context"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// PowerShellExe is Windows PowerShell's full path, built from the System32 folder
// Windows reports (GetSystemDirectory), never looked up on PATH.
func PowerShellExe() (string, error) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	return WindowsPowerShellIn(dir)
}

// powerShellCommand is a hidden Windows PowerShell, by its full path, with
// -NoProfile -NonInteractive and args, stopped when ctx ends.
func powerShellCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	exe, err := PowerShellExe()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, append([]string{"-NoProfile", "-NonInteractive"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd, nil
}
