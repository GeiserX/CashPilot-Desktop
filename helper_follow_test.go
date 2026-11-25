package main

import (
	"errors"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/bgservice"
)

type fakeHelperAgent struct {
	registered string
	readErr    error
	installs   []string
	args       [][]string
}

func (f *fakeHelperAgent) Install(exe string, args []string) error {
	f.installs = append(f.installs, exe)
	f.args = append(f.args, args)
	f.registered = exe
	return nil
}
func (f *fakeHelperAgent) Uninstall() error { f.registered = ""; return nil }
func (f *fakeHelperAgent) Status() (bgservice.Status, error) {
	return bgservice.Status{Installed: f.registered != ""}, nil
}
func (f *fakeHelperAgent) RegisteredCommand() (string, error) { return f.registered, f.readErr }

func TestFollowThisCopyMovesAHelperThatStartsAnotherCopy(t *testing.T) {
	portable := `C:\Users\u\Downloads\CashPilot-Desktop-windows-amd64.exe`
	installed := `C:\Users\u\AppData\Local\Programs\CashPilot Desktop\CashPilot Desktop.exe`
	agent := &fakeHelperAgent{registered: portable}

	moved, err := followThisCopy(agent, installed)
	if err != nil || !moved {
		t.Fatalf("followThisCopy = %v, %v; want true, nil", moved, err)
	}
	if agent.registered != installed || len(agent.installs) != 1 || len(agent.args[0]) != 1 || agent.args[0][0] != "--daemon" {
		t.Fatalf("registration %q, installs %v args %v; want the installed copy with --daemon", agent.registered, agent.installs, agent.args)
	}

	// Opening the same copy again changes nothing.
	if moved, err := followThisCopy(agent, installed); err != nil || moved || len(agent.installs) != 1 {
		t.Fatalf("second open: moved %v, err %v, installs %v; want no change", moved, err, agent.installs)
	}

	// The portable copy opened afterwards takes the helper back.
	if moved, _ := followThisCopy(agent, portable); !moved || agent.registered != portable {
		t.Fatalf("portable copy opened later: moved %v, registered %q", moved, agent.registered)
	}
}

func TestFollowThisCopyNeverRegistersAHelperTheUserDidNotAskFor(t *testing.T) {
	agent := &fakeHelperAgent{}
	if moved, err := followThisCopy(agent, `C:\x\CashPilot Desktop.exe`); err != nil || moved || len(agent.installs) != 0 {
		t.Fatalf("no helper registered: moved %v, err %v, installs %v; want nothing", moved, err, agent.installs)
	}
}

func TestFollowThisCopyLeavesTheHelperWhenItCannotRead(t *testing.T) {
	agent := &fakeHelperAgent{registered: `C:\old.exe`, readErr: errors.New("powershell failed")}
	if moved, err := followThisCopy(agent, `C:\new.exe`); err == nil || moved || len(agent.installs) != 0 {
		t.Fatalf("unreadable task: moved %v, err %v, installs %v; want the error and no change", moved, err, agent.installs)
	}
	var plain bgservice.Agent = &plainAgent{}
	if moved, err := followThisCopy(plain, `C:\new.exe`); err != nil || moved {
		t.Fatalf("agent without CommandReporter: moved %v, err %v", moved, err)
	}
}

type plainAgent struct{ installs int }

func (p *plainAgent) Install(string, []string) error { p.installs++; return nil }
func (p *plainAgent) Uninstall() error               { return nil }
func (p *plainAgent) Status() (bgservice.Status, error) {
	return bgservice.Status{Installed: true}, nil
}
