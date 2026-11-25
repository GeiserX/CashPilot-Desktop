package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/setupresume"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// The consented changes of the Docker checklist (internal/winvm/setupsteps.go):
// turn on WSL, update it, install Docker Desktop, join docker-users, switch to
// Linux containers, turn Windows' hypervisor back on, and set the Server service
// to Automatic. The frontend calls RunSetupStep only from a change's confirm
// screen. RunSetupStep reads the PC again first and runs the change only while
// its row still asks for it, so a stale screen, or a docker-users add that no
// access-denied check proved needed, runs nothing.
//
// A change that needs a restart, or a sign-out, writes the setup journal and the
// RunOnce value before it answers, so the restart button the frontend shows next
// always finds them. Nothing here restarts or signs out by itself: RestartNow and
// SignOutNow run only from their own click.

// SetupStepResult is how a change ended, for the row that asked for it.
type SetupStepResult struct {
	// Step is the checklist row, Op the change.
	Step string `json:"step"`
	Op   string `json:"op"`
	// State is one of winvm's Outcome states; Text says it in plain words.
	State string `json:"state"`
	Text  string `json:"text"`
	Code  string `json:"code,omitempty"`
	// Retry is "web-download" when a WSL change may work through a direct download.
	Retry string `json:"retry,omitempty"`
	// URL is the page a fallback opens.
	URL string `json:"url,omitempty"`
	// Reopens is true when Windows will reopen CashPilot at the next sign-in.
	Reopens bool `json:"reopens"`
}

// setupRunner makes the changes. The real one is systemSetupRunner (Windows
// only); the package's tests replace defaultSetupRunner, so no test raises an
// admin prompt, opens winget or restarts the machine.
type setupRunner interface {
	elevated(ctx context.Context, script string) (runtime.ElevatedResult, error)
	installDocker(ctx context.Context) (runtime.ElevatedResult, error)
	switchToLinux(ctx context.Context) error
	restart() error
	signOut() error
}

var defaultSetupRunner setupRunner = systemSetupRunner{}

// setupTimeout is each change's time limit; a test shortens it.
var setupTimeout = winvm.SetupTimeout

// setupMu lets one change run at a time.
var setupMu sync.Mutex

// errDockerCliMissing is the switch to Linux containers on a PC where Docker's
// DockerCli.exe is not next to Docker Desktop.
var errDockerCliMissing = errors.New("DockerCli.exe is not next to Docker Desktop")

// RunSetupStep makes the change op, with webDownload for a WSL retry through a
// direct download. It returns how it ended; an error means it could not start.
func (a *App) RunSetupStep(op string, webDownload bool) (SetupStepResult, error) {
	if err := a.ready(); err != nil {
		return SetupStepResult{}, err
	}
	stepID, ok := winvm.SetupStepFor[op]
	if !ok {
		return SetupStepResult{}, fmt.Errorf("%q is not a change the checklist makes", op)
	}
	if !setupMu.TryLock() {
		return SetupStepResult{}, errors.New("another change is still running; wait for it to finish")
	}
	defer setupMu.Unlock()

	result := SetupStepResult{Step: stepID, Op: op}
	// The machine is the truth: the row must still ask for this change.
	facts := a.hostCache().refresh(a.ctx)
	status := a.runtimeStatus(a.catalog.ListVisible())
	row, found := findStep(winvm.DockerSteps(facts, status.Reason), stepID)
	if !found || row.State != winvm.StepNeedsYou || row.Action.Op != op {
		result.State = winvm.OutcomeFailed
		result.Text = "Nothing ran: CashPilot read this PC again, and this step no longer asks for this change. The list below shows where it stands now."
		return result, nil
	}

	ctx, cancel := context.WithTimeout(a.ctx, setupTimeout(op))
	defer cancel()
	res, err := runSetupChange(ctx, defaultSetupRunner, op, webDownload)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	out := winvm.SetupOutcome(op, res, err, timedOut, webDownload)
	if op == winvm.OpLinuxEngine && (out.State == winvm.OutcomeFailed || errors.Is(err, errDockerCliMissing)) {
		// Docker's own menu always works: say how.
		out = winvm.Outcome{State: winvm.OutcomeFallback, Code: out.Code,
			Text: "CashPilot could not switch Docker Desktop itself. Do it from Docker's menu instead: " + row.Action.Manual}
	}
	result.State, result.Text, result.Code, result.Retry, result.URL = out.State, out.Text, out.Code, out.Retry, out.URL
	if err != nil && out.State != winvm.OutcomeCancelled {
		log.Printf("setup: %s ended %s: %v", op, out.State, err)
	}

	finish := ""
	switch out.State {
	case winvm.OutcomeRestart:
		finish = setupresume.FinishRestart
	case winvm.OutcomeSignOut:
		finish = setupresume.FinishSignOut
	}
	if finish != "" {
		reopens, jerr := a.beginFinish(stepID, finish)
		if jerr != nil {
			log.Printf("setup: %v", jerr)
			result.Text += " CashPilot could not note where the setup stands, so open it yourself afterwards."
		}
		result.Reopens = reopens
	}
	return result, nil
}

// runSetupChange runs op through r: the admin changes as their script, the
// other two as the user.
func runSetupChange(ctx context.Context, r setupRunner, op string, webDownload bool) (runtime.ElevatedResult, error) {
	switch {
	case winvm.SetupElevated(op):
		script, err := winvm.SetupScript(op, webDownload)
		if err != nil {
			return runtime.ElevatedResult{}, err
		}
		return r.elevated(ctx, script)
	case op == winvm.OpDockerInstall:
		return r.installDocker(ctx)
	case op == winvm.OpLinuxEngine:
		return runtime.ElevatedResult{}, r.switchToLinux(ctx)
	}
	return runtime.ElevatedResult{}, fmt.Errorf("%q has no runner", op)
}

// restartCodes reads a process that ended 3010 or 1641 as done with a restart
// needed, as RunElevated does for admin scripts.
func restartCodes(err error) (runtime.ElevatedResult, error) {
	if code, ok := runtime.ExitCodeOf(err); ok && (code == 3010 || code == 1641) {
		return runtime.ElevatedResult{RestartNeeded: true, ExitCode: int(code)}, nil
	}
	return runtime.ElevatedResult{}, err
}

func findStep(steps []winvm.Step, id string) (winvm.Step, bool) {
	for _, s := range steps {
		if s.ID == id {
			return s, true
		}
	}
	return winvm.Step{}, false
}

// RestartNow restarts the PC to finish a change. The frontend calls it only
// from the screen that says open apps close and unsaved work can be lost. It
// refuses when no setup step waits, so nothing else can restart the PC through
// it. Windows asks every open app to close and shows the apps that hold out; it
// never forces them.
func (a *App) RestartNow() error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.armForFinish(""); err != nil {
		return err
	}
	return defaultSetupRunner.restart()
}

// SignOutNow signs the user out to finish the docker-users change, from the
// screen that says open apps close. It refuses unless that step waits.
func (a *App) SignOutNow() error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.armForFinish(setupresume.FinishSignOut); err != nil {
		return err
	}
	return defaultSetupRunner.signOut()
}

// armForFinish checks that a setup step waits (for a sign-out, when finish says
// so) and points RunOnce at this app again, in case a start in between removed it.
func (a *App) armForFinish(finish string) error {
	j := a.cfg.Config().SetupJournal
	if j == nil {
		return errors.New("no setup step waits for a restart")
	}
	if finish == setupresume.FinishSignOut && j.Finish != setupresume.FinishSignOut {
		return errors.New("no setup step waits for a sign-out")
	}
	if err := a.runOnceKey().Arm(currentExe()); err != nil {
		log.Printf("setup: Windows will not reopen CashPilot by itself: %v", err)
	}
	return nil
}

// waitProcess waits for a started process, or for ctx, whichever ends first.
// It never kills the process: the Docker install window belongs to the user.
func waitProcess(ctx context.Context, wait func() error) error {
	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
