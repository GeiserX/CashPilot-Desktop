//go:build !windows

package main

import "context"

const firmwareRestartSupported = false

func launchDockerDesktop() error { return errWindowsOnly }

func openWindowsUpdate() error { return errWindowsOnly }

func restartIntoFirmware() error { return errWindowsOnly }

func dockerDesktopEngineReady(context.Context) bool { return false }
