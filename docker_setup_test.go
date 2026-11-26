package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/setupresume"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

type fixedReason struct {
	stubProvider
	reason string
}

func (p fixedReason) Status(context.Context) runtime.Status {
	return runtime.Status{Reason: p.reason}
}

type codeErr uint32

func (e codeErr) Error() string { return fmt.Sprintf("exit status %d", uint32(e)) }
func (e codeErr) ExitCode() int { return int(uint32(e)) }

// fakeSetupRunner records what the app asked to run, in place of Windows.
type fakeSetupRunner struct {
	mu       sync.Mutex
	scripts  []string
	installs int
	switches int
	restarts int
	signOuts int
	// answer is what elevated, installDocker and switchToLinux return.
	res runtime.ElevatedResult
	err error
	// block waits for ctx to end, as a hung tool does.
	block bool
	// journalAtAnswer is the journal as it stood when the change answered.
	app             *App
	journalAtAnswer string
}

func (f *fakeSetupRunner) answer(ctx context.Context) (runtime.ElevatedResult, error) {
	if f.block {
		<-ctx.Done()
		return runtime.ElevatedResult{}, ctx.Err()
	}
	if f.app != nil {
		if j := f.app.cfg.Config().SetupJournal; j != nil {
			f.journalAtAnswer = j.Step
		}
	}
	return f.res, f.err
}

func (f *fakeSetupRunner) elevated(ctx context.Context, script string) (runtime.ElevatedResult, error) {
	f.mu.Lock()
	f.scripts = append(f.scripts, script)
	f.mu.Unlock()
	return f.answer(ctx)
}

func (f *fakeSetupRunner) installDocker(ctx context.Context) (runtime.ElevatedResult, error) {
	f.installs++
	return f.answer(ctx)
}

func (f *fakeSetupRunner) switchToLinux(ctx context.Context) error {
	f.switches++
	_, err := f.answer(ctx)
	return err
}

func (f *fakeSetupRunner) restart() error { f.restarts++; return nil }
func (f *fakeSetupRunner) signOut() error { f.signOuts++; return nil }

func (f *fakeSetupRunner) ran() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.scripts) + f.installs + f.switches
}

// newSetupTestApp is a guide test app on Windows facts, with a fake runner.
func newSetupTestApp(t *testing.T, reason string, facts winvm.HostFacts) (*App, *fakeRunOnce, *fakeSetupRunner) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "") // The fake engine belongs to the Windows PC in these tests.
	app, runOnce := newGuideTestApp(t, fixedReason{reason: reason})
	facts.OS, facts.Supported = "windows", true
	if facts.LastBoot == "" {
		facts.LastBoot = "2026-11-25T07:59:58.5Z"
	}
	app.host = &hostFacts{read: func(context.Context) winvm.HostFacts { return facts }}
	runner := &fakeSetupRunner{app: app}
	prev := defaultSetupRunner
	defaultSetupRunner = runner
	t.Cleanup(func() { defaultSetupRunner = prev })
	return app, runOnce, runner
}

// wslMissing is a Windows 11 PC with virtualization on and WSL off.
func wslMissing() winvm.HostFacts {
	return winvm.HostFacts{Build: 26100, HypervisorPresent: winvm.Yes, Virtualization: winvm.Yes,
		VMPlatformFeature: winvm.No, WSL: winvm.WSLMissing, DockerDesktopInstalled: winvm.Yes, LanmanServer: "auto"}
}

func TestSetupStepRunsTheRowsScript(t *testing.T) {
	app, _, runner := newSetupTestApp(t, runtime.ReasonWSLMissing, wslMissing())
	res, err := app.RunSetupStep(winvm.OpWSLInstall, true)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := winvm.SetupScript(winvm.OpWSLInstall, true)
	if len(runner.scripts) != 1 || runner.scripts[0] != want {
		t.Fatalf("ran %q, want the --web-download install", runner.scripts)
	}
	if res.Step != "a3" || res.State != winvm.OutcomeDone {
		t.Fatalf("result %+v", res)
	}
	if app.cfg.Config().SetupJournal != nil {
		t.Fatal("a change with no restart wrote the journal")
	}
}

// A change the row no longer asks for runs nothing: docker-users without the
// access-denied check, LanmanServer already Automatic, an op from nowhere.
func TestSetupStepRunsNothingTheRowDoesNotAskFor(t *testing.T) {
	f := wslMissing()
	f.DockerUsersListed, f.DockerUsersToken = winvm.No, winvm.No
	app, runOnce, runner := newSetupTestApp(t, runtime.ReasonWSLMissing, f)
	for _, op := range []string{winvm.OpDockerUsers, winvm.OpLanmanServer, winvm.OpHypervisorLaunch, winvm.OpLinuxEngine, winvm.OpDockerInstall} {
		res, err := app.RunSetupStep(op, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.State != winvm.OutcomeFailed || res.Text == "" {
			t.Errorf("%s: %+v", op, res)
		}
	}
	if _, err := app.RunSetupStep("format-c", false); err == nil {
		t.Error("an unknown change was accepted")
	}
	if runner.ran() != 0 {
		t.Fatalf("%d changes ran that no row asked for", runner.ran())
	}
	if _, arms, _ := runOnce.state(); arms != 0 || app.cfg.Config().SetupJournal != nil {
		t.Fatal("a refused change touched the journal or RunOnce")
	}
}

// A failure leaves the row as it was, with the reason and the code, and writes
// nothing for a restart.
func TestSetupStepFailureKeepsTheRowWithItsReason(t *testing.T) {
	app, runOnce, runner := newSetupTestApp(t, runtime.ReasonWSLMissing, wslMissing())
	runner.err = codeErr(0x80072EE7)
	res, err := app.RunSetupStep(winvm.OpWSLInstall, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != winvm.OutcomeFailed || res.Code != "0x80072EE7" || res.Retry != winvm.RetryWebDownload || res.Text == "" {
		t.Fatalf("result %+v", res)
	}
	if _, arms, _ := runOnce.state(); arms != 0 || app.cfg.Config().SetupJournal != nil {
		t.Fatal("a failure wrote the journal or armed RunOnce")
	}
	// The row still asks for the change.
	check, _ := app.HostCheck()
	if row, _ := findStep(check.Steps, "a3"); row.State != winvm.StepNeedsYou || row.Action.Op != winvm.OpWSLInstall {
		t.Fatalf("after the failure the row is %+v", row)
	}
}

// 3010 writes the journal and arms RunOnce before RunSetupStep answers, so the
// restart button the frontend shows next finds both. Nothing restarts.
func TestSetupStepRestartNeededWritesTheJournalFirst(t *testing.T) {
	app, runOnce, runner := newSetupTestApp(t, runtime.ReasonWSLMissing, wslMissing())
	runner.res = runtime.ElevatedResult{RestartNeeded: true, ExitCode: 3010}
	res, err := app.RunSetupStep(winvm.OpWSLInstall, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != winvm.OutcomeRestart {
		t.Fatalf("result %+v", res)
	}
	if runner.journalAtAnswer != "" {
		t.Fatal("the journal was written before the change ran")
	}
	j := app.cfg.Config().SetupJournal
	if j == nil || j.Step != "a3" || j.Finish != "" || j.BootTimeBefore != "2026-11-25T07:59:58.5Z" {
		t.Fatalf("journal %+v", j)
	}
	if armed, arms, _ := runOnce.state(); arms != 1 || armed == "" {
		t.Fatalf("RunOnce: armed %q after %d arms", armed, arms)
	}
	if runner.restarts != 0 {
		t.Fatal("the PC restarted without the restart click")
	}
	// The restart click.
	if err := app.RestartNow(); err != nil {
		t.Fatal(err)
	}
	if runner.restarts != 1 {
		t.Fatalf("%d restarts", runner.restarts)
	}
}

// docker-users runs only on access denied, and asks for a sign-out, noted in the
// journal so the sign-in RunOnce brings back counts as done.
func TestSetupStepDockerUsersAsksForASignOut(t *testing.T) {
	f := wslMissing()
	f.VMPlatformFeature, f.WSL, f.WSLVersion = winvm.Yes, winvm.WSLReady, "2.3.26.0"
	f.DockerUsersListed, f.DockerUsersToken = winvm.No, winvm.No
	app, runOnce, runner := newSetupTestApp(t, runtime.ReasonAccessDenied, f)
	res, err := app.RunSetupStep(winvm.OpDockerUsers, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != winvm.OutcomeSignOut || res.Step != "a8" {
		t.Fatalf("result %+v", res)
	}
	want, _ := winvm.SetupScript(winvm.OpDockerUsers, false)
	if len(runner.scripts) != 1 || runner.scripts[0] != want {
		t.Fatal("the docker-users script did not run")
	}
	if j := app.cfg.Config().SetupJournal; j == nil || j.Step != "a8" || j.Finish != setupresume.FinishSignOut {
		t.Fatalf("journal %+v", j)
	}
	if armed, _, _ := runOnce.state(); armed == "" {
		t.Fatal("RunOnce is not armed for the sign-in")
	}
	if runner.signOuts != 0 {
		t.Fatal("signed out without the click")
	}
	if err := app.SignOutNow(); err != nil || runner.signOuts != 1 {
		t.Fatalf("sign out: %v, %d", err, runner.signOuts)
	}
}

// Restart and sign-out refuse when no step waits for them, and run nothing.
func TestRestartAndSignOutNeedAWaitingStep(t *testing.T) {
	app, _, runner := newSetupTestApp(t, runtime.ReasonWSLMissing, wslMissing())
	if err := app.RestartNow(); err == nil {
		t.Error("RestartNow ran with no step waiting")
	}
	if err := app.SignOutNow(); err == nil {
		t.Error("SignOutNow ran with no step waiting")
	}
	// A step that waits for a restart is not one that waits for a sign-out.
	if _, err := app.beginRestart("a3"); err != nil {
		t.Fatal(err)
	}
	if err := app.SignOutNow(); err == nil {
		t.Error("SignOutNow ran for a step that waits for a restart")
	}
	if runner.restarts != 0 || runner.signOuts != 0 {
		t.Fatalf("%d restarts, %d sign-outs", runner.restarts, runner.signOuts)
	}
}

func TestSetupStepTimeoutOffersTheDirectDownload(t *testing.T) {
	app, _, runner := newSetupTestApp(t, runtime.ReasonWSLMissing, wslMissing())
	prev := setupTimeout
	setupTimeout = func(string) time.Duration { return 20 * time.Millisecond }
	t.Cleanup(func() { setupTimeout = prev })
	runner.block = true
	res, err := app.RunSetupStep(winvm.OpWSLInstall, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != winvm.OutcomeTimeout || res.Retry != winvm.RetryWebDownload {
		t.Fatalf("result %+v", res)
	}
	if app.cfg.Config().SetupJournal != nil {
		t.Fatal("a timeout wrote the journal")
	}
}

func TestSetupStepCancelledPromptChangesNothing(t *testing.T) {
	f := wslMissing()
	f.LanmanServer = "disabled"
	app, _, runner := newSetupTestApp(t, runtime.ReasonWSLMissing, f)
	runner.err = codeErr(runtime.ExitCancelled)
	res, err := app.RunSetupStep(winvm.OpLanmanServer, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != winvm.OutcomeCancelled || res.Step != "a12" {
		t.Fatalf("result %+v", res)
	}
}

// No winget: the install falls back to the download page. No DockerCli.exe: the
// switch falls back to Docker's menu.
func TestSetupStepFallsBackWhenTheToolIsMissing(t *testing.T) {
	f := wslMissing()
	f.VMPlatformFeature, f.WSL, f.WSLVersion = winvm.Yes, winvm.WSLReady, "2.3.26.0"
	f.DockerDesktopInstalled = winvm.No
	app, _, runner := newSetupTestApp(t, runtime.ReasonNotInstalled, f)
	runner.err = codeErr(winvm.CodeWingetGone)
	res, err := app.RunSetupStep(winvm.OpDockerInstall, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != winvm.OutcomeFallback || res.URL != winvm.DockerDownloadURL || runner.installs != 1 {
		t.Fatalf("result %+v", res)
	}

	f.DockerDesktopInstalled, f.DockerDesktopRunning = winvm.Yes, winvm.Yes
	app2, _, runner2 := newSetupTestApp(t, runtime.ReasonWindowsContainers, f)
	runner2.err = errDockerCliMissing
	res, err = app2.RunSetupStep(winvm.OpLinuxEngine, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != winvm.OutcomeFallback || runner2.switches != 1 || res.Text == "" {
		t.Fatalf("result %+v", res)
	}
}

func TestRestartCodesFromTheInstallWindow(t *testing.T) {
	for _, c := range []struct {
		err     error
		restart bool
		failed  bool
	}{
		{nil, false, false},
		{codeErr(winvm.WingetRebootToFinish), true, false},
		{codeErr(winvm.WingetRebootInitiated), true, false},
		// "Restart your PC then try again": nothing was installed.
		{codeErr(winvm.WingetRebootForInstall), false, true},
		// winget never passes the installer's own codes through.
		{codeErr(3010), false, true},
		{codeErr(1641), false, true},
		{codeErr(1), false, true},
		{errors.New("no exit code"), false, true},
	} {
		res, err := restartCodes(c.err)
		if res.RestartNeeded != c.restart || (err != nil) != c.failed {
			t.Errorf("%v: %+v, %v", c.err, res, err)
		}
	}
}

// waitProcess stops waiting at the deadline and never waits for the process.
func TestWaitProcessStopsAtTheDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	err := waitProcess(ctx, func() error { <-release; return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}
