//go:build !darwin

package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/GeiserX/CashPilot-Desktop/internal/securevm"
)

func goos() string { return runtime.GOOS }

// The Windows engine (internal/winvm) plugs in here once it is wired.
func newSecureVMEngine(*App) (securevm.Engine, error) { return nil, nil }

func runMacVMMain(string, []string) int {
	fmt.Fprintln(os.Stderr, "--macvm runs only on macOS")
	return 2
}
