package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/GeiserX/CashPilot-Desktop/internal/bgservice"
)

// The background helper is a login task that starts an executable by its full
// path. Two things leave it running something other than the copy the user just
// opened:
//
//   - An update replaces the file under a running helper on macOS and Linux. The
//     old process keeps running until the next sign-in. Both then send heartbeats
//     as this computer: the server's Fleet row swaps between the two versions,
//     and once server commands are on its address comes and goes, because a 1.x
//     helper sends none.
//   - The user moves from the portable exe to the installed app, or moves the app
//     to another folder, and the task still starts the old copy, or a file that
//     is gone.
//
// So each time the window opens, it checks the record the running helper wrote
// when it started (writeHelperRecord). Unless a live process matches that record
// and runs this executable at this version, the window registers this copy again
// and replaces the running helper with it. The copy you open last is the one the
// helper runs. The data folder is the same for every copy (config.AppDataDir), so
// nothing is lost either way.

// helperRecordFile is the file in the data folder where a running helper records
// who it is. 1.x helpers never write it.
const helperRecordFile = "helper-version"

// helperRecord says which process the running helper is and what it runs. The
// PID with its start time names one process: a record left by a helper that has
// since exited, or crashed, matches no live process.
type helperRecord struct {
	Version string `json:"version"`
	Exe     string `json:"exe"`
	PID     int    `json:"pid"`
	Started int64  `json:"started"` // process start time, ms since the epoch
}

// processStart returns a live process's start time, or false when it is gone.
// A variable so tests can name processes that do not exist.
var processStart = func(pid int) (int64, bool) {
	proc, err := process.NewProcess(int32(pid))
	if err != nil {
		return 0, false
	}
	started, err := proc.CreateTime()
	return started, err == nil
}

// writeHelperRecord records the running helper. The daemon calls it first, before
// the core starts, so a window that opens during the start does not take a
// helper that is starting for an old one.
func writeHelperRecord(dir string) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("background helper: could not record itself: %v", err)
		return
	}
	rec := helperRecord{Version: AppVersion(), Exe: exe, PID: os.Getpid()}
	if started, ok := processStart(rec.PID); ok {
		rec.Started = started
	}
	data, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, helperRecordFile), data, 0o600); err != nil {
		log.Printf("background helper: could not record itself: %v", err)
	}
}

// helperIsCurrent reports whether a live helper process runs exe at version,
// according to the record in dir.
func helperIsCurrent(dir, exe, version string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, helperRecordFile))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var rec helperRecord
	if json.Unmarshal(data, &rec) != nil || rec.PID <= 0 || rec.Started == 0 {
		return false, nil
	}
	if rec.Version != version || !samePath(rec.Exe, exe) {
		return false, nil
	}
	started, alive := processStart(rec.PID)
	return alive && started == rec.Started, nil
}

// replaceOlderHelper registers exe as the helper and replaces the running one,
// unless the helper already runs exe at version. It reports whether it replaced
// the helper. It never registers a helper that is not there: that stays the
// user's choice in Settings.
func replaceOlderHelper(agent bgservice.Agent, exe, dir, version string) (bool, error) {
	replacer, ok := agent.(bgservice.Replacer)
	if !ok || exe == "" {
		return false, nil
	}
	st, err := agent.Status()
	if err != nil || !st.Installed {
		return false, err
	}
	current, err := helperIsCurrent(dir, exe, version)
	if err != nil || current {
		return false, err
	}
	if err := replacer.Replace(exe, []string{"--daemon"}); err != nil {
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

// keepHelperOnThisCopy runs replaceOlderHelper for the window. A failure is
// logged only: the helper keeps running as it did before.
func (a *App) keepHelperOnThisCopy() {
	agent, err := a.backgroundAgent()
	if err != nil {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	logHelperReplace(replaceOlderHelper(agent, exe, a.cfg.AppDir(), AppVersion()))
}

func logHelperReplace(replaced bool, err error) {
	if err != nil {
		log.Printf("background helper: could not move it to this copy: %v", err)
	} else if replaced {
		log.Printf("background helper: now runs this copy, version %q", AppVersion())
	}
}
