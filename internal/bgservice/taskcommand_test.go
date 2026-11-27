package bgservice

import (
	"strings"
	"testing"
)

func TestTaskCommandScriptSplitsFolderAndName(t *testing.T) {
	s := taskCommandScript(`CashPilot\Helper`)
	if !strings.Contains(s, `GetFolder('\CashPilot').GetTask('Helper')`) {
		t.Errorf("script does not ask for folder \\CashPilot\\ and name Helper:\n%s", s)
	}
	if s := taskCommandScript(`Helper`); !strings.Contains(s, `GetFolder('\').GetTask('Helper')`) {
		t.Errorf("a task at the root is not asked for in \\:\n%s", s)
	}
	// A quote in a name cannot end the literal.
	if s := taskCommandScript(`O'Brien\Helper`); !strings.Contains(s, `GetFolder('\O''Brien')`) {
		t.Errorf("quote not escaped:\n%s", s)
	}
	if !strings.Contains(s, "-2147024894") {
		t.Error("the script does not read a missing task (0x80070002) as none")
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

func TestTaskStateScriptPrintsTheStateNumber(t *testing.T) {
	s := taskStateScript(`CashPilot\Helper`)
	if !strings.Contains(s, "[Console]::Out.Write([string]$task.State)") {
		t.Fatalf("the script does not print the task state:\n%s", s)
	}
	if strings.Contains(s, "Definition.Actions") {
		t.Fatal("the script still prints the task's command")
	}
	if !strings.Contains(s, `GetFolder('\CashPilot').GetTask('Helper')`) || !strings.Contains(s, "-2147024894") {
		t.Fatal("the script does not ask for the task, or does not read a missing task as none")
	}
}

func TestTaskStateRunning(t *testing.T) {
	for out, want := range map[string]bool{"4": true, "4\r\n": true, "\ufeff4": true, "3": false, "": false, "1": false} {
		if got := taskStateRunning(out); got != want {
			t.Errorf("taskStateRunning(%q) = %v, want %v", out, got, want)
		}
	}
}
