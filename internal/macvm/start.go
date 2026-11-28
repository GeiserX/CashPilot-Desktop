package macvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// StartOptions changes a start for the proof.
type StartOptions struct {
	// Holes opens the proof's gaps in softnet (L1). Production passes none.
	Holes []Hole
}

// Start timeouts.
var (
	// vmDiskWait is how long a start at boot waits for the VM's folder, which
	// may live on a volume that mounts after launchd starts the daemon.
	vmDiskWait = 300 * time.Second
	// portWait is how long the guest's vmenet port may take to show.
	portWait = 60 * time.Second
	// agentWait is how long the guest agent may take to answer after a start.
	agentWait = 5 * time.Minute
)

// errGuestLimit is Virtualization.framework refusing a third macOS guest.
var errGuestLimit = errors.New("macOS runs at most two macOS virtual machines at once, and two are running already. Stop another macOS VM, then start the Secure VM again")

// guestLimitText is how Tart reports the refusal.
var guestLimitText = regexp.MustCompile(`(?i)(maximum number|number of (supported )?virtual machines|limit.*virtual machine|VZErrorDomain Code=6\b)`)

// running is whether Tart lists the VM as running.
func (d *Daemon) running(ctx context.Context, inst Install) (bool, error) {
	out, err := asOwner(ctx, d.R, inst, d.P.Bin, TartExe(d.P.Bin), "list", "--format", "json")
	if err != nil {
		return false, err
	}
	var vms []struct {
		Name    string `json:"Name"`
		State   string `json:"State"`
		Running bool   `json:"Running"`
		Source  string `json:"Source"`
	}
	if err := json.Unmarshal([]byte(out), &vms); err != nil {
		return false, fmt.Errorf("tart list: %w", err)
	}
	for _, v := range vms {
		if v.Name == VMName && (v.Running || v.State == "running") {
			return true, nil
		}
	}
	return false, nil
}

// Start starts the VM with the fence: Tart with softnet (L1), the guest's port
// cut the moment it shows, pf (L2) loaded for that bridge and gateway, then a
// guard pass, which connects only on an exact match with a valid proof.
// A VM that already runs is left to the guard.
func (d *Daemon) Start(ctx context.Context, opt StartOptions) error {
	inst, err := d.loadInstall()
	if err != nil {
		return err
	}
	cfg := filepath.Join(VMDir(inst.TartHome), "config.json")
	deadline := d.now().Add(vmDiskWait)
	for {
		if _, err := os.Stat(cfg); err == nil {
			break
		}
		if d.now().After(deadline) {
			return fmt.Errorf("the Secure VM's files are not at %s", VMDir(inst.TartHome))
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	run, err := d.running(ctx, inst)
	if err != nil {
		return fmt.Errorf("could not ask Tart whether the VM runs, so it is not started: %w", err)
	}
	if run {
		d.logf("start: %s already runs; leaving it to the guard", VMName)
		return nil
	}
	raw, err := os.ReadFile(d.state(DenyFile))
	if err != nil {
		return err
	}
	deny, err := ParseDeny(string(raw))
	if err != nil {
		return err
	}
	block := SoftnetBlock(deny)
	before, err := d.bridges(ctx)
	if err != nil {
		return err
	}
	beforeSet := MemberSet(before)

	// --no-graphics: Tart prints the VNC address and opens no window; the app
	// opens the screen only when the user asks (OpenScreen).
	args := []string{TartExe(d.P.Bin), "run", VMName, "--vnc-experimental", "--no-graphics", "--no-audio", "--no-clipboard", "--net-softnet-block", block}
	if len(opt.Holes) > 0 {
		args = append(args, "--net-softnet-allow", SoftnetAllow(opt.Holes))
	}
	logPath := d.state(TartLog)
	if _, err := startDetached(inst, d.P.Bin, logPath, args...); err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			d.stopVM(context.WithoutCancel(ctx), inst)
		}
	}()
	// Our port is the vmenet member that was not there before. It shows
	// before the guest has booted, so the cut lands before the guest sends a
	// frame.
	var br, port string
	deadline = d.now().Add(portWait)
	for br == "" {
		if d.now().After(deadline) {
			return d.startFailure(logPath, "the Secure VM's network port never showed")
		}
		if err := sleep(ctx, 100*time.Millisecond); err != nil {
			return err
		}
		after, err := d.bridges(ctx)
		if err != nil {
			continue
		}
		if b, p, ok := NewMember(beforeSet, MemberSet(after)); ok {
			br, port = b, p
		}
		if exited, _ := tartExited(logPath); exited {
			return d.startFailure(logPath, "Tart stopped while starting the VM")
		}
	}
	if _, err := d.R.Run(ctx, "/sbin/ifconfig", br, "deletem", port); err != nil {
		return fmt.Errorf("could not cut the new guest port: %w", err)
	}
	d.logf("start: cut %s from %s", port, br)
	// Another VM that starts in the same moment adds a port too, and the port
	// that showed first need not be ours. A second new port within the settle
	// time means the two cannot be told apart: stop ours, give the other VM
	// its port back, and fail, so the daemon starts again.
	if other := d.settle(ctx, beforeSet, br+" "+port); other != "" {
		d.logf("start: %s also showed, so %s cannot be told apart; stopping %s", other, port, VMName)
		d.stopVM(ctx, inst)
		started = true // stopped already
		if _, err := d.R.Run(ctx, "/sbin/ifconfig", port); err == nil {
			_, _ = d.R.Run(ctx, "/sbin/ifconfig", br, "addm", port)
		}
		return errPortAmbiguous
	}
	bs, err := d.bridges(ctx)
	if err != nil {
		return err
	}
	var bridge Bridge
	for _, b := range bs {
		if b.Name == br {
			bridge = b
		}
	}
	if !bridge.Addr.IsValid() {
		return fmt.Errorf("bridge %s has no IPv4 address", br)
	}
	if err := d.loadFence(ctx, bridge, deny, nil); err != nil {
		return err
	}
	if _, err := d.recordDesired(ctx, bridge, port, block); err != nil {
		return err
	}
	d.handOverVNC(inst, logPath)
	started = true
	d.logf("start: fence loaded on %s gw=%s subnet=%s", br, bridge.Addr, bridge.Prefix)
	return nil
}

// portSettle is how long a start watches for a second new port after it cut
// the first one.
const portSettle = 2 * time.Second

// errPortAmbiguous means two VMs added a port at once, so the start could not
// tell which one is the Secure VM's.
var errPortAmbiguous = errors.New("another virtual machine started at the same moment, so CashPilot could not tell which network port is the Secure VM's")

// settle watches the bridges for portSettle and returns a new vmenet member
// other than cut, or "" when none showed.
func (d *Daemon) settle(ctx context.Context, before map[string]bool, cut string) string {
	end := d.now().Add(portSettle)
	for d.now().Before(end) {
		if after, err := d.bridges(ctx); err == nil {
			if other := OtherNewMember(before, MemberSet(after), cut); other != "" {
				return other
			}
		}
		if sleep(ctx, 100*time.Millisecond) != nil {
			return ""
		}
	}
	return ""
}

// loadFence loads the pf anchor for bridge, turns pf on with our own
// reference, and kills the guest's old states, so every flow meets the new
// rules (lab trap M1-A3).
func (d *Daemon) loadFence(ctx context.Context, b Bridge, deny []netip.Addr, holes []Hole) error {
	rules, err := Rules(b.Name, b.Addr, deny, holes)
	if err != nil {
		return err
	}
	return d.loadAnchor(ctx, rules, b.Prefix)
}

func (d *Daemon) loadAnchor(ctx context.Context, rules string, subnet netip.Prefix) error {
	path := d.state(RulesFile)
	if err := WriteAtomic(path, []byte(rules), 0o644); err != nil {
		return err
	}
	if _, err := d.R.Run(ctx, "/sbin/pfctl", "-a", Anchor, "-f", path); err != nil {
		return fmt.Errorf("pf refused the fence: %w", err)
	}
	if err := d.enablePF(ctx); err != nil {
		return err
	}
	if subnet.IsValid() {
		// A state left from before the reload would let its flow skip the
		// new rules, so a failed clear fails the reload.
		if _, err := d.R.Run(ctx, "/sbin/pfctl", "-k", subnet.String()); err != nil {
			return fmt.Errorf("pf could not clear the guest's old states: %w", err)
		}
		if _, err := d.R.Run(ctx, "/sbin/pfctl", "-k", "0.0.0.0/0", "-k", subnet.String()); err != nil {
			return fmt.Errorf("pf could not clear the guest's old states: %w", err)
		}
	}
	return nil
}

// enablePF takes a pf reference (`pfctl -E`), which keeps pf on while any
// holder has one. A reboot drops every reference, so the token is taken again
// after a boot.
func (d *Daemon) enablePF(ctx context.Context) error {
	info, _ := d.R.Run(ctx, "/sbin/pfctl", "-s", "References")
	tokPath := d.state(PFToken)
	if tok, err := os.ReadFile(tokPath); err == nil && strings.TrimSpace(string(tok)) != "" && strings.Contains(info, strings.TrimSpace(string(tok))) {
		return nil
	}
	// pfctl -E prints its token on standard error, and Exec returns stderr
	// only inside a failure, so the shell folds it into the output.
	out, err := d.R.Run(ctx, "/bin/sh", "-c", "/sbin/pfctl -E 2>&1")
	text := out
	if err != nil {
		text += err.Error()
	}
	if m := regexp.MustCompile(`Token : (\d+)`).FindStringSubmatch(text); m != nil {
		// Remove releases the reference by this token; one that cannot be
		// saved is released now, or it would keep pf on for good.
		if werr := WriteAtomic(tokPath, []byte(m[1]+"\n"), 0o600); werr != nil {
			_, _ = d.R.Run(ctx, "/sbin/pfctl", "-X", m[1])
			return fmt.Errorf("could not save the pf reference: %w", werr)
		}
	}
	info, _ = d.R.Run(ctx, "/sbin/pfctl", "-s", "info")
	if !strings.Contains(info, "Status: Enabled") {
		return errors.New("pf did not turn on")
	}
	return nil
}

// recordDesired records the fence as pf prints it now, for the guard.
func (d *Daemon) recordDesired(ctx context.Context, b Bridge, port, block string) (Desired, error) {
	raw, err := d.R.Run(ctx, "/sbin/pfctl", "-a", Anchor, "-sr")
	if err != nil {
		return Desired{}, err
	}
	t, err := d.R.Run(ctx, "/sbin/pfctl", "-a", Anchor, "-t", "cp_deny", "-T", "show")
	if err != nil {
		return Desired{}, err
	}
	ds := Desired{
		Bridge: b.Name, Port: port, Gateway: b.Addr, Subnet: b.Prefix,
		SoftnetBlock: block, Rules: NormalizeRules(raw), Table: NormalizeTable(t), StartedAt: d.now(),
	}
	return ds, WriteJSON(d.state(DesiredFile), ds, 0o644)
}

var vncURL = regexp.MustCompile(`vnc://[^\s]*@127\.0\.0\.1:[0-9]+`)

// handOverVNC copies the guest screen's address to the owner's folder. The
// owner already controls the VM through Tart, so this gives nothing new; it
// lets the app open the screen for a sign-in.
func (d *Daemon) handOverVNC(inst Install, logPath string) {
	for i := 0; i < 50; i++ {
		b, _ := os.ReadFile(logPath)
		if m := vncURL.Find(b); m != nil {
			u := strings.TrimSuffix(string(m), "...")
			path := filepath.Join(d.P.Owner, VNCFile)
			if err := WriteAtomic(path, []byte(u+"\n"), 0o600); err == nil {
				_ = os.Chown(path, inst.OwnerUID, -1)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	d.logf("start: Tart printed no VNC address")
}

// tartExited is whether the Tart log says the VM stopped.
func tartExited(logPath string) (bool, string) {
	b, _ := os.ReadFile(logPath)
	s := string(b)
	if strings.Contains(s, "Error") || strings.Contains(s, "error:") || guestLimitText.MatchString(s) {
		return true, s
	}
	return false, s
}

// startFailure turns the Tart log into the error the app shows.
func (d *Daemon) startFailure(logPath, what string) error {
	b, _ := os.ReadFile(logPath)
	s := strings.TrimSpace(vncURL.ReplaceAllString(string(b), "vnc://…"))
	if guestLimitText.MatchString(s) {
		return errGuestLimit
	}
	if len(s) > 600 {
		s = s[len(s)-600:]
	}
	if s != "" {
		return fmt.Errorf("%s. Tart said: %s", what, s)
	}
	return errors.New(what)
}

// stopVM stops the VM as its owner.
func (d *Daemon) stopVM(ctx context.Context, inst Install) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := asOwner(ctx, d.R, inst, d.P.Bin, TartExe(d.P.Bin), "stop", VMName); err != nil {
		d.logf("stop: %v", err)
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
