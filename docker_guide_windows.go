//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"golang.org/x/sys/windows"
)

const firmwareRestartSupported = true

// dockerDesktopExe finds Docker Desktop where its installers put it: for all
// users under Program Files, or for this user under LocalAppData.
func dockerDesktopExe() (string, error) {
	var paths []string
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		paths = append(paths, filepath.Join(pf, "Docker", "Docker", "Docker Desktop.exe"))
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		paths = append(paths, filepath.Join(local, "Programs", "DockerDesktop", "Docker Desktop.exe"))
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("Docker Desktop is not installed where its installer puts it")
}

// launchDockerDesktop starts Docker Desktop as this user, with no admin rights.
// Docker Desktop runs once per user, so a second start shows its window. The app
// does not wait for it: the checklist waits for the engine instead.
func launchDockerDesktop() error {
	exe, err := dockerDesktopExe()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func dockerDesktopEngineReady(ctx context.Context) bool {
	reason := runtime.DockerDesktopEngineReason(ctx)
	return reason == runtime.ReasonRunning || reason == runtime.ReasonWindowsContainers
}

// openWindowsUpdate opens Settings at Windows Update.
func openWindowsUpdate() error {
	verb, _ := windows.UTF16PtrFromString("open")
	target, _ := windows.UTF16PtrFromString("ms-settings:windowsupdate")
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

// restartIntoFirmware asks Windows to restart now, into the firmware settings.
// /t 0 restarts at once without /f: a timeout above 0 implies /f, which closes
// apps with unsaved work. A PC with a legacy BIOS, or a policy that refuses,
// makes shutdown exit non-zero, and nothing restarts.
func restartIntoFirmware() error {
	shutdown, err := systemTool("shutdown.exe")
	if err != nil {
		return err
	}
	cmd := exec.Command(shutdown, "/r", "/fw", "/t", "0")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd.Run()
}
