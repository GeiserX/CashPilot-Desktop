package winvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The guard runs as SYSTEM from the admin-only copy of the exe (--vm-guard).
// Boot and Resume take the VM off the network first, then connect and start it
// only when the live fence is exactly the proven one. Every pass after that
// refreshes the Denies that follow the host, compares, and may only disconnect.
// It never reads the guest: PowerShell Direct decodes the guest's answer on the
// host, and a bug there must hit a user-level process, never SYSTEM.

// GuardInterval is how often Run passes.
const GuardInterval = 60 * time.Second

// guardInterval is GuardInterval, shortened by tests.
var guardInterval = GuardInterval

// resumeGap is how far the wall clock may move from the end of one routine to
// the start of the next before Run treats the next one as a resume from sleep.
// Without a sleep that gap is at most one GuardInterval, however slow the
// routine was. The guard task ignores the resume trigger while Run already
// runs, so Run spots the jump itself.
const resumeGap = GuardInterval + GuardInterval/2

const (
	maxStateBytes = maxPlanBytes
	maxLockBytes  = 4 << 10
)

// GuardDeps is what the guard works with.
type GuardDeps struct {
	HV Guarder
	// Echo asks api.ipify.org and icanhazip.com; see NewEcho. Nil asks nobody,
	// so the own IPs never fill and the VM stays off the network.
	Echo func(ctx context.Context) []netip.Addr
	Now  func() time.Time
	// Dir is Names.DataDir: the plan, the state, the status, the proof lock and
	// the user's preferences live here.
	Dir string
	// Logf writes one log line. Nil logs nothing.
	Logf func(format string, args ...any)
}

// GuardState is what the guard carries from one pass to the next, in
// Dir\state.json.
type GuardState struct {
	OwnIPs    OwnIPs      `json:"ownIps"`
	Drops     DropTracker `json:"drops"`
	LastBuild string      `json:"lastBuild"`
	// AdapterID is the adapter Drops and Denied belong to. A rebuilt VM starts
	// both again.
	AdapterID string `json:"adapterId"`
	// BlockedSince is when Drops started counting.
	BlockedSince time.Time `json:"blockedSince"`
	// Denied are the Denies the guard manages itself (own IPs, host addresses,
	// tunnels) that were in place. One of them gone later was removed by someone
	// else: the guard does not put it back, so Diff sees it and the VM is cut
	// off until Repair.
	Denied   []netip.Prefix `json:"denied"`
	PortACLs []PortACL      `json:"portAcls,omitempty"`
	// VMID is the VM of the last plan the guard could read. When the plan cannot
	// be read later, the guard still takes that VM off the network.
	VMID  string `json:"vmId"`
	Owner string `json:"owner,omitempty"`
	Names *Names `json:"names,omitempty"`
	// Cut is set once the guard took the VM off the network because its fence
	// changed. It holds across passes and guard processes until Boot or Resume
	// runs the exact check again (Repair, a reboot, a resume or a new guard), so
	// no pass in between shows the fence green or promises a reconnect.
	Cut bool `json:"cut"`
	// CutChanges are the changes that cut the VM off.
	CutChanges []Change `json:"cutChanges"`
}

// Boot is the boot routine: disconnect, compare, then connect and start only
// on an exact match.
func (d GuardDeps) Boot(ctx context.Context) Status {
	st, _ := d.routine(ctx, Boot, false, nil)
	return st
}

// outcome is where a routine left the VM, for Run.
type outcome int

const (
	// leftOn: the VM is on the network and the routine did all it meant to: a
	// Pass found nothing to stop, or Boot and Resume connected and started it.
	leftOn outcome = iota
	// leftOff: the VM is off the network, or not running, and not because the
	// fence changed. This includes a boot routine that could not take the
	// adapter off, could not connect it, or could not start the VM.
	leftOff
	// leftCut: the VM is off the network because the fence changed.
	leftCut
)

// Pass refreshes the guard's own Denies, compares, may only disconnect, and
// writes the status.
func (d GuardDeps) Pass(ctx context.Context) Status {
	st, _ := d.routine(ctx, Periodic, false, nil)
	return st
}

// Run runs Boot, then a tick every GuardInterval until ctx ends.
func (d GuardDeps) Run(ctx context.Context) error {
	l := guardLoop{d: d}
	l.boot(ctx)
	t := time.NewTicker(guardInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		l.tick(ctx)
	}
}

// guardLoop is what Run remembers between two routines.
type guardLoop struct {
	d GuardDeps
	// last is when the previous routine ended, so a slow routine never counts
	// as a sleep. A sleep in the middle of a routine is missed, but the passes
	// after it still cut the VM off on any change.
	last time.Time
	// retry is set while the VM is off the network for a reason that is not a
	// change to the fence: keepOff, a network not approved yet, no own IP yet,
	// a failed reading, a proof lock, a plan not proven yet. Each tick then runs
	// the boot routine again, so the VM comes back once the reason is gone, and
	// only after a fresh exact check.
	// A cut for a change to the fence (GuardState.Cut) stops the retries, even
	// after the change is undone: only Repair, a reboot, a resume or a new guard
	// brings the VM back from that.
	retry bool
	// cut is the cut as the last routine left it, kept here as well as in
	// state.json: a state file that could not be written must not lift it.
	cut cutMemo
}

// cutMemo is a cut for a change to the fence, and the changes behind it.
type cutMemo struct {
	held    bool
	changes []Change
}

func (l *guardLoop) boot(ctx context.Context) Status {
	st, o := l.d.routine(ctx, Boot, false, &l.cut)
	l.last = l.d.now()
	l.note(o)
	return st
}

// note records where a routine left the VM: only a stop that was not a cut is
// retried.
func (l *guardLoop) note(o outcome) { l.retry = o == leftOff }

// tick is one of Run's passes. After the wall clock jumped (the host slept) it
// runs the boot routine as Resume. While retry holds it runs the boot routine
// too, except under a proof lock. Otherwise it is a Pass.
func (l *guardLoop) tick(ctx context.Context) Status {
	gap := l.d.now().Round(0).Sub(l.last.Round(0))
	var st Status
	var o outcome
	switch {
	case gap > resumeGap:
		l.d.logf("resume: the clock moved %s since the last routine ended", gap.Round(time.Second))
		st, o = l.d.routine(ctx, Resume, false, &l.cut)
	case l.retry:
		st, o = l.d.routine(ctx, Resume, true, &l.cut)
	default:
		st, o = l.d.routine(ctx, Periodic, false, &l.cut)
	}
	l.last = l.d.now()
	l.note(o)
	return st
}

func (d GuardDeps) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

func (d GuardDeps) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

// routine is Boot, Resume and Pass. Only a mode other than Periodic can reach
// Connect or Start, and only when Decide allows it. With retrying set it is
// Run's retry of the boot routine, which a valid proof lock turns into a Pass:
// the proof must not lose its network to a retry.
//
// mem is Run's memory of a cut (nil for a one-shot routine). A routine that
// does not run the exact check again keeps a cut it holds, even when state.json
// lost it, and mem ends as the cut state.json should hold.
//
// It also reports where it left the VM, for Run.
func (d GuardDeps) routine(ctx context.Context, m Mode, retrying bool, mem *cutMemo) (Status, outcome) {
	now := d.now()
	st := Status{Version: StatusVersion, WrittenAt: now, Network: NetAbsent, Fence: FenceUnproven, Changes: []Change{}}
	defer func() {
		if err := WriteStatus(d.Dir, st); err != nil {
			d.logf("could not write the status: %v", err)
		}
	}()

	gs := d.loadState()
	// Even without a readable plan, a prior identity is usable only when
	// the VM still carries the saved owner tag.
	failClosed := func(until string, plan *Plan) {
		names, vmID, owner, adapterID := gs.Names, gs.VMID, gs.Owner, gs.AdapterID
		if plan != nil {
			names, vmID, owner, adapterID = &plan.Names, plan.VMID, plan.Owner, plan.AdapterID
			if gs.Names != nil && *gs.Names == plan.Names && gs.Owner == owner {
				if vmID == "" {
					vmID = gs.VMID
				}
				if adapterID == "" && strings.EqualFold(gs.VMID, vmID) {
					adapterID = gs.AdapterID
				}
			}
		}
		st.Fence, st.Network = FenceCut, NetConnected
		var owned string
		if names != nil && ownerRe.MatchString(owner) {
			inv, err := d.HV.Inventory(ctx, *names, KnownIDs{VM: vmID, Owner: owner}, "")
			if err == nil {
				owned = adopt(inv.VMs, vmID, owner)
				if len(inv.VMs) == 0 {
					st.Network = NetAbsent
					return
				}
			} else {
				d.logf("list VMs: %v", err)
			}
		}
		if owned == "" {
			st.Reason += " The guard cannot tell which VM belongs to this install. Run Repair or Remove VM."
			return
		}
		vmID = owned
		if err := d.HV.Disconnect(ctx, vmID, owner, adapterID); err != nil {
			d.logf("disconnect failed: %v", err)
			st.Reason += " The guard could not disconnect the recorded adapter: " + err.Error()
			return
		}
		st.Network = NetDisconnected
		st.Reason += " The VM stays off the network until " + until + "."
	}
	p, err := d.loadPlan()
	if err != nil {
		st.Reason = "Could not read the VM's desired state, so the guard cannot check it: " + err.Error()
		d.logf("%s", st.Reason)
		failClosed("the guard can read it again", nil)
		return st, leftOff
	}
	st.VM, st.ProvedAt = p.Names.VM, p.ProvedAt
	if p.VMID == "" || p.AdapterID == "" || p.SwitchID == "" {
		st.Reason = "The VM is not built yet."
		failClosed("its desired state names it again", &p)
		return st, leftOff
	}
	inv, err := d.HV.Inventory(ctx, p.Names, KnownIDs{VM: p.VMID, Switch: p.SwitchID}, "")
	if err != nil || adopt(inv.VMs, p.VMID, p.Owner) == "" {
		st.Fence, st.Network = FenceCut, NetConnected
		if err == nil && len(inv.VMs) == 0 {
			st.Network = NetAbsent
		}
		st.Reason = "The guard cannot prove which VM belongs to this install. Run Repair or Remove VM."
		return st, leftOff
	}

	gs = gs.forPlan(p)
	defer func() {
		if mem != nil {
			*mem = cutMemo{held: gs.Cut, changes: slices.Clone(gs.CutChanges)}
		}
		if err := d.saveState(gs); err != nil {
			d.logf("could not write the state: %v", err)
		}
	}()
	switch {
	case m != Periodic && !retrying:
		// Boot and Resume run the exact check again: the only way back from a
		// cut for a change to the fence. A retry never lifts a cut.
		gs.Cut, gs.CutChanges = false, nil
	case mem != nil && mem.held && !gs.Cut:
		// state.json lost the cut (a write that failed): Run still holds it.
		gs.Cut, gs.CutChanges = true, slices.Clone(mem.changes)
	}

	connected := true // unknown until read: assume the worst
	disconnect := func() {
		if err := d.HV.Disconnect(ctx, p.VMID, p.Owner, p.AdapterID); err != nil {
			d.logf("disconnect failed: %v", err)
			return
		}
		connected = false
	}
	lock := d.proofLock(now)
	if lock && retrying {
		m = Periodic
	}
	if m != Periodic {
		disconnect()
	}
	prefs, prefsErr := LoadPrefs(d.Dir)
	if prefsErr != nil {
		d.logf("prefs: %v", prefsErr)
	}

	// Under a valid proof lock the proof has holes open on purpose and closes
	// them itself, so nothing here touches the fence. Boot and Resume have
	// already disconnected; Periodic does nothing. Once the lock is gone, a VM
	// left off here gets the boot routine's exact check.
	if lock {
		st.Reason = Decide(m, Inputs{ProofLock: true}).Reason
		l, err := d.HV.ReadLive(ctx, p.Names, p.VMID, p.SwitchID)
		if err == nil && connected {
			connected = anyConnected(l)
		}
		st.Network = networkState(l, err, connected)
		// Only a plain Pass can leave the VM on here: Boot and Resume never
		// connect under the lock, so a VM still on the network after them has
		// an adapter that would not come off, and Run retries.
		if m == Periodic && !retrying && connected {
			return st, leftOn
		}
		return st, leftOff
	}

	// 1. Refresh what the Denies follow: the own IPs, the host's addresses and
	// routes, and the network.
	in := Inputs{KeepOff: prefs.KeepOff || prefsErr != nil}
	if d.Echo != nil {
		gs.OwnIPs.Observe(now, d.Echo(ctx))
	}
	in.OwnIPsFilled = gs.OwnIPs.Filled
	var dyn Dynamic
	h, hErr := d.HV.ReadHostNet(ctx, EgressSamples)
	if hErr != nil {
		d.logf("read host network: %v", hErr)
	} else {
		dyn = Dynamic{
			HostAddrs: HostDenies(h, p.SwitchID),
			OwnIPs:    gs.OwnIPs.Prefixes(),
			Tunnels:   TunnelPrefixes(h, p.SwitchID, StaticDeny()),
		}
		_, in.FullTunnel = FullTunnel(h, p.SwitchID)
		_, in.Collision = SubnetCollision(h, p.SwitchID, p.Subnet)
	}
	if cur, err := d.HV.ReadNetwork(ctx); err != nil {
		in.NetworkWhy = NetworkUnknown
		d.logf("read network: %v", err)
	} else {
		in.NetworkOK, in.NetworkWhy = NetworkOK(cur, prefs.ApprovedNetworks)
	}

	// 2. Add the missing Denies the guard manages and remove its expired ones.
	// Never on a partial reading: a host reading that failed would make every
	// host Deny look expired.
	live, lErr := d.HV.ReadLive(ctx, p.Names, p.VMID, p.SwitchID)
	var notAdded, notRemoved []netip.Prefix
	if lErr == nil && hErr == nil && live.VMExists {
		var changed bool
		changed, notAdded, notRemoved = d.refreshDenies(ctx, p, live, dyn, &gs)
		if changed {
			live, lErr = d.HV.ReadLive(ctx, p.Names, p.VMID, p.SwitchID)
		}
	}

	// 3. Compare. A reading that failed counts as a change, and so does a plan
	// that was never proven.
	var drift []Change
	switch {
	case lErr != nil:
		drift = []Change{{Field: "reading", Want: "the VM's settings", Got: lErr.Error()}}
		d.logf("read live state: %v", lErr)
	case hErr != nil:
		drift = []Change{{Field: "reading", Want: "this PC's routes", Got: hErr.Error()}}
	default:
		drift = Diff(p, live, dyn)
	}
	// ownWrite: the only change is a Deny write of the guard's own that failed
	// in this pass. Nobody else changed the fence, so this is a stop to retry,
	// not a cut.
	ownWrite := lErr == nil && hErr == nil && len(drift) > 0 && len(notAdded)+len(notRemoved) > 0 &&
		len(Diff(p, asIfWritten(live, notAdded, notRemoved), dyn)) == 0
	if lErr == nil {
		connected = anyConnected(live)
	}
	in.Changes = drift
	if p.ProvedAt.IsZero() {
		in.Changes = append(slices.Clone(drift), Change{Field: "proof", Want: "proven", Got: "not proven"})
	}
	for _, c := range in.Changes {
		d.logf("MISMATCH %s: want %s, got %s", c.Field, c.Want, c.Got)
	}
	var reason string // a plainer reason than Decide's, when the guard has one
	switch {
	case lErr != nil || hErr != nil:
		reason = "Could not read the VM's protection or this PC's routes, so the VM stays off the network."
	case ownWrite:
		reason = "Could not update a rule of the VM's protection, so the VM stays off the network. The guard tries again at its next check."
	case len(drift) == 0 && p.ProvedAt.IsZero():
		reason = "The VM's protection has not been proven yet. Run Repair."
	case prefsErr != nil && len(drift) == 0 && in.Collision == "" && in.FullTunnel == "":
		// A broken prefs file approves no network, so Decide would blame the
		// network; the file is the real reason.
		reason = "Could not read your VM settings (prefs.json), so the VM stays off the network."
	}

	act := Decide(m, in)
	if (act.Disconnect || gs.Cut) && connected {
		disconnect()
	}
	started, booted := false, false
	var afterConnect []Change
	var startErr error
	if m != Periodic && act.Connect && !gs.Cut {
		connected, started, afterConnect, startErr = d.connect(ctx, p, dyn, act.Start)
		switch {
		case startErr != nil:
			reason = "The VM could not be started (" + shortErr(startErr) + "), so it stays off the network. The guard tries again at its next check."
		case !connected:
			reason = "The VM could not be connected safely, so it stays off the network."
		default:
			booted = true
		}
	}
	if act.Reason != "" {
		d.logf("off the network: %s", act.Reason)
	}
	// A change to the fence, not a failed reading or a failed write of the
	// guard's own, cuts the VM off until Boot or Resume checks it again.
	changedNow := len(afterConnect) > 0 || (lErr == nil && hErr == nil && len(drift) > 0 && !ownWrite)
	if changedNow {
		gs.Cut, gs.CutChanges = true, append(slices.Clone(drift), afterConnect...)
	}

	// 4. The drop counter, the host's measures, and the status.
	if s, err := d.HV.DropCounters(ctx, p.VMID, p.AdapterID); err != nil {
		d.logf("drop counter: %v", err)
	} else {
		gs.Drops.Add(s)
		if gs.BlockedSince.IsZero() {
			gs.BlockedSince = now
		}
		total := gs.Drops.Total
		st.Blocked, st.BlockedSince = &total, gs.BlockedSince
	}
	if u, err := d.HV.Usage(ctx, p.VMID); err == nil {
		st.Usage = &u
	}
	if lErr == nil {
		gs.LastBuild = live.HostBuild
		st.EnhancedSession = EnhancedSessionLabel(p, live.Enhanced)
		if started {
			live.VMState = "Running"
		}
	}
	st.Changes = append(append(st.Changes, in.Changes...), afterConnect...)
	if gs.Cut && !changedNow {
		st.Changes = append(st.Changes, gs.CutChanges...)
	}
	st.Reason = act.Reason
	if reason != "" && (!connected || startErr != nil) {
		st.Reason = reason
	}
	switch {
	case gs.Cut && !connected:
		st.Reason = cutReason(gs.CutChanges)
	case gs.Cut:
		// The cut holds but Disconnect failed: the VM is still on the network,
		// so the status must not say it is cut off.
		st.Reason = "The VM's protection changed, but the guard could not take the VM off the network. It tries again at its next check."
	}
	if st.Reason == "" && !connected && lErr == nil && live.VMExists {
		st.Reason = "The VM is off the network. The guard checks its protection again and connects it at its next check."
	}
	switch {
	case gs.Cut || lErr != nil || hErr != nil || len(drift) > 0 || len(afterConnect) > 0:
		st.Fence = FenceCut
	case p.ProvedAt.IsZero():
		st.Fence = FenceUnproven
	case live.HostBuild != p.HostBuild:
		st.Fence = FenceReprove
		if st.Reason == "" {
			st.Reason = "Windows was updated since the VM's protection was proven. Run Repair to prove it again."
		}
	default:
		st.Fence = FenceHeld
	}
	st.Network = networkState(live, lErr, connected)
	switch {
	case gs.Cut:
		return st, leftCut
	case connected && (m == Periodic || booted):
		return st, leftOn
	}
	// Off the network, or a boot routine that did not finish (the adapter would
	// not come off, or the VM would not start): Run retries the boot routine.
	return st, leftOff
}

// shortErr is the first line of err, cut to 200 bytes, for the status.
func shortErr(err error) string {
	s, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// cutReason is the status reason while a cut holds: it names what changed and
// says how the VM gets back.
func cutReason(cs []Change) string {
	why := blocker(Inputs{Changes: cs})
	if why == "" {
		why = "Your VM is cut off from the internet because its protection changed."
	}
	return why + " Run Repair to check the protection and restore it."
}

// asIfWritten is live with the guard's Deny writes that failed in this pass
// done: the rows not added are there (as Hyper-V lists a Both rule), and the
// rows not removed are gone.
func asIfWritten(live LiveState, notAdded, notRemoved []netip.Prefix) LiveState {
	have := map[netip.Prefix]bool{}
	var rows []ACL
	for _, a := range live.ACLs {
		if strings.EqualFold(a.Action, "Deny") {
			q := a.Remote.Masked()
			if slices.Contains(notRemoved, q) {
				continue
			}
			have[q] = true
		}
		rows = append(rows, a)
	}
	for _, q := range notAdded {
		if !have[q] {
			rows = append(rows, ACL{Action: "Deny", Direction: "Inbound", Remote: q}, ACL{Action: "Deny", Direction: "Outbound", Remote: q})
		}
	}
	live.ACLs = rows
	return live
}

// undoTimeout bounds the Disconnect that undoes a Connect.
const undoTimeout = 2 * time.Minute

// connect puts the VM on its switch, reads the fence again with the adapter
// connected, and starts the VM only if that reading still matches. Anything
// else takes it off again. It reports whether the VM is left connected,
// whether it was started, the changes to the fence the reading after the
// connect showed, and why the VM did not start.
func (d GuardDeps) connect(ctx context.Context, p Plan, dyn Dynamic, start bool) (connected, started bool, cut []Change, startErr error) {
	// off undoes the Connect on its own bounded context: a routine cut short
	// (the service stopping) right after the Connect must still undo it.
	off := func() bool {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), undoTimeout)
		defer cancel()
		if err := d.HV.Disconnect(uctx, p.VMID, p.Owner, p.AdapterID); err != nil {
			d.logf("disconnect failed: %v", err)
			return true
		}
		return false
	}
	if err := d.HV.Connect(ctx, p.VMID, p.SwitchID, p.Owner, p.AdapterID); err != nil {
		d.logf("connect failed: %v", err)
		return off(), false, nil, nil
	}
	live, err := d.HV.ReadLive(ctx, p.Names, p.VMID, p.SwitchID)
	if err != nil {
		d.logf("read live state after connect: %v", err)
		return off(), false, nil, nil
	}
	if cs := Diff(p, live, dyn); len(cs) > 0 {
		for _, c := range cs {
			d.logf("MISMATCH after connect %s: want %s, got %s", c.Field, c.Want, c.Got)
		}
		return off(), false, cs, nil
	}
	d.logf("connected: the fence matches the proven one")
	if start && live.VMState != "Running" {
		if err := d.HV.Start(ctx, p.VMID, p.Owner); err != nil {
			// A start that failed (often too little free memory) is a stop to
			// retry, not a VM that is up. The VM goes off the network again and
			// the routine reports it off, so Run's next tick runs the whole boot
			// routine: disconnect, an exact check, Connect, Start. Pass never
			// gets a Start or a Connect of its own.
			d.logf("start failed: %v", err)
			return off(), false, nil, err
		}
		d.logf("started the VM")
		return true, true, nil, nil
	}
	return true, false, nil, nil
}

// refreshDenies adds each Deny the guard manages that is missing and new, and
// removes each one it placed that is no longer wanted. It reports whether it
// tried to change anything, and the rows it could not add or remove. A Deny it
// placed before that is now missing stays missing: someone removed it, and
// Diff must see that.
func (d GuardDeps) refreshDenies(ctx context.Context, p Plan, live LiveState, dyn Dynamic, gs *GuardState) (changed bool, notAdded, notRemoved []netip.Prefix) {
	if requireRecordedAdapter(p, live) != nil || live.PortACLs == nil {
		return false, nil, nil // Diff reports the changed or unread adapter.
	}
	base := map[netip.Prefix]bool{}
	for _, a := range p.WantACLs(Dynamic{}) {
		base[a.Remote] = true
	}
	var owned []netip.Prefix
	for _, a := range p.WantACLs(dyn) {
		if !base[a.Remote] {
			owned = append(owned, a.Remote)
		}
	}
	var rows []ACL
	for _, a := range live.ACLs {
		if strings.EqualFold(a.Action, "Deny") {
			rows = append(rows, ACL{Action: "Deny", Direction: a.Direction, Remote: a.Remote.Masked()})
		}
	}
	// inPlace: the row is there both ways. present: it is there at least one
	// way, so a row left one way by a remove that stopped halfway still counts.
	inPlace, present := map[netip.Prefix]bool{}, map[netip.Prefix]bool{}
	for _, a := range foldBoth(rows) {
		present[a.Remote] = true
		if a.Direction == "Both" {
			inPlace[a.Remote] = true
		}
	}
	var add, remove []netip.Prefix
	for _, q := range owned {
		if !inPlace[q] && !slices.Contains(gs.Denied, q) {
			add = append(add, q)
		}
	}
	// A row of the guard's own that is no longer wanted goes, even when only
	// one way of it is left: otherwise the leftover is extra for Diff and the
	// VM stays cut off.
	for _, q := range gs.Denied {
		if present[q] && !slices.Contains(owned, q) && !base[q] {
			remove = append(remove, q)
		}
	}
	// Add before remove, so a wider new Deny is in place before the narrower
	// ones it covers go. When the add fails, nothing is removed in this pass.
	if len(add) > 0 {
		rows, err := d.HV.AddPortACLs(ctx, p, "Deny", add)
		gs.PortACLs = append(gs.PortACLs, rows...)
		if err != nil {
			d.logf("add Deny %v failed: %v", add, err)
			notAdded = add
		} else {
			d.logf("added Deny %v", add)
			for _, q := range add {
				inPlace[q] = true
			}
		}
		changed = true
	}
	if len(remove) > 0 {
		switch {
		case notAdded != nil:
			notRemoved = remove
		default:
			var rows []PortACL
			for _, a := range live.PortACLs {
				if slices.Contains(remove, a.prefix()) && (slices.Contains(p.PortACLs, a) || slices.Contains(gs.PortACLs, a)) {
					rows = append(rows, a)
				}
			}
			if err := d.HV.RemovePortACLs(ctx, p, rows); err != nil {
				d.logf("remove Deny %v failed: %v", remove, err)
				notRemoved = remove
			} else {
				gs.PortACLs = slices.DeleteFunc(gs.PortACLs, func(a PortACL) bool {
					return slices.Contains(remove, a.prefix())
				})
				d.logf("checked expired Deny %v", remove)
			}
			changed = true
		}
	}
	var denied []netip.Prefix
	for _, q := range owned {
		if inPlace[q] || slices.Contains(gs.Denied, q) {
			denied = append(denied, q)
		}
	}
	// A row not removed is still the guard's, so the next pass removes it.
	gs.Denied = append(denied, notRemoved...)
	return changed, notAdded, notRemoved
}

// anyConnected is whether any adapter of the VM has a switch.
func anyConnected(l LiveState) bool {
	for _, a := range l.Adapters {
		if a.SwitchID != "" {
			return true
		}
	}
	return false
}

// networkState names the VM's network for the status from the last reading.
func networkState(l LiveState, err error, connected bool) string {
	switch {
	case err != nil && connected:
		return NetConnected // not shown to be off: assume the worst
	case err != nil:
		return NetDisconnected
	case !l.VMExists:
		return NetAbsent
	case l.VMState != "Running":
		return NetOff
	case connected:
		return NetConnected
	}
	return NetDisconnected
}

// loadPlan reads Dir\desired.json, which only an admin can have written.
func (d GuardDeps) loadPlan() (Plan, error) {
	b, err := readCapped(filepath.Join(d.Dir, PlanFile), maxPlanBytes, true)
	if err != nil {
		return Plan{}, err
	}
	return LoadPlan(b)
}

// loadState reads Dir\state.json. A missing, broken or foreign file starts
// fresh: the own IPs fill again on the next answer, and the VM stays off the
// network until they do.
func (d GuardDeps) loadState() GuardState {
	var gs GuardState
	b, err := readCapped(filepath.Join(d.Dir, StateFile), maxStateBytes, true)
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		err = dec.Decode(&gs)
	}
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			d.logf("state: starting fresh: %v", err)
		}
		gs = GuardState{}
	}
	return gs
}

// forPlan fits the state to the plan. A new adapter starts the counter and the
// guard's own Deny list again; a cut holds.
func (gs GuardState) forPlan(p Plan) GuardState {
	if !strings.EqualFold(gs.AdapterID, p.AdapterID) {
		gs = GuardState{OwnIPs: gs.OwnIPs, LastBuild: gs.LastBuild, AdapterID: p.AdapterID, Cut: gs.Cut, CutChanges: gs.CutChanges}
	}
	gs.VMID, gs.Owner, gs.Names = p.VMID, p.Owner, &p.Names
	return gs
}

func (d GuardDeps) saveState(gs GuardState) error {
	b, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(d.Dir, StateFile), append(b, '\n'), adminOnly)
}

// proofLock is whether a valid proof lock holds now: written by an admin, and
// younger than ProofLockTTL. Anything else is ignored.
func (d GuardDeps) proofLock(now time.Time) bool {
	at, err := ReadProofLock(d.Dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			d.logf("proof lock ignored: %v", err)
		}
		return false
	}
	if !ProofLockValid(at, now) {
		d.logf("proof lock from %s ignored: older than %s", at.Format(time.RFC3339), ProofLockTTL)
		return false
	}
	return true
}

// proofLockFile is Dir\proof.lock.
type proofLockFile struct {
	TakenAt time.Time `json:"takenAt"`
}

// WriteProofLock takes the proof lock: setup writes it before the proof opens
// its first hole, and removes it after the last one is closed.
func WriteProofLock(dir string, takenAt time.Time) error {
	b, err := json.Marshal(proofLockFile{TakenAt: takenAt})
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, LockFile), append(b, '\n'), adminOnly)
}

// ReadProofLock reads when the proof lock was taken. Only an admin-owned file
// counts, so a standard user cannot hold the guard back.
func ReadProofLock(dir string) (time.Time, error) {
	b, err := readCapped(filepath.Join(dir, LockFile), maxLockBytes, true)
	if err != nil {
		return time.Time{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var l proofLockFile
	if err := dec.Decode(&l); err != nil {
		return time.Time{}, fmt.Errorf("proof lock: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return time.Time{}, errors.New("proof lock: trailing data")
	}
	return l.TakenAt, nil
}
