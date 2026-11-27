package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/bgservice"
)

type fakeHelperAgent struct {
	registered string
	statusErr  error
	replaceErr error
	replaces   []string
	args       [][]string
}

func (f *fakeHelperAgent) Install(exe string, args []string) error { f.registered = exe; return nil }
func (f *fakeHelperAgent) Uninstall() error                        { f.registered = ""; return nil }
func (f *fakeHelperAgent) Status() (bgservice.Status, error) {
	return bgservice.Status{Installed: f.registered != ""}, f.statusErr
}
func (f *fakeHelperAgent) Replace(exe string, args []string) error {
	if f.replaceErr != nil {
		return f.replaceErr
	}
	f.replaces = append(f.replaces, exe)
	f.args = append(f.args, args)
	f.registered = exe
	return nil
}

// plainAgent cannot replace a helper.
type plainAgent struct{ fakeHelperAgent }

func (p *plainAgent) Replace() {}

const (
	installed = `/Applications/CashPilot.app/Contents/MacOS/CashPilot`
	portable  = `/Users/u/Downloads/CashPilot.app/Contents/MacOS/CashPilot`
)

// liveProcesses fakes processStart: pid -> start time of a process that runs.
func liveProcesses(t *testing.T, live map[int]int64) {
	t.Helper()
	old := processStart
	processStart = func(pid int) (int64, bool) { s, ok := live[pid]; return s, ok }
	t.Cleanup(func() { processStart = old })
}

func writeRecord(t *testing.T, dir string, rec helperRecord) {
	t.Helper()
	data, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, helperRecordFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceOlderHelperReplacesA1xHelper(t *testing.T) {
	liveProcesses(t, nil)
	agent := &fakeHelperAgent{registered: installed}
	// A 1.x helper never writes the record.
	replaced, err := replaceOlderHelper(agent, installed, t.TempDir(), "2.0.0")
	if err != nil || !replaced || len(agent.replaces) != 1 || agent.replaces[0] != installed {
		t.Fatalf("1.x helper: replaced %v, err %v, replaces %v; want one replace with this copy", replaced, err, agent.replaces)
	}
	if len(agent.args[0]) != 1 || agent.args[0][0] != "--daemon" {
		t.Fatalf("args %v; want --daemon", agent.args[0])
	}
}

func TestReplaceOlderHelperLeavesAHelperThatRunsThisCopy(t *testing.T) {
	dir := t.TempDir()
	liveProcesses(t, map[int]int64{4242: 1700})
	writeRecord(t, dir, helperRecord{Version: "2.0.0", Exe: installed, PID: 4242, Started: 1700})
	agent := &fakeHelperAgent{registered: installed}
	if replaced, err := replaceOlderHelper(agent, installed, dir, "2.0.0"); err != nil || replaced || len(agent.replaces) != 0 {
		t.Fatalf("current helper: replaced %v, err %v; want nothing", replaced, err)
	}
}

func TestReplaceOlderHelperDoesNotTrustARecordOfAnotherProcess(t *testing.T) {
	cases := map[string]struct {
		live map[int]int64
		rec  helperRecord
	}{
		// 2.0 -> 1.x -> 2.0: the 2.0 helper that wrote the record is gone, and a
		// 1.x helper runs from the same path.
		"writer exited":     {nil, helperRecord{Version: "2.0.0", Exe: installed, PID: 4242, Started: 1700}},
		"pid reused":        {map[int]int64{4242: 9999}, helperRecord{Version: "2.0.0", Exe: installed, PID: 4242, Started: 1700}},
		"older version":     {map[int]int64{4242: 1700}, helperRecord{Version: "1.9.0", Exe: installed, PID: 4242, Started: 1700}},
		"another copy":      {map[int]int64{4242: 1700}, helperRecord{Version: "2.0.0", Exe: portable, PID: 4242, Started: 1700}},
		"no start recorded": {map[int]int64{4242: 1700}, helperRecord{Version: "2.0.0", Exe: installed, PID: 4242}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			liveProcesses(t, c.live)
			writeRecord(t, dir, c.rec)
			agent := &fakeHelperAgent{registered: installed}
			if replaced, err := replaceOlderHelper(agent, installed, dir, "2.0.0"); err != nil || !replaced {
				t.Fatalf("replaced %v, err %v; want a replace", replaced, err)
			}
		})
	}
}

func TestReplaceOlderHelperReadsAGarbledRecordAsNone(t *testing.T) {
	dir := t.TempDir()
	liveProcesses(t, nil)
	if err := os.WriteFile(filepath.Join(dir, helperRecordFile), []byte("2.0.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if replaced, err := replaceOlderHelper(&fakeHelperAgent{registered: installed}, installed, dir, "2.0.0"); err != nil || !replaced {
		t.Fatalf("replaced %v, err %v; want a replace", replaced, err)
	}
}

func TestReplaceOlderHelperNeverRegistersAHelperTheUserDidNotAskFor(t *testing.T) {
	agent := &fakeHelperAgent{}
	if replaced, err := replaceOlderHelper(agent, installed, t.TempDir(), "2.0.0"); err != nil || replaced || len(agent.replaces) != 0 {
		t.Fatalf("no helper registered: replaced %v, err %v; want nothing", replaced, err)
	}
	if replaced, _ := replaceOlderHelper(&plainAgent{fakeHelperAgent{registered: installed}}, installed, t.TempDir(), "2.0.0"); replaced {
		t.Fatal("an agent that cannot replace was replaced")
	}
	if replaced, _ := replaceOlderHelper(agent, "", t.TempDir(), "2.0.0"); replaced {
		t.Fatal("replaced with no executable path")
	}
}

func TestReplaceOlderHelperReportsFailures(t *testing.T) {
	liveProcesses(t, nil)
	boom := errors.New("boom")

	if _, err := replaceOlderHelper(&fakeHelperAgent{registered: installed, statusErr: boom}, installed, t.TempDir(), "2.0.0"); !errors.Is(err, boom) {
		t.Fatalf("status error: err %v; want boom", err)
	}
	if replaced, err := replaceOlderHelper(&fakeHelperAgent{registered: installed, replaceErr: boom}, installed, t.TempDir(), "2.0.0"); replaced || !errors.Is(err, boom) {
		t.Fatalf("replace error: replaced %v, err %v; want false, boom", replaced, err)
	}
	// A record that cannot be read (here a folder) leaves the helper alone.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, helperRecordFile), 0o700); err != nil {
		t.Fatal(err)
	}
	agent := &fakeHelperAgent{registered: installed}
	if replaced, err := replaceOlderHelper(agent, installed, dir, "2.0.0"); replaced || err == nil || len(agent.replaces) != 0 {
		t.Fatalf("unreadable record: replaced %v, err %v; want false and an error", replaced, err)
	}
}

func TestWriteHelperRecordNamesThisProcess(t *testing.T) {
	old := appVersion
	appVersion = "v2.0.0"
	t.Cleanup(func() { appVersion = old })
	dir := t.TempDir()
	writeHelperRecord(dir)

	exe, _ := os.Executable()
	data, err := os.ReadFile(filepath.Join(dir, helperRecordFile))
	if err != nil {
		t.Fatal(err)
	}
	var rec helperRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Version != "2.0.0" || rec.Exe != exe || rec.PID != os.Getpid() || rec.Started == 0 {
		t.Fatalf("record %+v; want 2.0.0, %s, pid %d and a start time", rec, exe, os.Getpid())
	}
	// The real processStart finds this process with the recorded start time.
	if current, err := helperIsCurrent(dir, exe, "2.0.0"); err != nil || !current {
		t.Fatalf("helperIsCurrent = %v, %v; want true for this live process", current, err)
	}
}

func TestWriteHelperRecordLogsAFolderItCannotWrite(t *testing.T) {
	writeHelperRecord(filepath.Join(t.TempDir(), "missing"))
}

func TestLogHelperReplace(t *testing.T) {
	logHelperReplace(true, nil)
	logHelperReplace(false, errors.New("boom"))
	logHelperReplace(false, nil)
}
