package macvm

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// InstallRequest is what the app hands the admin step.
type InstallRequest struct {
	Owner    string
	TartHome string
	Tools    string // the owner's tool cache (ToolsAt)
	Deny     []netip.Addr
	AppVer   string
}

// daemonPlist is the one LaunchDaemon. RunAtLoad starts the VM at boot;
// KeepAlive brings the guard back if it dies; AbandonProcessGroup keeps Tart
// alive when the daemon restarts (lab, M5).
func daemonPlist(p Paths) string {
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + Label + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + esc(filepath.Join(p.Bin, DaemonExe)) + `</string>
    <string>--macvm</string>
    <string>daemon</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>AbandonProcessGroup</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>` + esc(filepath.Join(p.State, "daemon.log")) + `</string>
  <key>StandardErrorPath</key><string>` + esc(filepath.Join(p.State, "daemon.log")) + `</string>
</dict>
</plist>
`
}

// lookupOwner resolves the owner's uid and home.
func lookupOwner(name string) (int, string, error) {
	if !ValidOwner(name) {
		return 0, "", fmt.Errorf("%q is not a user CashPilot can set up a VM for", name)
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, "", err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil || uid < 500 {
		return 0, "", fmt.Errorf("%s is not a normal user account", name)
	}
	return uid, u.HomeDir, nil
}

// ownedBy checks that path exists and belongs to uid.
func ownedBy(path string, uid int) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st, ok := statOwner(fi); !ok || st != uid {
		return fmt.Errorf("%s does not belong to the VM's owner", path)
	}
	return nil
}

// stageArchive copies an owner-writable archive into a root-only folder and
// checks its hash there, so nothing can swap it between the check and the use.
func stageArchive(ctx context.Context, src, dir string, pin Pin) (string, error) {
	dst := filepath.Join(dir, filepath.Base(src))
	if err := copyFile(src, dst, 0o600); err != nil {
		return "", err
	}
	ok, err := fileHashIs(dst, pin.SHA256)
	if err != nil || !ok {
		return "", fmt.Errorf("%s does not match its pinned checksum", pin.Name)
	}
	out := filepath.Join(dir, pin.Name)
	if err := Extract(ctx, dst, out); err != nil {
		return "", err
	}
	return out, nil
}

// Install is the admin step. It runs as root, once:
//  1. copies this program, Tart and softnet (setuid) into a folder only root
//     can change, from archives it checks again itself;
//  2. writes the guest agent into the VM's disk (Inject);
//  3. records the owner, the VM's MAC and the deny list;
//  4. installs and loads the daemon, which starts the VM with the fence and
//     keeps it cut off until the proof is green.
func (d *Daemon) Install(ctx context.Context, req InstallRequest) error {
	uid, home, err := lookupOwner(req.Owner)
	if err != nil {
		return err
	}
	if req.TartHome == "" {
		req.TartHome = filepath.Join(home, ".tart")
	}
	if err := ownedBy(VMDir(req.TartHome), uid); err != nil {
		return fmt.Errorf("the VM is not ready: %w", err)
	}
	cfg, err := ReadVMConfig(req.TartHome)
	if err != nil {
		return err
	}
	for _, dir := range []string{d.P.Root, d.P.Bin, d.P.State} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.Chown(dir, 0, 0); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			return err
		}
	}
	stage, err := os.MkdirTemp(d.P.Root, ".stage.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	tart, err := stageArchive(ctx, filepath.Join(req.Tools, "tart.tar.gz"), stage, TartPin)
	if err != nil {
		return err
	}
	softnet, err := stageArchive(ctx, filepath.Join(req.Tools, "softnet.tar.gz"), stage, SoftnetPin)
	if err != nil {
		return err
	}
	agent, err := stageArchive(ctx, filepath.Join(req.Tools, "agent.tar.gz"), stage, AgentPin)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	inst := Install{
		Owner: req.Owner, OwnerUID: uid, OwnerHome: home, TartHome: req.TartHome, MAC: cfg.MAC,
		InstalledAt: d.now(), Tart: TartPin.Version, Softnet: SoftnetPin.Version, App: req.AppVer,
	}
	// Only now, with every archive checked, the old guard goes. On a repair or
	// an update the VM runs under it, so the VM stops at once: nothing must run
	// unguarded. A later failure loads the old guard again.
	hadGuard := false
	if _, err := os.Stat(d.P.Plist); err == nil {
		hadGuard = true
	}
	if err := d.removeDaemon(ctx); err != nil {
		return err
	}
	done := false
	defer func() {
		if !done && hadGuard {
			if _, err := os.Stat(d.P.Plist); err == nil {
				_, _ = d.R.Run(context.WithoutCancel(ctx), "/bin/launchctl", "bootstrap", "system", d.P.Plist)
			}
		}
	}()
	if _, err := os.Stat(TartExe(d.P.Bin)); err == nil {
		if err := d.stopForInstall(ctx, inst); err != nil {
			return err
		}
	}
	for _, dir := range []string{d.P.Request, d.P.Owner} {
		_ = os.RemoveAll(dir)
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chown(dir, uid, -1); err != nil {
			return err
		}
	}
	_ = os.RemoveAll(filepath.Join(d.P.Bin, TartApp))
	if _, err := d.R.Run(ctx, "/usr/bin/ditto", filepath.Join(tart, TartApp), filepath.Join(d.P.Bin, TartApp)); err != nil {
		return err
	}
	for _, x := range []struct {
		src, name string
		mode      os.FileMode
	}{{self, DaemonExe, 0o755}, {filepath.Join(softnet, SoftnetExe), SoftnetExe, 0o755 | os.ModeSetuid}} {
		dst := filepath.Join(d.P.Bin, x.name)
		_ = os.Remove(dst)
		if err := copyFile(x.src, dst, 0o700); err != nil {
			return err
		}
		if err := os.Chown(dst, 0, 0); err != nil {
			return err
		}
		if err := os.Chmod(dst, x.mode); err != nil {
			return err
		}
	}
	if _, err := d.R.Run(ctx, "/usr/sbin/chown", "-R", "root:wheel", filepath.Join(d.P.Bin, TartApp)); err != nil {
		return err
	}
	// The agent is written into the VM's disk, so the VM must not run.
	if err := d.stopForInstall(ctx, inst); err != nil {
		return err
	}
	if err := Inject(ctx, d.R, filepath.Join(VMDir(req.TartHome), "disk.img"), filepath.Join(agent, "tart-guest-agent")); err != nil {
		return fmt.Errorf("could not prepare the guest: %w", err)
	}
	deny := append([]netip.Addr{}, req.Deny...)
	if d.PublicIPs != nil {
		if ips, err := d.PublicIPs(ctx); err == nil {
			deny = append(deny, ips...)
		}
	}
	raw := FormatDeny(deny)
	deny, err = ParseDeny(raw)
	if err != nil {
		return err
	}
	if len(deny) == 0 {
		return errors.New("CashPilot could not learn this connection's public IP, which the fence must block. Check the internet connection and try again")
	}
	// Keep the Internet Sharing preference as it was the first time, so
	// Remove can put it back after softnet rewrote it.
	if _, err := os.Lstat(d.state(SharingOrig)); err != nil {
		orig, rerr := os.ReadFile(SharingPlist)
		if errors.Is(rerr, os.ErrNotExist) {
			orig = []byte("absent")
		} else if rerr != nil {
			return rerr
		}
		if err := WriteAtomic(d.state(SharingOrig), orig, 0o600); err != nil {
			return err
		}
	}
	if err := WriteAtomic(d.state(DenyFile), []byte(FormatDeny(deny)), 0o644); err != nil {
		return err
	}
	_ = os.Remove(d.state(VerdictFile))
	_ = os.Remove(d.state(DesiredFile))
	if err := WriteJSON(d.state(InstallFile), inst, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(d.P.Plist, []byte(daemonPlist(d.P)), 0o644); err != nil {
		return err
	}
	if err := os.Chown(d.P.Plist, 0, 0); err != nil {
		return err
	}
	if _, err := d.R.Run(ctx, "/bin/launchctl", "bootstrap", "system", d.P.Plist); err != nil {
		return fmt.Errorf("could not start the Secure VM's guard: %w", err)
	}
	done = true
	d.logf("install: done for %s, VM %s, MAC %s, deny %v", req.Owner, VMName, cfg.MAC, deny)
	return nil
}

// stopForInstall stops the VM and waits until Tart no longer lists it as
// running.
func (d *Daemon) stopForInstall(ctx context.Context, inst Install) error {
	if run, err := d.running(ctx, inst); err == nil && run {
		d.stopVM(ctx, inst)
		for i := 0; i < 60; i++ {
			if run, err = d.running(ctx, inst); err != nil || !run {
				break
			}
			time.Sleep(time.Second)
		}
	}
	if run, err := d.running(ctx, inst); err != nil || run {
		return errors.New("the VM did not stop, and it must not run while CashPilot writes the guest agent into its disk. Restart the Mac, then try again")
	}
	return nil
}

// removeDaemon unloads the daemon if it is loaded, and waits until it is gone.
func (d *Daemon) removeDaemon(ctx context.Context) error {
	if _, err := d.R.Run(ctx, "/bin/launchctl", "print", "system/"+Label); err != nil {
		return nil
	}
	_, _ = d.R.Run(ctx, "/bin/launchctl", "bootout", "system/"+Label)
	for i := 0; i < 50; i++ {
		if _, err := d.R.Run(ctx, "/bin/launchctl", "print", "system/"+Label); err != nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("the old Secure VM daemon did not stop")
}
