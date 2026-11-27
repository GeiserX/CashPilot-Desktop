//go:build darwin

package bgservice

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestDarwinReplaceKeepsTheHelperWhenThePlistCannotBeWritten replaces a loaded
// throwaway job while its plist cannot be written. Replace must fail and leave
// the running job loaded, with its old plist.
func TestDarwinReplaceKeepsTheHelperWhenThePlistCannotBeWritten(t *testing.T) {
	if os.Getenv("CASHPILOT_LIVE_LAUNCHD") != "1" {
		t.Skip("set CASHPILOT_LIVE_LAUNCHD=1 to run the live launchd replace test")
	}

	const label = "cloud.cashpilot.desktop.replacetest"
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	t.Cleanup(func() {
		_ = exec.Command("launchctl", "bootout", target).Run()
		_ = os.Remove(plistPath)
		_ = os.RemoveAll(plistPath + ".tmp")
	})

	agent := New(label, t.TempDir())
	if err := agent.Install("/bin/sleep", []string{"3600"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	before, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatal(err)
	}

	// A folder, with a file in it, where the temporary plist goes makes the
	// write fail.
	if err := os.MkdirAll(filepath.Join(plistPath+".tmp", "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := agent.(Replacer).Replace("/bin/sleep", []string{"7200"}); err == nil {
		t.Fatal("Replace succeeded although the plist could not be written")
	}

	if out, err := exec.Command("launchctl", "print", target).CombinedOutput(); err != nil {
		t.Fatalf("the job was unloaded by a failed Replace: %v\n%s", err, out)
	}
	after, err := os.ReadFile(plistPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("the plist changed after a failed Replace (err %v)", err)
	}

	// Once the write works again, Replace loads the new program.
	if err := os.RemoveAll(plistPath + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := agent.(Replacer).Replace("/bin/sleep", []string{"7200"}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	out, err := exec.Command("launchctl", "print", target).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "7200") {
		t.Fatalf("the replaced job does not run the new arguments: %v\n%s", err, out)
	}
}
