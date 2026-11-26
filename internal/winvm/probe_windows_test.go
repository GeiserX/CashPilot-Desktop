//go:build windows

package winvm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
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
	t.Logf("facts after %v: %+v", time.Since(start), f)
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
		t.Error("the CPU architecture was not read")
	}
	if f.Firmware == Unknown || f.SLAT == Unknown {
		t.Error("the firmware and SLAT flags were not read")
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
	if f.LanmanServer == "unknown" {
		t.Errorf("the Server service's start type was not read (%v)", f.Problems["services"])
	}
	if f.OtherHypervisors == Unknown {
		t.Errorf("the VirtualBox and VMware services were not read (%v)", f.Problems["services"])
	}
}

// The CIM scripts must run and parse on real Windows. WMI on a loaded CI runner can
// take longer than the reading's deadline, which the reading survives by leaving
// these facts unknown, so this test gives each script its own generous deadline:
// it checks the script and its parser, not the speed.
func TestCIMScriptsOnThisPC(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("the private script directory requires elevation")
	}
	checkCIMReads(t, systemProbes{}.cpu, systemProbes{}.features)
}

func TestCIMReadsThroughWMI(t *testing.T) {
	checkCIMReads(t, unelevatedWMI.cpu, unelevatedWMI.features)
}

func checkCIMReads(t *testing.T, cpu, features func(context.Context) (func(*HostFacts), error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	start := time.Now()
	apply, err := cpu(ctx)
	if err != nil {
		t.Fatalf("the CPU probe failed after %v: %v", time.Since(start), err)
	}
	f := unknownFacts("windows")
	apply(&f)
	t.Logf("CPU probe took %v: hypervisor %s, arch %s", time.Since(start), f.HypervisorPresent, f.Arch)
	if f.HypervisorPresent == Unknown || f.Arch == "unknown" {
		t.Errorf("the CPU probe answered without HypervisorPresent or Architecture: %+v", f)
	}
	if f.LastBoot == "" || f.Model == "" {
		t.Errorf("the CPU probe answered without the boot time or the machine: boot %q, maker %q, model %q", f.LastBoot, f.Manufacturer, f.Model)
	}

	start = time.Now()
	apply, err = features(ctx)
	if err != nil {
		t.Fatalf("the feature probe failed after %v: %v", time.Since(start), err)
	}
	f = unknownFacts("windows")
	apply(&f)
	raw, _ := json.Marshal(map[string]Tri{"wsl": f.WSLFeature, "vmp": f.VMPlatformFeature})
	t.Logf("feature probe took %v: %s", time.Since(start), raw)
	if f.WSLFeature == Unknown || f.VMPlatformFeature == Unknown {
		t.Errorf("the feature probe answered without the feature states: %s", raw)
	}
}

// The CIM reads run System32's Windows PowerShell by its full path, never the
// bare name PATH would resolve.
func TestCIMRunsPowerShellByItsFullPath(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("the private script directory requires elevation")
	}
	cmd, release, err := cimCommand(context.Background(), "exit 0")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "WindowsPowerShell", "v1.0", "powershell.exe")
	if !filepath.IsAbs(cmd.Path) || !strings.EqualFold(cmd.Path, want) {
		t.Fatalf("the CIM reads run %q, want %q", cmd.Path, want)
	}
}
