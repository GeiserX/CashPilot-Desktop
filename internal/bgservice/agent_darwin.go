//go:build darwin

package bgservice

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// darwinAgent registers the helper as a per-user launchd LaunchAgent — no admin, shows in
// Login Items, revived by launchd (KeepAlive) after a crash and started at login
// (RunAtLoad). This mirrors how Docker Desktop and Tailscale keep their engines alive.
type darwinAgent struct {
	label  string
	logDir string
}

// New returns the macOS login agent. logDir receives the helper's captured stdout/stderr.
func New(label, logDir string) Agent {
	return &darwinAgent{label: label, logDir: logDir}
}

func (a *darwinAgent) plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", a.label+".plist"), nil
}

func (a *darwinAgent) stdoutPath() string { return filepath.Join(a.logDir, "helper.out.log") }
func (a *darwinAgent) stderrPath() string { return filepath.Join(a.logDir, "helper.err.log") }

func (a *darwinAgent) Install(execPath string, args []string) error {
	plistPath, err := a.plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if a.logDir != "" {
		if err := os.MkdirAll(a.logDir, 0o700); err != nil {
			return fmt.Errorf("create log dir: %w", err)
		}
	}
	content := plistLaunchAgent(a.label, execPath, args, a.stdoutPath(), a.stderrPath())
	if err := os.WriteFile(plistPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// bootstrap is the modern per-user-domain load; fall back to the legacy load -w on
	// older macOS. The plist is left on disk either way so a reboot reloads it.
	target := fmt.Sprintf("gui/%d", os.Getuid())
	if out, err := runLaunchctl("bootstrap", target, plistPath); err != nil {
		if out2, err2 := runLaunchctl("load", "-w", plistPath); err2 != nil {
			return fmt.Errorf("launchctl bootstrap failed (%v: %s) and load -w fallback failed (%v: %s)",
				err, strings.TrimSpace(out), err2, strings.TrimSpace(out2))
		}
	}
	return nil
}

func (a *darwinAgent) Uninstall() error {
	plistPath, err := a.plistPath()
	if err != nil {
		return err
	}
	// bootout is the modern unload; fall back to the legacy unload -w. A "not loaded"
	// error is fine — the desired end state (not loaded) is already met — so we ignore
	// the fallback's error and always remove the plist so a reboot cannot reload it.
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), a.label)
	if _, err := runLaunchctl("bootout", target); err != nil {
		_, _ = runLaunchctl("unload", "-w", plistPath)
	}
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func (a *darwinAgent) Status() (Status, error) {
	st := Status{Label: a.label}
	plistPath, err := a.plistPath()
	if err != nil {
		return st, err
	}
	if _, err := os.Stat(plistPath); err == nil {
		st.Installed = true
	} else if !os.IsNotExist(err) {
		return st, err
	}
	// A successful `launchctl print gui/<uid>/<label>` means the agent is loaded (so it
	// is installed even if the file stat raced) and its output tells us if it is running.
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), a.label)
	if out, err := runLaunchctl("print", target); err == nil {
		st.Installed = true
		st.Running = launchctlPrintRunning(out)
	}
	return st, nil
}

func runLaunchctl(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	return string(out), err
}
