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
	// Finish, if set, sees the result before Remove releases the host lock,
	// and only while desired.json remains: a Remove that did not finish, or
	// kept the plan for a retry. The data folder is still there to log into.
	Finish func(RemoveResult, error)
}

// RemoveResult is what Remove did: what it removed or put back, what it kept
// on purpose, and what it left because this install did not make it.
type RemoveResult struct {
	Removed []string `json:"removed"`
	Kept    []string `json:"kept"`
	Left    []string `json:"left"`
}

// guardianCertificatesKept: Remove-HgsGuardian leaves the guardian's two
// certificates, with their private keys, and Remove leaves them too. A copy
// or backup of the VM needs them to start its vTPM, and nothing on the host
// tells Remove that no such copy exists.
const guardianCertificatesKept = `the vTPM guardian's two certificates, "Shielded VM Signing Certificate (UntrustedGuardian)" and ` +
	`"Shielded VM Encryption Certificate (UntrustedGuardian)", in Cert:\LocalMachine\Shielded VM Local Certificates. ` +
	`A copy or backup of the VM needs them to start. If you keep no copy, delete both in certlm.msc`

const proofDir = "proof"
const guardDir = "guard"

// Remove takes the Secure VM off this PC. n must be the instance desired.json
// belongs to. Without desired.json nothing on the host is touched: this
// install cannot tell its objects apart, so they are only listed.
func Remove(ctx context.Context, d RemoveDeps, n Names) (res RemoveResult, retErr error) {
	unlock, err := lockMutation(ctx)
	if err != nil {
		return res, err
	}
	defer unlock()
	defer func() {
		if d.Finish == nil {
			return
		}
		if _, err := os.Lstat(filepath.Join(d.Dir, PlanFile)); err == nil {
			d.Finish(res, retErr)
		}
	}()
	owner := ""
	defer func() {
		res.Left = append(res.Left, unknownData(d.Dir)...)
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
	owner = p.Owner
	inv, err := d.HV.Inventory(ctx, n, KnownIDs{VM: p.VMID, Switch: p.SwitchID, Owner: p.Owner}, "")
	if err != nil {
		return res, err
	}
	var errs []error
	fail := func(what string, err error) {
		logf("remove: %s: %v", what, err)
		errs = append(errs, fmt.Errorf("%s: %w", what, err))
	}

	for _, sid := range slices.Concat(p.AddedAdminSIDs, p.PendingAdminSIDs) {
		res.Kept = append(res.Kept, "the Hyper-V Administrators grant for "+sid+
			". To remove it, run Remove-LocalGroupMember -SID "+hvAdmins+" -Member "+sid+" in elevated PowerShell")
	}

	vmID := adopt(inv.VMs, p.VMID, p.Owner)
	if vmID != "" {
		live, err := d.HV.ReadLive(ctx, n, vmID, p.SwitchID)
		if err != nil {
			return res, err
		}
		gs := (GuardDeps{Dir: d.Dir}).loadState().forPlan(p)
		recorded := slices.Concat(p.PortACLs, gs.PortACLs)
		if requireRecordedAdapter(p, live) != nil || live.PortACLs == nil || live.ExtACLs != 0 ||
			slices.ContainsFunc(live.PortACLs, func(a PortACL) bool { return !slices.Contains(recorded, a) }) {
			res.Left = append(res.Left, "the VM "+vmID+" because it has an unverified adapter or port ACL")
			return res, nil
		}
		// The native remove verifies this inventory again before deleting the VM.
		p.PortACLs = live.PortACLs
	}

	// The guard first, so it never acts on a VM that is going.
	if t := inv.Task; t != nil {
		if ownedBy(*t, p.GuardTaskID, p.Owner) {
			if err := d.HV.UnregisterGuardTask(ctx, n, p.Owner); err != nil {
				fail("guard task", err)
				return res, errors.Join(errs...)
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
	for _, v := range inv.VMs {
		if vmID == "" || !strings.EqualFold(v.ID, vmID) {
			res.Left = append(res.Left, "the VM "+v.ID+" (not made by this install)")
			continue
		}
		left, err := d.HV.RemoveVM(ctx, v.ID, p)
		if err != nil {
			fail("VM", err)
			return res, errors.Join(errs...)
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

	keepNetwork := false
	swID := adopt(inv.Switches, p.SwitchID, p.Owner)
	for _, s := range inv.Switches {
		if swID == "" || !strings.EqualFold(s.ID, swID) {
			if strings.EqualFold(s.ID, p.SwitchID) {
				keepNetwork = true
			}
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
			keepNetwork = true
			res.Kept = append(res.Kept, fmt.Sprintf("the switch %s, its address and its neighbour entry: another VM (%s) has an adapter on it", s.ID, strings.Join(others, ", ")))
			continue
		}
		switchPlan := p
		switchPlan.SwitchID = s.ID
		kept, err := d.HV.RemoveSwitch(ctx, switchPlan)
		if err != nil {
			keepNetwork = true
			fail("switch", err)
			continue
		}
		if kept != "" {
			keepNetwork = true
			res.Kept = append(res.Kept, "the switch "+s.ID+" because "+kept)
			continue
		}
		res.Removed = append(res.Removed, "the switch "+s.ID+" with its host vNIC, address and neighbour entry")
	}

	recordedNAT := LiveNAT{Name: n.NAT, ID: p.NATID, Prefix: p.Subnet}
	for _, x := range inv.NATs {
		if !strings.EqualFold(x.Name, n.NAT) {
			continue
		}
		if !matchesRecordedNAT(x, recordedNAT) {
			res.Left = append(res.Left, fmt.Sprintf("the NAT %s on %s (ownership could not be verified)", x.Name, x.Prefix))
			continue
		}
		if keepNetwork {
			res.Kept = append(res.Kept, "the NAT "+x.Name+" because the switch remains")
			continue
		}
		kept, err := d.HV.RemoveNAT(ctx, n, p.Subnet, p.NATID)
		if err != nil {
			fail("NAT", err)
			continue
		}
		if kept != "" {
			res.Kept = append(res.Kept, "the NAT "+x.Name+" because "+kept)
			keepNetwork = true
			continue
		}
		res.Removed = append(res.Removed, "the NAT "+x.Name)
	}

	mine := 0
	for _, r := range inv.Firewall {
		if p.Owner != "" && r.Owner == p.Owner && r.ID == blockFirewallID(n, p.Owner) {
			mine++
		} else {
			res.Left = append(res.Left, "the firewall rule "+r.ID+" (not made by this install)")
		}
	}
	if mine > 0 && keepNetwork {
		res.Kept = append(res.Kept, "the firewall rule, because the switch or the NAT remains")
	}
	if mine > 0 && !keepNetwork {
		if kept, err := d.HV.RemoveFirewall(ctx, p); err != nil {
			fail("firewall", err)
		} else if kept != "" {
			res.Left = append(res.Left, kept)
		} else {
			res.Removed = append(res.Removed, fmt.Sprintf("%d firewall rule(s) in %s", mine, n.FirewallGroup))
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
			res.Kept = append(res.Kept, guardianCertificatesKept)
		}
	}

	if len(errs) > 0 {
		return res, fmt.Errorf("Remove VM did not finish; run it again: %w", errors.Join(errs...))
	}
	if owner == "" {
		return res, nil
	}
	cleanupArtifacts(d.Dir, proof.UnverifiedCapture, func(path string, err error) {
		if errors.Is(err, errUntrackedFile) {
			return // unknownData lists the replacement.
		}
		if err != nil {
			fail(path, err)
		} else {
			res.Removed = append(res.Removed, path)
		}
	})
	if len(errs) > 0 {
		return res, fmt.Errorf("Remove VM did not finish; run it again: %w", errors.Join(errs...))
	}
	if keepNetwork {
		return res, nil // Keep the plan and journal so the owner can retry.
	}
	removed, err := finishArtifactRemoval(d.Dir)
	if err != nil {
		return res, err
	}
	if removed {
		res.Removed = append(res.Removed, PlanFile)
	}
	return res, nil
}

// unknownData lists unrecognized paths even if Remove cannot read a plan
// or a host operation fails.
func unknownData(dir string) []string {
	var out []string
	exe, _ := os.Executable()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		if strings.EqualFold(path, exe) {
			return nil
		}
		out = append(out, path+" (left on disk)")
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
