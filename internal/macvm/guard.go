package macvm

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// GuardInterval is how often the guard passes.
const GuardInterval = 60 * time.Second

// wakeGap is the gap since the last pass that counts as a sleep: the first
// pass after it cuts before it checks, like the pass at boot.
const wakeGap = 90 * time.Second

// ownIPEvery is how often the guard asks the echo services for the owner's
// public IP. The answer changes rarely, and each ask is two HTTPS requests.
const ownIPEvery = 5 * time.Minute

// Daemon is the root side.
type Daemon struct {
	P    Paths
	R    Runner
	Now  func() time.Time
	Logf func(format string, args ...any)
	// PublicIPs asks the echo services for the host's public IPv4 addresses.
	PublicIPs func(ctx context.Context) ([]netip.Addr, error)
	// HostBuild reads the host's macOS build.
	HostBuild func(ctx context.Context) (string, error)

	lastPass  time.Time
	lastOwnIP time.Time
	// distrust is set when an old verdict could not be cleared before a new
	// proof. The guard then treats the verdict on disk as unproven, until a
	// new green verdict is written.
	distrust bool
	// startErr is the last start's error, nil after a start that worked.
	startErr error
}

func (d *Daemon) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Daemon) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

func (d *Daemon) state(name string) string { return filepath.Join(d.P.State, name) }

// Live is one reading of the fence on the host.
type Live struct {
	Bridges     []Bridge
	Bridge      Bridge
	Port        string
	Member      bool
	PFEnabled   bool
	MainAnchor  bool
	Rules       string // normalized
	RawRules    string
	Table       string
	Softnet     []string // the args of every softnet that serves our MAC
	TartRunning bool
}

// Read reads the live fence for inst.
func (d *Daemon) Read(ctx context.Context, inst Install, desired Desired) (Live, error) {
	var l Live
	bs, err := d.bridges(ctx)
	if err != nil {
		return l, err
	}
	l.Bridges = bs
	l.Bridge, l.Port = d.locate(ctx, bs, inst, desired)
	l.Member = l.Port != "" && l.Bridge.HasMember(l.Port)
	info, _ := d.R.Run(ctx, "/sbin/pfctl", "-s", "info")
	l.PFEnabled = strings.Contains(info, "Status: Enabled")
	main, _ := d.R.Run(ctx, "/sbin/pfctl", "-sr")
	l.MainAnchor = slices.Contains(strings.Split(main, "\n"), `anchor "com.apple/*" all`)
	l.RawRules, _ = d.R.Run(ctx, "/sbin/pfctl", "-a", Anchor, "-sr")
	l.Rules = NormalizeRules(l.RawRules)
	t, _ := d.R.Run(ctx, "/sbin/pfctl", "-a", Anchor, "-t", "cp_deny", "-T", "show")
	l.Table = NormalizeTable(t)
	l.Softnet, err = d.softnets(ctx, inst.MAC)
	if err != nil {
		return l, err
	}
	return l, nil
}

// locate finds the guest's bridge and port: by its MAC in a bridge's cache,
// or else by the port the start recorded. A cut port has no traffic, so its
// MAC leaves the cache; the recorded port counts while its bridge exists and
// the interface is still there.
func (d *Daemon) locate(ctx context.Context, bs []Bridge, inst Install, desired Desired) (Bridge, string) {
	if b, p, ok := GuestPort(bs, inst.MAC); ok {
		return b, p
	}
	if desired.Port != "" {
		for _, b := range bs {
			if b.Name == desired.Bridge {
				if _, err := d.R.Run(ctx, "/sbin/ifconfig", desired.Port); err == nil {
					return b, desired.Port
				}
			}
		}
	}
	return Bridge{}, ""
}

func (d *Daemon) bridges(ctx context.Context) ([]Bridge, error) {
	names, err := d.R.Run(ctx, "/sbin/ifconfig", "-l")
	if err != nil {
		return nil, err
	}
	var bs []Bridge
	for _, n := range strings.Fields(names) {
		if !IsVMNetBridge(n) {
			continue
		}
		out, err := d.R.Run(ctx, "/sbin/ifconfig", n)
		if err != nil {
			// A bridge that went away between the two reads is fine. A bridge
			// that is still there but cannot be read can hold the guest, so
			// it fails the read: the guard must not take it for an absent guest.
			if again, lerr := d.R.Run(ctx, "/sbin/ifconfig", "-l"); lerr != nil || slices.Contains(strings.Fields(again), n) {
				return nil, fmt.Errorf("could not read bridge %s: %w", n, err)
			}
			continue
		}
		bs = append(bs, ParseBridge(n, out))
	}
	return bs, nil
}

// softnets lists the argument strings of the softnet processes that serve mac.
func (d *Daemon) softnets(ctx context.Context, mac string) ([]string, error) {
	out, err := d.R.Run(ctx, "/bin/ps", "-axww", "-o", "args=")
	if err != nil {
		return nil, err
	}
	m, _ := NormalizeMAC(mac)
	var found []string
	for _, line := range strings.Split(out, "\n") {
		_, f, ok := splitSoftnet(line)
		if !ok {
			continue
		}
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "--vm-mac-address" {
				if got, ok := NormalizeMAC(f[i+1]); ok && got == m {
					found = append(found, strings.TrimSpace(line))
				}
			}
		}
	}
	return found, nil
}

// splitSoftnet splits one `ps -o args=` line into softnet's executable path
// and its arguments. The path can hold spaces (/Library/Application Support),
// so it ends at the first "/softnet" that a space or the line's end follows.
func splitSoftnet(line string) (exe string, args []string, ok bool) {
	line = strings.TrimSpace(line)
	name := "/" + SoftnetExe
	for i := 0; ; {
		j := strings.Index(line[i:], name)
		if j < 0 {
			return "", nil, false
		}
		end := i + j + len(name)
		if end == len(line) || line[end] == ' ' {
			return line[:end], strings.Fields(line[end:]), true
		}
		i = end
	}
}

// SoftnetMatches is whether line is our softnet with exactly the proven block
// list, from our own copy, and with no allow rule.
func SoftnetMatches(line, bin, block string) bool {
	exe, f, ok := splitSoftnet(line)
	if !ok || exe != filepath.Join(bin, SoftnetExe) {
		return false
	}
	got := ""
	for i := 0; i < len(f); i++ {
		switch f[i] {
		case "--allow", "--expose", "--control-fd":
			return false
		case "--block":
			if i+1 < len(f) {
				got = f[i+1]
			}
		}
	}
	return got == block
}

// Drift is every way live differs from desired. Empty means an exact match.
func Drift(l Live, desired Desired, bin string) []string {
	var d []string
	if !l.PFEnabled {
		d = append(d, "pf-off")
	}
	if !l.MainAnchor {
		d = append(d, "main-ruleset")
	}
	if desired.Rules == "" || l.Rules != desired.Rules {
		d = append(d, "rules")
	}
	if desired.Table == "" || l.Table != desired.Table {
		d = append(d, "table")
	}
	switch {
	case len(l.Softnet) != 1:
		d = append(d, "softnet")
	case !SoftnetMatches(l.Softnet[0], bin, desired.SoftnetBlock):
		d = append(d, "softnet")
	}
	if l.Bridge.Name == "" || l.Bridge.Name != desired.Bridge || l.Port != desired.Port {
		d = append(d, "wrong-bridge")
	} else if !rulesOnBridge(l.RawRules, l.Bridge.Name) {
		d = append(d, "wrong-bridge")
	}
	if !l.Bridge.Addr.IsValid() || l.Bridge.Addr != desired.Gateway || !gatewayRule(l.RawRules, desired.Gateway) {
		d = append(d, "wrong-gateway")
	}
	return d
}

// rulesOnBridge is whether raw has rules and every rule that names an
// interface names bridge. NormalizeRules drops the name, so only this check
// sees a rule moved to another interface.
func rulesOnBridge(raw, bridge string) bool {
	found := false
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] != "on" {
				continue
			}
			if f[i+1] != bridge {
				return false
			}
			found = true
		}
	}
	return found
}

func gatewayRule(raw string, gw netip.Addr) bool {
	for _, line := range strings.Split(raw, "\n") {
		if strings.Contains(line, `label "cp-host-gw"`) && strings.Contains(line, " to "+gw.String()+" ") {
			return true
		}
	}
	return false
}

// ProofValid is whether the proof on record covers this host and these tools.
// It returns the fence state to show when it does not.
func ProofValid(pr Proven, inst Install, hostBuild string) (bool, string, string) {
	switch {
	case !pr.Green:
		return false, FenceUnproven, "the fence has not been proven yet"
	case hostBuild == "" || pr.HostBuild != hostBuild:
		return false, FenceReprove, "macOS on this Mac changed since the proof (" + pr.HostBuild + " → " + hostBuild + ")"
	case pr.Tart != inst.Tart || pr.Softnet != inst.Softnet:
		return false, FenceReprove, "Tart or softnet changed since the proof"
	}
	return true, FenceHeld, ""
}

func (d *Daemon) cut(ctx context.Context, l Live) error {
	if !l.Member {
		return nil
	}
	_, err := d.R.Run(ctx, "/sbin/ifconfig", l.Bridge.Name, "deletem", l.Port)
	return err
}

// takeOff takes the guest off the network. It cuts the port, tries once more
// when the cut fails, and then stops the VM. The network state it returns is
// the truth: disconnected after a cut, off after a stop, and connected with
// the error when both failed.
func (d *Daemon) takeOff(ctx context.Context, inst Install, l Live) (string, error) {
	err := d.cut(ctx, l)
	if err == nil {
		return NetDisconnected, nil
	}
	if err = d.cut(ctx, l); err == nil {
		return NetDisconnected, nil
	}
	d.logf("cut of %s from %s FAILED: %v; stopping %s", l.Port, l.Bridge.Name, err, VMName)
	d.stopVM(ctx, inst)
	if run, rerr := d.running(ctx, inst); rerr == nil && !run {
		return NetOff, nil
	}
	return NetConnected, fmt.Errorf("could not cut the Secure VM off the network (%v), and it did not stop", err)
}

func (d *Daemon) connect(ctx context.Context, l Live) error {
	if l.Member {
		return nil
	}
	_, err := d.R.Run(ctx, "/sbin/ifconfig", l.Bridge.Name, "addm", l.Port)
	return err
}

// errNoInstall means no admin step ran yet, or remove undid it.
var errNoInstall = errors.New("no Secure VM is installed")

func (d *Daemon) loadInstall() (Install, error) {
	var inst Install
	if err := ReadJSON(d.state(InstallFile), &inst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return inst, errNoInstall
		}
		return inst, err
	}
	if !ValidOwner(inst.Owner) || inst.OwnerUID < 500 {
		return inst, fmt.Errorf("install.json names no valid owner")
	}
	return inst, nil
}

// Pass is one guard pass. boot makes it cut before it checks: at the daemon's
// start and after a sleep. It may only cut, unless the fence matches the
// proven one exactly and the proof covers this host, when it connects.
func (d *Daemon) Pass(ctx context.Context, boot bool) Status {
	now := d.now()
	if !d.lastPass.IsZero() && now.Sub(d.lastPass) > wakeGap {
		boot = true
	}
	d.lastPass = now
	st := Status{Version: StatusVersion, WrittenAt: now, VM: VMName}
	defer func() { d.writeStatus(&st) }()

	inst, err := d.loadInstall()
	if err != nil {
		st.Network, st.Fence, st.Reason = NetAbsent, FenceUnproven, err.Error()
		return st
	}
	var desired Desired
	_ = ReadJSON(d.state(DesiredFile), &desired)
	var pr Proven
	_ = ReadJSON(d.state(VerdictFile), &pr)
	st.ProvedAt = pr.At

	if boot {
		// Cut before anything else is read: only the port is needed for it.
		if bs, err := d.bridges(ctx); err == nil {
			var pre Live
			pre.Bridge, pre.Port = d.locate(ctx, bs, inst, desired)
			if pre.Port != "" && pre.Bridge.HasMember(pre.Port) {
				pre.Member = true
				if net, err := d.takeOff(ctx, inst, pre); err == nil && net == NetDisconnected {
					d.logf("boot/wake: %s cut from %s before the check", pre.Port, pre.Bridge.Name)
				} else if err != nil {
					st.Network, st.Fence, st.Reason = net, FenceCut, err.Error()
					return st
				}
			}
		}
	}
	l, err := d.Read(ctx, inst, desired)
	if err != nil {
		st.Fence, st.Reason = FenceCut, "could not read the fence: "+err.Error()
		// Fail closed: cut whatever we can find, or the port the start
		// recorded when the read found none.
		if l.Port == "" && desired.Port != "" && desired.Bridge != "" {
			l.Bridge, l.Port, l.Member = Bridge{Name: desired.Bridge}, desired.Port, true
		}
		st.Network = NetDisconnected
		if l.Port != "" {
			var terr error
			if st.Network, terr = d.takeOff(ctx, inst, l); terr != nil {
				st.Reason += "; " + terr.Error()
			}
			return st
		}
		// No port to cut: a running VM must stop, or it could still be on
		// the network.
		if run, rerr := d.running(ctx, inst); rerr != nil || run {
			d.stopVM(ctx, inst)
			if run, rerr = d.running(ctx, inst); rerr == nil && !run {
				st.Network = NetOff
				st.Reason += "; the Secure VM was stopped, because its port could not be found"
			} else {
				st.Reason += "; its port could not be found, and the Secure VM did not stop"
			}
		}
		return st
	}
	build := ""
	if d.HostBuild != nil {
		build, _ = d.HostBuild(ctx)
	}
	ok, fence, why := ProofValid(pr, inst, build)
	if ok && d.distrust {
		ok, fence, why = false, FenceUnproven, "the last proof could not be cleared before a new one"
	}
	if l.Port == "" {
		// A stopped VM shows the proof's scope too: a proof from another
		// macOS build is not held.
		st.Network, st.Fence = NetOff, fence
		st.Reason = "the Secure VM is not running"
		if !ok {
			st.Reason += "; " + why
		}
		return st
	}
	if boot && l.Member {
		if net, err := d.takeOff(ctx, inst, l); err == nil && net == NetDisconnected {
			d.logf("boot/wake: %s cut from %s before the check", l.Port, l.Bridge.Name)
			l.Member = false
		} else if err != nil {
			st.Network, st.Fence, st.Reason = net, FenceCut, err.Error()
			return st
		}
	}
	if out, err := d.R.Run(ctx, "/sbin/pfctl", "-a", Anchor, "-vsr"); err == nil {
		if n, ok := Blocked(out); ok {
			st.Blocked, st.BlockedSince = &n, desired.StartedAt
		}
	}
	drift := Drift(l, desired, d.P.Bin)
	if err := d.ownIPChanged(ctx); err != nil {
		// A new own IP that could not be saved is not blocked yet.
		d.logf("own public IP changed, but %v", err)
		drift = append(drift, "own-ip")
	}
	// The deny list on disk must be the one the running fence applies. A new
	// own IP changes the list, and only a new start applies it, so the guest
	// stays cut until then, also when a restart failed.
	if raw, err := os.ReadFile(d.state(DenyFile)); err != nil {
		drift = append(drift, "own-ip")
	} else if deny, err := ParseDeny(string(raw)); err != nil || SoftnetBlock(deny) != desired.SoftnetBlock {
		drift = append(drift, "own-ip")
	}
	switch {
	case len(drift) > 0:
		net, err := d.takeOff(ctx, inst, l)
		switch {
		case err != nil:
			d.logf("MISMATCH: %s; %v", strings.Join(drift, " "), err)
		case l.Member:
			d.logf("MISMATCH: %s; %s cut from %s", strings.Join(drift, " "), l.Port, l.Bridge.Name)
		default:
			d.logf("MISMATCH: %s; staying cut", strings.Join(drift, " "))
		}
		st.Network, st.Fence, st.Changes = net, FenceCut, drift
		st.Reason = "the fence changed, so the Secure VM is cut off the network: " + strings.Join(drift, ", ")
		if err != nil {
			st.Reason = "the fence changed (" + strings.Join(drift, ", ") + "), and " + err.Error()
		}
	case !ok:
		net, err := d.takeOff(ctx, inst, l)
		st.Network, st.Fence, st.Reason = net, fence, why
		if err != nil {
			st.Reason = why + "; " + err.Error()
		}
		d.logf("match, but %s; staying cut", why)
	default:
		// The checks above took time: read the fence again right before the
		// port goes on, and connect only if it still matches.
		if !l.Member {
			again, err := d.Read(ctx, inst, desired)
			if err != nil || len(Drift(again, desired, d.P.Bin)) > 0 || again.Port != l.Port {
				st.Network, st.Fence, st.Reason = NetDisconnected, FenceCut, "the fence changed during the check, so the Secure VM stays cut"
				d.logf("match, but the fence changed before the connect; staying cut")
				return st
			}
			l = again
		}
		if err := d.connect(ctx, l); err != nil {
			st.Network, st.Fence, st.Reason = NetDisconnected, FenceCut, "could not connect: "+err.Error()
			d.logf("match; connect FAILED: %v", err)
			return st
		}
		if !l.Member {
			d.logf("match; %s connected to %s", l.Port, l.Bridge.Name)
		} else {
			d.logf("match; connected")
		}
		st.Network, st.Fence = NetConnected, FenceHeld
	}
	return st
}

// ownIPChanged asks for the host's public IPs now and then, and reports one
// that deny.list lacks. It adds it, so the next start blocks it. A failed ask
// changes nothing: the last known list stays.
// An IP it found but could not save is an error, and the next pass asks again.
func (d *Daemon) ownIPChanged(ctx context.Context) error {
	if d.PublicIPs == nil || d.now().Sub(d.lastOwnIP) < ownIPEvery {
		return nil
	}
	ips, err := d.PublicIPs(ctx)
	if err != nil || len(ips) == 0 {
		return nil
	}
	d.lastOwnIP = d.now()
	// The pass already cuts the guest when deny.list cannot be read.
	raw, err := os.ReadFile(d.state(DenyFile))
	if err != nil {
		return nil
	}
	deny, err := ParseDeny(string(raw))
	if err != nil {
		return nil
	}
	added := false
	for _, ip := range ips {
		if !slices.Contains(deny, ip) {
			deny = append(deny, ip)
			added = true
		}
	}
	if !added {
		return nil
	}
	slices.SortFunc(deny, func(a, b netip.Addr) int { return a.Compare(b) })
	if err := WriteAtomic(d.state(DenyFile), []byte(FormatDeny(deny)), 0o644); err != nil {
		d.lastOwnIP = time.Time{}
		return fmt.Errorf("could not save it to the deny list: %w", err)
	}
	d.logf("own public IP changed: deny list now %v; the guest restarts with it", deny)
	return nil
}

func (d *Daemon) writeStatus(st *Status) {
	st.FreeDisk = d.freeDisk()
	if d.startErr != nil {
		st.StartError, st.GuestLimit = d.startErr.Error(), errors.Is(d.startErr, errGuestLimit)
	}
	if err := WriteJSON(d.state(StatusFile), st, 0o644); err != nil {
		d.logf("status.json: %v", err)
	}
}
