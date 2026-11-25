package main

import (
	"context"
	"testing"

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

func bootAt(boot string) *hostFacts {
	return &hostFacts{read: func(context.Context) winvm.HostFacts {
		return winvm.HostFacts{OS: "windows", Supported: true, LastBoot: boot}
	}}
}

// Before a restart the journal is written with the boot time it compares
// against, the app's path and the step; asking again counts the attempt.
func TestBeginRestartWritesTheJournalFirst(t *testing.T) {
	app := newRemovalTestApp(t, reasonProvider{})
	app.host = bootAt("2026-11-25T07:59:58.5Z")
	if _, err := app.beginRestart("a2"); err != nil {
		t.Fatal(err)
	}
	j := app.cfg.Config().SetupJournal
	if j == nil || j.Flow != setupresume.FlowDocker || j.Step != "a2" || j.BootTimeBefore != "2026-11-25T07:59:58.5Z" || j.ExePath == "" || j.Attempts != 1 {
		t.Fatalf("journal %+v", j)
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
}

// The journal survives the restart: a new App on the same data folder reads it.
func TestSetupResumeLandsOnTheChecklistAfterTheRestart(t *testing.T) {
	app := newRemovalTestApp(t, reasonProvider{})
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

	// After the restart, started by RunOnce.
	if _, err := app.cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	after := &App{cfg: app.cfg, store: app.store, catalog: app.catalog, runtime: app.runtime, services: app.services, ctx: context.Background()}
	after.host = bootAt("2026-11-25T09:12:00Z")
	after.resumeSetup.Store(true)
	v, err = after.SetupResume()
	if err != nil {
		t.Fatal(err)
	}
	if !v.Active || v.Restarted != setupresume.RestartedYes || !v.FromRunOnce {
		t.Fatalf("after the restart: %+v", v)
	}
	// Landing re-runs the checks; the journal stays until the flow ends.
	if after.cfg.Config().SetupJournal == nil {
		t.Fatal("landing cleared the journal before the flow ended")
	}
}

func TestCancelSetupClearsTheJournal(t *testing.T) {
	app := newRemovalTestApp(t, reasonProvider{})
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
	app := newRemovalTestApp(t, reasonProvider{})
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
	app := newRemovalTestApp(t, reasonProvider{})
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
}
