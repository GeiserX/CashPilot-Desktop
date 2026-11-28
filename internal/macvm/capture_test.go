package macvm

import (
	"fmt"
	"os/exec"
	"runtime"
	"slices"
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

func TestCanaryKeepsOnlyRegisteredTokens(t *testing.T) {
	c := &canary{got: map[string]bool{}, want: map[string]int{}}
	tok := newToken()
	c.expect(tok)
	c.note([]byte(tok + "-gateway/tcp\n" + newToken() + "-gateway/tcp\nnoise " + tok))
	for i := 0; i < 2*maxLabels; i++ {
		c.note([]byte(fmt.Sprintf("%s-flood%d", tok, i)))
	}
	if got := c.arrived(tok); !slices.Contains(got, "gateway/tcp") || len(got) != maxLabels {
		t.Fatalf("the canary must keep the registered token's labels, bounded: %d %v", len(got), got[:3])
	}
	if len(c.got) != maxLabels {
		t.Fatalf("an unregistered token or noise was kept: %d entries", len(c.got))
	}
}
