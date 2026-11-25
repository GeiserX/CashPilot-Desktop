package main

import (
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

// keepHelperOnThisCopy runs followThisCopy for the window. A failure is logged
// only: the helper keeps its old path, which is what happened before.
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
}
