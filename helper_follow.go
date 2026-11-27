package main

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/bgservice"
)

// The background helper is a login task that starts this executable by its full
// path. A user who moves from the portable exe to the installed app, or moves the
// exe to another folder, would otherwise keep a task that starts the old copy, or
// a file that is gone. So each time the window opens, a registered helper that
// starts another path is pointed at the copy the user just opened. The copy you
// open last is the one the helper runs. The data folder is the same for every
// copy (config.AppDataDir), so nothing is lost either way.

// followThisCopy points a registered helper at exe when it starts another file.
// It reports whether it changed the registration. It never registers a helper
// that is not there: that stays the user's choice in Settings.
func followThisCopy(agent bgservice.Agent, exe string) (bool, error) {
	reporter, ok := agent.(bgservice.CommandReporter)
	if !ok || exe == "" {
		return false, nil
	}
	registered, err := reporter.RegisteredCommand()
	if err != nil || registered == "" || samePath(registered, exe) {
		return false, err
	}
	if err := agent.Install(exe, []string{"--daemon"}); err != nil {
		return false, err
	}
	return true, nil
}

// samePath compares two executable paths the way this OS does: Windows paths are
// not case-sensitive.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if goruntime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// helperVersionFile is the file in the data folder where a running helper
// records the version it started as (writeHelperVersion).
const helperVersionFile = "helper-version"

// writeHelperVersion records the version this helper started as, so the window
// can tell when the helper still runs an older build. 1.x helpers never write it.
func writeHelperVersion(dir string) {
	if err := os.WriteFile(filepath.Join(dir, helperVersionFile), []byte(AppVersion()), 0o600); err != nil {
		log.Printf("background helper: could not record its version: %v", err)
	}
}

// restartOlderHelper restarts a running helper that started as another build
// than version. An update replaces the file under a running helper on macOS and
// Linux, and the old process keeps running until the next sign-in. Both then send
// heartbeats as this computer: the server's Fleet row swaps between the two
// versions, and once server commands are on, its address comes and goes, because
// a 1.x helper sends none. The restart starts the registered file, which is the
// new build. It reports whether it restarted. A build that does not know its own
// version (a development build) changes nothing.
func restartOlderHelper(agent bgservice.Agent, dir, version string) (bool, error) {
	restarter, ok := agent.(bgservice.Restarter)
	if !ok || version == "" {
		return false, nil
	}
	st, err := agent.Status()
	if err != nil || !st.Installed || !st.Running {
		return false, err
	}
	recorded, err := os.ReadFile(filepath.Join(dir, helperVersionFile))
	if err == nil && strings.TrimSpace(string(recorded)) == version {
		return false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := restarter.Restart(); err != nil {
		return false, err
	}
	return true, nil
}

// keepHelperOnThisCopy runs followThisCopy and then restartOlderHelper for the
// window. A failure is logged only: the helper keeps running as it did before.
func (a *App) keepHelperOnThisCopy() {
	agent, err := a.backgroundAgent()
	if err != nil {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	moved, err := followThisCopy(agent, exe)
	if err != nil {
		log.Printf("background helper: could not point it at %s: %v", exe, err)
		return
	}
	if moved {
		log.Printf("background helper: now starts %s, the copy that was opened", exe)
	}
	restarted, err := restartOlderHelper(agent, a.cfg.AppDir(), AppVersion())
	if err != nil {
		log.Printf("background helper: could not restart it on this build: %v", err)
		return
	}
	if restarted {
		log.Printf("background helper: restarted, because it ran another build than %s", AppVersion())
	}
}
