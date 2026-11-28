package macvm

import (
	"os/exec"
	"runtime"
	"testing"
)

// Only the kill that stop sends ends a capture cleanly. A tcpdump that exited
// by itself saw only part of the step.
func TestKilledByUs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a Unix shell")
	}
	killed := exec.Command("/bin/sleep", "30")
	if err := killed.Start(); err != nil {
		t.Fatal(err)
	}
	_ = killed.Process.Kill()
	if !killedByUs(killed.Wait()) {
		t.Fatal("a capture ended by our kill was refused")
	}
	if killedByUs(exec.Command("/bin/sh", "-c", "exit 1").Run()) {
		t.Fatal("a capture that exited by itself was accepted")
	}
	if killedByUs(exec.Command("/bin/sh", "-c", "exit 0").Run()) {
		t.Fatal("a capture that ended cleanly by itself was accepted")
	}
}
