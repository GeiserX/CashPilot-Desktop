//go:build linux

package bgservice

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// linuxUnitName is the systemd --user unit filename. The reverse-DNS DefaultLabel is kept
// for the human-facing Status.Label, but the unit uses a conventional, dash-cased name.
const linuxUnitName = "cashpilot-helper.service"

// linuxAgent registers the helper as a systemd --user unit (Restart=always) plus a
// best-effort `loginctl enable-linger` so it can run pre-login/headless across a reboot —
// the only OS in this design that survives without a graphical login.
type linuxAgent struct {
	label    string
	logDir   string
	unitName string
}

// New returns the Linux login agent. logDir is retained for parity with the other OSes;
// systemd captures the helper's output via the journal rather than an explicit file.
func New(label, logDir string) Agent {
	return &linuxAgent{label: label, logDir: logDir, unitName: linuxUnitName}
}

func (a *linuxAgent) unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", a.unitName), nil
}

func (a *linuxAgent) Install(execPath string, args []string) error {
	if !systemctlUserAvailable() {
		return errors.New("systemd --user is not available on this system (no user service manager); a fallback supervisor is a later task")
	}
	unitPath, err := a.unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}
	content := systemdUserUnit(execPath, args, helperDescription)
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	if out, err := runSystemctlUser("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %v: %s", err, strings.TrimSpace(out))
	}
	if out, err := runSystemctlUser("enable", "--now", a.unitName); err != nil {
		return fmt.Errorf("systemctl --user enable --now: %v: %s", err, strings.TrimSpace(out))
	}
	// Best-effort: let the user manager run at boot and persist across logout. Some
	// polkit configurations deny linger without admin — that is non-fatal (the helper
	// still starts at login), so log a note instead of failing the install.
	if out, err := enableLinger(); err != nil {
		log.Printf("bgservice: loginctl enable-linger failed (helper still starts at login, just not pre-login): %v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func (a *linuxAgent) Uninstall() error {
	if !systemctlUserAvailable() {
		return errors.New("systemd --user is not available on this system (no user service manager)")
	}
	// disable --now stops the unit and drops its enable symlink; ignore its error (the
	// unit may already be stopped/absent).
	_, _ = runSystemctlUser("disable", "--now", a.unitName)
	unitPath, err := a.unitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}
	if out, err := runSystemctlUser("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func (a *linuxAgent) Status() (Status, error) {
	st := Status{Label: a.unitName}
	unitPath, err := a.unitPath()
	if err != nil {
		return st, err
	}
	if _, err := os.Stat(unitPath); err == nil {
		st.Installed = true
	} else if !os.IsNotExist(err) {
		return st, err
	}
	if !systemctlUserAvailable() {
		return st, nil
	}
	// is-enabled / is-active exit non-zero when false — that is not an error for Status.
	if out, _ := runSystemctlUser("is-enabled", a.unitName); systemdEnabled(out) {
		st.Installed = true
	}
	if out, _ := runSystemctlUser("is-active", a.unitName); systemdActive(out) {
		st.Running = true
	}
	return st, nil
}

func runSystemctlUser(args ...string) (string, error) {
	full := append([]string{"--user"}, args...)
	out, err := exec.Command("systemctl", full...).CombinedOutput()
	return string(out), err
}

// systemctlUserAvailable reports whether a systemd --user manager can be reached. It
// needs both the binary and a live user instance (a running session bus); without a user
// instance `systemctl --user` fails with "Failed to connect to bus".
func systemctlUserAvailable() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	if err := exec.Command("systemctl", "--user", "show", "--property=Version").Run(); err != nil {
		return false
	}
	return true
}

func enableLinger() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	out, err := exec.Command("loginctl", "enable-linger", u.Username).CombinedOutput()
	return string(out), err
}
