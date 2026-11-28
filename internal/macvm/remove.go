package macvm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RemoveRequest names what Remove deletes beyond the root side.
type RemoveRequest struct {
	Owner    string
	TartHome string
	Tools    string // the owner's tool cache
}

// RemoveReport is what Remove did, and what the check after it found.
type RemoveReport struct {
	Removed []string `json:"removed"`
	Kept    []string `json:"kept"`
	Left    []string `json:"left"`
}

// Remove is the reverse of Install and of the create, as root: the daemon,
// the VM, its pf anchor and pf reference, softnet's bootpd lease, the root
// folder, the VM's files, the restore image and the tool cache. It goes on
// past a failure, then checks every item again: Left is what is still there.
func (d *Daemon) Remove(ctx context.Context, req RemoveRequest) RemoveReport {
	var rep RemoveReport
	did := func(what string) { rep.Removed = append(rep.Removed, what) }
	inst, ierr := d.loadInstall()
	if ierr != nil && req.Owner != "" {
		if uid, home, err := lookupOwner(req.Owner); err == nil {
			inst = Install{Owner: req.Owner, OwnerUID: uid, OwnerHome: home, TartHome: req.TartHome}
			ierr = nil
		}
	}
	if req.TartHome == "" {
		req.TartHome = inst.TartHome
	}
	// The guard uses the fence, the tools and the state. While it runs,
	// deleting them would leave it guarding nothing, so a guard that does not
	// stop stops the removal.
	if err := d.removeDaemon(ctx); err != nil {
		rep.Left = append(rep.Left, "the guard "+Label+" did not stop, so CashPilot removed nothing else: "+err.Error())
		return rep
	}
	did("the guard daemon " + Label)
	if ierr == nil {
		tart := TartExe(d.P.Bin)
		if _, err := os.Stat(tart); err != nil && req.Tools != "" {
			tart = ToolsAt(req.Tools).Tart
		}
		if _, err := os.Stat(tart); err == nil {
			if _, err := asOwner(ctx, d.R, inst, d.P.Bin, tart, "stop", VMName); err == nil {
				did("stopped " + VMName)
			}
			if _, err := asOwner(ctx, d.R, inst, d.P.Bin, tart, "delete", VMName); err == nil {
				did("the VM " + VMName)
			}
		}
		// A Tart that did not stop keeps the disk busy.
		_, _ = d.R.Run(ctx, "/usr/bin/pkill", "-f", "tart run "+VMName+"( |$)")
	}
	if _, err := d.R.Run(ctx, "/sbin/pfctl", "-a", Anchor, "-F", "all"); err == nil {
		did("the pf anchor " + Anchor)
	}
	// The pf token and the saved Internet Sharing file are what a second
	// Remove needs to finish, so the state folder stays when either step fails.
	keepState := false
	if tok, err := os.ReadFile(d.state(PFToken)); err == nil {
		if t := strings.TrimSpace(string(tok)); t != "" {
			if _, err := d.R.Run(ctx, "/sbin/pfctl", "-X", t); err == nil {
				did("CashPilot's pf reference")
			} else {
				keepState = true
				rep.Left = append(rep.Left, "CashPilot's pf reference could not be released: "+err.Error())
			}
		}
	}
	if orig, err := os.ReadFile(d.state(SharingOrig)); err == nil {
		switch {
		case string(orig) == "absent":
			if err := os.Remove(SharingPlist); err == nil || errors.Is(err, os.ErrNotExist) {
				did("softnet's DHCP setting in " + SharingPlist)
			} else {
				keepState = true
				rep.Left = append(rep.Left, SharingPlist+" could not be removed: "+err.Error())
			}
		default:
			if err := WriteAtomic(SharingPlist, orig, 0o644); err == nil {
				did("put back " + SharingPlist + " as it was")
			} else {
				keepState = true
				rep.Left = append(rep.Left, SharingPlist+" could not be put back: "+err.Error())
			}
		}
	} else if _, err := os.Lstat(SharingPlist); err == nil {
		rep.Kept = append(rep.Kept, SharingPlist+": it was there before CashPilot's Secure VM, so it stays. It sets the DHCP lease time of macOS's virtual networks. To remove it: sudo rm '"+SharingPlist+"'")
	}
	if err := os.Remove(d.P.Plist); err == nil {
		did(d.P.Plist)
	}
	if keepState {
		// Only the state folder stays, with what a second Remove needs.
		for _, dir := range []string{d.P.Bin, d.P.Request, d.P.Owner} {
			_ = os.RemoveAll(dir)
		}
	} else if err := os.RemoveAll(d.P.Root); err == nil {
		did(d.P.Root)
	}
	if req.TartHome != "" {
		vm := VMDir(req.TartHome)
		if _, err := os.Lstat(vm); err == nil && safeOwnerPath(vm, inst.OwnerHome, req.TartHome) {
			if os.RemoveAll(vm) == nil {
				did(vm)
			}
		}
		ipsw := IPSWPath(req.TartHome)
		for _, p := range []string{ipsw, ipsw + ".part"} {
			if _, err := os.Lstat(p); err == nil && os.Remove(p) == nil {
				did(p)
			}
		}
	}
	if req.Tools != "" && safeOwnerPath(req.Tools, inst.OwnerHome, "") {
		if _, err := os.Lstat(req.Tools); err == nil && os.RemoveAll(req.Tools) == nil {
			did(req.Tools)
		}
	}
	rep.Left = d.Leftovers(ctx, req, inst)
	return rep
}

// safeOwnerPath keeps a root delete inside the owner's home or Tart folder.
// It checks the path as written, and then its real path: a symlinked parent
// folder could otherwise lead the delete out of the home. The last part is not
// followed, because a delete of a symlink removes only the link.
func safeOwnerPath(p, home, tartHome string) bool {
	if !underRoot(filepath.Clean(p), home, tartHome) {
		return false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if errors.Is(err, os.ErrNotExist) {
		return true // nothing there to delete
	}
	if err != nil {
		return false
	}
	var real []string
	for _, r := range []string{home, tartHome} {
		if r == "" {
			continue
		}
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			real = append(real, rr)
		}
	}
	return underRoot(filepath.Join(dir, filepath.Base(p)), real...)
}

// underRoot is whether p is inside one of roots, by its cleaned text.
func underRoot(p string, roots ...string) bool {
	for _, root := range roots {
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if strings.HasPrefix(p, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Leftovers lists every trace of the Secure VM still on this Mac. Empty means
// nothing is left. It reads only.
func (d *Daemon) Leftovers(ctx context.Context, req RemoveRequest, inst Install) []string {
	var left []string
	if _, err := d.R.Run(ctx, "/bin/launchctl", "print", "system/"+Label); err == nil {
		left = append(left, "the daemon "+Label+" is still loaded")
	}
	// A check that could not run proves nothing is gone, so it counts as left.
	exists := func(p string) {
		if _, err := os.Lstat(p); err == nil {
			left = append(left, p)
		} else if !errors.Is(err, os.ErrNotExist) {
			left = append(left, p+" (could not check: "+err.Error()+")")
		}
	}
	for _, p := range []string{d.P.Plist, d.P.Root} {
		exists(p)
	}
	if out, err := d.R.Run(ctx, "/sbin/pfctl", "-a", Anchor, "-sr"); err != nil {
		left = append(left, "pf rules in "+Anchor+" (could not check: "+err.Error()+")")
	} else if strings.TrimSpace(out) != "" {
		left = append(left, "pf rules in "+Anchor)
	}
	if out, err := d.R.Run(ctx, "/sbin/pfctl", "-a", Anchor, "-s", "Tables"); err != nil {
		left = append(left, "pf tables in "+Anchor+" (could not check: "+err.Error()+")")
	} else if strings.TrimSpace(out) != "" {
		left = append(left, "pf tables in "+Anchor)
	}
	if ps, err := d.R.Run(ctx, "/bin/ps", "-axww", "-o", "args="); err != nil {
		left = append(left, "the VM's processes (could not check: "+err.Error()+")")
	} else {
		for _, l := range strings.Split(ps, "\n") {
			if strings.Contains(l, "tart run "+VMName) || strings.Contains(l, filepath.Join(d.P.Bin, SoftnetExe)) || strings.Contains(l, filepath.Join(d.P.Bin, DaemonExe)) {
				left = append(left, "a process: "+strings.TrimSpace(l))
			}
		}
	}
	if req.TartHome != "" {
		for _, p := range []string{VMDir(req.TartHome), IPSWPath(req.TartHome), IPSWPath(req.TartHome) + ".part"} {
			exists(p)
		}
	}
	if req.Tools != "" {
		exists(req.Tools)
	}
	return left
}

// errRemoveLeft is returned when the check after a remove found something.
var errRemoveLeft = errors.New("the removal left something behind")

// RemoveError is the report as an error, nil when nothing is left.
func (r RemoveReport) Err() error {
	if len(r.Left) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", errRemoveLeft, strings.Join(r.Left, "; "))
}
