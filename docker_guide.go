package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/setupresume"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
	"github.com/wailsapp/wails/v2/pkg/options"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// The Docker checklist's actions and its resume after a restart. The checklist
// itself is derived in internal/winvm (DockerSteps) and comes back with HostCheck.
// Every action here runs without admin rights. The one that restarts the PC runs
// only from the confirm screen, which says that open apps close.

// errWindowsOnly is what the Windows actions answer on another OS.
var errWindowsOnly = errors.New("this step exists only on Windows")

// StartDockerDesktop opens Docker Desktop, or brings its window forward when it
// already runs. It returns once Windows started it; the checklist then waits for
// the engine. It never accepts Docker's terms: the user does, in Docker's window.
func (a *App) StartDockerDesktop() error {
	if err := a.ready(); err != nil {
		return err
	}
	return launchDockerDesktop()
}

// DockerDesktopEngineReady checks only the local Docker Desktop engine pipes.
func (a *App) DockerDesktopEngineReady() (bool, error) {
	if err := a.ready(); err != nil {
		return false, err
	}
	return dockerDesktopEngineReady(a.ctx), nil
}

// OpenWindowsUpdate opens Settings at Windows Update.
func (a *App) OpenWindowsUpdate() error {
	if err := a.ready(); err != nil {
		return err
	}
	return openWindowsUpdate()
}

// FirmwareRestart is how a "Restart into BIOS" ended when the PC did not restart
// at once.
type FirmwareRestart struct {
	// Started is true when Windows accepted the restart.
	Started bool `json:"started"`
	// Reopens is true when Windows will reopen CashPilot at the next sign-in.
	// False means the user opens it by hand; it then picks up where it was.
	Reopens bool `json:"reopens"`
}

// RestartToFirmware restarts the PC into its firmware settings, for the checklist
// step step. The checklist calls it only from its confirm screen. Before the
// restart it writes the setup journal and the RunOnce value, so the app opens on
// the checklist again after the next sign-in. A PC that cannot restart into its
// firmware (an old BIOS, or a policy) answers Started false; the journal stays,
// so a restart by hand still brings the checklist back.
func (a *App) RestartToFirmware(step string) (FirmwareRestart, error) {
	if err := a.ready(); err != nil {
		return FirmwareRestart{}, err
	}
	if !firmwareRestartSupported {
		return FirmwareRestart{}, errWindowsOnly
	}
	reopens, err := a.beginRestart(step)
	if err != nil {
		return FirmwareRestart{}, err
	}
	if err := restartIntoFirmware(); err != nil {
		log.Printf("setup: the restart into the firmware failed: %v", err)
		return FirmwareRestart{Reopens: reopens}, nil
	}
	return FirmwareRestart{Started: true, Reopens: reopens}, nil
}

// beginRestart writes the journal for step, then the RunOnce value. It reports
// whether RunOnce was armed: a path too long for RunOnce, or an app that moved,
// leaves the journal alone to bring the checklist back at the next manual start.
func (a *App) beginRestart(step string) (bool, error) {
	return a.beginFinish(step, setupresume.FinishRestart)
}

// journalMu keeps the journal and the RunOnce value in step: whoever writes one
// writes the other before anyone else reads or changes either. Without it, a check
// that ends the setup while a change notes its restart can leave RunOnce armed
// with no journal, or a journal with no RunOnce.
var journalMu sync.Mutex

// beginFinish is beginRestart for a step that waits for finish: a restart, or a
// sign-out and back in. RunOnce runs at the next sign-in, so it covers both.
func (a *App) beginFinish(step, finish string) (bool, error) {
	step = strings.TrimSpace(step)
	if step == "" || len(step) > 16 {
		return false, fmt.Errorf("%q is not a checklist step", step)
	}
	exe := currentExe()
	boot := a.hostCache().cached().LastBoot
	if boot == "" {
		boot = a.hostCache().refresh(a.ctx).LastBoot
	}
	journalMu.Lock()
	defer journalMu.Unlock()
	err := a.cfg.Update(func(c *config.AppConfig) {
		j := setupresume.Next(c.SetupJournal, setupresume.FlowDocker, step, boot, exe)
		if finish == setupresume.FinishSignOut {
			j.Finish = finish
		}
		c.SetupJournal = j
	})
	if err != nil {
		return false, fmt.Errorf("could not save where the setup stands before the restart: %w", err)
	}
	if err := a.runOnceKey().Arm(exe); err != nil {
		log.Printf("setup: Windows will not reopen CashPilot by itself: %v", err)
		return false, nil
	}
	return firmwareRestartSupported, nil
}

// SetupResume says, at start, whether to land on the checklist and what the
// restart did. It also settles RunOnce: armed again while the restart is still to
// come, removed once it has run.
func (a *App) SetupResume() (setupresume.View, error) {
	if err := a.ready(); err != nil {
		return setupresume.View{}, err
	}
	var facts winvm.HostFacts
	if a.cfg.Config().SetupJournal != nil {
		facts = a.hostCache().cached()
		if facts.LastBoot == "" {
			facts = a.hostCache().refresh(a.ctx)
		}
	}
	journalMu.Lock()
	defer journalMu.Unlock()
	j := a.cfg.Config().SetupJournal
	// This app's token carrying docker-users proves a sign-in since the group was
	// added, even when the app was opened by hand rather than by RunOnce.
	v := setupresume.Decide(j, facts.LastBoot, a.resumeSetup.Load(), facts.DockerUsersToken == winvm.Yes, fileExists)
	if j != nil {
		var err error
		if v.Rearm {
			err = a.runOnceKey().Arm(currentExe())
		} else {
			err = a.runOnceKey().Disarm()
		}
		if err != nil {
			log.Printf("setup: RunOnce: %v", err)
		}
	}
	return v, nil
}

// CancelSetup ends a guided setup the user stopped: the journal and the RunOnce
// value go, so nothing reopens the checklist after a restart.
func (a *App) CancelSetup() error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.endSetup()
}

// endSetup clears the RunOnce value, then the journal. With no journal it does
// nothing, so HostCheck can call it on every check that finds Docker running.
// When RunOnce cannot be removed, the journal stays and the error comes back, so
// a cancel can be tried again and nothing reopens the app with no journal. The
// resume flag goes only with the journal, so a failed cancel keeps both.
func (a *App) endSetup() error {
	if a.cfg == nil {
		a.resumeSetup.Store(false)
		return nil
	}
	journalMu.Lock()
	defer journalMu.Unlock()
	if a.cfg.Config().SetupJournal == nil {
		a.resumeSetup.Store(false)
		return nil
	}
	if err := a.runOnceKey().Disarm(); err != nil {
		return fmt.Errorf("could not remove the RunOnce value, so the guided setup is not ended yet: %w", err)
	}
	if err := a.cfg.Update(func(c *config.AppConfig) { c.SetupJournal = nil }); err != nil {
		return err
	}
	a.resumeSetup.Store(false)
	return nil
}

// onSecondInstance runs in the open app when CashPilot is started again: the
// single-instance lock (main.go) keeps one window on one store. It brings the
// window forward and passes a --resume-setup through, so a RunOnce start that
// finds the app already open still lands on the checklist.
func (a *App) onSecondInstance(data options.SecondInstanceData) {
	if a.ctx == nil || a.ctx.Value("events") == nil {
		return
	}
	wailsruntime.WindowUnminimise(a.ctx)
	wailsruntime.WindowShow(a.ctx)
	if setupresume.HasFlag(data.Args) {
		a.resumeSetup.Store(true)
		a.emitEvent("setup:resume")
	}
}

// runOnceWriter arms and disarms the RunOnce value. The real one is
// setupresume's, which writes HKCU\...\RunOnce on Windows and does nothing
// elsewhere.
type runOnceWriter interface {
	Arm(exe string) error
	Disarm() error
}

type realRunOnce struct{}

func (realRunOnce) Arm(exe string) error { return setupresume.Arm(exe) }
func (realRunOnce) Disarm() error        { return setupresume.Disarm() }

// defaultRunOnce is what an App with no runOnce of its own uses. The package's
// tests replace it, so no test can arm the real RunOnce key.
var defaultRunOnce runOnceWriter = realRunOnce{}

func (a *App) runOnceKey() runOnceWriter {
	if a.runOnce != nil {
		return a.runOnce
	}
	return defaultRunOnce
}

// currentExe is this app's path, with links resolved.
func currentExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
