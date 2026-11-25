//go:build winvmlab

package winvm_test

import (
	"context"
	"slices"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// TestLabBuildKeepsEnhancedSession: a lab build honours the overrides: its
// own names, a lower RAM floor, and ESKeep, which never calls
// SetEnhancedSession, in the build or in Remove VM.
func TestLabBuildKeepsEnhancedSession(t *testing.T) {
	f := hvfake.New()
	f.SetHyperVAdmin(testUserSID, true)
	f.SetEnhanced(true)
	f.InstalledRAM, f.FreeRAM = 8<<30, 6<<30
	sp := setupParams()
	sp.Lab = &winvm.LabOverrides{Instance: 2, MinHostRAM: 8 << 30, GuestMemory: 4 << 30, KeepEnhancedSession: true}
	n, _, _, err := winvm.SetupNames(sp)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	before := f.Objects()
	if _, err := winvm.Build(context.Background(), buildDeps(t, f, dir), sp); err != nil {
		t.Fatal(err)
	}
	p := readPlan(t, dir)
	if p.Names != n || p.EnhancedSession != winvm.ESKeep || p.MemoryBytes != 4<<30 {
		t.Fatalf("the plan ignored the lab settings: %+v", p)
	}
	if _, err := winvm.Remove(context.Background(), winvm.RemoveDeps{HV: f, Dir: dir}, n); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.Calls(), "SetEnhancedSession") {
		t.Fatal("ESKeep called SetEnhancedSession")
	}
	diffObjects(t, before, f.Objects())
}
