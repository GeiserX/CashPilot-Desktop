//go:build !darwin

package main

// InstallTrayIcon is currently implemented natively on macOS.
func InstallTrayIcon(_ []byte) {}
