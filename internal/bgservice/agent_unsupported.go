//go:build !darwin && !linux && !windows

package bgservice

import (
	"fmt"
	"runtime"
)

// unsupportedAgent is the fallback for any OS without a login-agent implementation, so the
// package (and any code that constructs an Agent) still builds everywhere. Its mutating
// operations fail clearly; Status simply reports "not installed".
type unsupportedAgent struct{ label string }

// New returns the no-op agent for unsupported platforms.
func New(label, logDir string) Agent {
	_ = logDir
	return &unsupportedAgent{label: label}
}

func (a *unsupportedAgent) Install(string, []string) error {
	return fmt.Errorf("background helper registration is not supported on %s", runtime.GOOS)
}

func (a *unsupportedAgent) Uninstall() error {
	return fmt.Errorf("background helper registration is not supported on %s", runtime.GOOS)
}

func (a *unsupportedAgent) Status() (Status, error) {
	return Status{Label: a.label}, nil
}
