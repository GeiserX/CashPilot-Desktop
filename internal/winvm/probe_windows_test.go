//go:build windows

package winvm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSnapshotReadsThisPC runs the real probes on the Windows machine running the
// tests (the windows-latest CI runner, or a dev PC). It changes nothing. It checks
// that the reads work without admin-only APIs failing the whole reading, that the
// deadline holds, and that facts every Windows PC has come back known.
func TestSnapshotReadsThisPC(t *testing.T) {
	start := time.Now()
	f := Snapshot(context.Background())
	if took := time.Since(start); took > ProbeTimeout+5*time.Second {
		t.Fatalf("Snapshot took %v, past its %v deadline", took, ProbeTimeout)
	}
	t.Logf("facts: %+v", f)
	if !f.Supported {
		t.Fatal("Windows reported as unsupported")
	}
	if f.Build < 10000 {
		t.Errorf("CurrentBuild = %d; the registry read failed (%v)", f.Build, f.Problems["os"])
	}
	if f.EditionID == "" {
		t.Error("EditionID is empty")
	}
	if f.Arch == "unknown" {
		t.Errorf("Win32_Processor.Architecture was not read (%v)", f.Problems["cim"])
	}
	if !strings.HasPrefix(f.UserSID, "S-1-5-") {
		t.Errorf("user SID %q (%v)", f.UserSID, f.Problems["account"])
	}
	if f.DockerUsersGroup == Unknown {
		t.Errorf("whether docker-users exists was not read (%v)", f.Problems["account"])
	}
	if f.RestartPending == Unknown {
		t.Errorf("the restart markers were not read (%v)", f.Problems["restart"])
	}
	if f.DockerService == "unknown" {
		t.Error("the service manager was not read")
	}
}
