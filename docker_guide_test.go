package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/setupresume"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
	"github.com/wailsapp/wails/v2/pkg/options"
)

type runningProvider struct{ stubProvider }

func (runningProvider) Status(context.Context) runtime.Status {
	return runtime.Status{Available: true, Reason: runtime.ReasonRunning}
}

// fakeRunOnce records what the app asked of RunOnce, in place of the real key.
type fakeRunOnce struct {
	mu       sync.Mutex
	armed    string
	arms     int
	disarms  int
	armError error
	// disarmError fails Disarm; duringDisarm runs inside Disarm, after the value
	// is gone, with the fake's lock free.
	disarmError  error
	duringDisarm func()
}

func (f *fakeRunOnce) Arm(exe string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.arms++
	if f.armError != nil {
		return f.armError
	}
	f.armed = exe
	return nil
}

func (f *fakeRunOnce) Disarm() error {
	f.mu.Lock()
	f.disarms++
	if err := f.disarmError; err != nil {
		f.mu.Unlock()
		return err
	}
	f.armed = ""
	during := f.duringDisarm
	f.mu.Unlock()
	if during != nil {
		during()
	}
	return nil
}

func (f *fakeRunOnce) state() (armed string, arms, disarms int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.armed, f.arms, f.disarms
}

// newGuideTestApp is a test App whose RunOnce is a fake. On Windows it also
// checks that the real RunOnce value is the same after the test as before.
func newGuideTestApp(t *testing.T, provider runtime.Provider) (*App, *fakeRunOnce) {
	t.Helper()
	guardRealRunOnce(t)
	app := newRemovalTestApp(t, provider)
	fake := &fakeRunOnce{}
	app.runOnce = fake
	return app, fake
}

func bootAt(boot string) *hostFacts {
	return &hostFacts{read: func(context.Context) winvm.HostFacts {
		return winvm.HostFacts{OS: "windows", Supported: true, LastBoot: boot}
	}}
}

// Before a restart the journal is written with the boot time it compares
// against, the app's path and the step; asking again counts the attempt.
func TestBeginRestartWritesTheJournalFirst(t *testing.T) {
	app, fake := newGuideTestApp(t, reasonProvider{})
	app.host = bootAt("2026-11-25T07:59:58.5Z")
	if _, err := app.beginRestart("a2"); err != nil {
		t.Fatal(err)
	}
	j := app.cfg.Config().SetupJournal
	if j == nil || j.Flow != setupresume.FlowDocker || j.Step != "a2" || j.BootTimeBefore != "2026-11-25T07:59:58.5Z" || j.ExePath == "" || j.Attempts != 1 {
		t.Fatalf("journal %+v", j)
	}
	if armed, arms, _ := fake.state(); arms != 1 || armed != j.ExePath {
		t.Fatalf("RunOnce armed %d times for %q, want once for %q", arms, armed, j.ExePath)
	}
	if _, err := app.beginRestart("a2"); err != nil {
		t.Fatal(err)
	}
	if j = app.cfg.Config().SetupJournal; j.Attempts != 2 {
		t.Fatalf("a second try is attempt %d", j.Attempts)
	}
	if _, err := app.beginRestart(""); err == nil {
		t.Fatal("a restart with no step was accepted")
	}
	if _, arms, _ := fake.state(); arms != 2 {
		t.Fatalf("a refused step still armed RunOnce: %d arms", arms)
	}
}

// RunOnce refusing (a path too long, an app that moved) keeps the journal, and
// the app says Windows will not reopen it.
func TestBeginRestartKeepsTheJournalWhenRunOnceRefuses(t *testing.T) {
	app, fake := newGuideTestApp(t, reasonProvider{})
	app.host = bootAt("2026-11-25T07:59:58.5Z")
	fake.armError = errors.New("the command is 300 characters long")
	reopens, err := app.beginRestart("a2")
	if err != nil {
		t.Fatal(err)
	}
	if reopens {
		t.Fatal("RunOnce refused, but the app says Windows reopens it")
	}
	if j := app.cfg.Config().SetupJournal; j == nil || j.Step != "a2" {
		t.Fatalf("journal %+v", j)
	}
}

// The journal survives the restart: a new App on the same data folder reads it.
func TestSetupResumeLandsOnTheChecklistAfterTheRestart(t *testing.T) {
	app, fake := newGuideTestApp(t, reasonProvider{})
	app.host = bootAt("2026-11-25T07:59:58.5Z")
	if _, err := app.beginRestart("a2"); err != nil {
		t.Fatal(err)
	}

	// Same boot: the user came back without restarting.
	v, err := app.SetupResume()
	if err != nil {
		t.Fatal(err)
	}
	if !v.Active || v.Restarted != setupresume.RestartedNo || v.Step != "a2" {
		t.Fatalf("same boot: %+v", v)
	}
	// The restart is still to come, so RunOnce points at the app again.
	if armed, arms, disarms := fake.state(); armed == "" || arms != 2 || disarms != 0 {
		t.Fatalf("same boot: RunOnce %q, %d arms, %d disarms", armed, arms, disarms)
	}

	// After the restart, started by RunOnce.
	if _, err := app.cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	after := &App{cfg: app.cfg, store: app.store, catalog: app.catalog, runtime: app.runtime, services: app.services, ctx: context.Background()}
	after.runOnce = fake
	after.host = bootAt("2026-11-25T09:12:00Z")
	after.resumeSetup.Store(true)
	v, err = after.SetupResume()
	if err != nil {
		t.Fatal(err)
	}
	if !v.Active || v.Restarted != setupresume.RestartedYes || !v.FromRunOnce {
		t.Fatalf("after the restart: %+v", v)
	}
	// RunOnce ran, so its value goes.
	if armed, _, disarms := fake.state(); armed != "" || disarms != 1 {
		t.Fatalf("after the restart: RunOnce %q, %d disarms", armed, disarms)
	}
	// Landing re-runs the checks; the journal stays until the flow ends.
	if after.cfg.Config().SetupJournal == nil {
		t.Fatal("landing cleared the journal before the flow ended")
	}
}

func TestCancelSetupClearsTheJournal(t *testing.T) {
	app, fake := newGuideTestApp(t, reasonProvider{})
	app.host = bootAt("2026-11-25T07:59:58.5Z")
	if _, err := app.beginRestart("a2"); err != nil {
		t.Fatal(err)
	}
	app.resumeSetup.Store(true)
	if err := app.CancelSetup(); err != nil {
		t.Fatal(err)
	}
	if j := app.cfg.Config().SetupJournal; j != nil {
		t.Fatalf("cancel left the journal: %+v", j)
	}
	if armed, _, _ := fake.state(); armed != "" {
		t.Fatalf("cancel left RunOnce armed for %q", armed)
	}
	if v, _ := app.SetupResume(); v.Active {
		t.Fatalf("after cancel the app still lands on the checklist: %+v", v)
	}
	// Cancelling with nothing under way is fine.
	if err := app.CancelSetup(); err != nil {
		t.Fatal(err)
	}
}

// The flow finishes when a check finds Docker running: the journal goes, so the
// next start opens the dashboard.
func TestHostCheckEndsTheSetupWhenDockerRuns(t *testing.T) {
	app, fake := newGuideTestApp(t, reasonProvider{})
	app.host = bootAt("2026-11-25T07:59:58.5Z")
	if _, err := app.beginRestart("a2"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.HostCheck(); err != nil {
		t.Fatal(err)
	}
	if app.cfg.Config().SetupJournal == nil {
		t.Fatal("a check with Docker still down ended the setup")
	}
	app.runtime = runningProvider{}
	res, err := app.HostCheck()
	if err != nil {
		t.Fatal(err)
	}
	if app.cfg.Config().SetupJournal != nil {
		t.Fatal("Docker runs, but the journal is still there")
	}
	if armed, _, _ := fake.state(); armed != "" {
		t.Fatalf("Docker runs, but RunOnce is still armed for %q", armed)
	}
	if len(res.Steps) == 0 {
		t.Fatal("HostCheck on Windows facts returned no checklist")
	}
}

func TestHostCheckReturnsNoChecklistOutsideWindows(t *testing.T) {
	app := newRemovalTestApp(t, reasonProvider{})
	app.host = &hostFacts{read: func(context.Context) winvm.HostFacts { return winvm.HostFacts{OS: "darwin"} }}
	res, err := app.HostCheck()
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps != nil {
		t.Fatalf("macOS got a checklist: %+v", res.Steps)
	}
}

// A second start before the window exists does nothing, and never panics.
func TestSecondInstanceWithoutAWindow(t *testing.T) {
	app := newRemovalTestApp(t, reasonProvider{})
	app.onSecondInstance(options.SecondInstanceData{Args: []string{"--resume-setup"}})
	if app.resumeSetup.Load() {
		t.Fatal("a second start with no window set the resume flag")
	}
}

// The journal is config, so it reaches disk and a restart.
func TestSetupJournalIsSaved(t *testing.T) {
	app, fake := newGuideTestApp(t, reasonProvider{})
	app.host = bootAt("2026-11-25T07:59:58.5Z")
	if _, err := app.beginRestart("a2"); err != nil {
		t.Fatal(err)
	}
	fresh, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if j := fresh.Config().SetupJournal; j == nil || j.Step != "a2" {
		t.Fatalf("the saved config holds %+v", j)
	}
	if armed, _, _ := fake.state(); armed == "" {
		t.Fatal("the restart did not arm RunOnce")
	}
}

// HostCheck hands DOCKER_HOST to the checklist: an engine it names elsewhere
// proves nothing about this PC, even with Docker Desktop open here.
func TestHostCheckTrustsNoEngineDockerHostNames(t *testing.T) {
	app, _ := newGuideTestApp(t, runningProvider{})
	app.host = &hostFacts{read: func(context.Context) winvm.HostFacts {
		return winvm.HostFacts{OS: "windows", Supported: true, Build: 26100, DockerDesktopRunning: winvm.Yes,
			HypervisorPresent: winvm.Unknown, Firmware: winvm.Unknown, Virtualization: winvm.Unknown, DockerDesktopInstalled: winvm.Yes}
	}}
	a2 := func() string {
		res, err := app.HostCheck()
		if err != nil {
			t.Fatal(err)
		}
		s, _ := findStep(res.Steps, "a2")
		return s.State
	}
	t.Setenv("DOCKER_HOST", "")
	if got := a2(); got != winvm.StepDone {
		t.Fatalf("this PC's Docker Desktop answers, but a2 is %s", got)
	}
	t.Setenv("DOCKER_HOST", "tcp://203.0.113.10:2375")
	if got := a2(); got != winvm.StepUnknown {
		t.Fatalf("an engine DOCKER_HOST names elsewhere answered, but a2 is %s", got)
	}
}

// A cancel whose RunOnce value could not be removed keeps the journal and says
// so, so the user can cancel again and nothing reopens the app with no journal.
func TestCancelSetupKeepsTheJournalWhenRunOnceStays(t *testing.T) {
	app, fake := newGuideTestApp(t, reasonProvider{})
	app.host = bootAt("2026-11-25T07:59:58.5Z")
	if _, err := app.beginRestart("a2"); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.disarmError = errors.New("access is denied")
	fake.mu.Unlock()
	if err := app.CancelSetup(); err == nil {
		t.Fatal("RunOnce stayed armed, but the cancel reported success")
	}
	if app.cfg.Config().SetupJournal == nil {
		t.Fatal("RunOnce stayed armed, but the journal is gone, so a retry has nothing to cancel")
	}
	fake.mu.Lock()
	fake.disarmError = nil
	fake.mu.Unlock()
	if err := app.CancelSetup(); err != nil {
		t.Fatal(err)
	}
	if armed, _, _ := fake.state(); armed != "" || app.cfg.Config().SetupJournal != nil {
		t.Fatalf("the retried cancel left RunOnce %q and journal %+v", armed, app.cfg.Config().SetupJournal)
	}
}

// A check that ends the setup while a change notes its restart leaves RunOnce
// and the journal in step: either both or neither, never RunOnce alone.
func TestEndSetupAndARestartDoNotRace(t *testing.T) {
	app, fake := newGuideTestApp(t, reasonProvider{})
	app.host = bootAt("2026-11-25T07:59:58.5Z")
	if _, err := app.beginRestart("a2"); err != nil {
		t.Fatal(err)
	}
	inDisarm := make(chan struct{})
	release := make(chan struct{})
	fake.mu.Lock()
	fake.duringDisarm = func() {
		close(inDisarm)
		<-release
	}
	fake.mu.Unlock()

	ended := make(chan error, 1)
	go func() { ended <- app.endSetup() }()
	<-inDisarm
	noted := make(chan error, 1)
	go func() {
		_, err := app.beginFinish("a3", setupresume.FinishRestart)
		noted <- err
	}()
	// Without the lock the restart's note lands now, inside the cancel.
	time.Sleep(50 * time.Millisecond)
	close(release)
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	if err := <-noted; err != nil {
		t.Fatal(err)
	}
	armed, _, _ := fake.state()
	j := app.cfg.Config().SetupJournal
	if (armed != "") != (j != nil) {
		t.Fatalf("RunOnce %q and journal %+v are out of step", armed, j)
	}
	if j == nil || j.Step != "a3" {
		t.Fatalf("the restart noted after the cancel is lost: journal %+v", j)
	}
}

// The app opened by hand after the sign-out: its token carries docker-users, so
// it does not ask for the sign-out again, and RunOnce is not armed again.
func TestSetupResumeSeesASignInByTheToken(t *testing.T) {
	app, fake := newGuideTestApp(t, reasonProvider{})
	token := winvm.No
	app.host = &hostFacts{read: func(context.Context) winvm.HostFacts {
		return winvm.HostFacts{OS: "windows", Supported: true, LastBoot: "2026-11-25T07:59:58.5Z", DockerUsersToken: token}
	}}
	if _, err := app.beginFinish("a8", setupresume.FinishSignOut); err != nil {
		t.Fatal(err)
	}
	v, err := app.SetupResume()
	if err != nil {
		t.Fatal(err)
	}
	if v.SignedIn || v.Finish != setupresume.FinishSignOut {
		t.Fatalf("before the sign-out: %+v", v)
	}
	_, arms, _ := fake.state()

	token = winvm.Yes
	app.host.refresh(context.Background())
	v, err = app.SetupResume()
	if err != nil {
		t.Fatal(err)
	}
	if !v.SignedIn || v.Restarted != setupresume.RestartedNo {
		t.Fatalf("the token carries the group, but the view still waits for a sign-out: %+v", v)
	}
	if armed, again, _ := fake.state(); armed != "" || again != arms {
		t.Fatalf("after the sign-in RunOnce is %q, armed %d times (was %d)", armed, again, arms)
	}
}
