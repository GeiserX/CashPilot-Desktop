// Package setupresume brings a guided Windows setup back after a restart: the
// journal that says where to land, the RunOnce value that reopens the app at the
// next sign-in, and the decisions made from both.
//
// The decisions are pure and test on every OS. Only RunOnce touches Windows, in
// runonce_windows.go; elsewhere Arm and Disarm do nothing.
package setupresume

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
)

// Flag is the command-line flag RunOnce passes: --resume-setup.
const Flag = "resume-setup"

// FlowDocker is the Docker checklist.
const FlowDocker = "docker"

// What a step waits for before it is finished (config.SetupJournal.Finish).
const (
	FinishRestart = "restart"
	FinishSignOut = "signout"
)

// RunOnceKey is the signed-in user's RunOnce key, under HKCU. HKLM's runs only
// for administrators, so it is never used.
const RunOnceKey = `Software\Microsoft\Windows\CurrentVersion\RunOnce`

// ValueName is the RunOnce value. The leading "!" makes Windows delete it only
// after the command ran, so a failed start keeps it for the next sign-in
// (https://learn.microsoft.com/en-us/windows/win32/setupapi/run-and-runonce-registry-keys).
const ValueName = "!CashPilotResumeSetup"

// MaxCommand is the longest command line a RunOnce value may hold.
const MaxCommand = 260

// Restarted values.
const (
	RestartedYes     = "yes"     // the PC booted since the journal was written
	RestartedNo      = "no"      // same boot: the restart is still to come
	RestartedUnknown = "unknown" // a boot time could not be read
)

var windowsAbs = regexp.MustCompile(`^(?:[A-Za-z]:\\|\\\\)`)

// Command builds the RunOnce command for exe: the path in quotes, then
// --resume-setup. It refuses a path that is not absolute, holds a quote, or makes
// the command longer than MaxCommand characters.
func Command(exe string) (string, error) {
	if !windowsAbs.MatchString(exe) {
		return "", fmt.Errorf("%q is not an absolute Windows path", exe)
	}
	if strings.ContainsAny(exe, "\"\r\n\x00") {
		return "", errors.New("the app's path holds a quote or a line break")
	}
	cmd := `"` + exe + `" --` + Flag
	if n := len(utf16.Encode([]rune(cmd))); n > MaxCommand {
		return "", fmt.Errorf("the command is %d characters long; RunOnce takes at most %d", n, MaxCommand)
	}
	return cmd, nil
}

// HasFlag reports whether a command line carries --resume-setup (or the
// one-dash form the flag package also takes).
func HasFlag(args []string) bool {
	for _, a := range args {
		if a == "--"+Flag || a == "-"+Flag {
			return true
		}
	}
	return false
}

// Next is the journal to write before a restart that step of flow asks for:
// the boot time now, the app's path, and one more attempt when the same step
// asked before.
func Next(prev *config.SetupJournal, flow, step, bootNow, exe string) *config.SetupJournal {
	attempts := 1
	if prev != nil && prev.Flow == flow && prev.Step == step {
		attempts = prev.Attempts + 1
	}
	return &config.SetupJournal{Flow: flow, Step: step, BootTimeBefore: bootNow, ExePath: exe, Attempts: attempts}
}

// View is what the app does on start: whether to land on the checklist, and what
// the restart did.
type View struct {
	// Active is true with a journal, or when started with --resume-setup: land on
	// the checklist and run every check again.
	Active bool   `json:"active"`
	Flow   string `json:"flow"`
	Step   string `json:"step"`
	// Restarted compares the boot time now with the journal's.
	Restarted string `json:"restarted"`
	// ExeMoved is true when the app that wrote the journal is no longer at its
	// path, so RunOnce could not have opened it.
	ExeMoved    bool `json:"exeMoved"`
	Attempts    int  `json:"attempts"`
	FromRunOnce bool `json:"fromRunOnce"`
	// Finish is what the step waits for: FinishRestart or FinishSignOut.
	Finish string `json:"finish"`
	// Rearm is true while the restart is still to come: RunOnce must point at this
	// app again, so the restart reopens it. Otherwise the value is removed: it ran,
	// or it points at a path that is gone and would fail at every sign-in.
	Rearm bool `json:"-"`
}

// Decide reads the journal against the boot time now. flag is whether the app
// was started with --resume-setup; exists says whether a file is there.
func Decide(j *config.SetupJournal, bootNow string, flag bool, exists func(string) bool) View {
	if j == nil {
		if !flag {
			return View{}
		}
		return View{Active: true, Flow: FlowDocker, Restarted: RestartedUnknown, FromRunOnce: true}
	}
	v := View{
		Active:      true,
		Flow:        j.Flow,
		Step:        j.Step,
		Restarted:   restarted(j.BootTimeBefore, bootNow),
		Attempts:    j.Attempts,
		FromRunOnce: flag,
		Finish:      FinishRestart,
	}
	if j.Finish == FinishSignOut {
		v.Finish = FinishSignOut
	}
	v.ExeMoved = j.ExePath != "" && exists != nil && !exists(j.ExePath)
	// RunOnce runs at the next sign-in. A sign-out keeps the boot time, so for a
	// step that waits for one, a start by RunOnce in the same boot is the proof
	// that the sign-in happened: the value ran and is not armed again.
	v.Rearm = v.Restarted == RestartedNo && !(v.Finish == FinishSignOut && flag)
	return v
}

// bootSlack absorbs rounding between two readings of one boot time.
const bootSlack = time.Second

// restarted compares two boot times. Equal is the same boot; later is a restart;
// anything unreadable, or a boot time that went back, is unknown.
func restarted(before, now string) string {
	b, err1 := time.Parse(time.RFC3339Nano, before)
	n, err2 := time.Parse(time.RFC3339Nano, now)
	if err1 != nil || err2 != nil {
		return RestartedUnknown
	}
	d := n.Sub(b)
	switch {
	case d < -bootSlack:
		return RestartedUnknown
	case d <= bootSlack:
		return RestartedNo
	}
	return RestartedYes
}
