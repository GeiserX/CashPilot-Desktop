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
	if err := f.AddDeny(context.Background(), p.VMID, []netip.Prefix{netip.PrefixFrom(f.PublicIP, 32)}); err != nil {
		t.Fatal(err)
	}
	h, err := f.ReadHostNet(context.Background(), winvm.EgressSamples)
	if err != nil {
		t.Fatal(err)
	}
	live, err := f.ReadLive(context.Background(), p.Names, p.VMID)
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
		"LinkLocal", "Canary", "Pktmon", "DropCounters", "HVListener", "TCP", "Neighbor", "HVSock", "DNS", "Egress"}
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
