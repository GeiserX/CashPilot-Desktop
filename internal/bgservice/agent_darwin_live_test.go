//go:build darwin

package bgservice

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDarwinLaunchdLive exercises the REAL launchd path (write plist → launchctl bootstrap
// → launchctl print → bootout → verify gone) against a THROWAWAY label with a harmless
// ProgramArguments (/bin/sleep), never the real daemon. It is gated behind
// CASHPILOT_LIVE_LAUNCHD=1 so ordinary `go test ./...` never touches the user's real
// LaunchAgents. Cleanup (bootout + plist removal) is deferred via t.Cleanup so a mid-test
// failure still leaves the system clean.
func TestDarwinLaunchdLive(t *testing.T) {
	if os.Getenv("CASHPILOT_LIVE_LAUNCHD") != "1" {
		t.Skip("set CASHPILOT_LIVE_LAUNCHD=1 to run the live launchd install/uninstall test")
	}

	const label = "cloud.cashpilot.desktop.helpertest"
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)

	// Guaranteed cleanup even if an assertion below fails.
	t.Cleanup(func() {
		_ = exec.Command("launchctl", "bootout", target).Run()
		_ = os.Remove(plistPath)
	})

	agent := New(label, t.TempDir())

	// Install a throwaway agent that just sleeps (kept alive by KeepAlive).
	if err := agent.Install("/bin/sleep", []string{"3600"}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	printOut, err := exec.Command("launchctl", "print", target).CombinedOutput()
	if err != nil {
		t.Fatalf("launchctl print after install failed (agent not loaded): %v\n%s", err, printOut)
	}
	t.Logf("launchctl print after install:\n%s", printOut)

	st, err := agent.Status()
	if err != nil {
		t.Fatalf("Status after install: %v", err)
	}
	if !st.Installed {
		t.Fatalf("Status.Installed = false after install (%+v)", st)
	}
	t.Logf("Status after install: %+v", st)

	// Uninstall and prove it is fully gone.
	if err := agent.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	if out, err := exec.Command("launchctl", "print", target).CombinedOutput(); err == nil {
		t.Fatalf("agent still loaded after Uninstall:\n%s", out)
	} else {
		t.Logf("launchctl print after uninstall (expected non-zero exit = gone): %v", err)
	}

	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist still present after Uninstall (stat err = %v)", err)
	}

	st, err = agent.Status()
	if err != nil {
		t.Fatalf("Status after uninstall: %v", err)
	}
	if st.Installed || st.Running {
		t.Fatalf("Status still installed/running after uninstall: %+v", st)
	}
	t.Logf("Status after uninstall: %+v", st)
}
