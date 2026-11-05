package activity

import (
	"os/exec"
	"runtime"
	"testing"
)

// The vendored helper, run as the helper container runs it, prints what
// ParseProbe accepts. The byte comparison with the web copy cannot catch an
// envelope change that both copies share; this does.
func TestHelperOutputParses(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the helper reads /proc/net, which only Linux has")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 here")
	}
	cmd := exec.Command(python, "-I", "-c", ProcnetScript)
	cmd.Env = []string{"PROBE_SNAPSHOTS=1", "PROBE_GAP=0", `PROBE_CTX={"gateways":[],"dns":[],"subnets":[],"peers":null}`}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper failed: %v", err)
	}
	if _, err := ParseProbe(out); err != nil {
		t.Fatalf("ParseProbe refused the helper's own output: %v\n%s", err, out)
	}
}
