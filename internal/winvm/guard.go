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

// resumeGap is how far the wall clock may move between two passes before Run
// treats the next one as a resume from sleep. The guard task ignores the resume
// trigger while Run already runs, so Run spots the jump itself.
const resumeGap = 3 * GuardInterval

const (
	maxStateBytes = 256 << 10
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
	Denied []netip.Prefix `json:"denied"`
}

// Boot is the boot routine: disconnect, compare, then connect and start only
// on an exact match.
func (d GuardDeps) Boot(ctx context.Context) Status { return d.routine(ctx, Boot) }

// Pass refreshes the guard's own Denies, compares, may only disconnect, and
// writes the status.
func (d GuardDeps) Pass(ctx context.Context) Status { return d.routine(ctx, Periodic) }

// Run runs Boot, then a Pass every GuardInterval until ctx ends. A pass that
// comes after the wall clock jumped (the host slept) runs the boot routine as
// Resume instead.
func (d GuardDeps) Run(ctx context.Context) error {
	// last is when the previous routine started, so a sleep in the middle of a
	// routine still counts toward the gap.
	last := d.now()
	d.Boot(ctx)
	t := time.NewTicker(guardInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		now := d.now()
		gap := now.Round(0).Sub(last.Round(0))
		last = now
		if gap > resumeGap {
			d.logf("resume: the clock moved %s since the last routine started", gap.Round(time.Second))
			d.routine(ctx, Resume)
		} else {
			d.Pass(ctx)
		}
	}
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
// Connect or Start, and only when Decide allows it.
func (d GuardDeps) routine(ctx context.Context, m Mode) Status {
	now := d.now()
	st := Status{Version: StatusVersion, WrittenAt: now, Network: NetAbsent, Fence: FenceUnproven, Changes: []Change{}}
	defer func() {
		if err := WriteStatus(d.Dir, st); err != nil {
			d.logf("could not write the status: %v", err)
		}
	}()

	p, err := d.loadPlan()
	if err != nil {
		st.Reason = "Could not read the VM's desired state, so the guard cannot check it: " + err.Error()
		d.logf("%s", st.Reason)
		return st
	}
	st.VM, st.ProvedAt = p.Names.VM, p.ProvedAt
	if p.VMID == "" || p.AdapterID == "" || p.SwitchID == "" {
		st.Reason = "The VM is not built yet."
		return st
	}
	gs := d.loadState(p)
	defer func() {
		if err := d.saveState(gs); err != nil {
			d.logf("could not write the state: %v", err)
		}
	}()

	connected := true // unknown until read: assume the worst
	disconnect := func() {
		if err := d.HV.Disconnect(ctx, p.VMID); err != nil {
			d.logf("disconnect failed: %v", err)
			return
		}
		connected = false
	}
	if m != Periodic {
		disconnect()
	}

	lock := d.proofLock(now)
	prefs, prefsErr := LoadPrefs(d.Dir)
	if prefsErr != nil {
		d.logf("prefs: %v", prefsErr)
	}

	// Under a valid proof lock the proof has holes open on purpose and closes
	// them itself, so nothing here touches the fence. Boot and Resume have
	// already disconnected; Periodic does nothing.
	if lock {
		st.Reason = Decide(m, Inputs{ProofLock: true}).Reason
		l, err := d.HV.ReadLive(ctx, p.Names, p.VMID)
		st.Network = networkState(l, err, connected)
		return st
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
	live, lErr := d.HV.ReadLive(ctx, p.Names, p.VMID)
	if lErr == nil && hErr == nil && live.VMExists {
		if d.refreshDenies(ctx, p, live, dyn, &gs) {
			live, lErr = d.HV.ReadLive(ctx, p.Names, p.VMID)
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
	noKeepOff := in
	noKeepOff.KeepOff = false
	switch {
	case lErr != nil || hErr != nil:
		reason = "Could not read the VM's protection or this PC's routes, so the VM stays off the network."
	case len(drift) == 0 && p.ProvedAt.IsZero():
		reason = "The VM's protection has not been proven yet. Run Repair."
	case prefsErr != nil && Decide(Periodic, noKeepOff).Reason == "":
		reason = "Could not read your VM settings (prefs.json), so the VM stays off the network."
	}

	act := Decide(m, in)
	if act.Disconnect && connected {
		disconnect()
	}
	started := false
	if m != Periodic && act.Connect {
		connected, started = d.connect(ctx, p, dyn, act.Start)
		if !connected {
			reason = "The VM could not be connected safely, so it stays off the network."
		}
	}
	if act.Reason != "" {
		d.logf("off the network: %s", act.Reason)
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
	st.Changes = append(st.Changes, in.Changes...)
	st.Reason = act.Reason
	if reason != "" && !connected {
		st.Reason = reason
	}
	switch {
	case lErr != nil || hErr != nil || len(drift) > 0:
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
	return st
}

// connect puts the VM on its switch, reads the fence again with the adapter
// connected, and starts the VM only if that reading still matches. Anything
// else takes it off again. It reports whether the VM is left connected, and
// whether it was started.
func (d GuardDeps) connect(ctx context.Context, p Plan, dyn Dynamic, start bool) (connected, started bool) {
	off := func() bool {
		if err := d.HV.Disconnect(ctx, p.VMID); err != nil {
			d.logf("disconnect failed: %v", err)
			return true
		}
		return false
	}
	if err := d.HV.Connect(ctx, p.VMID, p.Names.Switch); err != nil {
		d.logf("connect failed: %v", err)
		return off(), false
	}
	live, err := d.HV.ReadLive(ctx, p.Names, p.VMID)
	cs := Diff(p, live, dyn)
	if err != nil {
		cs = []Change{{Field: "reading", Want: "the VM's settings", Got: err.Error()}}
	}
	if len(cs) > 0 {
		for _, c := range cs {
			d.logf("MISMATCH after connect %s: want %s, got %s", c.Field, c.Want, c.Got)
		}
		return off(), false
	}
	d.logf("connected: the fence matches the proven one")
	if start && live.VMState != "Running" {
		if err := d.HV.Start(ctx, p.VMID); err != nil {
			d.logf("start failed: %v", err)
			return true, false
		}
		d.logf("started the VM")
		return true, true
	}
	return true, false
}

// refreshDenies adds each Deny the guard manages that is missing and new, and
// removes each one it placed that is no longer wanted. It reports whether it
// changed anything. A Deny it placed before that is now missing stays missing:
// someone removed it, and Diff must see that.
func (d GuardDeps) refreshDenies(ctx context.Context, p Plan, live LiveState, dyn Dynamic, gs *GuardState) bool {
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
	inPlace := map[netip.Prefix]bool{}
	for _, a := range foldBoth(rows) {
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
	for _, q := range gs.Denied {
		if inPlace[q] && !slices.Contains(owned, q) && !base[q] {
			remove = append(remove, q)
		}
	}
	changed := false
	// Add before remove, so a wider new Deny is in place before the narrower
	// ones it covers go.
	if len(add) > 0 {
		if err := d.HV.AddDeny(ctx, p.VMID, add); err != nil {
			d.logf("add Deny %v failed: %v", add, err)
		} else {
			d.logf("added Deny %v", add)
			for _, q := range add {
				inPlace[q] = true
			}
		}
		changed = true
	}
	if len(remove) > 0 {
		if err := d.HV.RemoveDeny(ctx, p.VMID, remove); err != nil {
			d.logf("remove Deny %v failed: %v", remove, err)
		} else {
			d.logf("removed expired Deny %v", remove)
		}
		changed = true
	}
	var denied []netip.Prefix
	for _, q := range owned {
		if inPlace[q] || slices.Contains(gs.Denied, q) {
			denied = append(denied, q)
		}
	}
	gs.Denied = denied
	return changed
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
// network until they do. A new adapter starts the counter and the guard's own
// Deny list again.
func (d GuardDeps) loadState(p Plan) GuardState {
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
	if !strings.EqualFold(gs.AdapterID, p.AdapterID) {
		gs = GuardState{OwnIPs: gs.OwnIPs, LastBuild: gs.LastBuild, AdapterID: p.AdapterID}
	}
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
