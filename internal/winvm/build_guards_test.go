package winvm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// These tests pin the safety branches that a mutation left green: each one
// fails when its branch is broken.

// holeAfterProof is a host whose L3 rule reads as off once the proof's last
// step ran, as a host where closing the last hole did not take would.
type holeAfterProof struct {
	*hvfake.HyperV
	done *bool
}

func (h holeAfterProof) ReadLive(ctx context.Context, n winvm.Names, vmID, switchID string) (winvm.LiveState, error) {
	live, err := h.HyperV.ReadLive(ctx, n, vmID, switchID)
	if err == nil && *h.done {
		for i := range live.Firewall {
			live.Firewall[i].Enabled = false
		}
	}
	return live, err
}

// lastStepGuest marks the proof's last step (the function check) as run.
type lastStepGuest struct {
	winvm.GuestProbe
	done *bool
}

func (g lastStepGuest) Egress(ctx context.Context) (netip.Addr, error) {
	ip, err := g.GuestProbe.Egress(ctx)
	*g.done = true
	return ip, err
}

// TestProofReportsAFenceNotPutBack: every step passed, but the fence reads
// open after the proof. Run reports it, so nobody marks the plan proven.
func TestProofReportsAFenceNotPutBack(t *testing.T) {
	f, p, r, h, tg := proofRig(t)
	done := false
	r.B, r.G = holeAfterProof{f, &done}, lastStepGuest{r.G, &done}
	rs, err := r.Run(context.Background(), p, h, tg)
	if ok, why := winvm.Verdict(rs); !ok {
		t.Fatalf("the steps are not green, so this case tests nothing: %s", why)
	}
	if err == nil || !strings.Contains(err.Error(), "still has a hole") {
		t.Fatalf("Run = %v, want the hole the proof left", err)
	}
}

// TestBuildIsNotProvenWhenTheFenceIsNotPutBack: a proof whose steps are green
// but whose fence reads open afterwards never marks the plan proven, and the
// guard does not connect the VM.
func TestBuildIsNotProvenWhenTheFenceIsNotPutBack(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	d := buildDeps(t, f, dir)
	done := false
	d.HV, d.Guest = holeAfterProof{f, &done}, lastStepGuest{f, &done}
	res, err := winvm.Build(context.Background(), d, setupParams())
	if err == nil {
		t.Fatal("Build passed with the fence open after the proof")
	}
	if ok, why := winvm.Verdict(res.Proof); !ok {
		t.Fatalf("the steps are not green, so this case tests nothing: %s", why)
	}
	if p := readPlan(t, dir); !p.ProvedAt.IsZero() {
		t.Fatalf("ProvedAt = %v with the fence open after the proof", p.ProvedAt)
	}
	if res.Status.Network == winvm.NetConnected {
		t.Fatalf("the guard connected with the fence open after the proof: %s", res.Status.Reason)
	}
	raw, err := os.ReadFile(filepath.Join(dir, winvm.ProofFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Green  bool
		Reason string
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Green || !strings.Contains(doc.Reason, "still has a hole") {
		t.Fatalf("proof report lost the restoration failure: %+v", doc)
	}

}

// TestBuildRefusesTwoVMsWithOurToken: a copy of the Secure VM carries our
// owner token too. Repair cannot tell which one is ours, so it stops before
// it changes anything.
func TestBuildRefusesTwoVMsWithOurToken(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	d := buildDeps(t, f, dir)
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatal(err)
	}
	p := readPlan(t, dir)
	other := f.AddForeignVM(p.Names.VM, false)
	if err := f.SetVMOwner(other, p.Owner); err != nil {
		t.Fatal(err)
	}
	before := f.Objects()
	m := len(f.Calls())
	if _, err := winvm.Build(context.Background(), d, setupParams()); !errors.Is(err, winvm.ErrNotOwned) {
		t.Fatalf("Repair with two VMs that carry our token = %v, want ErrNotOwned", err)
	}
	for _, c := range f.Calls()[m:] {
		if c != "Inventory" {
			t.Fatalf("the refused Repair called %s", c)
		}
	}
	diffObjects(t, before, f.Objects())
}

// TestBuildRefusesWithoutThePublicAddress: with no answer from the echo
// services the build cannot place the Deny for this line's own address, so
// it stops before it creates anything.
func TestBuildRefusesWithoutThePublicAddress(t *testing.T) {
	f := hvfake.New()
	before := f.Objects()
	d := buildDeps(t, f, t.TempDir())
	d.Echo = func(context.Context) []netip.Addr { return nil }
	_, err := winvm.Build(context.Background(), d, setupParams())
	if err == nil || !strings.Contains(err.Error(), "public address") {
		t.Fatalf("Build with no public address = %v, want a refusal", err)
	}
	for _, c := range f.Calls() {
		if c == "EnsureSwitch" || c == "CreateVM" {
			t.Fatalf("the refused build called %s", c)
		}
	}
	diffObjects(t, before, f.Objects())
}

// TestBuildRefusesAMeterRow: resource metering adds Meter rows to the
// adapter. Setup never deletes them as extra Denies; it asks the user to turn
// metering off.
func TestBuildRefusesAMeterRow(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	d := buildDeps(t, f, dir)
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatal(err)
	}
	p := readPlan(t, dir)
	if err := f.AddMeterRow(p.VMID, netip.MustParsePrefix("203.0.113.0/24")); err != nil {
		t.Fatal(err)
	}
	_, err := winvm.Build(context.Background(), d, setupParams())
	if err == nil || !strings.Contains(err.Error(), "Meter row") {
		t.Fatalf("Repair with a Meter row = %v, want a refusal that names it", err)
	}
	live, err := f.ReadLive(context.Background(), p.Names, p.VMID, p.SwitchID)
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, a := range live.ACLs {
		kept = kept || a.Action == "Meter"
	}
	if !kept {
		t.Fatal("Repair deleted the Meter row")
	}
}

// TestRemoveKeepsASwitchAnotherVMUses: another VM has an adapter on our
// switch, so Remove VM keeps the switch and says why.
func TestRemoveKeepsASwitchAnotherVMUses(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	n := winvm.ProductNames()
	other := f.AddForeignVM("someone else's VM", false)
	if err := f.SetAdapterSwitch(other, n.Switch); err != nil {
		t.Fatal(err)
	}
	res := remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), " "+n.Switch+" ") {
		t.Fatalf("Remove took out a switch another VM uses:\n%s", strings.Join(f.Objects(), "\n"))
	}
	if !strings.Contains(strings.Join(res.Kept, "\n"), other) {
		t.Fatalf("Remove did not say why it kept the switch: kept %v", res.Kept)
	}
}

// TestRemoveKeepsTheDataWhenAStepFails: a Remove VM that fails part way
// keeps every data file, not only desired.json, so the guard and the next
// run still have their state.
func TestRemoveKeepsTheDataWhenAStepFails(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	f.FailNext("RemoveSwitch", errors.New("injected"))
	if _, err := winvm.Remove(context.Background(), winvm.RemoveDeps{HV: f, Dir: dir}, winvm.ProductNames()); err == nil {
		t.Fatal("Remove passed with RemoveSwitch failing")
	}
	for _, name := range []string{winvm.PlanFile, winvm.StateFile, winvm.PrefsFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s is gone after a failed Remove: %v", name, err)
		}
	}
}

// TestBuildTagsTheProofWithTheOwner: Build hands the proof's Prober this
// install's owner token, so every canary rule and hv_sock test key it makes
// carries the token ClearProof matches.
func TestBuildTagsTheProofWithTheOwner(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	d := buildDeps(t, f, dir)
	got := ""
	d.Probe = winvm.OwnerSpy{Prober: f, Got: &got}
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatal(err)
	}
	if want := readPlan(t, dir).Owner; got != want || got == "" {
		t.Fatalf("the proof's Prober got owner %q, want %q", got, want)
	}
}

// TestClearProofKeepsObjectsWithoutOurToken: a rule in the proof group with
// the canary's text, and a GCS key with the proof's name, but neither with
// this install's owner token, are not ours. Repair and Remove VM leave them,
// and still clear what this install's killed proof left.
func TestClearProofKeepsObjectsWithoutOurToken(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	d := buildDeps(t, f, dir)
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatal(err)
	}
	n := winvm.ProductNames()
	owner := readPlan(t, dir).Owner
	key := f.AddForeignProofObjects(n)
	check := func(when string) {
		t.Helper()
		objs := strings.Join(f.Objects(), "\n")
		if !strings.Contains(objs, "-proof-40002 ") || !strings.Contains(objs, key) {
			t.Fatalf("%s took out a proof object without our token:\n%s", when, objs)
		}
		if strings.Contains(objs, "-proof-40001 ") || strings.Contains(objs, "12345678-1234-1234-1234-123456789ABC") {
			t.Fatalf("%s kept what our own killed proof left:\n%s", when, objs)
		}
	}
	f.LeaveProofLeftovers(n, owner)
	if _, err := winvm.Build(context.Background(), d, setupParams()); err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("Repair did not report the unverified proof object: %v", err)
	}
	check("Repair")
	f.LeaveProofLeftovers(n, owner)
	remove(t, f, dir)
	check("Remove VM")
}
