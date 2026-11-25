package winvm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Remove VM (--vm-remove) undoes what desired.json records, by ID or owner
// token, and nothing else: no object is matched by name alone or by a
// wildcard. An object with our name that this install did not make is left
// and listed. A step that fails leaves desired.json in place, so Remove VM
// runs again from the start and finishes the rest; desired.json goes last.

// RemoveDeps is what Remove works with.
type RemoveDeps struct {
	HV Builder
	// Dir is Names.DataDir.
	Dir  string
	Logf func(format string, args ...any)
}

// RemoveResult is what Remove did: what it removed or put back, what it kept
// on purpose, and what it left because this install did not make it.
type RemoveResult struct {
	Removed []string `json:"removed"`
	Kept    []string `json:"kept"`
	Left    []string `json:"left"`
}

// proofDir holds pktmon's capture files while a proof runs. A proof that was
// killed leaves files in it, so it is removed with what it holds.
const proofDir = "proof"

// dataFiles are the files in DataDir that setup and the guard write, deleted
// by exact name. desired.json is not in the list: it goes last, on its own.
var dataFiles = []string{
	installFile, answerISOFile, StateFile, StatusFile, LockFile, ProofFile, PrefsFile, proofDir, "setup.log",
	"guard.log", "guard.log.1",
	// A setup stopped while it wrote the answer ISO leaves its staging folder
	// and the half-written image.
	answerISOFile + ".stage", answerISOFile + ".tmp",
}

// guardDir is the admin-only folder of the guard's exe copy. remove.ps1
// deletes the copy and the two scripts after Remove VM, and the folder when
// it is empty.
const guardDir = "guard"

// guardFiles are the files remove.ps1 deletes itself.
var guardFiles = []string{"CashPilot-guard.exe", "remove.ps1", "setup.ps1"}

// paramsCopy is setup.ps1's copy of the params file, with the guest
// passwords. setup.ps1 deletes it when the exe ends; a setup that was killed
// leaves it.
const paramsCopy = "setup-params.json"

// isAtomicTemp is a temp file writeAtomic makes next to its target
// (".<name>.<random>.tmp"); one is left when the writer was killed.
func isAtomicTemp(name string) bool {
	return strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".tmp") && len(name) > len("..tmp")
}

// Remove takes the Secure VM off this PC. n must be the instance desired.json
// belongs to. Without desired.json nothing on the host is touched: this
// install cannot tell its objects apart, so they are only listed.
func Remove(ctx context.Context, d RemoveDeps, n Names) (RemoveResult, error) {
	var res RemoveResult
	logf := func(format string, args ...any) {
		if d.Logf != nil {
			d.Logf(format, args...)
		}
	}
	p, err := loadPlanFile(filepath.Join(d.Dir, PlanFile))
	if errors.Is(err, fs.ErrNotExist) {
		inv, ierr := d.HV.Inventory(ctx, n, KnownIDs{}, "")
		if ierr != nil {
			return res, ierr
		}
		res.Left = listInventory(inv, n)
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("desired.json cannot be read, so nothing was removed: %w", err)
	}
	if p.Names != n {
		return res, fmt.Errorf("desired.json belongs to %q, not %q", p.Names.VM, n.VM)
	}
	inv, err := d.HV.Inventory(ctx, n, KnownIDs{VM: p.VMID, Switch: p.SwitchID}, "")
	if err != nil {
		return res, err
	}
	var errs []error
	fail := func(what string, err error) {
		logf("remove: %s: %v", what, err)
		errs = append(errs, fmt.Errorf("%s: %w", what, err))
	}

	// The guard first, so it never acts on a VM that is going.
	if t := inv.Task; t != nil {
		if p.Owner != "" && t.Owner == p.Owner {
			if err := d.HV.UnregisterGuardTask(ctx, n, p.Owner); err != nil {
				fail("guard task", err)
			} else {
				res.Removed = append(res.Removed, "the guard task "+t.ID)
			}
		} else {
			res.Left = append(res.Left, "the scheduled task "+t.ID+" (not made by this install)")
		}
	}

	if cleared, err := d.HV.ClearProof(ctx, n, p.Owner); err != nil {
		fail("what an earlier proof left", err)
	} else {
		res.Removed = append(res.Removed, cleared...)
	}

	ourVM := ""
	for _, v := range inv.VMs {
		if !ownedBy(v, p.VMID, p.Owner) {
			res.Left = append(res.Left, "the VM "+v.ID+" (not made by this install)")
			continue
		}
		ourVM = v.ID
		left, err := d.HV.RemoveVM(ctx, v.ID, p.VHDXDir)
		if err != nil {
			fail("VM", err)
			continue
		}
		res.Removed = append(res.Removed, "the VM "+v.ID+" and its disk")
		for _, f := range left {
			res.Left = append(res.Left, f+" (outside the VM folder, so not deleted)")
		}
	}

	for _, s := range inv.Switches {
		if !ownedBy(s, p.SwitchID, p.Owner) {
			res.Left = append(res.Left, "the switch "+s.ID+" (not made by this install)")
			continue
		}
		var others []string
		for _, vm := range s.Ports {
			if !strings.EqualFold(vm, ourVM) {
				others = append(others, vm)
			}
		}
		if len(others) > 0 {
			res.Kept = append(res.Kept, fmt.Sprintf("the switch %s, its address and its neighbour entry: another VM (%s) has an adapter on it", s.ID, strings.Join(others, ", ")))
			continue
		}
		if err := d.HV.RemoveSwitch(ctx, s.ID); err != nil {
			fail("switch", err)
			continue
		}
		res.Removed = append(res.Removed, "the switch "+s.ID+" with its host vNIC, address and neighbour entry")
	}

	for _, x := range inv.NATs {
		if x.Name != n.NAT {
			continue
		}
		if !p.Subnet.IsValid() || x.Prefix.Masked() != p.Subnet {
			res.Left = append(res.Left, fmt.Sprintf("the NAT %s on %s (not the range this install recorded)", x.Name, x.Prefix))
			continue
		}
		if err := d.HV.RemoveNAT(ctx, n, p.Subnet); err != nil {
			fail("NAT", err)
			continue
		}
		res.Removed = append(res.Removed, "the NAT "+x.Name)
	}

	mine := 0
	for _, r := range inv.Firewall {
		if p.Owner != "" && r.Owner == p.Owner {
			mine++
		} else {
			res.Left = append(res.Left, "the firewall rule "+r.ID+" (not made by this install)")
		}
	}
	if mine > 0 {
		if err := d.HV.RemoveFirewall(ctx, n, p.Owner); err != nil {
			fail("firewall", err)
		} else {
			res.Removed = append(res.Removed, fmt.Sprintf("%d firewall rule(s) in %s", mine, n.FirewallGroup))
		}
	}

	if p.EnhancedSession == ESOff && p.EnhancedSessionOriginal != nil {
		want := *p.EnhancedSessionOriginal
		if inv.Enhanced != want {
			if _, err := d.HV.SetEnhancedSession(ctx, want); err != nil {
				fail("Enhanced Session", err)
			} else {
				res.Removed = append(res.Removed, fmt.Sprintf("Enhanced Session put back to %v", want))
			}
		}
	}

	if p.CreatedGuardian && inv.Guardian {
		var others []string
		for _, id := range inv.GuardianVMs {
			if !strings.EqualFold(id, ourVM) {
				others = append(others, id)
			}
		}
		if len(others) > 0 {
			res.Kept = append(res.Kept, "the vTPM guardian: other VMs use it ("+strings.Join(others, ", ")+")")
		} else if err := d.HV.RemoveGuardian(ctx); err != nil {
			fail("guardian", err)
		} else {
			res.Removed = append(res.Removed, "the vTPM guardian")
		}
	}

	// Each account setup added comes out; taking out one that is no longer a
	// member does nothing.
	confirmed, pending := p.AddedAdminSIDs, p.PendingAdminSIDs
	if !p.AdminGrantsTracked {
		pending = append(slices.Clone(pending), confirmed...)
		confirmed = nil
	}
	for _, sid := range pending {
		res.Left = append(res.Left, "the account "+sid+" (Hyper-V Administrators grant unconfirmed)")
	}
	for _, sid := range confirmed {
		if err := d.HV.RemoveHyperVAdmin(ctx, sid); err != nil {
			fail("Hyper-V Administrators", err)
		} else {
			res.Removed = append(res.Removed, "the account "+sid+" setup added to Hyper-V Administrators")
		}
	}

	if len(errs) > 0 {
		return res, fmt.Errorf("Remove VM did not finish; run it again: %w", errors.Join(errs...))
	}
	for _, f := range dataFiles {
		path := filepath.Join(d.Dir, f)
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		rm := os.Remove
		if f == proofDir || f == answerISOFile+".stage" {
			rm = os.RemoveAll
		}
		if err := rm(path); err != nil {
			fail(f, err)
		} else {
			res.Removed = append(res.Removed, f)
		}
	}
	for _, f := range leftovers(d.Dir) {
		if err := os.Remove(filepath.Join(d.Dir, f)); err != nil {
			fail(f, err)
		} else {
			res.Removed = append(res.Removed, f)
		}
	}
	if len(errs) > 0 {
		return res, fmt.Errorf("Remove VM did not finish; run it again: %w", errors.Join(errs...))
	}
	// Anything else in DataDir was not made by setup: it stays, and is listed
	// so the folder is never kept without a word.
	res.Left = append(res.Left, unknownData(d.Dir)...)
	if err := os.Remove(filepath.Join(d.Dir, PlanFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	res.Removed = append(res.Removed, PlanFile)
	return res, nil
}

// leftovers are the files a killed setup or guard leaves in DataDir and in
// the guard folder: writeAtomic's temp files and setup.ps1's params copy.
// Only regular files match.
func leftovers(dir string) []string {
	var out []string
	for _, sub := range []string{"", guardDir} {
		ents, _ := os.ReadDir(filepath.Join(dir, sub))
		for _, e := range ents {
			if !e.Type().IsRegular() {
				continue
			}
			if isAtomicTemp(e.Name()) || (sub == guardDir && e.Name() == paramsCopy) {
				out = append(out, filepath.Join(sub, e.Name()))
			}
		}
	}
	return out
}

// unknownData lists what is left in DataDir and the guard folder that setup
// did not make, besides desired.json, which goes last, and the files
// remove.ps1 deletes itself.
func unknownData(dir string) []string {
	var out []string
	for _, sub := range []string{"", guardDir} {
		ents, _ := os.ReadDir(filepath.Join(dir, sub))
		for _, e := range ents {
			name := e.Name()
			switch {
			case sub == "" && (name == PlanFile || (name == guardDir && e.IsDir())):
				continue
			case sub == guardDir && slices.Contains(guardFiles, name):
				continue
			}
			out = append(out, filepath.Join(dir, sub, name)+" (not made by setup, so not deleted)")
		}
	}
	return out
}

// loadPlanFile reads a desired.json SYSTEM or Administrators wrote.
func loadPlanFile(path string) (Plan, error) {
	raw, err := readCapped(path, maxPlanBytes, true)
	if err != nil {
		return Plan{}, err
	}
	return LoadPlan(raw)
}

// listInventory names every object with one of our names, for a Remove that
// has no desired.json and so removes none of them.
func listInventory(inv Inventory, n Names) []string {
	var out []string
	for _, v := range inv.VMs {
		out = append(out, "the VM "+v.ID)
	}
	for _, s := range inv.Switches {
		out = append(out, "the switch "+s.ID)
	}
	for _, x := range inv.NATs {
		if x.Name == n.NAT {
			out = append(out, fmt.Sprintf("the NAT %s on %s", x.Name, x.Prefix))
		}
	}
	for _, r := range inv.Firewall {
		out = append(out, "the firewall rule "+r.ID)
	}
	if inv.Task != nil {
		out = append(out, "the scheduled task "+inv.Task.ID)
	}
	slices.Sort(out)
	if len(out) > 0 {
		out = append(out, "(no desired.json, so this install cannot tell which of these it made)")
	}
	return out
}
