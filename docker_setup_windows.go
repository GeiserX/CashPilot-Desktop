//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
	"golang.org/x/sys/windows"
)

// systemSetupRunner makes the checklist's changes on this PC.
type systemSetupRunner struct{}

// systemTool is a tool in System32, which only administrators can write to.
func systemTool(parts ...string) (string, error) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{dir}, parts...)...), nil
}

func (systemSetupRunner) elevated(ctx context.Context, script string) (runtime.ElevatedResult, error) {
	return runtime.RunElevatedContext(ctx, script)
}

// installDocker opens a PowerShell window the user sees, running winget as the
// user (winvm.DockerInstallScript). It waits until the user closes the window,
// or until ctx ends; then it stops waiting and leaves the window alone.
func (systemSetupRunner) installDocker(ctx context.Context) (runtime.ElevatedResult, error) {
	ps, err := systemTool("WindowsPowerShell", "v1.0", "powershell.exe")
	if err != nil {
		return runtime.ElevatedResult{}, err
	}
	cmd := exec.Command(ps, "-NoProfile", "-EncodedCommand", runtime.EncodeCommand(winvm.DockerInstallScript))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
	if err := cmd.Start(); err != nil {
		return runtime.ElevatedResult{}, err
	}
	return restartCodes(waitProcess(ctx, cmd.Wait))
}

// switchToLinux runs Docker's own DockerCli.exe -SwitchLinuxEngine as the user,
// hidden. It lives next to Docker Desktop.exe; a PC without it gets the menu
// instructions instead.
func (systemSetupRunner) switchToLinux(ctx context.Context) error {
	exe, err := dockerDesktopExe()
	if err != nil {
		return errDockerCliMissing
	}
	cli := filepath.Join(filepath.Dir(exe), "DockerCli.exe")
	if _, err := os.Stat(cli); err != nil {
		return errDockerCliMissing
	}
	cmd := exec.CommandContext(ctx, cli, "-SwitchLinuxEngine")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd.Run()
}

// restart asks Windows to restart now. /t 0 without /f: a timeout above 0 implies
// /f, which closes apps with unsaved work. Without it Windows asks each app to
// close and shows the ones that hold out.
func (systemSetupRunner) restart() error {
	shutdown, err := systemTool("shutdown.exe")
	if err != nil {
		return err
	}
	cmd := exec.Command(shutdown, "/r", "/t", "0")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd.Run()
}

// signOut signs the user out now, without /f, so apps with unsaved work can ask.
func (systemSetupRunner) signOut() error {
	shutdown, err := systemTool("shutdown.exe")
	if err != nil {
		return err
	}
	cmd := exec.Command(shutdown, "/l")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd.Run()
}
