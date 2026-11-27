package winvm

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The proof runs once at install, and again at each Repair, while the proof
// lock holds the guard back. Each red step opens one hole on purpose and must
// see a probe from the guest get through; each green step must see nothing get
// through and the host count the drop. The verdict reads only what the host
// saw: a canary that heard the token, pktmon, the drop counter and an hv_sock
// listener. What the guest reports only says that a probe was sent, so a broken
// or hostile guest can make a step broken, never green.

// StepID names one proof step.
type StepID string

// The proof steps, in the order they run.
const (
	StepRed1LAN      StepID = "red1-lan"
	StepRed2Router   StepID = "red2-router"
	StepRed3OwnIP    StepID = "red3-own-ip"
	StepL3Alone      StepID = "l3-alone"
	StepL2Alone      StepID = "l2-alone"
	StepRed4Gateway  StepID = "red4-gateway"
	StepIPv6Red      StepID = "ipv6-red"
	StepIPv6StackOff StepID = "ipv6-stack-off"
	StepTunnelRed    StepID = "tunnel-red"
	StepTailnetRed   StepID = "tailnet-red"
	StepVMBusRed     StepID = "vmbus-red"
	StepVMBusGreen   StepID = "vmbus-green"
	StepBindings     StepID = "bindings"
	StepGreen        StepID = "green"
	StepFunction     StepID = "function"
)

// optionalSteps may be n/a on a host: they exist only when the host routes
// something through a tunnel. Every other step must apply, or the proof is not
// green.
var optionalSteps = []StepID{StepTunnelRed, StepTailnetRed}

// Kind is what a step must show.
type Kind string

const (
	// Red opens a hole and must see the probe get through.
	Red Kind = "red"
	// Green must see nothing get through, plus its witness.
	Green Kind = "green"
	// Check reads the host's config; nothing is probed.
	Check Kind = "check"
	// Function checks that the guest works through the fence.
	Function Kind = "function"
)

// Witness is what a green step needs on top of "nothing got through", so a
// probe that was never sent cannot pass as green.
type Witness string

const (
	// WitnessNone: the step's red control proved the probe works (hv_sock).
	WitnessNone Witness = ""
	// WitnessDrops: the host's drop counter rose.
	WitnessDrops Witness = "drops"
	// WitnessDelivered: pktmon saw the frame get past the port to the host.
	WitnessDelivered Witness = "delivered"
)

// StepSpec is one step as it applies to this host.
type StepSpec struct {
	ID      StepID   `json:"id"`
	Kind    Kind     `json:"kind"`
	Opens   []string `json:"opens"` // what the step opens, for the UI table
	Applies bool     `json:"applies"`
	Why     string   `json:"why"` // why the step is n/a on this host
	Witness Witness  `json:"witness"`
}

// Evidence is what the host saw during one step.
type Evidence struct {
	Arrived []string `json:"arrived"` // tokens a canary or hv_sock listener received
	// Stray is what a canary heard that did not carry the step's token. A
	// green step counts it as a leak; a red step never counts it as red.
	Stray     []string    `json:"stray,omitempty"`
	Left      []PktRecord `json:"left"`      // packets past the fence: out of a host NIC, or of a class pktmon could not place
	Delivered bool        `json:"delivered"` // the frame got past the port to the host's vNIC
	DropDelta uint64      `json:"dropDelta"` // FailedSecurityPolicy drops during the step, out + in
	Answer    string      `json:"answer"`    // a DNS answer the guest got, or the guest's public IP
	Err       string      `json:"err"`       // why the probe could not be sent or read
}

// StepOutcome is how a step ended.
type StepOutcome string

const (
	WentRed     StepOutcome = "went-red"
	StayedGreen StepOutcome = "stayed-green"
	Broken      StepOutcome = "broken"   // a red step stayed silent, or a check failed: the proof shows nothing
	Leak        StepOutcome = "leak"     // a green step saw something get through
	Unproven    StepOutcome = "unproven" // a green step was silent, but its witness is missing
	NA          StepOutcome = "n/a"
	Held        StepOutcome = "held" // a check or the function step matched
)

// StepResult is one step's outcome with its evidence.
type StepResult struct {
	ID       StepID      `json:"id"`
	Kind     Kind        `json:"kind"`
	Outcome  StepOutcome `json:"outcome"`
	Evidence Evidence    `json:"evidence"`
	Detail   string      `json:"detail"`
}

// ProofTargets are the host's addresses the probes aim at.
type ProofTargets struct {
	LANIP       netip.Addr `json:"lanIp"`  // the host's address on its physical network
	Router      netip.Addr `json:"router"` // that network's gateway
	OwnIP       netip.Addr `json:"ownIp"`  // the line's public IPv4
	VNICIfIndex int        `json:"vnicIfIndex"`
	// MagicDNSName is a name the tailnet resolver answers, for the tailnet
	// red step. "" asks for a public name.
	MagicDNSName string `json:"magicDnsName"`
}

// magicDNS is the tailnet resolver.
var magicDNS = netip.MustParseAddr("100.100.100.100")

// tailnetProbe is the tailnet address the green step probes.
var tailnetProbe = netip.MustParseAddr("100.64.0.1")

// Targets finds the router (the default route's gateway through a
// physical-class adapter, lowest metric first) and the host's address on that
// network, and takes the first public own IP.
func Targets(h HostNet, ourSwitchID string, own []netip.Addr, vnicIfIndex int) ProofTargets {
	t := ProofTargets{VNICIfIndex: vnicIfIndex}
	var best *Route
	for i, r := range h.Routes {
		if r.Prefix.Bits() != 0 || !r.Prefix.Addr().Is4() || !r.NextHop.IsValid() || r.NextHop.IsUnspecified() {
			continue
		}
		if c, _ := classOf(h, r.IfIndex, ourSwitchID); !egressOK(c) {
			continue
		}
		if best == nil || r.Metric < best.Metric {
			best = &h.Routes[i]
		}
	}
	if best != nil {
		t.Router = best.NextHop
		for _, a := range h.Addrs {
			if a.Addr().Is4() && a.Addr() != t.Router && a.Masked().Contains(t.Router) {
				t.LANIP = a.Addr()
				break
			}
		}
	}
	for _, a := range own {
		if PublicIPv4(a) {
			t.OwnIP = a
			break
		}
	}
	return t
}

// tunnelTarget is an address inside the first tunnel prefix that no static
// Deny covers, for the tunnel red step.
func tunnelTarget(h HostNet, ourSwitchID string) (netip.Addr, netip.Prefix, bool) {
	ps := TunnelPrefixes(h, ourSwitchID, StaticDeny())
	if len(ps) == 0 {
		return netip.Addr{}, netip.Prefix{}, false
	}
	p := ps[0]
	if p.Bits() == 32 {
		return p.Addr(), p, true
	}
	return p.Addr().Next(), p, true
}

// routedThroughTunnel is whether the host's longest-prefix route to dst goes
// through a tunnel-class interface.
func routedThroughTunnel(h HostNet, ourSwitchID string, dst netip.Addr) bool {
	var best *Route
	for i, r := range h.Routes {
		if !r.Prefix.Contains(dst) {
			continue
		}
		if best == nil || r.Prefix.Bits() > best.Prefix.Bits() || (r.Prefix.Bits() == best.Prefix.Bits() && r.Metric < best.Metric) {
			best = &h.Routes[i]
		}
	}
	if best == nil {
		return false
	}
	c, _ := classOf(h, best.IfIndex, ourSwitchID)
	return c == IfTunnel
}

// Steps lists every step and whether it applies on this host.
func Steps(p Plan, h HostNet, t ProofTargets) []StepSpec {
	na := func(s StepSpec, why string) StepSpec { s.Applies, s.Why = false, why; return s }
	need := func(s StepSpec, ok bool, why string) StepSpec {
		if !ok {
			return na(s, why)
		}
		s.Applies = true
		return s
	}
	allow := func(a netip.Addr) string {
		if !a.IsValid() {
			return "L2 Allow (no address)"
		}
		return "L2 Allow " + netip.PrefixFrom(a, a.BitLen()).String()
	}
	gw := p.Gateway
	var out []StepSpec
	out = append(out,
		need(StepSpec{ID: StepRed1LAN, Kind: Red, Opens: []string{allow(t.LANIP)}}, t.LANIP.IsValid(), "this PC has no address on a physical network"),
		need(StepSpec{ID: StepRed2Router, Kind: Red, Opens: []string{allow(t.Router)}}, t.Router.IsValid(), "this PC has no default route through a physical adapter"),
		need(StepSpec{ID: StepRed3OwnIP, Kind: Red, Opens: []string{"own-IP Deny removed", allow(t.OwnIP)}}, t.OwnIP.IsValid(), "this line's public address is not known"),
		StepSpec{ID: StepL3Alone, Kind: Green, Opens: []string{allow(gw)}, Applies: true, Witness: WitnessDelivered},
		StepSpec{ID: StepL2Alone, Kind: Green, Opens: []string{"L3 off"}, Applies: true, Witness: WitnessDrops},
		StepSpec{ID: StepRed4Gateway, Kind: Red, Opens: []string{allow(gw), "L3 off"}, Applies: true},
		need(StepSpec{ID: StepIPv6Red, Kind: Red, Opens: []string{"ms_tcpip6 bound", "L2 Allow host link-local", "L3 off"}}, t.VNICIfIndex > 0, "the host vNIC is not known"),
		need(StepSpec{ID: StepIPv6StackOff, Kind: Green, Opens: []string{"L2 Allow host link-local", "L3 off"}, Witness: WitnessDelivered}, t.VNICIfIndex > 0, "the host vNIC is not known"),
	)
	if a, pre, ok := tunnelTarget(h, p.SwitchID); ok {
		out = append(out, StepSpec{ID: StepTunnelRed, Kind: Red, Opens: []string{allow(a) + " (inside " + pre.String() + ")"}, Applies: true})
	} else {
		out = append(out, na(StepSpec{ID: StepTunnelRed, Kind: Red}, "no public prefix is routed through a tunnel on this PC"))
	}
	out = append(out, need(StepSpec{ID: StepTailnetRed, Kind: Red, Opens: []string{allow(magicDNS)}},
		routedThroughTunnel(h, p.SwitchID, magicDNS), "this PC does not route the tailnet resolver through a tunnel"))
	out = append(out,
		StepSpec{ID: StepVMBusRed, Kind: Red, Opens: []string{"a registered hv_sock test listener for every VM"}, Applies: true},
		StepSpec{ID: StepVMBusGreen, Kind: Green, Opens: []string{}, Applies: true},
		StepSpec{ID: StepBindings, Kind: Check, Opens: []string{}, Applies: true},
		StepSpec{ID: StepGreen, Kind: Green, Opens: []string{}, Applies: true, Witness: WitnessDrops},
		need(StepSpec{ID: StepFunction, Kind: Function, Opens: []string{}}, t.OwnIP.IsValid(), "this line's public address is not known"),
	)
	for i := range out {
		if out[i].Opens == nil {
			out[i].Opens = []string{}
		}
	}
	return out
}

// passedFence is whether a pktmon record shows a packet past the fence. For a
// red step only a packet out of a physical or tunnel NIC counts. For a green
// step a packet of a class pktmon could not place counts too: it fails closed.
func passedFence(r PktRecord, green bool) bool {
	if r.Dropped {
		return false
	}
	switch r.Class {
	case "physical", "tunnel":
		return true
	case "vmport", "vnic":
		return false
	}
	return green
}

// Judge turns one step's evidence into its outcome.
func Judge(s StepSpec, e Evidence) StepResult {
	r := StepResult{ID: s.ID, Kind: s.Kind, Evidence: e}
	if !s.Applies {
		r.Outcome, r.Detail = NA, s.Why
		return r
	}
	escaped := func(green bool) bool {
		return len(e.Arrived) > 0 || (green && len(e.Stray) > 0) || slices.ContainsFunc(e.Left, func(p PktRecord) bool { return passedFence(p, green) })
	}
	switch s.Kind {
	case Red:
		switch {
		case e.Err != "":
			r.Outcome, r.Detail = Broken, "the probe could not run: "+e.Err
		case escaped(false):
			r.Outcome = WentRed
		default:
			r.Outcome, r.Detail = Broken, "with the hole open nothing arrived and nothing left this PC, so this probe proves nothing"
		}
	case Green:
		switch {
		case escaped(true):
			r.Outcome, r.Detail = Leak, "something got through the fence"
		case e.Err != "":
			r.Outcome, r.Detail = Unproven, "the probe could not run: "+e.Err
		case s.Witness == WitnessDrops && e.DropDelta == 0:
			r.Outcome, r.Detail = Unproven, "nothing got through, but the drop counter did not rise, so the probe may never have been sent"
		case s.Witness == WitnessDelivered && !e.Delivered:
			r.Outcome, r.Detail = Unproven, "nothing got through, but the frame never reached this PC, so the step shows nothing"
		default:
			r.Outcome = StayedGreen
		}
	case Check, Function:
		if e.Err != "" {
			r.Outcome, r.Detail = Broken, e.Err
		} else {
			r.Outcome = Held
		}
	default:
		r.Outcome, r.Detail = Broken, "unknown step kind"
	}
	return r
}

// Verdict is green only when every step that must apply did, every red step
// went red, every green step stayed green, and every check held. A step that
// is n/a never turns the verdict green.
func Verdict(rs []StepResult) (bool, string) {
	if len(rs) == 0 {
		return false, "the proof did not run"
	}
	seen := map[StepID]bool{}
	for _, r := range rs {
		seen[r.ID] = true
		want := map[Kind]StepOutcome{Red: WentRed, Green: StayedGreen, Check: Held, Function: Held}[r.Kind]
		switch {
		case r.Outcome == NA && slices.Contains(optionalSteps, r.ID):
		case r.Outcome == NA:
			return false, fmt.Sprintf("step %s could not run on this PC: %s", r.ID, r.Detail)
		case r.Outcome != want:
			return false, fmt.Sprintf("step %s: %s. %s", r.ID, r.Outcome, r.Detail)
		}
	}
	for _, id := range []StepID{StepRed1LAN, StepRed2Router, StepRed3OwnIP, StepL3Alone, StepL2Alone, StepRed4Gateway,
		StepIPv6Red, StepIPv6StackOff, StepVMBusRed, StepVMBusGreen, StepBindings, StepGreen, StepFunction} {
		if !seen[id] {
			return false, fmt.Sprintf("step %s did not run", id)
		}
	}
	return true, ""
}

// ProofWait is how long a step waits for arrivals after its probes.
const ProofWait = 10 * time.Second

// restoreTimeout bounds closing the holes after the proof.
const restoreTimeout = 2 * time.Minute

// Runner runs the proof.
type Runner struct {
	B     Builder
	P     Prober
	G     GuestProbe
	Clock func() time.Time
	// Sleep waits; nil waits for real. Tests pass a no-op.
	Sleep func(ctx context.Context, d time.Duration) error
	// Dir is Names.DataDir, where the proof lock lives.
	Dir string
	// Rand makes tokens and ports; nil is crypto/rand.
	Rand io.Reader
	Logf func(format string, args ...any)
	// Wait is how long a step waits for arrivals; 0 is ProofWait.
	Wait time.Duration
}

func (r Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

func (r Runner) now() time.Time {
	if r.Clock == nil {
		return time.Now()
	}
	return r.Clock()
}

func (r Runner) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep == nil {
		return sleepCtx(ctx, d)
	}
	return r.Sleep(ctx, d)
}

func (r Runner) wait() time.Duration {
	if r.Wait == 0 {
		return ProofWait
	}
	return r.Wait
}

func (r Runner) random(n int) []byte {
	src := r.Rand
	if src == nil {
		src = rand.Reader
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(src, b); err != nil {
		panic("winvm: no randomness: " + err.Error())
	}
	return b
}

func (r Runner) token() string { return "cpd-" + hex.EncodeToString(r.random(8)) }

// port is a random port in 40000..49151, below Windows' dynamic port range
// (49152 and up). Hyper-V and WinNAT reserve blocks of that range, and a
// listener there fails with "access ... forbidden by its access permissions"
// (live run 5: a canary on 49697, inside the reserved 49675-49774).
func (r Runner) port() uint16 { return 40000 + uint16(binary.BigEndian.Uint16(r.random(2))%9152) }

// guid is a random GUID, for the hv_sock test service.
func (r Runner) guid() string {
	b := r.random(16)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := strings.ToUpper(hex.EncodeToString(b))
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// holes is the stack of what the proof opened. Each entry closes one hole. An
// entry that fails to close stays on the stack, so the final restore tries it
// again.
type holes struct {
	undo []hole
}

type hole struct {
	name  string
	close func(context.Context) error
}

func (h *holes) push(name string, f func(context.Context) error) {
	h.undo = append(h.undo, hole{name, f})
}

// closeAll closes every hole, newest first. The ones that fail stay.
func (h *holes) closeAll(ctx context.Context) error {
	var kept []hole
	var errs []error
	for i := len(h.undo) - 1; i >= 0; i-- {
		x := h.undo[i]
		if err := x.close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", x.name, err))
			kept = append([]hole{x}, kept...)
		}
	}
	h.undo = kept
	return errors.Join(errs...)
}

// proofRun is one run's state.
type proofRun struct {
	Runner
	p     *Plan
	save  func() error
	t     ProofTargets
	h     HostNet
	holes holes
}

// RemoveProofLock releases the proof lock.
func RemoveProofLock(dir string) error {
	err := removeJournaledFile(filepath.Join(dir, LockFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Run runs every step, closing what each one opened before the next starts.
// It takes the proof lock first, renews it before each step, and releases it
// last, and connects and starts
// the VM under the lock. A deferred restore closes every hole still open, in
// reverse order, even when ctx ends; Run then checks that the fence is back:
// no Allow row, L3 on, ms_tcpip6 off. It returns the results so far with any
// error.
func (r Runner) Run(ctx context.Context, p Plan, h HostNet, t ProofTargets) (rs []StepResult, err error) {
	return r.run(ctx, &p, h, t, nil)
}

func (r Runner) run(ctx context.Context, p *Plan, h HostNet, t ProofTargets, save func() error) (rs []StepResult, err error) {
	if err := WriteProofLock(r.Dir, r.now()); err != nil {
		return nil, fmt.Errorf("could not take the proof lock: %w", err)
	}
	run := &proofRun{Runner: r, p: p, t: t, h: h, save: save}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
		defer cancel()
		if cerr := run.holes.closeAll(rctx); cerr != nil {
			err = errors.Join(err, fmt.Errorf("the proof could not close every hole it opened; the guard keeps the VM off the network until Repair: %w", cerr))
		} else if verr := run.checkRestored(rctx); verr != nil {
			err = errors.Join(err, verr)
		}
		if lerr := RemoveProofLock(r.Dir); lerr != nil {
			err = errors.Join(err, fmt.Errorf("could not release the proof lock: %w", lerr))
		}
	}()
	if err := run.ready(ctx); err != nil {
		return nil, err
	}
	for _, s := range Steps(*p, h, t) {
		if !s.Applies {
			rs = append(rs, Judge(s, Evidence{}))
			continue
		}
		if s.ID == StepIPv6StackOff || s.ID == StepVMBusGreen {
			continue // runs inside its red step, on the same holes
		}
		// A whole proof can take close to ProofLockTTL on a slow host, and
		// the guard task's 5-minute trigger starts then. Taking the lock again
		// before each step keeps the guard back until the last one ends.
		if err := WriteProofLock(r.Dir, r.now()); err != nil {
			return rs, fmt.Errorf("could not renew the proof lock: %w", err)
		}
		got, err := run.step(ctx, s)
		rs = append(rs, got...)
		if cerr := run.holes.closeAll(ctx); cerr != nil {
			return rs, cerr
		}
		if err != nil {
			return rs, err
		}
		if ctx.Err() != nil {
			return rs, ctx.Err()
		}
	}
	return rs, nil
}

// ready connects and starts the VM for the proof, and refuses a fence that
// already has a hole: an Allow row left by an earlier proof, or L3 off.
func (r *proofRun) ready(ctx context.Context) error {
	inv, err := r.B.Inventory(ctx, r.p.Names, KnownIDs{VM: r.p.VMID, Switch: r.p.SwitchID}, "")
	if err != nil {
		return err
	}
	if adopt(inv.VMs, r.p.VMID, r.p.Owner) == "" || adopt(inv.Switches, r.p.SwitchID, r.p.Owner) == "" {
		return fmt.Errorf("%w: the recorded VM or switch has no matching owner tag", ErrNotOwned)
	}

	live, err := r.B.ReadLive(ctx, r.p.Names, r.p.VMID, r.p.SwitchID)
	if err != nil {
		return fmt.Errorf("could not read the VM before the proof: %w", err)
	}
	if !live.VMExists {
		return errors.New("the VM is not there")
	}
	if err := requireRecordedAdapter(*r.p, live); err != nil {
		return err
	}
	if err := fenceClosed(live, r.p.FirewallIdentity); err != nil {
		return fmt.Errorf("the fence has a hole before the proof starts: %w", err)
	}
	for _, a := range live.Adapters {
		if a.SwitchID != "" && !strings.EqualFold(a.SwitchID, r.p.SwitchID) {
			return errors.New("the VM is already connected to another switch")
		}
	}
	if !anyConnected(live) {
		if err := r.B.Connect(ctx, r.p.VMID, r.p.SwitchID, r.p.Owner, r.p.AdapterID); err != nil {
			return fmt.Errorf("could not connect the VM for the proof: %w", err)
		}
	}
	if live.VMState != "Running" {
		if err := r.B.Start(ctx, r.p.VMID, r.p.Owner); err != nil {
			return fmt.Errorf("could not start the VM for the proof: %w", err)
		}
	}
	return nil
}

// fenceClosed checks the holes the proof opens: no Allow row, the L3 rule on,
// ms_tcpip6 off.
func fenceClosed(live LiveState, firewallIdentity string) error {
	var why []string
	for _, a := range live.ACLs {
		if strings.EqualFold(a.Action, "Allow") {
			why = append(why, "an Allow row for "+a.Remote.String())
		}
	}
	if len(live.Firewall) == 0 {
		why = append(why, "no L3 rule")
	}
	for _, f := range live.Firewall {
		if firewallIdentity != "" && f.Identity != firewallIdentity {
			why = append(why, "the firewall rule differs from Setup")
		}
		if !f.Enabled {
			why = append(why, "the L3 rule is off")
		}
	}
	if on, ok := live.Bindings["ms_tcpip6"]; !ok || on {
		why = append(why, "ms_tcpip6 is bound")
	}
	if len(why) > 0 {
		return errors.New(strings.Join(why, "; "))
	}
	return nil
}

// checkRestored reads the fence after the proof and wants it closed.
func (r *proofRun) checkRestored(ctx context.Context) error {
	live, err := r.B.ReadLive(ctx, r.p.Names, r.p.VMID, r.p.SwitchID)
	if err != nil {
		return fmt.Errorf("could not read the fence after the proof: %w", err)
	}
	if err := requireRecordedAdapter(*r.p, live); err != nil {
		return err
	}
	if err := fenceClosed(live, r.p.FirewallIdentity); err != nil {
		return fmt.Errorf("the fence still has a hole after the proof: %w", err)
	}
	return nil
}

// openAllow opens an L2 hole. Hyper-V rejects Allow and Deny at the same
// prefix. Temporarily remove only the exact Deny identities Setup recorded.
func (r *proofRun) openAllow(ctx context.Context, a netip.Addr) error {
	q := netip.PrefixFrom(a.WithZone(""), a.BitLen())
	live, err := r.B.ReadLive(ctx, r.p.Names, r.p.VMID, r.p.SwitchID)
	if err != nil {
		return err
	}
	if live.PortACLs == nil {
		return errors.New("could not verify the adapter ACL identities")
	}
	var deny []PortACL
	for _, row := range live.PortACLs {
		if row.prefix() != q || row.Action != 2 {
			continue
		}
		if !slices.Contains(r.p.PortACLs, row) {
			return fmt.Errorf("an unrecorded or changed Deny for %s was left in place", q)
		}
		deny = append(deny, row)
	}
	if len(deny) > 0 {
		if err := r.B.RemovePortACLs(ctx, *r.p, deny); err != nil {
			return err
		}
		r.holes.push("Deny "+q.String()+" removed", func(ctx context.Context) error {
			rows, err := r.B.AddPortACLs(ctx, *r.p, "Deny", []netip.Prefix{q})
			r.p.PortACLs = slices.DeleteFunc(r.p.PortACLs, func(a PortACL) bool { return slices.Contains(deny, a) })
			r.p.PortACLs = append(r.p.PortACLs, rows...)
			if r.save != nil {
				err = errors.Join(err, r.save())
			}
			return err
		})
	}
	rows, err := r.B.AddPortACLs(ctx, *r.p, "Allow", []netip.Prefix{q})
	if len(rows) > 0 {
		r.holes.push("Allow "+q.String(), func(ctx context.Context) error {
			return r.B.RemovePortACLs(ctx, *r.p, rows)
		})
	}
	return err
}

func (r *proofRun) l3Off(ctx context.Context) error {
	if err := r.P.SetL3(ctx, r.p.Names, r.p.Owner, false); err != nil {
		return err
	}
	r.holes.push("L3 off", func(ctx context.Context) error { return r.P.SetL3(ctx, r.p.Names, r.p.Owner, true) })
	return nil
}

// probeSet is the canaries, the capture and the counter around one step.
type probeSet struct {
	r        *proofRun
	tok      string // the step's token: a canary arrival without it is stray
	canaries []Canary
	ext      []Canary // canaries the step keeps open past collect
	extFrom  []int    // arrivals each one had when the step started
	capture  Capture
	before   *DropSample
	errs     []string
}

// alsoWatch adds a canary the step opened itself; collect reads its new
// arrivals and leaves it open.
func (ps *probeSet) alsoWatch(c Canary) *probeSet {
	ps.ext = append(ps.ext, c)
	ps.extFrom = append(ps.extFrom, len(c.Arrived()))
	return ps
}

func (r *proofRun) watch(ctx context.Context, tok string, canaries []netip.AddrPort, filters []PktFilter) *probeSet {
	ps := &probeSet{r: r, tok: tok}
	for _, c := range canaries {
		cn, err := r.P.Canary(ctx, c.Addr(), c.Port(), netip.PrefixFrom(r.p.GuestIP, 32))
		if err != nil {
			ps.errs = append(ps.errs, "canary: "+err.Error())
			continue
		}
		ps.canaries = append(ps.canaries, cn)
	}
	if len(filters) > 0 {
		capt, err := r.P.Pktmon(ctx, filters)
		if err != nil {
			ps.errs = append(ps.errs, "pktmon: "+err.Error())
		} else {
			ps.capture = capt
		}
	}
	if s, err := r.B.DropCounters(ctx, r.p.VMID, r.p.AdapterID); err == nil {
		ps.before = &s
	}
	return ps
}

// collect waits, stops the capture, reads the counter and closes the canaries.
func (ps *probeSet) collect(ctx context.Context, e *Evidence) {
	if err := ps.r.sleep(ctx, ps.r.wait()); err != nil {
		ps.errs = append(ps.errs, err.Error())
	}
	if ps.capture != nil {
		recs, err := ps.capture.Stop(context.WithoutCancel(ctx))
		if err != nil {
			ps.errs = append(ps.errs, "pktmon: "+err.Error())
		}
		for _, rec := range recs {
			if rec.Dropped {
				continue
			}
			switch rec.Class {
			case "vmport", "vnic":
				e.Delivered = true
			default:
				e.Left = append(e.Left, rec)
			}
		}
	}
	if ps.before != nil {
		if s, err := ps.r.B.DropCounters(ctx, ps.r.p.VMID, ps.r.p.AdapterID); err == nil {
			var t DropTracker
			t.Add(*ps.before)
			e.DropDelta = t.Add(s)
		}
	}
	split := func(got []string) {
		for _, a := range got {
			if a == ps.tok {
				e.Arrived = append(e.Arrived, a)
			} else {
				e.Stray = append(e.Stray, a)
			}
		}
	}
	for _, c := range ps.canaries {
		split(c.Arrived())
		c.Close()
	}
	for i, c := range ps.ext {
		if got := c.Arrived(); len(got) > ps.extFrom[i] {
			split(got[ps.extFrom[i]:])
		}
	}
	if len(ps.errs) > 0 && e.Err == "" {
		e.Err = strings.Join(ps.errs, "; ")
	}
}

func (r *proofRun) tcp(ctx context.Context, to netip.AddrPort, tok string, e *Evidence) {
	sent, err := r.G.TCP(ctx, to, tok)
	switch {
	case err != nil:
		e.Err = joinErr(e.Err, "guest TCP probe to "+to.String()+": "+err.Error())
	case !sent:
		e.Err = joinErr(e.Err, "the guest did not send the TCP probe to "+to.String())
	}
}

func joinErr(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// step runs one step and returns its result, or two for the IPv6 pair.
func (r *proofRun) step(ctx context.Context, s StepSpec) ([]StepResult, error) {
	r.logf("proof: %s", s.ID)
	var e Evidence
	fail := func(err error) []StepResult {
		e.Err = joinErr(e.Err, err.Error())
		return []StepResult{Judge(s, e)}
	}
	tok := r.token()
	switch s.ID {
	case StepRed1LAN, StepRed2Router, StepRed3OwnIP, StepTunnelRed:
		target := map[StepID]netip.Addr{StepRed1LAN: r.t.LANIP, StepRed2Router: r.t.Router, StepRed3OwnIP: r.t.OwnIP}[s.ID]
		if s.ID == StepTunnelRed {
			target, _, _ = tunnelTarget(r.h, r.p.SwitchID)
		}
		if err := r.openAllow(ctx, target); err != nil {
			return fail(err), nil
		}
		to := netip.AddrPortFrom(target, r.port())
		var canaries []netip.AddrPort
		if s.ID == StepRed1LAN {
			canaries = append(canaries, to)
		}
		ps := r.watch(ctx, tok, canaries, []PktFilter{{Addr: target, Proto: "TCP", Port: to.Port()}})
		r.tcp(ctx, to, tok, &e)
		ps.collect(ctx, &e)
	case StepL3Alone, StepL2Alone, StepRed4Gateway:
		if s.ID != StepL2Alone {
			if err := r.openAllow(ctx, r.p.Gateway); err != nil {
				return fail(err), nil
			}
		}
		if s.ID != StepL3Alone {
			if err := r.l3Off(ctx); err != nil {
				return fail(err), nil
			}
		}
		to := netip.AddrPortFrom(r.p.Gateway, r.port())
		ps := r.watch(ctx, tok, []netip.AddrPort{to}, []PktFilter{{Addr: r.p.Gateway, Proto: "TCP", Port: to.Port()}})
		r.tcp(ctx, to, tok, &e)
		ps.collect(ctx, &e)
	case StepIPv6Red:
		return r.ipv6(ctx, s, tok)
	case StepTailnetRed:
		if err := r.openAllow(ctx, magicDNS); err != nil {
			return fail(err), nil
		}
		ps := r.watch(ctx, tok, nil, []PktFilter{{Addr: magicDNS, Proto: "UDP", Port: 53}})
		name := r.t.MagicDNSName
		if name == "" {
			name = "example.com"
		}
		// A timeout is what green looks like; red reads pktmon, and the
		// answer is kept for the table.
		if ans, _ := r.G.DNS(ctx, magicDNS, name); len(ans) > 0 {
			e.Answer = ans[0].String()
		}
		ps.collect(ctx, &e)
	case StepVMBusRed:
		return r.vmbus(ctx, s, tok)
	case StepBindings:
		live, err := r.B.ReadLive(ctx, r.p.Names, r.p.VMID, r.p.SwitchID)
		if err != nil {
			return fail(err), nil
		}
		var bad []string
		for _, c := range r.p.BindingsOff {
			if on, ok := live.Bindings[c]; !ok || on {
				bad = append(bad, c+" is bound or unread")
			}
		}
		if !slices.Contains(live.ProtocolsOn, "ms_tcpip") {
			bad = append(bad, "the protocol list is unread")
		}
		for _, c := range live.ProtocolsOn {
			if c != "ms_tcpip" {
				bad = append(bad, "protocol "+c+" is bound")
			}
		}
		if len(bad) > 0 {
			e.Err = strings.Join(bad, "; ")
		}
	case StepGreen:
		live, err := r.B.ReadLive(ctx, r.p.Names, r.p.VMID, r.p.SwitchID)
		if err == nil {
			err = fenceClosed(live, r.p.FirewallIdentity)
		}
		if err != nil {
			return fail(fmt.Errorf("the fence is not back as planned: %w", err)), nil
		}
		var targets []netip.AddrPort
		var canaries []netip.AddrPort
		for _, a := range []netip.Addr{r.t.LANIP, r.p.Gateway} {
			if a.IsValid() {
				to := netip.AddrPortFrom(a, r.port())
				targets = append(targets, to)
				canaries = append(canaries, to)
			}
		}
		for _, a := range []netip.Addr{r.t.Router, r.t.OwnIP, tailnetProbe} {
			if a.IsValid() {
				targets = append(targets, netip.AddrPortFrom(a, r.port()))
			}
		}
		if a, _, ok := tunnelTarget(r.h, r.p.SwitchID); ok {
			targets = append(targets, netip.AddrPortFrom(a, r.port()))
		}
		filters := []PktFilter{{Addr: magicDNS, Proto: "UDP", Port: 53}}
		for _, to := range targets {
			filters = append(filters, PktFilter{Addr: to.Addr(), Proto: "TCP", Port: to.Port()})
		}
		ps := r.watch(ctx, tok, canaries, filters)
		for _, to := range targets {
			r.tcp(ctx, to, tok, &e)
		}
		name := r.t.MagicDNSName
		if name == "" {
			name = "example.com"
		}
		if ans, err := r.G.DNS(ctx, magicDNS, name); err == nil && len(ans) > 0 {
			e.Arrived = append(e.Arrived, "dns:"+ans[0].String())
		}
		ps.collect(ctx, &e)
	case StepFunction:
		ip, err := r.G.Egress(ctx)
		switch {
		case err != nil:
			e.Err = "the guest could not reach the internet: " + err.Error()
		case ip != r.t.OwnIP:
			e.Answer = ip.String()
			e.Err = fmt.Sprintf("the guest's public address is %v, this PC's is %v", ip, r.t.OwnIP)
		default:
			e.Answer = ip.String()
		}
	default:
		return fail(fmt.Errorf("no runner for step %s", s.ID)), nil
	}
	return []StepResult{Judge(s, e)}, nil
}

// ipv6 runs the IPv6 red step, then the stack-off green step on the same
// holes: ms_tcpip6 bound, the host's link-local address recorded before it is
// unbound again, an Allow for that address, L3 off.
func (r *proofRun) ipv6(ctx context.Context, red StepSpec, tok string) ([]StepResult, error) {
	var off StepSpec
	for _, s := range Steps(*r.p, r.h, r.t) {
		if s.ID == StepIPv6StackOff {
			off = s
		}
	}
	var e, e2 Evidence
	both := func(err error) []StepResult {
		e.Err = joinErr(e.Err, err.Error())
		e2.Err = joinErr(e2.Err, "the red step before it could not run: "+err.Error())
		return []StepResult{Judge(red, e), Judge(off, e2)}
	}
	ifx := r.t.VNICIfIndex
	if err := r.P.SetBinding(ctx, ifx, "ms_tcpip6", true); err != nil {
		return both(err), nil
	}
	r.holes.push("ms_tcpip6 bound", func(ctx context.Context) error { return r.P.SetBinding(ctx, ifx, "ms_tcpip6", false) })
	ll, err := r.P.LinkLocal(ctx, ifx)
	if err != nil {
		return both(fmt.Errorf("could not read the host's link-local address: %w", err)), nil
	}
	if !ll.Is6() || !ll.IsLinkLocalUnicast() {
		return both(fmt.Errorf("%v is not an IPv6 link-local address", ll)), nil
	}
	ll = ll.WithZone("")
	if err := r.openAllow(ctx, ll); err != nil {
		return both(err), nil
	}
	if err := r.l3Off(ctx); err != nil {
		return both(err), nil
	}
	to := netip.AddrPortFrom(ll, r.port())
	// One canary for both steps, opened while the address exists.
	// The guest's link-local address is its own; any fe80:: source reaches
	// this vNIC only from our switch.
	cn, err := r.P.Canary(ctx, ll.WithZone(strconv.Itoa(ifx)), to.Port(), netip.MustParsePrefix("fe80::/10"))
	if err != nil {
		return both(fmt.Errorf("canary: %w", err)), nil
	}
	r.holes.push("IPv6 canary", func(context.Context) error { cn.Close(); return nil })
	ps := r.watch(ctx, tok, nil, []PktFilter{{Addr: ll, Proto: "TCP", Port: to.Port()}}).alsoWatch(cn)
	r.tcp(ctx, to, tok, &e)
	ps.collect(ctx, &e)
	results := []StepResult{Judge(red, e)}

	// Stack off alone: the Allow stays and L3 stays off. The host no longer
	// answers neighbour solicitations, so the guest gets a static entry for
	// the recorded address first, or the probe would never leave it.
	if err := r.P.SetBinding(ctx, ifx, "ms_tcpip6", false); err != nil {
		e2.Err = err.Error()
		return append(results, Judge(off, e2)), nil
	}
	if err := r.G.Neighbor(ctx, ll, r.p.HostVNICMAC); err != nil {
		e2.Err = "the guest could not add its neighbour entry: " + err.Error()
		return append(results, Judge(off, e2)), nil
	}
	tok2 := r.token()
	ps2 := r.watch(ctx, tok2, nil, []PktFilter{{Addr: ll}}).alsoWatch(cn)
	r.tcp(ctx, to, tok2, &e2)
	ps2.collect(ctx, &e2)
	return append(results, Judge(off, e2)), nil
}

// hvWildcard is HV_GUID_WILDCARD: a listener bound to it takes every VM.
const hvWildcard = "00000000-0000-0000-0000-000000000000"

// vmbus runs the hv_sock red control, then the green step: every service in
// the setup baseline, and the test service once its registration is gone
// while its listener stays bound, must refuse the guest.
func (r *proofRun) vmbus(ctx context.Context, red StepSpec, tok string) ([]StepResult, error) {
	var green StepSpec
	for _, s := range Steps(*r.p, r.h, r.t) {
		if s.ID == StepVMBusGreen {
			green = s
		}
	}
	var e, e2 Evidence
	svc := r.guid()
	l, err := r.P.HVListener(ctx, hvWildcard, svc, true)
	if err != nil {
		e.Err = "could not register the test listener: " + err.Error()
		e2.Err = "the red control could not run"
		return []StepResult{Judge(red, e), Judge(green, e2)}, nil
	}
	closed := false
	closeL := func(context.Context) error {
		if closed {
			return nil
		}
		if err := l.Close(); err != nil {
			return err
		}
		closed = true
		return nil
	}
	r.holes.push("hv_sock test listener", closeL)
	// A new registration takes a moment to apply: try for up to 10 seconds.
	for try := 0; try < 5; try++ {
		ok, err := r.G.HVSock(ctx, svc, tok)
		if err != nil {
			e.Err = "guest hv_sock probe: " + err.Error()
			break
		}
		if ok {
			e.Err = ""
			break
		}
		if err := r.sleep(ctx, 2*time.Second); err != nil {
			e.Err = err.Error()
			break
		}
	}
	for _, got := range l.Received() {
		if got == tok {
			e.Arrived = append(e.Arrived, got)
		}
	}
	results := []StepResult{Judge(red, e)}
	if err := l.Unregister(); err != nil {
		e2.Err = "could not delete the test service's registration: " + err.Error()
		return append(results, Judge(green, e2)), nil
	}
	if err := r.sleep(ctx, r.wait()); err != nil {
		e2.Err = err.Error()
	}
	// The host's own listener is the witness, not the guest's word: anything
	// it takes after the registration is gone is an arrival, whatever the
	// guest reports.
	seen := len(l.Received())
	keys := append(slices.Clone(r.p.GCSBaseline), svc)
	for _, k := range keys {
		ok, err := r.G.HVSock(ctx, k, r.token())
		switch {
		case err != nil:
			e2.Err = joinErr(e2.Err, "guest hv_sock probe to "+k+": "+err.Error())
		case ok:
			e2.Arrived = append(e2.Arrived, "hvsock:"+k)
		}
	}
	if err := closeL(ctx); err != nil {
		e2.Err = joinErr(e2.Err, "could not remove the test listener: "+err.Error())
	}
	if got := l.Received(); len(got) > seen {
		for _, tok := range got[seen:] {
			e2.Arrived = append(e2.Arrived, "hvsock listener:"+tok)
		}
	}
	return append(results, Judge(green, e2)), nil
}

// ProofFile is where setup keeps the last proof's results, for the app.
const ProofFile = "proof.json"

// proofDoc is proof.json.
type proofDoc struct {
	Version int          `json:"version"`
	At      time.Time    `json:"at"`
	Green   bool         `json:"green"`
	Reason  string       `json:"reason"`
	Steps   []StepResult `json:"steps"`
}

// WriteProof saves the results for the app: SYSTEM and Administrators write,
// Users read.
func WriteProof(dir string, at time.Time, rs []StepResult, proofErr error) error {
	green, why := Verdict(rs)
	if proofErr != nil {
		green, why = false, proofErr.Error()
	}
	b, err := json.MarshalIndent(proofDoc{Version: 1, At: at, Green: green, Reason: why, Steps: rs}, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, ProofFile), append(b, '\n'), usersRead)
}

// markProven sets ProvedAt, and only when the results are green. It is the one
// place a plan becomes proven.
func (p *Plan) markProven(rs []StepResult, at time.Time) error {
	if green, why := Verdict(rs); !green {
		return fmt.Errorf("%w: %s", ErrProofFailed, why)
	}
	p.ProvedAt = at.UTC().Truncate(time.Second)
	return nil
}
