//go:build linux

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

func TestClampOomScoreAdj(t *testing.T) {
	cases := map[int]int{
		0:     0,
		500:   500,
		-500:  -500,
		1000:  1000,
		-1000: -1000,
		1500:  1000,  // above the kernel max clamps down
		-2000: -1000, // below the kernel min clamps up
	}
	for in, want := range cases {
		if got := clampOomScoreAdj(in); got != want {
			t.Errorf("clampOomScoreAdj(%d) = %d, want %d", in, got, want)
		}
	}
}

// TestApplyNativeResourceLimitsOomScoreAdj launches a real child we own and proves the
// OomScoreAdj knob reaches the kernel: after applyNativeResourceLimits, the child's
// /proc/<pid>/oom_score_adj reads back the requested value. Raising the value (more
// killable) is permitted unprivileged, so this is CI-safe on the Linux runner.
func TestApplyNativeResourceLimitsOomScoreAdj(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), stubBinName())
	if err := os.WriteFile(binPath, stubBinaryBytes(t), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(), "CASHPILOT_NATIVE_STUB=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stub: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })

	const adj = 753
	oom := adj
	applyNativeResourceLimits(cmd, catalog.ResourceLimits{OomScoreAdj: &oom})

	raw, err := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/oom_score_adj")
	if err != nil {
		t.Fatalf("read oom_score_adj: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != strconv.Itoa(adj) {
		t.Fatalf("oom_score_adj = %q, want %d", got, adj)
	}
}

// TestApplyNativeResourceLimitsNoOomScoreAdj proves a nil OomScoreAdj is a no-op: the
// child's oom_score_adj stays at its inherited default (0), not silently rewritten.
func TestApplyNativeResourceLimitsNoOomScoreAdj(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), stubBinName())
	if err := os.WriteFile(binPath, stubBinaryBytes(t), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(), "CASHPILOT_NATIVE_STUB=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stub: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })

	applyNativeResourceLimits(cmd, catalog.ResourceLimits{}) // no OomScoreAdj

	raw, err := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/oom_score_adj")
	if err != nil {
		t.Fatalf("read oom_score_adj: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "0" {
		t.Fatalf("oom_score_adj = %q, want 0 (untouched)", got)
	}
}
