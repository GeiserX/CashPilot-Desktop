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
// child's oom_score_adj is left at whatever it inherited, not rewritten. (The inherited
// value is NOT assumed to be 0 — CI runners often start with a non-zero oom_score_adj that
// the child inherits; the invariant under test is "unchanged", not a specific number.)
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

	path := "/proc/" + strconv.Itoa(cmd.Process.Pid) + "/oom_score_adj"
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read oom_score_adj (before): %v", err)
	}

	applyNativeResourceLimits(cmd, catalog.ResourceLimits{}) // no OomScoreAdj

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read oom_score_adj (after): %v", err)
	}
	if b, a := strings.TrimSpace(string(before)), strings.TrimSpace(string(after)); a != b {
		t.Fatalf("nil OomScoreAdj changed oom_score_adj from %q to %q, want untouched", b, a)
	}
}

func TestParseCgroupV2Path(t *testing.T) {
	// A hybrid /proc/<pid>/cgroup: v1 controller lines plus the v2 unified "0::" line.
	const content = "12:pids:/user.slice\n0::/user.slice/user-1000.slice/session-3.scope\n"
	got, err := parseCgroupV2Path(content)
	if err != nil {
		t.Fatalf("parseCgroupV2Path: %v", err)
	}
	if want := "/user.slice/user-1000.slice/session-3.scope"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	// cgroup v1-only content has no "0::" line → error (caller then applies no cap).
	if _, err := parseCgroupV2Path("11:memory:/foo\n10:cpu:/bar\n"); err == nil {
		t.Fatal("expected error for cgroup v1-only content")
	}
}

func TestCgroupMemoryMaxAt(t *testing.T) {
	parent := t.TempDir()
	pid := os.Getpid()
	const max = int64(768) << 20
	if err := cgroupMemoryMaxAt(parent, pid, max); err != nil {
		t.Fatalf("cgroupMemoryMaxAt: %v", err)
	}
	child := filepath.Join(parent, "cashpilot-"+strconv.Itoa(pid))
	assertFileContent(t, filepath.Join(child, "memory.max"), strconv.FormatInt(max, 10))
	assertFileContent(t, filepath.Join(child, "cgroup.procs"), strconv.Itoa(pid))
	assertFileContent(t, filepath.Join(parent, "cgroup.subtree_control"), "+memory")
}

func TestApplyCgroupMemoryMaxDegradesGracefully(t *testing.T) {
	// Point the mount at a regular file (not a directory) so the cgroup writes cannot
	// succeed: applyCgroupMemoryMax must return an error rather than panic, and its caller
	// (applyNativeResourceLimits) ignores it so the earner runs uncapped.
	orig := cgroupV2Mount
	t.Cleanup(func() { cgroupV2Mount = orig })
	bad := filepath.Join(t.TempDir(), "not-a-cgroup-mount")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cgroupV2Mount = bad
	if err := applyCgroupMemoryMax(os.Getpid(), 1<<20); err == nil {
		t.Fatal("expected applyCgroupMemoryMax to fail on an unwritable mount")
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := strings.TrimSpace(string(b)); got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
