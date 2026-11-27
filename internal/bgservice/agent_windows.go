//go:build windows

package bgservice

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
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

// Stop ends the helper the task started. A task that is not running is not an
// error worth reporting: the caller removes the task next either way.
func (a *windowsAgent) Stop() error {
	if out, err := runSchtasks("/end", "/tn", a.taskName); err != nil {
		return fmt.Errorf("schtasks /end: %v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// Replace ends the running helper, registers execPath and runs the task. The task
// ignores a start while an instance runs (MultipleInstancesPolicy IgnoreNew), so
// it waits until Task Scheduler no longer reports the old instance running. A
// task that is not running makes /end fail, which is fine. If the state query
// itself fails, Replace still registers and runs the new copy, and reports the
// query error: /end has already stopped the old helper, so returning early would
// leave no helper running. If the new registration cannot be written, Replace
// starts the old one again.
func (a *windowsAgent) Replace(execPath string, args []string) error {
	_ = a.Stop()
	var queryErr error
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		running, err := a.taskRunning()
		if err != nil {
			queryErr = fmt.Errorf("could not read the helper task state: %w", err)
			break
		}
		if !running {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the helper task %s did not stop", a.taskName)
		}
	}
	if err := a.Install(execPath, args); err != nil {
		// Install failed before or at /create, so the old registration is still
		// there: start it again rather than leave no helper running.
		if out, runErr := runSchtasks("/run", "/tn", a.taskName); runErr != nil {
			return errors.Join(queryErr, err, fmt.Errorf("schtasks /run: %v: %s", runErr, strings.TrimSpace(out)))
		}
		return errors.Join(queryErr, err)
	}
	if out, err := runSchtasks("/run", "/tn", a.taskName); err != nil {
		return errors.Join(queryErr, fmt.Errorf("schtasks /run: %v: %s", err, strings.TrimSpace(out)))
	}
	return queryErr
}

// taskRunning asks Task Scheduler whether the task runs now. It reads the task's
// State number through PowerShell, which is the same in every Windows language;
// schtasks prints its status in the system language.
func (a *windowsAgent) taskRunning() (bool, error) {
	exe, err := runtime.PowerShellExe()
	if err != nil {
		return false, err
	}
	cmd := exec.Command(exe, "-NoProfile", "-NonInteractive", "-EncodedCommand", psEncode(taskStateScript(a.taskName)))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("read the state of the scheduled task %s: %w", a.taskName, err)
	}
	return taskStateRunning(string(out)), nil
}

// RegisteredCommand reads the executable the task starts, or "" when there is no
// task. It asks Task Scheduler through PowerShell and reads UTF-8, because the
// path can hold any character of the user's name and schtasks writes in the
// console's code page.
func (a *windowsAgent) RegisteredCommand() (string, error) {
	exe, err := runtime.PowerShellExe()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(exe, "-NoProfile", "-NonInteractive", "-EncodedCommand", psEncode(taskCommandScript(a.taskName)))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.Output()
	if err != nil {
		var detail string
		if ee, ok := err.(*exec.ExitError); ok {
			detail = ": " + strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("read the scheduled task %s: %w%s", a.taskName, err, detail)
	}
	return parseTaskCommand(string(out)), nil
}

// psEncode is the -EncodedCommand form of a script: base64 of its UTF-16LE bytes.
func psEncode(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[2*i:], u)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func runSchtasks(args ...string) (string, error) {
	out, err := exec.Command("schtasks", args...).CombinedOutput()
	return string(out), err
}
