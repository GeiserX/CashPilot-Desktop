package winvm_test

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	if got := readPlan(t, dir).AddedAdminSIDs; len(got) != 0 {
		t.Fatalf("AddedAdminSIDs = %v though the user was a member", got)
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

// TestRemoveLeavesAForeignSwitch: a second switch with our name and no owner
// token is someone else's, so Remove VM takes out only the recorded one.
func TestRemoveLeavesAForeignSwitch(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	sw := f.AddSwitch(winvm.ProductNames().Switch, "Internal")
	res := remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), "switch "+sw+" ") {
		t.Fatalf("Remove took out a switch it did not make:\n%s", strings.Join(f.Objects(), "\n"))
	}
	if !strings.Contains(strings.Join(res.Left, "\n"), sw) {
		t.Fatalf("Remove did not list the switch it left: %v", res.Left)
	}
}

// TestRemoveLeavesAForeignNATAndTask: someone replaced our NAT with one on
// another range and our guard task with their own, under our names. Neither
// is ours, so Remove VM leaves both and lists them.
func TestRemoveLeavesAForeignNATAndTask(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	n := winvm.ProductNames()
	f.AddForeignNAT(n.NAT, netip.MustParsePrefix("10.250.250.0/29"))
	f.AddForeignTask(n.GuardTask)
	res := remove(t, f, dir)
	objs := strings.Join(f.Objects(), "\n")
	if !strings.Contains(objs, "nat "+n.NAT+" 10.250.250.0/29") {
		t.Fatalf("Remove took out a NAT it did not make:\n%s", objs)
	}
	if !strings.Contains(objs, "task "+n.GuardTask+" ") {
		t.Fatalf("Remove took out a task it did not make:\n%s", objs)
	}
	left := strings.Join(res.Left, "\n")
	if !strings.Contains(left, "the NAT "+n.NAT) || !strings.Contains(left, "the scheduled task") {
		t.Fatalf("Remove did not list the NAT and task it left: %v", res.Left)
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

// TestRemoveKeepsTheGuardianAKeyProtectorUses: a VM with a local key protector
// and no vTPM (encrypted state only) still needs the guardian.
func TestRemoveKeepsTheGuardianAKeyProtectorUses(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	other := f.AddForeignVM("someone-elses-vm", false)
	if err := f.SetKeyProtector(other); err != nil {
		t.Fatal(err)
	}
	res := remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), "guardian=true") {
		t.Fatal("Remove deleted the guardian a key-protector VM uses")
	}
	if len(res.Kept) != 1 || !strings.Contains(res.Kept[0], other) {
		t.Fatalf("kept = %v", res.Kept)
	}
}

// TestRemoveFindsRenamedObjects: a VM or a switch the user renamed after the
// build is still this install's by its recorded ID, so Remove takes it out
// with its disk, and the host ends as it was.
func TestRemoveFindsRenamedObjects(t *testing.T) {
	for _, what := range []string{"vm", "switch", "both"} {
		t.Run(what, func(t *testing.T) {
			f := hvfake.New()
			before := f.Objects()
			dir := build(t, f)
			p := readPlan(t, dir)
			if what != "switch" {
				if err := f.RenameVM(p.VMID, "my renamed vm"); err != nil {
					t.Fatal(err)
				}
			}
			if what != "vm" {
				if err := f.RenameSwitch(p.SwitchID, "my renamed switch"); err != nil {
					t.Fatal(err)
				}
			}
			res := remove(t, f, dir)
			diffObjects(t, before, f.Objects())
			if len(res.Left) != 0 || len(res.Kept) != 0 {
				t.Fatalf("left %v, kept %v", res.Left, res.Kept)
			}
		})
	}
}

// TestRemoveDeletesALeftoverProofFolder: a proof killed in the middle of a
// capture leaves DataDir\proof\pktmon-N; Remove VM still finishes.
func TestRemoveDeletesALeftoverProofFolder(t *testing.T) {
	f := hvfake.New()
	before := f.Objects()
	dir := build(t, f)
	left := filepath.Join(dir, "proof", "pktmon-1")
	if err := os.MkdirAll(left, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "capture.etl"), []byte("etl"), 0o600); err != nil {
		t.Fatal(err)
	}
	remove(t, f, dir)
	diffObjects(t, before, f.Objects())
	if _, err := os.Stat(filepath.Join(dir, "proof")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the proof folder is still there: %v", err)
	}
}

// TestRemoveDeletesSetupLeftovers: a setup killed while it wrote the answer
// ISO, a file, or ran the exe leaves the ISO's staging folder and temp image,
// writeAtomic's temp files and setup.ps1's params copy. Remove VM deletes
// them. A file setup never makes stays and is listed, so DataDir is never
// kept without a word.
func TestRemoveDeletesSetupLeftovers(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	made := []string{
		filepath.Join("answer.iso.stage", "autounattend.xml"), "answer.iso.tmp",
		"." + winvm.StateFile + ".123456.tmp", "." + winvm.PlanFile + ".42.tmp",
		filepath.Join("guard", "setup-params.json"),
	}
	for _, rel := range made {
		write(rel)
	}
	write(filepath.Join("guard", "CashPilot-guard.exe")) // remove.ps1 deletes it
	write(filepath.Join("guard", "."+winvm.StateFile+".7.tmp"))
	write(filepath.Join("proof", "pktmon-1", "private.txt"))
	write("notes.txt")
	res := remove(t, f, dir)
	for _, rel := range append(made, "answer.iso.stage") {
		if _, err := os.Lstat(filepath.Join(dir, rel)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there after Remove (%v)", rel, err)
		}
	}
	left := strings.Join(res.Left, "\n")
	if !strings.Contains(left, "notes.txt") || !strings.Contains(left, "private.txt") || !strings.Contains(left, "."+winvm.StateFile+".7.tmp") {
		t.Errorf("Remove did not list the file it kept: %v", res.Left)
	}
	if strings.Contains(left, "CashPilot-guard.exe") {
		t.Errorf("Remove listed the exe copy remove.ps1 deletes: %v", res.Left)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("Remove deleted a file setup never makes: %v", err)
	}
}

func TestRemoveLeavesVMCopyWithTheOwnerTag(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	p := readPlan(t, dir)
	copyID := f.AddForeignVM(p.Names.VM, false)
	if err := f.SetVMOwner(copyID, p.Owner); err != nil {
		t.Fatal(err)
	}
	res := remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), "vm "+copyID+" ") ||
		!strings.Contains(strings.Join(res.Left, "\n"), copyID) {
		t.Fatalf("Remove did not keep and list the copied VM: %+v", res)
	}
}

func TestRemoveLeavesRecordedVMWithoutTheOwnerTag(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	p := readPlan(t, dir)
	if err := f.SetVMOwner(p.VMID, ""); err != nil {
		t.Fatal(err)
	}
	res := remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), "vm "+p.VMID+" ") ||
		!strings.Contains(strings.Join(res.Left, "\n"), p.VMID) {
		t.Fatalf("Remove did not keep and list the untagged VM: %+v", res)
	}
}

func TestRemoveLeavesBothVMsWhenRecoveryIsAmbiguous(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	p := readPlan(t, dir)
	copyID := f.AddForeignVM(p.Names.VM, false)
	if err := f.SetVMOwner(copyID, p.Owner); err != nil {
		t.Fatal(err)
	}
	vmID := p.VMID
	p.VMID, p.AdapterID = "", ""
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, winvm.PlanFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	res := remove(t, f, dir)
	for _, id := range []string{vmID, copyID} {
		if !strings.Contains(strings.Join(f.Objects(), "\n"), "vm "+id+" ") ||
			!strings.Contains(strings.Join(res.Left, "\n"), id) {
			t.Fatalf("Remove did not keep and list VM %s: %+v", id, res)
		}
	}
}

func TestRemoveListsAndKeepsUnknownTemps(t *testing.T) {
	for _, withPlan := range []bool{true, false} {
		t.Run(strconv.FormatBool(withPlan), func(t *testing.T) {
			f := hvfake.New()
			dir := t.TempDir()
			if withPlan {
				dir = build(t, f)
			}
			name := filepath.Join(dir, ".somebody-elses-file.123.tmp")
			if err := os.WriteFile(name, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			res := remove(t, f, dir)
			if _, err := os.Stat(name); err != nil {
				t.Fatalf("unknown temp was removed: %v", err)
			}
			if !strings.Contains(strings.Join(res.Left, "\n"), name) {
				t.Fatalf("unknown temp was not listed: %v", res.Left)
			}
		})
	}
}

func TestRemoveKeepsAnUnrecordedDiskUnderTheVHDXRoot(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	p := readPlan(t, dir)
	path := p.VHDXDir + `\foreign.vhdx`
	f.SetDiskPath(p.VMID, path)
	res := remove(t, f, dir)
	if !strings.Contains(strings.Join(res.Left, "\n"), path) {
		t.Fatalf("Remove did not list the foreign disk it should keep: %+v", res)
	}
}
