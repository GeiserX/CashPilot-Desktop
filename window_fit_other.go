//go:build !windows

package main

import "context"

// fitMainWindow keeps the window inside the work area on Windows only. macOS
// places it itself (PositionMainWindowOnPrimaryScreen), and on Linux the window
// manager does.
func fitMainWindow(context.Context) {}
