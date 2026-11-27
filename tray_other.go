//go:build !darwin

package main

// InstallTrayIcon is currently implemented natively on macOS.
func InstallTrayIcon(_ []byte, _, _ string) {}

// SetTrayLabels has no tray to rename off macOS.
func SetTrayLabels(_, _ string) {}

// PositionMainWindowOnPrimaryScreen is only needed for macOS multi-monitor recovery.
func PositionMainWindowOnPrimaryScreen() {}
