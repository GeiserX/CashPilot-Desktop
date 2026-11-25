package winvm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// dataFiles names files setup and the guard write in DataDir.
var dataFiles = []string{
	installFile, answerISOFile, answerISOFile + ".tmp", StateFile, StatusFile, LockFile,
	ProofFile, PrefsFile, "setup.log", "guard.log", "guard.log.1",
}

const guardDir = "guard"

var guardFiles = []string{"CashPilot-guard.exe", "remove.ps1", "setup.ps1"}
var atomicTargets = []string{PlanFile, installFile, StateFile, StatusFile, LockFile, ProofFile, PrefsFile}
var atomicSuffix = regexp.MustCompile(`^[0-9]+\.tmp$`)
var bootstrapTemp = regexp.MustCompile(`^\.(bootstrap|setup-exe|setup-params|guard-backup|params-backup|guard-discard|lab-params)\.[0-9a-f]{32}\.tmp$`)
var captureDir = regexp.MustCompile(`^pktmon-([0-9a-f]{32}-)?[0-9]+$`)
var captureFile = regexp.MustCompile(`^c[0-9]+\.pcapng$`)

// generatedData identifies only the paths setup writes. The plan and the
// running bootstrap are cleaned up separately.
func generatedData(rel string, d fs.DirEntry) bool {
	parts := strings.Split(rel, string(filepath.Separator))
	if d.IsDir() {
		return len(parts) == 1 && (rel == proofDir || rel == answerISOFile+".stage") ||
			len(parts) == 2 && parts[0] == proofDir && captureDir.MatchString(parts[1])
	}
	if !d.Type().IsRegular() {
		return false
	}
	switch len(parts) {
	case 1:
		if slices.Contains(dataFiles, rel) {
			return true
		}
		for _, name := range atomicTargets {
			if suffix, ok := strings.CutPrefix(rel, "."+name+"."); ok && atomicSuffix.MatchString(suffix) {
				return true
			}
		}
	case 2:
		return parts[0] == guardDir && (parts[1] == "setup-params.json" || bootstrapTemp.MatchString(parts[1])) ||
			parts[0] == answerISOFile+".stage" && parts[1] == "autounattend.xml"
	case 3:
		return parts[0] == proofDir && captureDir.MatchString(parts[1]) &&
			(parts[2] == "capture.etl" || captureFile.MatchString(parts[2]))
	}
	return false
}

// cleanupData removes generated files and empty generated directories. It
// also serves Repair, which only clears stale proof captures.
func cleanupData(dir, under string, report func(string, error)) {
	var dirs []string
	root := filepath.Join(dir, under)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if generatedData(rel, d) {
			if d.IsDir() {
				dirs = append(dirs, path)
			} else {
				report(rel, os.Remove(path))
			}
		} else if d.IsDir() && rel != "." && rel != guardDir {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		report(under, err)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		path := dirs[i]
		ents, err := os.ReadDir(path)
		if err == nil && len(ents) == 0 {
			rel, _ := filepath.Rel(dir, path)
			report(rel, os.Remove(path))
		}
	}
}

// Remove takes the Secure VM off this PC. n must be the instance desired.json
// belongs to. Without desired.json nothing on the host is touched: this
// install cannot tell its objects apart, so they are only listed.
func Remove(ctx context.Context, d RemoveDeps, n Names) (res RemoveResult, retErr error) {
	knownPlan := false
	defer func() {
		res.Left = append(res.Left, unknownData(d.Dir, knownPlan)...)
	}()
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
		res.Left = append(res.Left, listInventory(inv, n)...)
		proof, err := d.HV.ClearProof(ctx, n, "")
		res.Left = append(res.Left, proof.Left...)
		return res, err
	}
	if err != nil {
		return res, fmt.Errorf("desired.json cannot be read, so nothing was removed: %w", err)
	}
	if p.Names != n {
		return res, fmt.Errorf("desired.json belongs to %q, not %q", p.Names.VM, n.VM)
	}
	knownPlan = p.Owner != ""
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

	proof, err := d.HV.ClearProof(ctx, n, p.Owner)
	if err != nil {
		fail("what an earlier proof left", err)
	}
	res.Removed = append(res.Removed, proof.Cleared...)
	res.Left = append(res.Left, proof.Left...)

	ourVM := ""
	vmID := adopt(inv.VMs, p.VMID, p.Owner)
	for _, v := range inv.VMs {
		if vmID == "" || !strings.EqualFold(v.ID, vmID) {
			res.Left = append(res.Left, "the VM "+v.ID+" (not made by this install)")
			continue
		}
		left, err := d.HV.RemoveVM(ctx, v.ID, p)
		if err != nil {
			fail("VM", err)
			continue
		}
		ourVM = v.ID
		res.Removed = append(res.Removed, "the VM "+v.ID)
		for _, f := range left {
			res.Left = append(res.Left, f+" (disk ownership or use prevented deletion)")
		}
	}

	if len(inv.VMs) == 0 && p.Disk != nil && p.Owner != "" {
		left, err := d.HV.RemoveVM(ctx, "", p)
		if err != nil {
			fail("VM disk", err)
		}
		for _, f := range left {
			res.Left = append(res.Left, f+" (disk ownership or use prevented deletion)")
		}
	}

	swID := adopt(inv.Switches, p.SwitchID, p.Owner)
	for _, s := range inv.Switches {
		if swID == "" || !strings.EqualFold(s.ID, swID) {
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
		if err := d.HV.RemoveSwitch(ctx, s.ID, p.Owner); err != nil {
			fail("switch", err)
			continue
		}
		res.Removed = append(res.Removed, "the switch "+s.ID+" with its host vNIC, address and neighbour entry")
	}

	for _, x := range inv.NATs {
		if !strings.EqualFold(x.Name, n.NAT) {
			continue
		}
		if !independentNATID(n.NAT, p.NATID) || p.NATID != x.ID || x.Prefix.Masked() != p.Subnet {
			res.Left = append(res.Left, fmt.Sprintf("the NAT %s on %s (ownership could not be verified)", x.Name, x.Prefix))
			continue
		}
		if err := d.HV.RemoveNAT(ctx, n, p.Subnet, p.NATID); err != nil {
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

	if inv.Guardian && (p.GuardianID == "" || inv.GuardianID != p.GuardianID) {
		res.Left = append(res.Left, "the vTPM guardian (ownership could not be verified)")
	} else if inv.Guardian {
		var others []string
		for _, id := range inv.GuardianVMs {
			if !strings.EqualFold(id, ourVM) {
				others = append(others, id)
			}
		}
		if len(others) > 0 {
			res.Kept = append(res.Kept, "the vTPM guardian: other VMs use it ("+strings.Join(others, ", ")+")")
		} else if err := d.HV.RemoveGuardian(ctx, p.GuardianID); err != nil {
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
	if !knownPlan {
		return res, nil
	}
	cleanupData(d.Dir, "", func(path string, err error) {
		if err != nil {
			fail(path, err)
		} else {
			res.Removed = append(res.Removed, path)
		}
	})
	if len(errs) > 0 {
		return res, fmt.Errorf("Remove VM did not finish; run it again: %w", errors.Join(errs...))
	}
	if err := os.Remove(filepath.Join(d.Dir, PlanFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	res.Removed = append(res.Removed, PlanFile)
	return res, nil
}

// unknownData lists unrecognized paths even if Remove cannot read a plan
// or a host operation fails.
func unknownData(dir string, knownPlan bool) []string {
	var out []string
	exe, _ := os.Executable()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if strings.EqualFold(path, exe) ||
			knownPlan && (generatedData(rel, d) || rel == PlanFile ||
				rel == guardDir && d.IsDir() ||
				filepath.Dir(rel) == guardDir && d.Type().IsRegular() && slices.Contains(guardFiles, d.Name())) {
			return nil
		}
		out = append(out, path+" (not made by setup, so not deleted)")
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		out = append(out, "could not list "+dir+": "+err.Error())
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
