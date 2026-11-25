//go:build !windows

package main

const firmwareRestartSupported = false

func launchDockerDesktop() error { return errWindowsOnly }

func openWindowsUpdate() error { return errWindowsOnly }

func restartIntoFirmware() error { return errWindowsOnly }
