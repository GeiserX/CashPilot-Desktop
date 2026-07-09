//go:build windows

package bgservice

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// windowsTaskName is the scheduled-task path (folder\name). schtasks accepts a
// backslash-separated path to nest the task under a "CashPilot" folder.
const windowsTaskName = `CashPilot\Helper`

// windowsAgent registers the helper as a per-user Scheduled Task with a LogonTrigger and
// an interactive token (RunLevel=LeastPrivilege) — no admin. RestartOnFailure revives it
// and ExecutionTimeLimit=PT0S keeps the long-lived helper from being force-stopped.
type windowsAgent struct {
	label    string
	logDir   string
	taskName string
}

// New returns the Windows login agent. logDir is retained for parity with the other OSes;
// the scheduled task does not redirect output to an explicit file.
func New(label, logDir string) Agent {
	return &windowsAgent{label: label, logDir: logDir, taskName: windowsTaskName}
}

func (a *windowsAgent) Install(execPath string, args []string) error {
	userID := ""
	if u, err := user.Current(); err == nil {
		userID = u.Username
	}
	xmlDoc := scheduledTaskXML(execPath, args, userID, helperDescription)

	tmp, err := os.CreateTemp("", "cashpilot-task-*.xml")
	if err != nil {
		return fmt.Errorf("create temp task xml: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	// schtasks expects the task XML as UTF-16 (matching the declaration).
	if _, err := tmp.Write(utf16LEWithBOM(xmlDoc)); err != nil {
		tmp.Close()
		return fmt.Errorf("write task xml: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close task xml: %w", err)
	}

	if out, err := runSchtasks("/create", "/tn", a.taskName, "/xml", tmpPath, "/f"); err != nil {
		return fmt.Errorf("schtasks /create: %v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func (a *windowsAgent) Uninstall() error {
	out, err := runSchtasks("/delete", "/tn", a.taskName, "/f")
	if err != nil {
		// A missing task is the desired end state; only surface other failures.
		lower := strings.ToLower(out)
		if strings.Contains(lower, "cannot find") || strings.Contains(lower, "does not exist") {
			return nil
		}
		return fmt.Errorf("schtasks /delete: %v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func (a *windowsAgent) Status() (Status, error) {
	st := Status{Label: a.taskName}
	out, err := runSchtasks("/query", "/tn", a.taskName, "/fo", "list")
	if err != nil {
		// A non-zero exit means the task was not found → not installed (not an error).
		return st, nil
	}
	st.Installed = true
	st.Running = schtasksRunning(out)
	return st, nil
}

func runSchtasks(args ...string) (string, error) {
	out, err := exec.Command("schtasks", args...).CombinedOutput()
	return string(out), err
}
