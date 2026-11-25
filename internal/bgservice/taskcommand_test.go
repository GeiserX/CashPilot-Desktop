package bgservice

import (
	"strings"
	"testing"
)

func TestTaskCommandScriptSplitsFolderAndName(t *testing.T) {
	s := taskCommandScript(`CashPilot\Helper`)
	if !strings.Contains(s, `-TaskPath '\CashPilot\' -TaskName 'Helper'`) {
		t.Errorf("script does not ask for folder \\CashPilot\\ and name Helper:\n%s", s)
	}
	if s := taskCommandScript(`Helper`); !strings.Contains(s, `-TaskPath '\' -TaskName 'Helper'`) {
		t.Errorf("a task at the root is not asked for in \\:\n%s", s)
	}
	// A quote in a name cannot end the literal.
	if s := taskCommandScript(`O'Brien\Helper`); !strings.Contains(s, `'\O''Brien\'`) {
		t.Errorf("quote not escaped:\n%s", s)
	}
	if !strings.Contains(s, "UTF8Encoding") {
		t.Error("the script does not write UTF-8, so a path with an accent would come back garbled")
	}
}

func TestParseTaskCommand(t *testing.T) {
	for in, want := range map[string]string{
		"":     "",
		"\r\n": "",
		`C:\Users\José\AppData\Local\Programs\CashPilot Desktop\CashPilot Desktop.exe`: `C:\Users\José\AppData\Local\Programs\CashPilot Desktop\CashPilot Desktop.exe`,
		"\ufeff\"C:\\Apps\\CashPilot Desktop.exe\"\r\n":                                `C:\Apps\CashPilot Desktop.exe`,
	} {
		if got := parseTaskCommand(in); got != want {
			t.Errorf("parseTaskCommand(%q) = %q, want %q", in, got, want)
		}
	}
}
