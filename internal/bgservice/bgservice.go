// Package bgservice registers the CashPilot executable with the current user's OS
// service manager so its headless --daemon supervisor auto-starts at login, restarts on
// crash, and survives the GUI being closed and a reboot — Docker-parity native
// supervision with no admin (see docs/NATIVE-SUPERVISION.md, Phase B).
//
// The OS keeps ONE process alive (our signed binary, run with --daemon); that helper in
// turn supervises the third-party earners. This package only handles the "keep the
// helper alive" registration; the supervisor itself lives in internal/runtime.
//
// Each supported OS has its own implementation behind a build tag:
//
//	agent_darwin.go   launchd LaunchAgent  (~/Library/LaunchAgents/<label>.plist + launchctl)
//	agent_linux.go    systemd --user unit  (~/.config/systemd/user/<name>.service + systemctl --user)
//	agent_windows.go  per-user Scheduled Task (schtasks /create /xml, LogonTrigger)
//
// The file-content generation (plist / unit / task XML) is kept in render.go and the
// service-manager output parsing in parse.go — both free of any build tag and any
// shell-out — so they are unit-testable on any CI runner regardless of GOOS.
package bgservice

// DefaultLabel is the stable reverse-DNS identifier for the CashPilot login agent. It is
// the launchd Label on macOS and is reused as the human-facing Status.Label on the other
// OSes (which key their registration off a service-manager-friendly name derived below).
const DefaultLabel = "cloud.cashpilot.desktop.helper"

// helperDescription is the human-readable description written into the systemd unit and
// the Windows scheduled task.
const helperDescription = "CashPilot Desktop background helper (native earner supervisor)"

// Status is a snapshot of the login agent's registration state.
type Status struct {
	// Installed is true when the agent is registered with the OS service manager
	// (its plist / unit / scheduled task exists).
	Installed bool `json:"installed"`
	// Running is true when the service manager reports the helper process as currently
	// alive (has a live PID / active state).
	Running bool `json:"running"`
	// Label is the OS-native identifier for the registration (launchd label, systemd
	// unit name, or scheduled-task path) — what you would use to inspect it by hand.
	Label string `json:"label"`
}

// Agent registers, removes, and reports on the CashPilot login agent for one OS.
type Agent interface {
	// Install registers the binary at execPath to run with args (["--daemon"]) at
	// login, kept alive by the OS. It is idempotent-friendly but explicit: callers
	// invoke it in response to a user action, never automatically on startup.
	Install(execPath string, args []string) error
	// Uninstall removes the registration and its backing file, leaving the system as
	// if Install had never run. Removing an absent agent is not an error.
	Uninstall() error
	// Status reports whether the agent is installed and currently running.
	Status() (Status, error)
}

// Stopper is an Agent that can stop the helper it registered while it runs. The
// uninstaller stops it before it removes the registration, because removing a
// Windows Scheduled Task does not end the process it started.
type Stopper interface {
	Stop() error
}

// Replacer is an Agent that can register execPath and make the running helper be
// that file: it stops a helper that runs, whatever file it started from, and
// starts execPath. An update replaces the file under a running helper on macOS
// and Linux, and the old process keeps running until the next sign-in. The
// window replaces it (helper_follow.go).
type Replacer interface {
	Replace(execPath string, args []string) error
}
