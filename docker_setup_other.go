//go:build !windows

package main

import (
	"context"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// systemSetupRunner has nothing to change outside Windows.
type systemSetupRunner struct{}

func (systemSetupRunner) elevated(context.Context, string) (runtime.ElevatedResult, error) {
	return runtime.ElevatedResult{}, errWindowsOnly
}

func (systemSetupRunner) installDocker(context.Context) (runtime.ElevatedResult, error) {
	return runtime.ElevatedResult{}, errWindowsOnly
}

func (systemSetupRunner) switchToLinux(context.Context) error { return errWindowsOnly }

func (systemSetupRunner) restart() error { return errWindowsOnly }

func (systemSetupRunner) signOut() error { return errWindowsOnly }
