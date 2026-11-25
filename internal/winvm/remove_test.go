package winvm_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

func build(t *testing.T, f *hvfake.HyperV) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := winvm.Build(context.Background(), buildDeps(t, f, dir), setupParams()); err != nil {
		t.Fatal(err)
	}
	return dir
}

func remove(t *testing.T, f *hvfake.HyperV, dir string) winvm.RemoveResult {
	t.Helper()
	res, err := winvm.Remove(context.Background(), winvm.RemoveDeps{HV: f, Dir: dir, Logf: t.Logf}, winvm.ProductNames())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func diffObjects(t *testing.T, before, after []string) {
	t.Helper()
	if !slices.Equal(before, after) {
		t.Fatalf("the host is not back as it was:\n%s\n---\n%s", strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}

func TestRemoveAfterBuildLeavesTheHostAsItWas(t *testing.T) {
	f := hvfake.New()
	before := f.Objects()
	dir := build(t, f)
	res := remove(t, f, dir)
	diffObjects(t, before, f.Objects())
	if len(res.Left) != 0 || len(res.Kept) != 0 {
		t.Fatalf("left %v, kept %v", res.Left, res.Kept)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("files left in the data folder: %v", names)
	}
	// Running it again finds nothing and changes nothing.
	res = remove(t, f, dir)
	diffObjects(t, before, f.Objects())
	if len(res.Left) != 0 {
		t.Fatalf("second run left %v", res.Left)
	}
}

func TestRemoveRestoresEnhancedSession(t *testing.T) {
	for _, was := range []bool{true, false} {
		f := hvfake.New()
		f.SetEnhanced(was)
		dir := build(t, f)
		remove(t, f, dir)
		if got := strings.Contains(strings.Join(f.Objects(), "\n"), "enhancedSession=true"); got != was {
			t.Fatalf("Enhanced Session was %v before the build and is %v after Remove", was, got)
		}
	}
}

func TestRemoveKeepsTheGuardianItDidNotMake(t *testing.T) {
	f := hvfake.New()
	f.SetGuardian(true)
	dir := build(t, f)
	if readPlan(t, dir).CreatedGuardian {
		t.Fatal("CreatedGuardian is set though the guardian was there")
	}
	remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), "guardian=true") {
		t.Fatal("Remove deleted a guardian setup did not create")
	}
}

func TestRemoveKeepsTheGuardianAnotherVMUses(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	other := f.AddForeignVM("someone-elses-vm", true)
	res := remove(t, f, dir)
	objs := strings.Join(f.Objects(), "\n")
	if !strings.Contains(objs, "guardian=true") {
		t.Fatal("Remove deleted the guardian another VM uses")
	}
	if !strings.Contains(objs, other) {
		t.Fatal("Remove touched another VM")
	}
	if len(res.Kept) != 1 || !strings.Contains(res.Kept[0], "guardian") {
		t.Fatalf("kept = %v", res.Kept)
	}
}

func TestRemoveKeepsAMembershipItDidNotAdd(t *testing.T) {
	f := hvfake.New()
	f.SetHyperVAdmin(testUserSID, true)
	dir := build(t, f)
	if readPlan(t, dir).AddedAdminSID != "" {
		t.Fatal("AddedAdminSID is set though the user was a member")
	}
	remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), "hyperv-admin "+testUserSID) {
		t.Fatal("Remove took out a membership setup did not add")
	}
}

func TestRemoveLeavesForeignObjects(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	n := winvm.ProductNames()
	f.AddForeignRule(n.FirewallGroup, "someone-elses-rule")
	vm := f.AddForeignVM(n.VM, false)
	res := remove(t, f, dir)
	objs := strings.Join(f.Objects(), "\n")
	if !strings.Contains(objs, "someone-elses-rule") || !strings.Contains(objs, vm) {
		t.Fatalf("Remove took out an object it did not make:\n%s", objs)
	}
	left := strings.Join(res.Left, "\n")
	if !strings.Contains(left, "someone-elses-rule") || !strings.Contains(left, vm) {
		t.Fatalf("Remove did not list what it left: %v", res.Left)
	}
}

func TestRemoveAfterAHalfBuild(t *testing.T) {
	for _, m := range []string{"EnsureNAT", "EnsureGuardian", "CreateVM", "Start", "RegisterGuardTask"} {
		t.Run(m, func(t *testing.T) {
			f := hvfake.New()
			before := f.Objects()
			dir := t.TempDir()
			f.FailNext(m, errors.New("injected"))
			if _, err := winvm.Build(context.Background(), buildDeps(t, f, dir), setupParams()); err == nil {
				t.Fatal("the build passed")
			}
			remove(t, f, dir)
			diffObjects(t, before, f.Objects())
		})
	}
}

func TestRemoveResumesAfterAFailure(t *testing.T) {
	f := hvfake.New()
	before := f.Objects()
	dir := build(t, f)
	f.FailNext("RemoveSwitch", errors.New("injected"))
	if _, err := winvm.Remove(context.Background(), winvm.RemoveDeps{HV: f, Dir: dir}, winvm.ProductNames()); err == nil {
		t.Fatal("Remove passed with RemoveSwitch failing")
	}
	if _, err := os.Stat(filepath.Join(dir, winvm.PlanFile)); err != nil {
		t.Fatalf("desired.json is gone after a failed Remove: %v", err)
	}
	remove(t, f, dir)
	diffObjects(t, before, f.Objects())
}

// TestRemoveWithoutAPlanTouchesNothing: with no desired.json this install
// cannot tell its objects apart, so it only lists them.
func TestRemoveWithoutAPlanTouchesNothing(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	if err := os.Remove(filepath.Join(dir, winvm.PlanFile)); err != nil {
		t.Fatal(err)
	}
	before := f.Objects()
	res := remove(t, f, dir)
	diffObjects(t, before, f.Objects())
	if len(res.Left) < 4 {
		t.Fatalf("left = %v, want the VM, switch, NAT, rule and task listed", res.Left)
	}
}
