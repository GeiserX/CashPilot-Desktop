package winvm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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

// keptBesidesCertificates is kept without the guardian's certificates, which
// every Remove of a guardian keeps.
func keptBesidesCertificates(kept []string) []string {
	return slices.DeleteFunc(slices.Clone(kept), func(s string) bool {
		return strings.Contains(s, `Cert:\LocalMachine\Shielded VM Local Certificates`)
	})
}

func diffObjects(t *testing.T, before, after []string) {
	t.Helper()
	if !slices.Equal(before, after) {
		t.Fatalf("the host is not back as it was:\n%s\n---\n%s", strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}

func TestRemoveAfterBuildLeavesTheHostAsItWas(t *testing.T) {
	f := hvfake.New()
	f.SetHyperVAdmin(testUserSID, true)
	before := f.Objects()
	dir := build(t, f)
	res := remove(t, f, dir)
	diffObjects(t, before, f.Objects())
	if len(res.Left) != 0 || len(keptBesidesCertificates(res.Kept)) != 0 {
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
	f.SetHyperVAdmin(testUserSID, true)
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
	for _, m := range []string{"EnsureNAT", "EnsureGuardian", "CreateVM", "BootKeys", "RegisterGuardTask"} {
		t.Run(m, func(t *testing.T) {
			f := hvfake.New()
			f.SetHyperVAdmin(testUserSID, true)
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
	f.SetHyperVAdmin(testUserSID, true)
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

// TestRemoveFinishSeesOnlyAnUnfinishedRemove: the bootstrap starts Remove
// with no standard error, so Finish is where its result is logged. Finish
// runs while desired.json remains, with the full result and the error, and
// before the host lock is released, so the next Remove cannot start between
// them. A finished Remove calls no Finish: its folder is gone.
func TestRemoveFinishSeesOnlyAnUnfinishedRemove(t *testing.T) {
	f := hvfake.New()
	f.SetHyperVAdmin(testUserSID, true)
	dir := build(t, f)
	var calls int
	var got winvm.RemoveResult
	var gotErr, lockErr error
	d := winvm.RemoveDeps{HV: f, Dir: dir, Finish: func(res winvm.RemoveResult, err error) {
		calls++
		got, gotErr = res, err
		if runtime.GOOS == "windows" {
			// Another Remove must wait for the host lock while Finish runs.
			ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := winvm.Remove(ctx, winvm.RemoveDeps{HV: f, Dir: t.TempDir()}, winvm.ProductNames())
				done <- err
			}()
			lockErr = <-done
		}
	}}
	f.FailNext("RemoveSwitch", errors.New("injected"))
	_, err := winvm.Remove(context.Background(), d, winvm.ProductNames())
	if err == nil || calls != 1 || gotErr == nil || !strings.Contains(gotErr.Error(), "injected") {
		t.Fatalf("Remove %v; Finish ran %d times with %v", err, calls, gotErr)
	}
	if !slices.ContainsFunc(got.Removed, func(s string) bool { return strings.HasPrefix(s, "the VM ") }) {
		t.Fatalf("Finish saw removed %v, want the VM", got.Removed)
	}
	if runtime.GOOS == "windows" && !errors.Is(lockErr, context.DeadlineExceeded) {
		t.Fatalf("a second Remove did not wait for the lock while Finish ran: %v", lockErr)
	}
	if _, err := winvm.Remove(context.Background(), d, winvm.ProductNames()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("a finished Remove called Finish (%d calls)", calls)
	}
}

// TestRemoveReportsTheGuardianCertificatesItKeeps: Windows leaves the
// guardian's two certificates when the guardian goes, and so does Remove,
// because a copy or backup of the VM needs them. The result says where they
// are, so the owner can delete them.
func TestRemoveReportsTheGuardianCertificatesItKeeps(t *testing.T) {
	f := hvfake.New()
	f.SetHyperVAdmin(testUserSID, true)
	res := remove(t, f, build(t, f))
	if !slices.Contains(res.Removed, "the vTPM guardian") ||
		!slices.ContainsFunc(res.Kept, func(s string) bool { return strings.Contains(s, `Cert:\LocalMachine\Shielded VM Local Certificates`) }) {
		t.Fatalf("removed %v, kept %v", res.Removed, res.Kept)
	}
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
	f.SetHyperVAdmin(testUserSID, true)
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
			f.SetHyperVAdmin(testUserSID, true)
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
			if len(res.Left) != 0 || len(keptBesidesCertificates(res.Kept)) != 0 {
				t.Fatalf("left %v, kept %v", res.Left, res.Kept)
			}
		})
	}
}

// TestRemoveDeletesALeftoverProofFolder: a proof killed in the middle of a
// capture leaves DataDir\proof\pktmon-N; Remove VM still finishes.
func TestRemoveKeepsUnverifiedFilesAndReplacements(t *testing.T) {
	f := hvfake.New()
	f.SetHyperVAdmin(testUserSID, true)
	before := f.Objects()
	dir := build(t, f)
	status := filepath.Join(dir, winvm.StatusFile)
	if err := os.Rename(status, status+".previous"); err != nil {
		t.Fatal(err)
	}
	files := []string{
		".status.json.123.tmp",
		filepath.Join("proof", "pktmon-123", "capture.etl"),
		filepath.Join("answer.iso.stage", "autounattend.xml"),
		filepath.Join("guard", "CashPilot-guard.exe"),
		winvm.StatusFile,
	}
	for _, rel := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("independent file"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res := remove(t, f, dir)
	diffObjects(t, before, f.Objects())
	for _, rel := range files {
		path := filepath.Join(dir, rel)
		b, err := os.ReadFile(path)
		if string(b) != "independent file" || err != nil {
			t.Errorf("independent file %s changed: %q, %v", rel, b, err)
		}
		if !strings.Contains(strings.Join(res.Left, "\n"), path) {
			t.Errorf("removal did not report %s", rel)
		}
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
	copyID := f.AddForeignVM("copied-vm", false)
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
	path := p.Names.DataDir + `\foreign.vhdx`
	f.SetDiskPath(p.VMID, path)
	res := remove(t, f, dir)
	if !strings.Contains(strings.Join(res.Left, "\n"), path) {
		t.Fatalf("Remove did not list the foreign disk it should keep: %+v", res)
	}
}

func TestRemoveRetryPreservesIndependentAdminGrant(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	sid := setupParams().UserSID
	f.FailNext("RemoveFirewall", errors.New("interrupted removal"))
	if _, err := winvm.Remove(context.Background(), winvm.RemoveDeps{HV: f, Dir: dir}, winvm.ProductNames()); err == nil {
		t.Fatal("expected an incomplete removal")
	}
	f.SetHyperVAdmin(sid, false)
	if added, err := f.AddHyperVAdmin(context.Background(), sid); err != nil || !added {
		t.Fatalf("could not restore independent grant: %v, %v", added, err)
	}
	remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), sid) {
		t.Fatal("retry revoked the independent grant")
	}
}

func TestRemoveLeavesEnhancedSessionEnabledAfterBuild(t *testing.T) {
	f := hvfake.New()
	f.SetEnhanced(false)
	dir := build(t, f)
	f.SetEnhanced(true)
	remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), "enhancedSession=true") {
		t.Fatal("removal turned off Enhanced Session without a setup change")
	}
}

func TestR6RemovePreservesGrantRecreatedBeforeFirstRemove(t *testing.T) {
	ctx := context.Background()
	f := hvfake.New()
	f.SetEnhanced(false)
	dir := build(t, f)
	sid := setupParams().UserSID
	if err := f.RemoveHyperVAdmin(ctx, sid); err != nil {
		t.Fatal(err)
	}
	if added, err := f.AddHyperVAdmin(ctx, sid); err != nil || !added {
		t.Fatalf("recreate grant: %v, %v", added, err)
	}
	res := remove(t, f, dir)
	if !slices.Contains(f.Objects(), "hyperv-admin "+sid) {
		t.Fatal("first Remove revoked an independently recreated grant")
	}
	if !strings.Contains(strings.Join(res.Kept, "\n"), sid) {
		t.Fatalf("Remove did not report the grant: %+v", res)
	}
}

func TestR6RemoveKeepsNATWhenAnotherVMUsesTheSwitch(t *testing.T) {
	f := hvfake.New()
	f.SetEnhanced(false)
	dir := build(t, f)
	p := readPlan(t, dir)
	other := f.AddForeignVM("other-vm", false)
	if err := f.SetAdapterSwitch(other, p.Names.Switch); err != nil {
		t.Fatal(err)
	}
	res := remove(t, f, dir)
	if !slices.Contains(f.Objects(), "nat "+p.Names.NAT+" "+p.Subnet.String()) ||
		!strings.Contains(strings.Join(res.Kept, "\n"), "the NAT "+p.Names.NAT) {
		t.Fatalf("Remove did not keep and report the NAT serving the other VM: %+v", res)
	}
}

func TestR6RemovePreservesAdminChangedFirewallRule(t *testing.T) {
	f := hvfake.New()
	f.SetEnhanced(false)
	dir := build(t, f)
	p := readPlan(t, dir)
	if err := f.SetL3(context.Background(), p.Names, p.Owner, false); err != nil {
		t.Fatal(err)
	}
	res := remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), "enabled=false") ||
		!strings.Contains(strings.Join(res.Left, "\n"), "firewall rule differs from Setup") {
		t.Fatalf("Remove did not keep and report the changed firewall rule: %+v", res)
	}
}

func TestR6RemoveLeavesEnhancedSessionPolicyFromLegacyPlan(t *testing.T) {
	f := hvfake.New()
	f.SetEnhanced(false)
	dir := build(t, f)
	path := filepath.Join(dir, winvm.PlanFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	plan["enhancedSessionOriginal"] = true
	plan["enhancedSessionChanged"] = true
	raw, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	f.SetEnhanced(true)
	f.SetEnhanced(false)
	remove(t, f, dir)
	if strings.Contains(strings.Join(f.Objects(), "\n"), "enhancedSession=true") {
		t.Fatal("Remove undid the administrator's independent Off policy")
	}
}
