//go:build !darwin

package main

// InstallTrayIcon is currently implemented natively on macOS.
func InstallTrayIcon(_ []byte) {}

// PositionMainWindowOnPrimaryScreen is only needed for macOS multi-monitor recovery.
func PositionMainWindowOnPrimaryScreen() {}
