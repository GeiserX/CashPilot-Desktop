package winvm_test

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

func spec(id winvm.StepID, k winvm.Kind, w winvm.Witness) winvm.StepSpec {
	return winvm.StepSpec{ID: id, Kind: k, Applies: true, Witness: w, Opens: []string{}}
}

func TestJudgeOutcomes(t *testing.T) {
	out := winvm.PktRecord{Component: "if7", Class: "physical", Proto: "TCP"}
	odd := winvm.PktRecord{Component: "if9", Class: "", Proto: "TCP"}
	cases := []struct {
		name string
		s    winvm.StepSpec
		e    winvm.Evidence
		want winvm.StepOutcome
	}{
		{"red with an arrival", spec(winvm.StepRed1LAN, winvm.Red, ""), winvm.Evidence{Arrived: []string{"t"}}, winvm.WentRed},
		{"red with a packet out", spec(winvm.StepRed2Router, winvm.Red, ""), winvm.Evidence{Left: []winvm.PktRecord{out}}, winvm.WentRed},
		{"red with nothing", spec(winvm.StepRed2Router, winvm.Red, ""), winvm.Evidence{}, winvm.Broken},
		{"red with only a stray connection", spec(winvm.StepRed1LAN, winvm.Red, ""), winvm.Evidence{Stray: []string{"connected"}}, winvm.Broken},
		{"green with a stray connection", spec(winvm.StepGreen, winvm.Green, winvm.WitnessDrops), winvm.Evidence{Stray: []string{"connected"}, DropDelta: 3}, winvm.Leak},
		{"red with an error", spec(winvm.StepRed2Router, winvm.Red, ""), winvm.Evidence{Err: "no guest"}, winvm.Broken},
		{"red: an unplaced record proves nothing", spec(winvm.StepRed2Router, winvm.Red, ""), winvm.Evidence{Left: []winvm.PktRecord{odd}}, winvm.Broken},
		{"green with an arrival", spec(winvm.StepGreen, winvm.Green, winvm.WitnessDrops), winvm.Evidence{Arrived: []string{"t"}, DropDelta: 3}, winvm.Leak},
		{"green: an unplaced record fails closed", spec(winvm.StepGreen, winvm.Green, winvm.WitnessDrops), winvm.Evidence{Left: []winvm.PktRecord{odd}, DropDelta: 3}, winvm.Leak},
		{"green with no drops", spec(winvm.StepGreen, winvm.Green, winvm.WitnessDrops), winvm.Evidence{}, winvm.Unproven},
		{"green with drops", spec(winvm.StepGreen, winvm.Green, winvm.WitnessDrops), winvm.Evidence{DropDelta: 1}, winvm.StayedGreen},
		{"green with an error", spec(winvm.StepGreen, winvm.Green, winvm.WitnessDrops), winvm.Evidence{DropDelta: 1, Err: "x"}, winvm.Unproven},
		{"stack off, never delivered", spec(winvm.StepIPv6StackOff, winvm.Green, winvm.WitnessDelivered), winvm.Evidence{}, winvm.Unproven},
		{"stack off, delivered", spec(winvm.StepIPv6StackOff, winvm.Green, winvm.WitnessDelivered), winvm.Evidence{Delivered: true}, winvm.StayedGreen},
		{"check failed", spec(winvm.StepBindings, winvm.Check, ""), winvm.Evidence{Err: "ms_server is bound"}, winvm.Broken},
		{"check held", spec(winvm.StepBindings, winvm.Check, ""), winvm.Evidence{}, winvm.Held},
		{"n/a", winvm.StepSpec{ID: winvm.StepTunnelRed, Kind: winvm.Red, Why: "no tunnel"}, winvm.Evidence{Arrived: []string{"t"}}, winvm.NA},
	}
	for _, c := range cases {
		if got := winvm.Judge(c.s, c.e).Outcome; got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// passing is a full set of passing results.
func passing() []winvm.StepResult {
	var rs []winvm.StepResult
	for _, s := range []struct {
		id winvm.StepID
		k  winvm.Kind
		o  winvm.StepOutcome
	}{
		{winvm.StepRed1LAN, winvm.Red, winvm.WentRed}, {winvm.StepRed2Router, winvm.Red, winvm.WentRed},
		{winvm.StepRed3OwnIP, winvm.Red, winvm.WentRed}, {winvm.StepL3Alone, winvm.Green, winvm.StayedGreen},
		{winvm.StepL2Alone, winvm.Green, winvm.StayedGreen}, {winvm.StepRed4Gateway, winvm.Red, winvm.WentRed},
		{winvm.StepIPv6Red, winvm.Red, winvm.WentRed}, {winvm.StepIPv6StackOff, winvm.Green, winvm.StayedGreen},
		{winvm.StepTunnelRed, winvm.Red, winvm.NA}, {winvm.StepTailnetRed, winvm.Red, winvm.NA},
		{winvm.StepVMBusRed, winvm.Red, winvm.WentRed}, {winvm.StepVMBusGreen, winvm.Green, winvm.StayedGreen},
		{winvm.StepBindings, winvm.Check, winvm.Held}, {winvm.StepGreen, winvm.Green, winvm.StayedGreen},
		{winvm.StepFunction, winvm.Function, winvm.Held},
	} {
		rs = append(rs, winvm.StepResult{ID: s.id, Kind: s.k, Outcome: s.o})
	}
	return rs
}

func TestVerdict(t *testing.T) {
	if ok, why := winvm.Verdict(passing()); !ok {
		t.Fatalf("a passing set is not green: %s", why)
	}
	if ok, _ := winvm.Verdict(nil); ok {
		t.Fatal("no results is green")
	}
	// n/a is allowed only for the optional steps; a mandatory step as n/a, or
	// missing, is never green.
	for i, r := range passing() {
		if r.Outcome == winvm.NA {
			continue
		}
		rs := passing()
		rs[i].Outcome = winvm.NA
		if ok, _ := winvm.Verdict(rs); ok {
			t.Errorf("%s as n/a turned the verdict green", r.ID)
		}
		rs = slices.Delete(passing(), i, i+1)
		if ok, _ := winvm.Verdict(rs); ok {
			t.Errorf("%s missing turned the verdict green", r.ID)
		}
		rs = passing()
		rs[i].Outcome = map[winvm.Kind]winvm.StepOutcome{winvm.Red: winvm.Broken, winvm.Green: winvm.Unproven, winvm.Check: winvm.Broken, winvm.Function: winvm.Broken}[r.Kind]
		if ok, _ := winvm.Verdict(rs); ok {
			t.Errorf("%s failing turned the verdict green", r.ID)
		}
	}
	all := passing()
	for i := range all {
		all[i].Outcome = winvm.NA
	}
	if ok, _ := winvm.Verdict(all); ok {
		t.Fatal("every step n/a is green")
	}
}

// proofRig is a fenced VM on the fake with an unproven plan.
func proofRig(t *testing.T) (*hvfake.HyperV, winvm.Plan, winvm.Runner, winvm.HostNet, winvm.ProofTargets) {
	t.Helper()
	f := hvfake.New()
	p, err := f.BuildFenced(true)
	if err != nil {
		t.Fatal(err)
	}
	// The guard places the own-IP Deny at its first pass; the fake build
	// leaves it out.
	rows, err := f.AddPortACLs(context.Background(), p, "Deny", []netip.Prefix{netip.PrefixFrom(f.PublicIP, 32)})
	if err != nil {
		t.Fatal(err)
	}
	p.PortACLs = append(p.PortACLs, rows...)
	h, err := f.ReadHostNet(context.Background(), winvm.EgressSamples)
	if err != nil {
		t.Fatal(err)
	}
	live, err := f.ReadLive(context.Background(), p.Names, p.VMID, p.SwitchID)
	if err != nil {
		t.Fatal(err)
	}
	r := winvm.Runner{B: f, P: f, G: f, Clock: func() time.Time { return buildNow },
		Sleep: func(ctx context.Context, d time.Duration) error { return ctx.Err() },
		Dir:   t.TempDir(), Logf: t.Logf, Wait: time.Millisecond}
	return f, p, r, h, winvm.Targets(h, p.SwitchID, []netip.Addr{f.PublicIP}, live.VNICIfIndex)
}

// fence is the part of the fake host the proof opens and must put back.
func fence(f *hvfake.HyperV) []string {
	var out []string
	for _, o := range f.Objects() {
		if strings.HasPrefix(o, "  acl ") || strings.HasPrefix(o, "rule ") || strings.HasPrefix(o, "  binding ") {
			out = append(out, o)
		}
	}
	return out
}

func TestProofPassesOnAGoodFence(t *testing.T) {
	f, p, r, h, tg := proofRig(t)
	before := fence(f)
	rs, err := r.Run(context.Background(), p, h, tg)
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := winvm.Verdict(rs); !ok {
		t.Fatalf("not green: %s", why)
	}
	if got := fence(f); !slices.Equal(got, before) {
		t.Fatalf("the fence is not back:\n%s\n---\n%s", strings.Join(before, "\n"), strings.Join(got, "\n"))
	}
	if _, err := os.Stat(filepath.Join(r.Dir, winvm.LockFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the proof lock is still there: %v", err)
	}
}

// TestProofMutantLeaks is the positive control: a fence that ignores its Deny
// rows while still listing them must fail the proof as a leak on green.
func TestProofMutantLeaks(t *testing.T) {
	f, p, r, h, tg := proofRig(t)
	f.SetMutant(true)
	rs, _ := r.Run(context.Background(), p, h, tg)
	if ok, _ := winvm.Verdict(rs); ok {
		t.Fatal("the mutant fence passed the proof")
	}
	i := slices.IndexFunc(rs, func(r winvm.StepResult) bool { return r.ID == winvm.StepGreen })
	if i < 0 || rs[i].Outcome != winvm.Leak {
		t.Fatalf("green step = %+v, want leak", rs)
	}
}

// TestProofRestoresAfterEachFailure injects a failure into each call the proof
// makes and checks that every hole is closed and the lock released, whatever
// failed, and that the verdict is not green when a needed call failed.
func TestProofRestoresAfterEachFailure(t *testing.T) {
	methods := []string{"ReadLive", "AddAllow", "RemoveAllow", "RemoveDeny", "AddDeny", "SetL3", "SetBinding",
		"LinkLocal", "Canary", "Pktmon", "DropCounters", "HVListener", "Unregister", "TCP", "Neighbor", "HVSock", "DNS", "Egress"}
	for _, m := range methods {
		for _, nth := range []int{1, 2} {
			t.Run(m+"#"+string(rune('0'+nth)), func(t *testing.T) {
				f, p, r, h, tg := proofRig(t)
				before := fence(f)
				pending := f.FailNth(m, nth, nil)
				rs, err := r.Run(context.Background(), p, h, tg)
				if pending() {
					t.Skipf("%s was called fewer than %d times", m, nth)
				}
				// These have a second witness, or fail on a step that does not
				// need them (the first drop reading is a red step's).
				mayPass := map[string]bool{"DNS": true, "Canary": true, "Pktmon": true, "DropCounters": true}
				if ok, _ := winvm.Verdict(rs); ok && err == nil && !mayPass[m] {
					t.Errorf("the proof passed with %s failing", m)
				}
				if got := fence(f); !slices.Equal(got, before) {
					t.Fatalf("the fence is not back after %s failed (%v):\n%s\n---\n%s", m, err, strings.Join(before, "\n"), strings.Join(got, "\n"))
				}
				if _, err := os.Stat(filepath.Join(r.Dir, winvm.LockFile)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("the proof lock is still there: %v", err)
				}
			})
		}
	}
}

// lockSpy records when the proof lock was taken each time a step starts a
// capture.
type lockSpy struct {
	*hvfake.HyperV
	dir   string
	taken []time.Time
}

func (s *lockSpy) Pktmon(ctx context.Context, fl []winvm.PktFilter) (winvm.Capture, error) {
	if at, err := winvm.ReadProofLock(s.dir); err == nil {
		s.taken = append(s.taken, at)
	}
	return s.HyperV.Pktmon(ctx, fl)
}

// TestProofRenewsItsLock: on a slow host a proof takes minutes, so the lock is
// taken again before each step and never ages past ProofLockTTL while the
// proof runs.
func TestProofRenewsItsLock(t *testing.T) {
	f, p, r, h, tg := proofRig(t)
	now := buildNow
	r.Clock = func() time.Time { now = now.Add(2 * time.Minute); return now }
	spy := &lockSpy{HyperV: f, dir: r.Dir}
	r.P = spy
	if _, err := r.Run(context.Background(), p, h, tg); err != nil {
		t.Fatal(err)
	}
	if len(spy.taken) < 2 {
		t.Fatalf("saw the lock %d times", len(spy.taken))
	}
	first, last := spy.taken[0], spy.taken[len(spy.taken)-1]
	if !last.After(first) {
		t.Fatalf("the lock was never renewed: first %v, last %v", first, last)
	}
	if !winvm.ProofLockValid(last, now) {
		t.Fatalf("the lock of the last step (%v) does not hold at %v", last, now)
	}
}

// strayProbe adds a connection from somewhere else on the LAN to every
// canary, as a host that found the open port would.
type strayProbe struct{ winvm.Prober }

type strayCanary struct{ winvm.Canary }

func (c strayCanary) Arrived() []string { return append([]string{"connected"}, c.Canary.Arrived()...) }

func (s strayProbe) Canary(ctx context.Context, ip netip.Addr, port uint16, from netip.Prefix) (winvm.Canary, error) {
	c, err := s.Prober.Canary(ctx, ip, port, from)
	if err != nil {
		return nil, err
	}
	return strayCanary{c}, nil
}

// lostGuest reports every TCP probe sent, but none leaves the guest.
type lostGuest struct{ winvm.GuestProbe }

func (lostGuest) TCP(context.Context, netip.AddrPort, string) (bool, error) { return true, nil }

// TestProofCountsOnlyTheStepsToken: a red step whose own probe never arrived
// is broken, even when another host connected to the canary. A green step
// counts any connection as a leak.
func TestProofCountsOnlyTheStepsToken(t *testing.T) {
	outcome := func(rs []winvm.StepResult, id winvm.StepID) winvm.StepResult {
		i := slices.IndexFunc(rs, func(r winvm.StepResult) bool { return r.ID == id })
		if i < 0 {
			t.Fatalf("no result for %s", id)
		}
		return rs[i]
	}
	t.Run("red", func(t *testing.T) {
		_, p, r, h, tg := proofRig(t)
		r.P, r.G = strayProbe{r.P}, lostGuest{r.G}
		rs, _ := r.Run(context.Background(), p, h, tg)
		for _, id := range []winvm.StepID{winvm.StepRed1LAN, winvm.StepRed4Gateway} {
			if got := outcome(rs, id); got.Outcome != winvm.Broken || len(got.Evidence.Stray) == 0 {
				t.Errorf("%s with only a stray connection = %s (stray %v), want broken", id, got.Outcome, got.Evidence.Stray)
			}
		}
	})
	t.Run("green", func(t *testing.T) {
		_, p, r, h, tg := proofRig(t)
		r.P = strayProbe{r.P}
		rs, _ := r.Run(context.Background(), p, h, tg)
		if got := outcome(rs, winvm.StepGreen); got.Outcome != winvm.Leak {
			t.Errorf("green with a stray connection = %s, want leak", got.Outcome)
		}
		if ok, _ := winvm.Verdict(rs); ok {
			t.Error("the proof passed with a stray connection on a green step")
		}
	})
}

// keptRegistration is a host where deleting the hv_sock registration does
// not take: the key stays until the listener closes.
type keptRegistration struct{ winvm.Prober }

type keptListener struct{ winvm.HVListener }

func (keptListener) Unregister() error { return nil }

func (k keptRegistration) HVListener(ctx context.Context, bindVMID, service string, register bool) (winvm.HVListener, error) {
	l, err := k.Prober.HVListener(ctx, bindVMID, service, register)
	if err != nil {
		return nil, err
	}
	return keptListener{l}, nil
}

// TestProofVMBusGreenKeepsTheListenerBound is the positive control for the
// VMBus green step: it probes the test service while its listener is still
// bound, so a registration that was not deleted shows as a leak, not as a
// refusal from a closed listener.
func TestProofVMBusGreenKeepsTheListenerBound(t *testing.T) {
	_, p, r, h, tg := proofRig(t)
	r.P = keptRegistration{r.P}
	rs, _ := r.Run(context.Background(), p, h, tg)
	i := slices.IndexFunc(rs, func(r winvm.StepResult) bool { return r.ID == winvm.StepVMBusGreen })
	if i < 0 || rs[i].Outcome != winvm.Leak {
		t.Fatalf("VMBus green with the registration kept = %+v, want leak", rs[i])
	}
}

// lyingGuest connects over hv_sock as asked but always reports a refusal, as
// a hostile guest would.
type lyingGuest struct{ winvm.GuestProbe }

func (g lyingGuest) HVSock(ctx context.Context, service, token string) (bool, error) {
	_, err := g.GuestProbe.HVSock(ctx, service, token)
	return false, err
}

// TestProofVMBusGreenReadsTheHostListener: the VMBus green step judges by
// what the still-bound host listener took after the registration was
// deleted, not by the guest's report. A guest that connects and reports a
// refusal leaks.
func TestProofVMBusGreenReadsTheHostListener(t *testing.T) {
	_, p, r, h, tg := proofRig(t)
	r.P, r.G = keptRegistration{r.P}, lyingGuest{r.G}
	rs, _ := r.Run(context.Background(), p, h, tg)
	i := slices.IndexFunc(rs, func(r winvm.StepResult) bool { return r.ID == winvm.StepVMBusGreen })
	if i < 0 || rs[i].Outcome != winvm.Leak {
		t.Fatalf("VMBus green with a guest that hides its connection = %+v, want leak", rs[i])
	}
	if ok, _ := winvm.Verdict(rs); ok {
		t.Fatal("the proof passed with a guest that hides its hv_sock connection")
	}
}

type delayedObservation struct{ winvm.Prober }

type delayedListener struct {
	winvm.HVListener
	baseline []string
	closing  bool
	closed   bool
}

func (l *delayedListener) Unregister() error {
	l.baseline = l.HVListener.Received()
	l.closing = true
	return nil // Keep the registration to simulate the leak.
}
func (l *delayedListener) Received() []string {
	if l.closing && !l.closed {
		return l.baseline
	}
	return l.HVListener.Received()
}
func (l *delayedListener) Close() error {
	l.closed = true
	return l.HVListener.Close()
}
func (p delayedObservation) HVListener(ctx context.Context, vm, service string, register bool) (winvm.HVListener, error) {
	l, err := p.Prober.HVListener(ctx, vm, service, register)
	if err != nil {
		return nil, err
	}
	return &delayedListener{HVListener: l}, nil
}

func TestProofVMBusReadsThroughListenerClose(t *testing.T) {
	_, p, r, h, tg := proofRig(t)
	r.P, r.G = delayedObservation{r.P}, lyingGuest{r.G}
	rs, _ := r.Run(context.Background(), p, h, tg)
	for _, s := range rs {
		if s.ID == winvm.StepVMBusGreen && s.Outcome == winvm.Leak {
			return
		}
	}
	t.Fatal("the proof missed a connection reported when the listener closed")
}
