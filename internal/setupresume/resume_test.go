package setupresume

import (
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
)

const exe = `C:\Users\ana\AppData\Local\Programs\CashPilot Desktop\CashPilot Desktop.exe`

func TestCommandQuotesThePathAndAddsTheFlag(t *testing.T) {
	cmd, err := Command(exe)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != `"`+exe+`" --resume-setup` {
		t.Fatalf("command %s", cmd)
	}
	if _, err := Command(`\\server\share\CashPilot Desktop.exe`); err != nil {
		t.Fatalf("a UNC path was refused: %v", err)
	}
}

func TestCommandRefusesWhatRunOnceCannotRun(t *testing.T) {
	for name, path := range map[string]string{
		"relative":     `CashPilot Desktop.exe`,
		"unix":         `/usr/bin/cashpilot`,
		"empty":        ``,
		"quote inside": `C:\a"b\CashPilot.exe`,
		"line break":   "C:\\a\nb\\CashPilot.exe",
	} {
		if _, err := Command(path); err == nil {
			t.Errorf("%s path %q was accepted", name, path)
		}
	}
}

// The cap is 260 characters for the whole command, quotes and flag included,
// counted the way Windows counts them (UTF-16 units).
func TestCommandCapsAt260Characters(t *testing.T) {
	overhead := len(`"" --resume-setup`)
	fits := `C:\` + strings.Repeat("a", MaxCommand-overhead-len(`C:\`))
	cmd, err := Command(fits)
	if err != nil || len(cmd) != MaxCommand {
		t.Fatalf("a %d-character command was refused: %v", len(cmd), err)
	}
	if _, err := Command(fits + "a"); err == nil {
		t.Fatal("a 261-character command was accepted")
	}
	// An emoji is two UTF-16 units.
	wide := `C:\` + strings.Repeat("a", MaxCommand-overhead-len(`C:\`)-1) + "\U0001F600"
	if _, err := Command(wide); err == nil {
		t.Fatal("a command over 260 UTF-16 units was accepted")
	}
}

func TestValueNameDeletesOnlyAfterTheRun(t *testing.T) {
	if !strings.HasPrefix(ValueName, "!") {
		t.Fatalf("the RunOnce value %q lacks the ! prefix, so a failed start loses it", ValueName)
	}
}

func TestHasFlag(t *testing.T) {
	if !HasFlag([]string{exe, "--resume-setup"}) || !HasFlag([]string{"-resume-setup"}) {
		t.Fatal("the flag was not found")
	}
	if HasFlag([]string{exe, "--daemon"}) || HasFlag(nil) {
		t.Fatal("a flag was found where none is")
	}
}

func TestNextCountsAttemptsPerStep(t *testing.T) {
	j := Next(nil, FlowDocker, "a2", "2026-11-25T08:00:00Z", exe)
	if j.Attempts != 1 || j.BootTimeBefore != "2026-11-25T08:00:00Z" || j.ExePath != exe || j.Step != "a2" {
		t.Fatalf("first journal %+v", j)
	}
	j = Next(j, FlowDocker, "a2", "2026-11-25T09:00:00Z", exe)
	if j.Attempts != 2 || j.BootTimeBefore != "2026-11-25T09:00:00Z" {
		t.Fatalf("second attempt %+v", j)
	}
	if j = Next(j, FlowDocker, "a3", "", exe); j.Attempts != 1 {
		t.Fatalf("another step starts again at 1: %+v", j)
	}
}

func there(string) bool { return true }
func gone(string) bool  { return false }

func TestDecide(t *testing.T) {
	journal := &config.SetupJournal{Flow: FlowDocker, Step: "a2", BootTimeBefore: "2026-11-25T07:59:58.5Z", ExePath: exe, Attempts: 1}
	cases := []struct {
		name      string
		j         *config.SetupJournal
		boot      string
		flag      bool
		exists    func(string) bool
		active    bool
		restarted string
		rearm     bool
		moved     bool
	}{
		{"no journal, no flag: nothing to do", nil, "2026-11-25T08:00:00Z", false, there, false, "", false, false},
		{"the flag alone lands on the checklist", nil, "2026-11-25T08:00:00Z", true, there, true, RestartedUnknown, false, false},
		{"same boot: restart to finish", journal, "2026-11-25T07:59:58.5Z", false, there, true, RestartedNo, true, false},
		{"same boot within rounding", journal, "2026-11-25T07:59:59Z", false, there, true, RestartedNo, true, false},
		{"new boot: the restart happened", journal, "2026-11-25T08:30:00Z", true, there, true, RestartedYes, false, false},
		{"boot unread: unknown, never 'restart to finish'", journal, "", true, there, true, RestartedUnknown, false, false},
		{"a boot time that went back is unknown", journal, "2026-11-24T08:00:00Z", false, there, true, RestartedUnknown, false, false},
		{"exe moved: still lands, and says so", journal, "2026-11-25T08:30:00Z", false, gone, true, RestartedYes, false, true},
		{"exe moved before the restart: rearm at the new path", journal, "2026-11-25T07:59:58.5Z", false, gone, true, RestartedNo, true, true},
	}
	for _, tc := range cases {
		v := Decide(tc.j, tc.boot, tc.flag, tc.exists)
		if v.Active != tc.active || v.Restarted != tc.restarted || v.Rearm != tc.rearm || v.ExeMoved != tc.moved {
			t.Errorf("%s: %+v", tc.name, v)
		}
		if tc.j != nil && (v.Step != tc.j.Step || v.Attempts != tc.j.Attempts) {
			t.Errorf("%s: the view lost the journal's step: %+v", tc.name, v)
		}
	}
}
