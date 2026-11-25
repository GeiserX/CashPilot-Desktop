//go:build windows

package bgservice

import (
	"fmt"
	"os"
	"testing"
)

// The real Task Scheduler round trip: register a task, read its command back
// through RegisteredCommand, remove it, and read "" back. It registers a task on
// the machine, so it runs on CI runners only (GITHUB_ACTIONS=true), never on a
// developer's PC. The path holds an accent, so a reader that used the console
// code page instead of UTF-8 would fail here.
func TestWindowsAgentReadsBackItsCommand(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("registers a real scheduled task; runs on CI runners only")
	}
	a := &windowsAgent{label: DefaultLabel, taskName: fmt.Sprintf(`CashPilotTest\Helper-%d`, os.Getpid())}
	exe := `C:\Prueba José\CashPilot Desktop\CashPilot Desktop.exe`
	t.Cleanup(func() { _ = a.Uninstall() })

	if got, err := a.RegisteredCommand(); err != nil || got != "" {
		t.Fatalf("before Install: RegisteredCommand = %q, %v; want \"\", nil", got, err)
	}
	if err := a.Install(exe, []string{"--daemon"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got, err := a.RegisteredCommand(); err != nil || got != exe {
		t.Fatalf("after Install: RegisteredCommand = %q, %v; want %q", got, err, exe)
	}
	_ = a.Stop() // not running; must not break anything
	if err := a.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if got, err := a.RegisteredCommand(); err != nil || got != "" {
		t.Fatalf("after Uninstall: RegisteredCommand = %q, %v; want \"\", nil", got, err)
	}
}
