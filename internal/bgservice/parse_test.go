package bgservice

import "testing"

func TestLaunchctlPrintRunning(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"running state", "cloud.cashpilot.desktop.helper = {\n\tstate = running\n\tpid = 4242\n}", true},
		{"not running state", "service = {\n\tstate = not running\n}", false},
		{"waiting state", "service = {\n\tstate = waiting\n}", false},
		{"pid fallback", "service = {\n\tpid = 91\n\tactive count = 1\n}", true},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := launchctlPrintRunning(c.out); got != c.want {
				t.Errorf("launchctlPrintRunning(%q) = %v, want %v", c.out, got, c.want)
			}
		})
	}
}

func TestSystemdActive(t *testing.T) {
	cases := map[string]bool{
		"active\n":     true,
		"active":       true,
		"inactive\n":   false,
		"failed\n":     false,
		"activating\n": false,
		"":             false,
	}
	for out, want := range cases {
		if got := systemdActive(out); got != want {
			t.Errorf("systemdActive(%q) = %v, want %v", out, got, want)
		}
	}
}

func TestSystemdEnabled(t *testing.T) {
	cases := map[string]bool{
		"enabled\n":         true,
		"enabled-runtime\n": true,
		"static\n":          true,
		"linked\n":          true,
		"disabled\n":        false,
		"masked\n":          false,
		"":                  false,
	}
	for out, want := range cases {
		if got := systemdEnabled(out); got != want {
			t.Errorf("systemdEnabled(%q) = %v, want %v", out, got, want)
		}
	}
}

func TestSchtasksRunning(t *testing.T) {
	running := "Folder: \\CashPilot\nHostName: PC\nTaskName: \\CashPilot\\Helper\nStatus: Running\nLogon Mode: Interactive only\n"
	ready := "TaskName: \\CashPilot\\Helper\nStatus: Ready\n"
	if !schtasksRunning(running) {
		t.Errorf("expected Running to parse as running")
	}
	if schtasksRunning(ready) {
		t.Errorf("expected Ready to parse as not running")
	}
	if schtasksRunning("") {
		t.Errorf("expected empty output to parse as not running")
	}
}
