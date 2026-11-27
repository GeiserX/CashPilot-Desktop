package main

import (
	"errors"
	"os"
	"path/filepath"
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

type fakeRestartAgent struct {
	fakeHelperAgent
	running  bool
	restarts int
}

func (f *fakeRestartAgent) Status() (bgservice.Status, error) {
	return bgservice.Status{Installed: f.registered != "", Running: f.running}, nil
}
func (f *fakeRestartAgent) Restart() error { f.restarts++; return nil }

func TestRestartOlderHelperRestartsAHelperFromAnotherBuild(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeRestartAgent{fakeHelperAgent: fakeHelperAgent{registered: "/Applications/CashPilot.app/Contents/MacOS/CashPilot"}, running: true}

	// A 1.x helper never wrote the file.
	if restarted, err := restartOlderHelper(agent, dir, "2.0.0"); err != nil || !restarted || agent.restarts != 1 {
		t.Fatalf("1.x helper: restarted %v, err %v, restarts %d; want one restart", restarted, err, agent.restarts)
	}

	// The restarted helper records 2.0.0; the next window start leaves it alone.
	if err := os.WriteFile(filepath.Join(dir, helperVersionFile), []byte("2.0.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if restarted, err := restartOlderHelper(agent, dir, "2.0.0"); err != nil || restarted || agent.restarts != 1 {
		t.Fatalf("same build: restarted %v, err %v, restarts %d; want none", restarted, err, agent.restarts)
	}

	// The next update restarts it again.
	if restarted, _ := restartOlderHelper(agent, dir, "2.0.1"); !restarted || agent.restarts != 2 {
		t.Fatalf("2.0.0 helper under a 2.0.1 window: restarted %v, restarts %d; want a restart", restarted, agent.restarts)
	}
}

func TestRestartOlderHelperLeavesAHelperThatIsNotRunning(t *testing.T) {
	stopped := &fakeRestartAgent{fakeHelperAgent: fakeHelperAgent{registered: "/x/CashPilot"}}
	none := &fakeRestartAgent{running: true}
	for name, agent := range map[string]*fakeRestartAgent{"stopped": stopped, "not registered": none} {
		if restarted, err := restartOlderHelper(agent, t.TempDir(), "2.0.0"); err != nil || restarted || agent.restarts != 0 {
			t.Fatalf("%s: restarted %v, err %v, restarts %d; want nothing", name, restarted, err, agent.restarts)
		}
	}
}

func TestRestartOlderHelperNeedsAKnownVersionAndARestarter(t *testing.T) {
	agent := &fakeRestartAgent{fakeHelperAgent: fakeHelperAgent{registered: "/x/CashPilot"}, running: true}
	if restarted, _ := restartOlderHelper(agent, t.TempDir(), ""); restarted || agent.restarts != 0 {
		t.Fatalf("development build: restarted %v; want nothing", restarted)
	}
	plain := &fakeHelperAgent{registered: "/x/CashPilot"}
	if restarted, err := restartOlderHelper(plain, t.TempDir(), "2.0.0"); err != nil || restarted {
		t.Fatalf("agent without Restart: restarted %v, err %v; want nothing", restarted, err)
	}
}

func TestWriteHelperVersionRecordsThisBuild(t *testing.T) {
	old := appVersion
	appVersion = "v2.0.0"
	t.Cleanup(func() { appVersion = old })
	dir := t.TempDir()
	writeHelperVersion(dir)
	got, err := os.ReadFile(filepath.Join(dir, helperVersionFile))
	if err != nil || string(got) != "2.0.0" {
		t.Fatalf("recorded %q, err %v; want 2.0.0", got, err)
	}
}
