package winvm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// Every guard test runs against hvfake, the stateful fake of the whole
// Hyper-V seam. Addresses come from the documentation ranges.

var provedAt = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

// rig is one fenced VM on the fake, booted once by the guard.
type rig struct {
	t   *testing.T
	f   *hvfake.HyperV
	p   winvm.Plan
	d   winvm.GuardDeps
	dir string

	mu  sync.Mutex
	own []netip.Addr
	now time.Time
	log []string
}

func (r *rig) setNow(t time.Time) { r.mu.Lock(); r.now = t; r.mu.Unlock() }
func (r *rig) setOwn(a ...netip.Addr) {
	r.mu.Lock()
	r.own = a
	r.mu.Unlock()
}

func (r *rig) logged(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.ContainsFunc(r.log, func(l string) bool { return strings.Contains(l, sub) })
}

// newRig builds the product VM on the fake the way setup will, marks its plan
// proven, approves the fake's network, and runs the guard's Boot once, which
// must connect.
func newRig(t *testing.T) *rig {
	t.Helper()
	return newRigWith(t, func(*winvm.Plan) {}, true)
}

func newRigWith(t *testing.T, edit func(*winvm.Plan), wantConnected bool) *rig {
	t.Helper()
	f := hvfake.New()
	p, err := f.BuildFenced(true)
	if err != nil {
		t.Fatal(err)
	}
	p.ProvedAt = provedAt
	edit(&p)
	r := &rig{t: t, f: f, p: p, dir: t.TempDir(), now: provedAt.Add(time.Hour), own: []netip.Addr{f.PublicIP}}
	r.writePlan()
	r.writePrefs(`{"keepOff":false,"approvedNetworks":[{"gatewayMac":"00-00-5E-00-53-01","profileId":"{6A5D2C1E-0B7F-4C3A-9E21-5F8D7C6B4A30}"}],"uploadLimitMbps":0}`)
	r.d = winvm.GuardDeps{
		HV: f,
		Echo: func(context.Context) []netip.Addr {
			r.mu.Lock()
			defer r.mu.Unlock()
			return slices.Clone(r.own)
		},
		Now: func() time.Time {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.now
		},
		Dir: r.dir,
		Logf: func(format string, args ...any) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.log = append(r.log, fmt.Sprintf(format, args...))
		},
	}
	st := r.d.Boot(context.Background())
	if wantConnected && (st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld) {
		t.Fatalf("the first Boot on a matching host did not connect: %+v", st)
	}
	return r
}

func (r *rig) writePlan() {
	r.t.Helper()
	b, err := r.p.Marshal()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, winvm.PlanFile), b, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) writePrefs(s string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, winvm.PrefsFile), []byte(s), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// mark is where the call log stands now; since lists the calls after it.
func (r *rig) mark() int { return len(r.f.Calls()) }
func (r *rig) since(m int) []string {
	return r.f.Calls()[m:]
}

func (r *rig) vnic() int {
	r.t.Helper()
	l, err := r.f.ReadLive(context.Background(), r.p.Names, r.p.VMID, r.p.SwitchID)
	if err != nil {
		r.t.Fatal(err)
	}
	return l.VNICIfIndex
}

// acls lists the Deny prefixes on the VM's adapter now.
func (r *rig) acls() []netip.Prefix {
	r.t.Helper()
	l, err := r.f.ReadLive(context.Background(), r.p.Names, r.p.VMID, r.p.SwitchID)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []netip.Prefix
	for _, a := range l.ACLs {
		if a.Action == "Deny" && a.Direction == "Outbound" {
			out = append(out, a.Remote)
		}
	}
	return out
}

// connectedNow reads whether the adapter is on a switch, from the fake.
func (r *rig) connectedNow() bool {
	r.t.Helper()
	l, err := r.f.ReadLive(context.Background(), r.p.Names, r.p.VMID, r.p.SwitchID)
	if err != nil {
		r.t.Fatal(err)
	}
	return slices.ContainsFunc(l.Adapters, func(a winvm.LiveAdapter) bool { return a.SwitchID != "" })
}

// fenceViolation is a probe that got through the fence, as against a probe
// that could not be set up.
type fenceViolation struct{ msg string }

func (e *fenceViolation) Error() string { return e.msg }

// probeFence is a check that depends on the fence itself, not on its config:
// the guest probes the host's LAN address, the LAN router, the gateway, the
// tailnet and the line's own public address. It wants no arrival, nothing
// leaving the host, and one drop counted per probe. The mutant fake fails it.
// A probe that got through is a *fenceViolation; any other error is a probe
// that could not run.
func (r *rig) probeFence() error {
	ctx := context.Background()
	f, p := r.f, r.p
	targets := []netip.AddrPort{
		netip.AddrPortFrom(f.LANIP, 40001),
		netip.AddrPortFrom(f.Router, 40002),
		netip.AddrPortFrom(p.Gateway, 40003),
		netip.AddrPortFrom(netip.MustParseAddr("100.64.0.9"), 40004),
		netip.AddrPortFrom(f.PublicIP, 40005),
	}
	var canaries []winvm.Canary
	var filters []winvm.PktFilter
	for _, to := range targets {
		c, err := f.Canary(ctx, to.Addr(), to.Port(), netip.MustParsePrefix("192.0.2.11/32"))
		if err != nil {
			return err
		}
		defer c.Close()
		canaries = append(canaries, c)
		filters = append(filters, winvm.PktFilter{Addr: to.Addr(), Proto: "TCP", Port: to.Port()})
	}
	before, err := f.DropCounters(ctx, p.VMID, p.AdapterID)
	if err != nil {
		return err
	}
	capt, err := f.Pktmon(ctx, filters)
	if err != nil {
		return err
	}
	for _, to := range targets {
		if sent, err := f.TCP(ctx, to, "tok"); err != nil || !sent {
			capt.Stop(ctx)
			return fmt.Errorf("the probe to %v was not sent: %v", to, err)
		}
	}
	recs, err := capt.Stop(ctx)
	if err != nil {
		return err
	}
	after, err := f.DropCounters(ctx, p.VMID, p.AdapterID)
	if err != nil {
		return err
	}
	for i, c := range canaries {
		if len(c.Arrived()) > 0 {
			return &fenceViolation{fmt.Sprintf("a probe arrived at %v", targets[i])}
		}
	}
	for _, rec := range recs {
		if rec.Class == "physical" || rec.Class == "tunnel" {
			return &fenceViolation{fmt.Sprintf("a probe left the host through %s to %v", rec.Class, rec.Dst)}
		}
	}
	if got := after.Out - before.Out; got != uint64(len(targets)) {
		return &fenceViolation{fmt.Sprintf("the drop counter rose by %d, want %d", got, len(targets))}
	}
	return nil
}

func hasChange(st winvm.Status, field string) bool {
	return slices.ContainsFunc(st.Changes, func(c winvm.Change) bool { return c.Field == field })
}

// expectCut checks one Pass's outcome after a change to the fence: the VM is
// off the network, the status names the field, and nothing reconnected.
func expectCut(r *rig, st winvm.Status, calls []string, field string) error {
	switch {
	case st.Network != winvm.NetDisconnected:
		return fmt.Errorf("network %q, want disconnected (%+v)", st.Network, st)
	case st.Fence != winvm.FenceCut:
		return fmt.Errorf("fence %q, want cut", st.Fence)
	case !hasChange(st, field):
		return fmt.Errorf("the changes %+v do not name %s", st.Changes, field)
	case !slices.Contains(calls, "Disconnect"):
		return fmt.Errorf("Pass did not call Disconnect: %v", calls)
	case slices.Contains(calls, "Connect") || slices.Contains(calls, "Start"):
		return fmt.Errorf("Pass connected or started the VM: %v", calls)
	case r.connectedNow():
		return errors.New("the adapter is still on a switch")
	case !r.logged("MISMATCH " + field):
		return fmt.Errorf("the log has no MISMATCH %s line", field)
	}
	return nil
}

// deleteOneACL is the plan's first guard case, and the positive control for the
// rest: the fence holds, one static Deny is deleted, one Pass cuts the VM off
// and a second Pass does not reconnect it, the Deny is restored and Boot
// connects it again, and the fence holds again. It returns the first thing that
// went wrong.
func deleteOneACL(t *testing.T, mutant bool) error {
	ctx := context.Background()
	r := newRig(t)
	r.f.SetMutant(mutant)
	if err := r.probeFence(); err != nil {
		return fmt.Errorf("before: %w", err)
	}
	gone := netip.MustParsePrefix("192.168.0.0/16")
	if err := r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{gone}); err != nil {
		t.Fatal(err)
	}
	m := r.mark()
	st := r.d.Pass(ctx)
	if err := expectCut(r, st, r.since(m), "acl"); err != nil {
		return err
	}
	if slices.Contains(r.since(m), "AddDeny") {
		return errors.New("Pass put a static Deny back by itself")
	}
	if !strings.Contains(st.Reason, "protection changed (acl)") {
		return fmt.Errorf("reason %q", st.Reason)
	}
	m = r.mark()
	if st := r.d.Pass(ctx); st.Network != winvm.NetDisconnected || slices.Contains(r.since(m), "Connect") {
		return fmt.Errorf("a second Pass reconnected: %+v %v", st, r.since(m))
	}
	if err := r.f.AddDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{gone}); err != nil {
		t.Fatal(err)
	}
	if st := r.d.Boot(ctx); st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld {
		return fmt.Errorf("Boot did not connect after the Deny came back: %+v", st)
	}
	if err := r.probeFence(); err != nil {
		return fmt.Errorf("after: %w", err)
	}
	return nil
}

func TestGuardDeleteOneACL(t *testing.T) {
	if err := deleteOneACL(t, false); err != nil {
		t.Fatal(err)
	}
}

// The positive control: with the fake's mutant switch on, every Deny is listed
// but drops nothing, and the same test must fail.
func TestGuardDeleteOneACLFailsWithTheMutant(t *testing.T) {
	err := deleteOneACL(t, true)
	if err == nil {
		t.Fatal("the delete-one-ACL test passed with every Deny ignored, so it proves nothing")
	}
	// It must fail because a probe got through, not because a probe could not
	// be set up.
	var fv *fenceViolation
	if !errors.As(err, &fv) {
		t.Fatalf("the mutant failed the test for another reason: %v", err)
	}
	t.Logf("mutant on: %v", err)
}

// tamper is one change to the fence made behind the guard's back, and the
// change field Diff names for it.
type tamper struct {
	name  string
	field string
	do    func(r *rig)
}

func tampers() []tamper {
	ctx := context.Background()
	return []tamper{
		{"delete one ACL", "acl", func(r *rig) {
			r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
		}},
		{"a second adapter", "adapters", func(r *rig) {
			if err := r.f.AddAdapter(r.p.VMID, ""); err != nil {
				r.t.Fatal(err)
			}
		}},
		{"the adapter on another switch", "adapterSwitch", func(r *rig) {
			r.f.AddSwitch("LAN", "External")
			if err := r.f.SetAdapterSwitch(r.p.VMID, "LAN"); err != nil {
				r.t.Fatal(err)
			}
		}},
		{"a new GCS key", "gcs", func(r *rig) {
			r.f.SetGCS([]string{"0000AAAA-FACB-11E6-BD58-64006A7986D3", "9D6C2B11-0000-4000-8000-000000000001"})
		}},
		{"ms_rspndr bound again", "bindings", func(r *rig) {
			r.f.SetBinding(ctx, r.vnic(), "ms_rspndr", true)
		}},
		{"a guard Deny removed by someone else", "acl", func(r *rig) {
			r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{netip.PrefixFrom(r.f.PublicIP, 32)})
		}},
	}
}

// Each change to the fence cuts the VM off after one Pass, and the Pass never
// connects or starts it.
func TestGuardCutsOffOnEveryChange(t *testing.T) {
	for _, c := range tampers() {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.do(r)
			m := r.mark()
			st := r.d.Pass(context.Background())
			if err := expectCut(r, st, r.since(m), c.field); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The guard puts back only the Denies it manages. A static Deny missing at a
// Boot with no state yet (the first one after setup) stays missing, and the VM
// stays off the network until Repair.
func TestGuardNeverPutsBackAStaticDeny(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	gone := netip.MustParsePrefix("10.0.0.0/8")
	r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{gone})
	os.Remove(filepath.Join(r.dir, winvm.StateFile))
	m := r.mark()
	st := r.d.Boot(ctx)
	if slices.Contains(r.acls(), gone) || slices.Contains(r.since(m), "Connect") {
		t.Fatalf("Boot put the static Deny back or connected: %v", r.since(m))
	}
	if st.Network != winvm.NetDisconnected || st.Fence != winvm.FenceCut {
		t.Fatalf("status %+v", st)
	}
}

// A public route through a tunnel gets its Deny in the same Pass, and the VM
// stays on the network: the Deny works per packet.
func TestGuardTunnelRouteGetsADenyInTheSamePass(t *testing.T) {
	r := newRig(t)
	route := netip.MustParsePrefix("198.51.100.0/24")
	r.f.SetRoute(winvm.Route{Prefix: route, IfIndex: 15, NextHop: netip.IPv4Unspecified(), Metric: 5})
	m := r.mark()
	st := r.d.Pass(context.Background())
	calls := r.since(m)
	if st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld || len(st.Changes) != 0 {
		t.Fatalf("status %+v", st)
	}
	if !slices.Contains(calls, "AddDeny") || slices.Contains(calls, "Disconnect") {
		t.Fatalf("calls %v, want an AddDeny and no Disconnect", calls)
	}
	if !slices.Contains(r.acls(), route) {
		t.Fatalf("no Deny for %v: %v", route, r.acls())
	}
}

// A new own public IP gets its Deny in the same Pass, with no disconnect. An
// old one goes 24 hours after its last sighting.
func TestGuardOwnIPs(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	old := netip.PrefixFrom(r.f.PublicIP, 32)
	nu := netip.MustParseAddr("198.51.100.77")
	r.setOwn(nu)
	m := r.mark()
	st := r.d.Pass(ctx)
	if st.Network != winvm.NetConnected || slices.Contains(r.since(m), "Disconnect") {
		t.Fatalf("a new own IP disconnected the VM: %+v %v", st, r.since(m))
	}
	if acls := r.acls(); !slices.Contains(acls, netip.PrefixFrom(nu, 32)) || !slices.Contains(acls, old) {
		t.Fatalf("both own IPs should be denied: %v", acls)
	}
	r.setNow(provedAt.Add(26 * time.Hour))
	m = r.mark()
	st = r.d.Pass(ctx)
	if st.Network != winvm.NetConnected || !slices.Contains(r.since(m), "RemoveDeny") {
		t.Fatalf("the expired own IP was not removed: %+v %v", st, r.since(m))
	}
	if acls := r.acls(); slices.Contains(acls, old) || !slices.Contains(acls, netip.PrefixFrom(nu, 32)) {
		t.Fatalf("after 24 h: %v", acls)
	}
}

// A full tunnel cuts the VM off with the plain reason: a VPN that takes the
// default route as 0.0.0.0/1 plus 128.0.0.0/1 (WireGuard, OpenVPN), and an
// exit node that takes 0.0.0.0/0.
func TestGuardFullTunnel(t *testing.T) {
	for name, routes := range map[string][]string{
		"split default": {"0.0.0.0/1", "128.0.0.0/1"},
		"exit node":     {"0.0.0.0/0"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			for _, p := range routes {
				r.f.SetRoute(winvm.Route{Prefix: netip.MustParsePrefix(p), IfIndex: 15, NextHop: netip.IPv4Unspecified(), Metric: 0})
			}
			if name == "exit node" {
				// The exit node wins the default route by its lower metric.
				r.f.SetRoute(winvm.Route{Prefix: netip.MustParsePrefix("0.0.0.0/0"), IfIndex: 7, NextHop: r.f.Router, Metric: 50})
			}
			m := r.mark()
			st := r.d.Pass(context.Background())
			if st.Network != winvm.NetDisconnected || !slices.Contains(r.since(m), "Disconnect") || slices.Contains(r.since(m), "Connect") {
				t.Fatalf("a full tunnel did not cut the VM off: %+v %v", st, r.since(m))
			}
			if !strings.Contains(st.Reason, "through a VPN") || !strings.Contains(st.Reason, "would sell the VPN's address") {
				t.Fatalf("reason %q", st.Reason)
			}
		})
	}
}

// An unapproved network, and an approved one that is metered, keep the VM
// off the network.
func TestGuardNetworks(t *testing.T) {
	approved := winvm.NetworkID{GatewayMAC: "00-00-5E-00-53-01", ProfileID: "{6A5D2C1E-0B7F-4C3A-9E21-5F8D7C6B4A30}"}
	for name, c := range map[string]struct {
		n    winvm.CurrentNetwork
		want string
	}{
		"unapproved":       {winvm.CurrentNetwork{ID: winvm.NetworkID{GatewayMAC: "00-00-5E-00-53-99", ProfileID: "{11111111-2222-4333-8444-555555555555}"}}, "did not approve"},
		"approved metered": {winvm.CurrentNetwork{ID: approved, Metered: true}, "metered"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.f.SetNetwork(c.n)
			st := r.d.Pass(context.Background())
			if st.Network != winvm.NetDisconnected || !strings.Contains(st.Reason, c.want) || st.Fence != winvm.FenceHeld {
				t.Fatalf("status %+v", st)
			}
			if st := r.d.Boot(context.Background()); st.Network != winvm.NetDisconnected {
				t.Fatalf("Boot connected on this network: %+v", st)
			}
		})
	}
}

// The Default Switch moving onto our /29 cuts the VM off.
func TestGuardSubnetCollision(t *testing.T) {
	r := newRig(t)
	r.f.SetRoute(winvm.Route{Prefix: netip.MustParsePrefix("172.31.240.0/20"), IfIndex: 44, NextHop: netip.IPv4Unspecified(), Metric: 256})
	st := r.d.Pass(context.Background())
	if st.Network != winvm.NetDisconnected || !strings.Contains(st.Reason, "now uses the VM's addresses") {
		t.Fatalf("status %+v", st)
	}
}

// A proof lock younger than 15 minutes holds the guard back: Pass does
// nothing, Boot disconnects and never connects. At 16 minutes the lock is
// ignored and the guard acts again.
func TestGuardProofLock(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	now := provedAt.Add(time.Hour)
	if err := winvm.WriteProofLock(r.dir, now.Add(-14*time.Minute)); err != nil {
		t.Fatal(err)
	}
	m := r.mark()
	st := r.d.Pass(ctx)
	if calls := r.since(m); slices.ContainsFunc(calls, func(c string) bool {
		return c == "Disconnect" || c == "Connect" || c == "AddDeny" || c == "RemoveDeny" || c == "Start"
	}) {
		t.Fatalf("Pass touched the VM under a valid lock: %v", calls)
	}
	if st.Network != winvm.NetConnected || st.Reason != "The fence proof is running." {
		t.Fatalf("under the lock: %+v", st)
	}
	m = r.mark()
	st = r.d.Boot(ctx)
	if calls := r.since(m); !slices.Contains(calls, "Disconnect") || slices.Contains(calls, "Connect") {
		t.Fatalf("Boot under the lock: %v", calls)
	}
	if st.Network != winvm.NetDisconnected {
		t.Fatalf("Boot under the lock: %+v", st)
	}
	r.f.Connect(ctx, r.p.VMID, r.p.SwitchID, r.p.Owner, r.p.AdapterID)
	if err := winvm.WriteProofLock(r.dir, now.Add(-16*time.Minute)); err != nil {
		t.Fatal(err)
	}
	m = r.mark()
	st = r.d.Pass(ctx)
	if err := expectCut(r, st, r.since(m), "acl"); err != nil {
		t.Fatalf("with a 16-minute-old lock: %v", err)
	}
	if !r.logged("proof lock") {
		t.Fatal("the ignored lock is not in the log")
	}
}

// Pass never calls Connect or Start, over every kind of input: each change to
// the fence, each blocker, failed readings, and a clean VM that is already off
// the network.
func TestGuardPassNeverConnects(t *testing.T) {
	ctx := context.Background()
	cases := tampers()
	cases = append(cases,
		tamper{"clean, already disconnected", "", func(r *rig) { r.f.Disconnect(ctx, r.p.VMID, r.p.Owner, r.p.AdapterID) }},
		tamper{"clean and off", "", func(r *rig) {
			r.f.Disconnect(ctx, r.p.VMID, r.p.Owner, r.p.AdapterID)
			r.f.SetVMState(r.p.VMID, "Off")
		}},
		tamper{"keepOff", "", func(r *rig) { r.writePrefs(`{"keepOff":true}`) }},
		tamper{"broken prefs", "", func(r *rig) { r.writePrefs(`{"keepOff":false,"allowAll":true}`) }},
		tamper{"no own IP answer yet", "", func(r *rig) { r.setOwn() }},
		tamper{"live reading fails", "", func(r *rig) { r.f.FailNext("ReadLive", nil) }},
		tamper{"host reading fails", "", func(r *rig) { r.f.FailNext("ReadHostNet", nil) }},
		tamper{"network reading fails", "", func(r *rig) { r.f.FailNext("ReadNetwork", nil) }},
		tamper{"drop counter fails", "", func(r *rig) { r.f.FailNext("DropCounters", nil) }},
		tamper{"a new tunnel route", "", func(r *rig) {
			r.f.SetRoute(winvm.Route{Prefix: netip.MustParsePrefix("198.51.100.0/24"), IfIndex: 15, NextHop: netip.IPv4Unspecified(), Metric: 5})
		}},
		tamper{"Windows updated", "", func(r *rig) { r.f.SetBuild("26100.2000") }},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.do(r)
			m := r.mark()
			r.d.Pass(ctx)
			r.d.Pass(ctx)
			if calls := r.since(m); slices.Contains(calls, "Connect") || slices.Contains(calls, "Start") {
				t.Fatalf("Pass called %v", calls)
			}
		})
	}
}

// Boot disconnects first and connects only on an exact match.
func TestGuardBootConnectsOnlyOnAnExactMatch(t *testing.T) {
	ctx := context.Background()
	t.Run("exact match", func(t *testing.T) {
		r := newRig(t)
		r.f.Disconnect(ctx, r.p.VMID, r.p.Owner, r.p.AdapterID)
		r.f.SetVMState(r.p.VMID, "Off")
		m := r.mark()
		st := r.d.Boot(ctx)
		calls := r.since(m)
		if len(calls) < 2 || calls[0] != "Inventory" || calls[1] != "Disconnect" {
			t.Fatalf("Boot did not disconnect first: %v", calls)
		}
		ci, si := slices.Index(calls, "Connect"), slices.Index(calls, "Start")
		if ci < 0 || si < ci {
			t.Fatalf("Boot should connect, then start: %v", calls)
		}
		if st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld || st.Reason != "" {
			t.Fatalf("status %+v", st)
		}
	})
	// Boot's own disconnect takes the adapter off any switch, so an adapter
	// moved to another switch is no change by the time Boot compares: Boot
	// puts it back on ours.
	t.Run("the adapter on another switch", func(t *testing.T) {
		r := newRig(t)
		r.f.AddSwitch("LAN", "External")
		if err := r.f.SetAdapterSwitch(r.p.VMID, "LAN"); err != nil {
			t.Fatal(err)
		}
		st := r.d.Boot(ctx)
		l, _ := r.f.ReadLive(ctx, r.p.Names, r.p.VMID, r.p.SwitchID)
		if st.Network != winvm.NetConnected || !strings.EqualFold(l.Adapters[0].SwitchID, r.p.SwitchID) {
			t.Fatalf("Boot left the adapter on %q: %+v", l.Adapters[0].SwitchID, st)
		}
	})
	var blockers []tamper
	for _, c := range tampers() {
		if c.field != "adapterSwitch" {
			blockers = append(blockers, c)
		}
	}
	blockers = append(blockers,
		tamper{"keepOff", "", func(r *rig) { r.writePrefs(`{"keepOff":true}`) }},
		tamper{"no approved network", "", func(r *rig) { os.Remove(filepath.Join(r.dir, winvm.PrefsFile)) }},
		tamper{"no own IP answer yet", "", func(r *rig) {
			r.setOwn()
			os.Remove(filepath.Join(r.dir, winvm.StateFile))
		}},
	)
	for _, c := range blockers {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.do(r)
			m := r.mark()
			st := r.d.Boot(ctx)
			calls := r.since(m)
			if len(calls) < 2 || calls[0] != "Inventory" || calls[1] != "Disconnect" || slices.Contains(calls, "Connect") || slices.Contains(calls, "Start") {
				t.Fatalf("Boot calls %v", calls)
			}
			if st.Network != winvm.NetDisconnected || st.Reason == "" || r.connectedNow() {
				t.Fatalf("status %+v", st)
			}
		})
	}
	t.Run("never proven", func(t *testing.T) {
		r := newRigWith(t, func(p *winvm.Plan) { p.ProvedAt = time.Time{} }, false)
		m := r.mark()
		st := r.d.Boot(ctx)
		if slices.Contains(r.since(m), "Connect") || st.Fence != winvm.FenceUnproven || !strings.Contains(st.Reason, "not been proven") {
			t.Fatalf("an unproven plan: %+v %v", st, r.since(m))
		}
	})
}

// A change of the host's Windows build needs a new proof: attention, never
// green, and no disconnect on its own.
func TestGuardReproveNeeded(t *testing.T) {
	r := newRig(t)
	r.f.SetBuild("26100.2000")
	m := r.mark()
	st := r.d.Pass(context.Background())
	if st.Fence != winvm.FenceReprove || st.Network != winvm.NetConnected || !strings.Contains(st.Reason, "Repair") {
		t.Fatalf("status %+v", st)
	}
	if slices.Contains(r.since(m), "Disconnect") {
		t.Fatal("a build change alone disconnected the VM")
	}
}

// A failed reading cuts the VM off, and a failed host reading never removes a
// Deny.
func TestGuardFailedReadingsCutOff(t *testing.T) {
	for _, method := range []string{"ReadLive", "ReadHostNet"} {
		t.Run(method, func(t *testing.T) {
			r := newRig(t)
			r.f.FailNext(method, nil)
			m := r.mark()
			st := r.d.Pass(context.Background())
			calls := r.since(m)
			if st.Fence != winvm.FenceCut || !slices.Contains(calls, "Disconnect") {
				t.Fatalf("status %+v, calls %v", st, calls)
			}
			if slices.Contains(calls, "RemoveDeny") || slices.Contains(calls, "AddDeny") {
				t.Fatalf("a partial reading changed the Denies: %v", calls)
			}
		})
	}
}

// The drop counter is read every pass. A pass whose counter did not answer
// shows the count as unknown (null), never 0.
func TestGuardBlockedCounter(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.f.UDP(ctx, netip.AddrPortFrom(r.f.LANIP, 53), "x", 100)
	st := r.d.Pass(ctx)
	if st.Blocked == nil || *st.Blocked != 100 || st.BlockedSince.IsZero() {
		t.Fatalf("blocked %v since %v, want 100", st.Blocked, st.BlockedSince)
	}
	r.f.FailNext("DropCounters", nil)
	st = r.d.Pass(ctx)
	if st.Blocked != nil {
		t.Fatalf("an unread counter shows %d", *st.Blocked)
	}
	b, err := os.ReadFile(filepath.Join(r.dir, winvm.StatusFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"blocked": null`) {
		t.Fatalf("status.json does not say null:\n%s", b)
	}
	r.f.UDP(ctx, netip.AddrPortFrom(r.f.LANIP, 53), "x", 5)
	if st := r.d.Pass(ctx); st.Blocked == nil || *st.Blocked != 105 {
		t.Fatalf("after the gap: %v", st.Blocked)
	}
}

// What Pass returns is what status.json holds.
func TestGuardStatusFileMatches(t *testing.T) {
	r := newRig(t)
	st := r.d.Pass(context.Background())
	got, err := winvm.ReadStatus(filepath.Join(r.dir, winvm.StatusFile))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(st)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Fatalf("returned %s\n    file %s", a, b)
	}
	if got.VM != r.p.Names.VM || !got.ProvedAt.Equal(provedAt) || got.EnhancedSession != "off" || got.Usage == nil {
		t.Fatalf("status %+v", got)
	}
}

// A plan that cannot be read fails closed: the VM of the last plan that could
// be read goes off the network, and the status says why. With no VM known yet
// there is nothing to act on, and the status says so.
func TestGuardWithoutAPlan(t *testing.T) {
	for _, c := range []struct {
		name  string
		write func(r *rig)
	}{
		{"broken", func(r *rig) {
			os.WriteFile(filepath.Join(r.dir, winvm.PlanFile), []byte(`{"version":1,"names":{}}`), 0o600)
		}},
		// A newer app wrote a plan version this guard does not read.
		{"newer version", func(r *rig) {
			b, err := r.p.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			nb := strings.Replace(string(b), `"version": 1,`, `"version": 99,`, 1)
			if nb == string(b) {
				t.Fatal("the plan has no version field to change")
			}
			os.WriteFile(filepath.Join(r.dir, winvm.PlanFile), []byte(nb), 0o600)
		}},
		{"missing", func(r *rig) { os.Remove(filepath.Join(r.dir, winvm.PlanFile)) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.write(r)
			m := r.mark()
			st := r.d.Pass(context.Background())
			if !slices.Contains(r.since(m), "Disconnect") || r.connectedNow() {
				t.Fatalf("the VM stayed on the network: %v", r.since(m))
			}
			if slices.Contains(r.since(m), "Connect") || slices.Contains(r.since(m), "Start") {
				t.Fatalf("calls %v", r.since(m))
			}
			if st.Network != winvm.NetDisconnected || st.Fence != winvm.FenceCut || !strings.Contains(st.Reason, "desired state") {
				t.Fatalf("status %+v", st)
			}
		})
	}
	// With no plan and no state the guard cannot tell which VM is this
	// install's, so it touches none, and it never reports the VM as absent:
	// it may still be on the network.
	for _, c := range []struct{ name, plan, state string }{
		{"no VM known", "", ""},
		{"garbage plan and garbage state", `{"version":1,"names":`, `not json at all`},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			for file, body := range map[string]string{winvm.StateFile: c.state, winvm.PlanFile: c.plan} {
				path := filepath.Join(r.dir, file)
				os.Remove(path)
				if body != "" {
					os.WriteFile(path, []byte(body), 0o600)
				}
			}
			m := r.mark()
			st := r.d.Pass(context.Background())
			if st.Network == winvm.NetAbsent || st.Fence != winvm.FenceCut || !strings.Contains(st.Reason, "cannot tell") {
				t.Fatalf("status %+v", st)
			}
			if calls := r.since(m); len(calls) != 0 {
				t.Fatalf("the guard touched a VM it cannot tell is ours: %v", calls)
			}
		})
	}
	// A plan and state without a VM ID cannot authorize a disconnect.
	t.Run("interrupted setup", func(t *testing.T) {
		for _, c := range []struct {
			name    string
			foreign bool
			drop    bool
			want    string
		}{
			{"owner tag without a recorded ID", false, false, winvm.NetConnected},
			{"no VM", false, true, winvm.NetAbsent},
			{"a VM without our token", true, true, winvm.NetConnected},
		} {
			t.Run(c.name, func(t *testing.T) {
				ctx := context.Background()
				r := newRig(t)
				vmID := r.p.VMID
				if c.drop {
					r.f.Disconnect(ctx, vmID, r.p.Owner, r.p.AdapterID)
					r.f.RemoveVM(ctx, vmID, r.p)
				}
				if c.foreign {
					r.f.AddForeignVM(r.p.Names.VM, false)
				}
				r.p.VMID, r.p.AdapterID = "", ""
				r.writePlan()
				os.Remove(filepath.Join(r.dir, winvm.StateFile))
				m := r.mark()
				st := r.d.Pass(ctx)
				if st.Network != c.want {
					t.Fatalf("network %q, want %q: %+v, calls %v", st.Network, c.want, st, r.since(m))
				}
				if slices.Contains(r.since(m), "Disconnect") {
					t.Fatal("the guard disconnected a VM without a recorded ID")
				}
			})
		}
	})
}

// Run boots, then passes, and stops when its context ends. The passes never
// disconnect or connect again.
func TestGuardRun(t *testing.T) {
	// A cleanup, not a defer: it must run after the cleanup below that waits
	// for Run, which reads the interval.
	t.Cleanup(winvm.SetGuardInterval(5 * time.Millisecond))
	r := newRig(t)
	count := func(calls []string, c string) int {
		n := 0
		for _, x := range calls {
			if x == c {
				n++
			}
		}
		return n
	}
	wait := func(m int, cond func([]string) bool) bool {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if cond(r.since(m)) {
				return true
			}
		}
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	m := r.mark()
	go func() { done <- r.d.Run(ctx) }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			cancel()
			<-done
		}
	})
	if !wait(m, func(c []string) bool { return count(c, "Connect") == 1 && count(c, "ReadNetwork") >= 4 }) {
		t.Fatalf("Run did not boot and pass: %v", r.since(m))
	}
	if n := count(r.since(m), "Disconnect"); n != 1 {
		t.Fatalf("passes without a clock jump disconnected: %d", n)
	}
	cancel()
	stopped = true
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
}

// After the clock jumps (the host slept) the next tick is the boot routine
// again: disconnect first, then connect on a match.
func TestGuardRunResumesAfterSleep(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	l := winvm.NewGuardLoop(r.d)
	l.Boot(ctx)
	m := r.mark()
	l.Tick(ctx)
	if calls := r.since(m); slices.Contains(calls, "Disconnect") || slices.Contains(calls, "Connect") {
		t.Fatalf("a tick without a clock jump: %v", calls)
	}
	r.setNow(provedAt.Add(2 * time.Hour))
	m = r.mark()
	st := l.Tick(ctx)
	if calls := r.since(m); len(calls) < 2 || calls[0] != "Inventory" || calls[1] != "Disconnect" || !slices.Contains(calls, "Connect") || st.Network != winvm.NetConnected {
		t.Fatalf("no resume after the clock jumped: %+v %v", st, calls)
	}
}

// slowReads is the fake with a ReadNetwork that takes four minutes of wall
// clock, as several PowerShell timeouts in a row would.
type slowReads struct {
	*hvfake.HyperV
	r *rig
}

func (h slowReads) ReadNetwork(ctx context.Context) (winvm.CurrentNetwork, error) {
	h.r.mu.Lock()
	h.r.now = h.r.now.Add(4 * time.Minute)
	h.r.mu.Unlock()
	return h.HyperV.ReadNetwork(ctx)
}

// A slow routine is not a sleep: the tick after it is a plain Pass, so it
// neither disconnects nor connects.
func TestGuardRunSlowRoutineIsNoResume(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.d.HV = slowReads{r.f, r}
	l := winvm.NewGuardLoop(r.d)
	l.Boot(ctx)
	for range 3 {
		m := r.mark()
		l.Tick(ctx)
		if calls := r.since(m); slices.Contains(calls, "Disconnect") || slices.Contains(calls, "Connect") {
			t.Fatalf("a slow tick ran as a resume: %v", calls)
		}
	}
}

// A guard Deny write that fails in a pass is not a change to the fence: the
// VM goes off the network, and the next tick runs the boot routine, which
// writes the Deny and connects again.
func TestGuardRunRetriesAFailedDenyWrite(t *testing.T) {
	ctx := context.Background()
	t.Run("add", func(t *testing.T) {
		r := newRig(t)
		l := winvm.NewGuardLoop(r.d)
		l.Boot(ctx)
		nu := netip.MustParseAddr("198.51.100.77")
		r.setOwn(r.f.PublicIP, nu)
		r.f.FailNext("AddDeny", nil)
		m := r.mark()
		st := l.Tick(ctx)
		if st.Network != winvm.NetDisconnected || !strings.Contains(st.Reason, "tries again") || slices.Contains(r.since(m), "Connect") {
			t.Fatalf("after the failed add: %+v %v", st, r.since(m))
		}
		m = r.mark()
		st = l.Tick(ctx)
		calls := r.since(m)
		if len(calls) < 2 || calls[0] != "Inventory" || calls[1] != "Disconnect" || !slices.Contains(calls, "AddDeny") || !slices.Contains(calls, "Connect") {
			t.Fatalf("the retry: %v", calls)
		}
		if st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld || !slices.Contains(r.acls(), netip.PrefixFrom(nu, 32)) {
			t.Fatalf("not back with the new Deny: %+v %v", st, r.acls())
		}
	})
	t.Run("remove", func(t *testing.T) {
		r := newRig(t)
		l := winvm.NewGuardLoop(r.d)
		l.Boot(ctx)
		old := netip.PrefixFrom(r.f.PublicIP, 32)
		r.setOwn(netip.MustParseAddr("198.51.100.77"))
		l.Tick(ctx)
		// The old own IP expires; its remove fails once.
		r.setNow(provedAt.Add(26 * time.Hour))
		r.f.FailNext("RemoveDeny", nil)
		st := l.Tick(ctx)
		if st.Network != winvm.NetDisconnected || !strings.Contains(st.Reason, "tries again") {
			t.Fatalf("after the failed remove: %+v", st)
		}
		m := r.mark()
		st = l.Tick(ctx)
		calls := r.since(m)
		if len(calls) < 2 || calls[0] != "Inventory" || calls[1] != "Disconnect" || !slices.Contains(calls, "RemoveDeny") || !slices.Contains(calls, "Connect") {
			t.Fatalf("the retry: %v", calls)
		}
		if st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld || slices.Contains(r.acls(), old) {
			t.Fatalf("not back without the old Deny: %+v %v", st, r.acls())
		}
	})
}

// Run comes back from a stop that was not a change to the fence: once the
// reason is gone, the next tick runs the boot routine (disconnect, exact
// compare, connect). Each case turns a reason on, ticks, turns it off, ticks.
func TestGuardRunReconnectsOnceTheReasonIsGone(t *testing.T) {
	ctx := context.Background()
	const approved = `{"keepOff":false,"approvedNetworks":[{"gatewayMac":"00-00-5E-00-53-01","profileId":"{6A5D2C1E-0B7F-4C3A-9E21-5F8D7C6B4A30}"}]}`
	for _, c := range []struct {
		name    string
		on, off func(r *rig)
	}{
		{"keepOff", func(r *rig) { r.writePrefs(`{"keepOff":true}`) }, func(r *rig) { r.writePrefs(approved) }},
		{"a network not approved yet", func(r *rig) { os.Remove(filepath.Join(r.dir, winvm.PrefsFile)) }, func(r *rig) { r.writePrefs(approved) }},
		{"a broken prefs file", func(r *rig) { r.writePrefs(`{"allowAll":true}`) }, func(r *rig) { r.writePrefs(approved) }},
		// A cold start: no state yet, no echo answer yet, so no own-IP Deny.
		{"no own IP yet", func(r *rig) {
			r.setOwn()
			os.Remove(filepath.Join(r.dir, winvm.StateFile))
			r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{netip.PrefixFrom(r.f.PublicIP, 32)})
		}, func(r *rig) { r.setOwn(r.f.PublicIP) }},
		{"a failed reading", func(r *rig) { r.f.FailNext("ReadLive", nil) }, func(*rig) {}},
		{"a failed host reading", func(r *rig) { r.f.FailNext("ReadHostNet", nil) }, func(*rig) {}},
		{"a plan that cannot be read", func(r *rig) {
			os.WriteFile(filepath.Join(r.dir, winvm.PlanFile), []byte(`{"version":99}`), 0o600)
		}, func(r *rig) { r.writePlan() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			l := winvm.NewGuardLoop(r.d)
			if st := l.Boot(ctx); st.Network != winvm.NetConnected {
				t.Fatalf("Boot: %+v", st)
			}
			c.on(r)
			m := r.mark()
			st := l.Tick(ctx)
			if st.Network != winvm.NetDisconnected || st.Reason == "" || slices.Contains(r.since(m), "Connect") {
				t.Fatalf("with the reason on: %+v %v", st, r.since(m))
			}
			c.off(r)
			m = r.mark()
			st = l.Tick(ctx)
			calls := r.since(m)
			if len(calls) < 2 || calls[0] != "Inventory" || calls[1] != "Disconnect" || !slices.Contains(calls, "Connect") {
				t.Fatalf("the tick after the reason went is not the boot routine: %v", calls)
			}
			if st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld || st.Reason != "" || !r.connectedNow() {
				t.Fatalf("not back on the network: %+v", st)
			}
			// Back to passes: the next tick neither disconnects nor connects.
			m = r.mark()
			l.Tick(ctx)
			if calls := r.since(m); slices.Contains(calls, "Disconnect") || slices.Contains(calls, "Connect") {
				t.Fatalf("a tick after the reconnect: %v", calls)
			}
		})
	}
}

// A cut for a change to the fence stays a cut: the ticks after it never
// connect, not even once the change is undone. Only Repair, a reboot or a
// resume brings the VM back.
func TestGuardRunNeverReconnectsAfterADriftCut(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	l := winvm.NewGuardLoop(r.d)
	l.Boot(ctx)
	ten := netip.MustParsePrefix("10.0.0.0/8")
	r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{ten})
	m := r.mark()
	if err := expectCut(r, l.Tick(ctx), r.since(m), "acl"); err != nil {
		t.Fatal(err)
	}
	if err := r.f.AddDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{ten}); err != nil {
		t.Fatal(err)
	}
	m = r.mark()
	for range 3 {
		if err := expectStillCut(l.Tick(ctx), "acl"); err != nil {
			t.Fatal(err)
		}
	}
	if calls := r.since(m); slices.Contains(calls, "Connect") || slices.Contains(calls, "Start") {
		t.Fatalf("a tick after a drift cut connected: %v", calls)
	}
	if r.connectedNow() {
		t.Fatal("the VM is back on the network after a drift cut")
	}
	// Someone puts the VM back on its switch by hand: the next tick takes it off
	// again, though the fence matches now.
	r.f.Connect(ctx, r.p.VMID, r.p.SwitchID, r.p.Owner, r.p.AdapterID)
	if err := expectStillCut(l.Tick(ctx), "acl"); err != nil || r.connectedNow() {
		t.Fatalf("a VM reconnected by hand during a cut: %v", err)
	}
	// The cut is in state.json: a one-shot Pass from another process sees it.
	if err := expectStillCut(r.d.Pass(ctx), "acl"); err != nil {
		t.Fatalf("a one-shot Pass: %v", err)
	}
	// A resume runs the exact check again and lifts the cut.
	r.setNow(provedAt.Add(3 * time.Hour))
	m = r.mark()
	st := l.Tick(ctx)
	if calls := r.since(m); len(calls) < 2 || calls[0] != "Inventory" || calls[1] != "Disconnect" || !slices.Contains(calls, "Connect") {
		t.Fatalf("the resume after the cut: %v", calls)
	}
	if st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld || st.Reason != "" || len(st.Changes) != 0 {
		t.Fatalf("after the resume: %+v", st)
	}
}

// expectStillCut checks the status while a cut holds after the change was
// undone: never green, the change still named, and the way back is Repair,
// never the next check. A restart is not promised: it does not undo a change
// that persists.
func expectStillCut(st winvm.Status, field string) error {
	switch {
	case st.Fence != winvm.FenceCut:
		return fmt.Errorf("fence %q while the cut holds (%+v)", st.Fence, st)
	case st.Network == winvm.NetConnected:
		return fmt.Errorf("network %q while the cut holds", st.Network)
	case !hasChange(st, field):
		return fmt.Errorf("the changes %+v do not name %s", st.Changes, field)
	case !strings.Contains(st.Reason, "Repair") || strings.Contains(st.Reason, "next check") || strings.Contains(st.Reason, "restart"):
		return fmt.Errorf("reason %q", st.Reason)
	}
	return nil
}

// A retry under a proof lock is a Pass: it never takes the proof's network
// away. Once the lock is gone, the retry runs the boot routine.
func TestGuardRunRetryHoldsOffUnderAProofLock(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	l := winvm.NewGuardLoop(r.d)
	l.Boot(ctx)
	r.writePrefs(`{"keepOff":true}`)
	l.Tick(ctx)
	r.writePrefs(`{"keepOff":false,"approvedNetworks":[{"gatewayMac":"00-00-5E-00-53-01","profileId":"{6A5D2C1E-0B7F-4C3A-9E21-5F8D7C6B4A30}"}]}`)
	if err := winvm.WriteProofLock(r.dir, provedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r.f.Connect(ctx, r.p.VMID, r.p.SwitchID, r.p.Owner, r.p.AdapterID) // the proof's own connect
	m := r.mark()
	l.Tick(ctx)
	l.Tick(ctx)
	if calls := r.since(m); slices.Contains(calls, "Disconnect") || slices.Contains(calls, "Connect") {
		t.Fatalf("a retry under the lock touched the network: %v", calls)
	}
	os.Remove(filepath.Join(r.dir, winvm.LockFile))
	r.f.Disconnect(ctx, r.p.VMID, r.p.Owner, r.p.AdapterID) // the proof ends with the VM off
	m = r.mark()
	st := l.Tick(ctx)
	if calls := r.since(m); len(calls) < 2 || calls[0] != "Inventory" || calls[1] != "Disconnect" || !slices.Contains(calls, "Connect") || st.Network != winvm.NetConnected {
		t.Fatalf("after the lock: %+v %v", st, calls)
	}
}

// A broken prefs file is named as the reason, not a network the user never
// approved.
func TestGuardBrokenPrefsReason(t *testing.T) {
	r := newRig(t)
	r.writePrefs(`{"keepOff":false,"allowAll":true}`)
	st := r.d.Pass(context.Background())
	if st.Network != winvm.NetDisconnected || !strings.Contains(st.Reason, "prefs.json") {
		t.Fatalf("status %+v", st)
	}
}

// A VM off the network always says why, even when nothing blocks it any more.
func TestGuardStatusNeverBlankWhenOff(t *testing.T) {
	r := newRig(t)
	r.writePrefs(`{"keepOff":true}`)
	r.d.Pass(context.Background())
	r.writePrefs(`{"keepOff":false,"approvedNetworks":[{"gatewayMac":"00-00-5E-00-53-01","profileId":"{6A5D2C1E-0B7F-4C3A-9E21-5F8D7C6B4A30}"}]}`)
	st := r.d.Pass(context.Background())
	if st.Network != winvm.NetDisconnected || st.Fence != winvm.FenceHeld || st.Reason == "" {
		t.Fatalf("status %+v", st)
	}
}

// Under a valid proof lock the status reads the network from the adapter: an
// adapter on no switch is disconnected, not assumed connected.
func TestGuardProofLockReadsTheNetwork(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.f.Disconnect(ctx, r.p.VMID, r.p.Owner, r.p.AdapterID)
	if err := winvm.WriteProofLock(r.dir, provedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if st := r.d.Pass(ctx); st.Network != winvm.NetDisconnected {
		t.Fatalf("under the lock with the adapter off: %+v", st)
	}
}

// Boot during a proof never connects, even on a fence that matches.
func TestGuardBootUnderALockOnACleanFence(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if err := winvm.WriteProofLock(r.dir, provedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	m := r.mark()
	st := r.d.Boot(ctx)
	if calls := r.since(m); !slices.Contains(calls, "Disconnect") || slices.Contains(calls, "Connect") || slices.Contains(calls, "Start") {
		t.Fatalf("Boot under the lock: %v", calls)
	}
	if st.Network != winvm.NetDisconnected || r.connectedNow() {
		t.Fatalf("Boot under the lock: %+v", st)
	}
}

// tamperOnConnect is the fake with a fence that changes the moment the VM is
// connected.
type tamperOnConnect struct {
	*hvfake.HyperV
	do func()
}

func (h tamperOnConnect) Connect(ctx context.Context, vmID, switchID, owner, adapterID string) error {
	if err := h.HyperV.Connect(ctx, vmID, switchID, owner, adapterID); err != nil {
		return err
	}
	h.do()
	return nil
}

// Boot reads the fence again after it connects: a change there takes the VM
// off again, it is not started, the status names the change, and the ticks
// after it do not reconnect, even once the change is undone.
func TestGuardBootChecksAgainAfterConnect(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	ten := netip.MustParsePrefix("10.0.0.0/8")
	armed := true
	r.d.HV = tamperOnConnect{r.f, func() {
		if armed {
			r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{ten})
		}
	}}
	r.f.Disconnect(ctx, r.p.VMID, r.p.Owner, r.p.AdapterID)
	r.f.SetVMState(r.p.VMID, "Off")
	l := winvm.NewGuardLoop(r.d)
	m := r.mark()
	st := l.Boot(ctx)
	calls := r.since(m)
	ci := slices.Index(calls, "Connect")
	if ci < 0 || !slices.Contains(calls[ci:], "Disconnect") || slices.Contains(calls, "Start") {
		t.Fatalf("Boot after a change on connect: %v", calls)
	}
	if st.Network == winvm.NetConnected || st.Fence != winvm.FenceCut || !hasChange(st, "acl") || r.connectedNow() {
		t.Fatalf("status %+v", st)
	}
	// Even with the change undone, the ticks do not reconnect.
	armed = false
	if err := r.f.AddDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{ten}); err != nil {
		t.Fatal(err)
	}
	m = r.mark()
	for range 2 {
		if err := expectStillCut(l.Tick(ctx), "acl"); err != nil {
			t.Fatal(err)
		}
	}
	if slices.Contains(r.since(m), "Connect") {
		t.Fatalf("a tick reconnected after the change on connect: %v", r.since(m))
	}
	// Boot runs the exact check again and connects.
	if st := r.d.Boot(ctx); st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld {
		t.Fatalf("Boot after the change was undone: %+v", st)
	}
}

// A Start that fails after the boot routine connected is a stop to retry, not
// a VM that is up: the VM goes off the network, the status says it did not
// start and why, and the next tick runs the boot routine again, which starts
// it.
func TestGuardRunRetriesAFailedStart(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.f.Disconnect(ctx, r.p.VMID, r.p.Owner, r.p.AdapterID)
	r.f.SetVMState(r.p.VMID, "Off")
	r.f.FailNext("Start", errors.New("fake Hyper-V: not enough memory to start the VM"))
	l := winvm.NewGuardLoop(r.d)
	st := l.Boot(ctx)
	if st.Network == winvm.NetConnected || r.connectedNow() {
		t.Fatalf("a VM that did not start stayed on the network: %+v", st)
	}
	if !strings.Contains(st.Reason, "could not be started") || !strings.Contains(st.Reason, "not enough memory") || !strings.Contains(st.Reason, "tries again") {
		t.Fatalf("reason %q", st.Reason)
	}
	m := r.mark()
	st = l.Tick(ctx)
	calls := r.since(m)
	if len(calls) < 2 || calls[0] != "Inventory" || calls[1] != "Disconnect" || !slices.Contains(calls, "Connect") || !slices.Contains(calls, "Start") {
		t.Fatalf("the tick after the failed start is not the boot routine: %v", calls)
	}
	if st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld || st.Reason != "" || !r.connectedNow() {
		t.Fatalf("not started on the retry: %+v", st)
	}
	// Back to passes.
	m = r.mark()
	l.Tick(ctx)
	if calls := r.since(m); slices.Contains(calls, "Connect") || slices.Contains(calls, "Start") {
		t.Fatalf("a tick after the start: %v", calls)
	}
}

// A Boot under a proof lock whose disconnect failed has not done its job: once
// the lock is gone, the next tick runs the boot routine, which disconnects.
func TestGuardRunRetriesABootDisconnectThatFailed(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if err := winvm.WriteProofLock(r.dir, provedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r.f.FailNext("Disconnect", nil)
	l := winvm.NewGuardLoop(r.d)
	l.Boot(ctx)
	if !r.connectedNow() {
		t.Fatal("the failed disconnect took the adapter off")
	}
	os.Remove(filepath.Join(r.dir, winvm.LockFile))
	m := r.mark()
	l.Tick(ctx)
	if calls := r.since(m); len(calls) < 2 || calls[0] != "Inventory" || calls[1] != "Disconnect" {
		t.Fatalf("the tick after the lock is not the boot routine: %v", calls)
	}
}

// failingState puts a folder where state.json goes, so the guard cannot write
// its state and starts each routine from a fresh one.
func failingState(t *testing.T, r *rig) {
	t.Helper()
	path := filepath.Join(r.dir, winvm.StateFile)
	os.Remove(path)
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// A cut holds in Run even when state.json cannot be written: after the change
// is undone, the ticks still do not reconnect. A resume lifts it.
func TestGuardRunKeepsACutWhenTheStateCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	l := winvm.NewGuardLoop(r.d)
	l.Boot(ctx)
	failingState(t, r)
	ten := netip.MustParsePrefix("10.0.0.0/8")
	r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{ten})
	m := r.mark()
	if err := expectCut(r, l.Tick(ctx), r.since(m), "acl"); err != nil {
		t.Fatal(err)
	}
	if !r.logged("could not write the state") {
		t.Fatal("the state write did not fail, so this test proves nothing")
	}
	if err := r.f.AddDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{ten}); err != nil {
		t.Fatal(err)
	}
	m = r.mark()
	for range 3 {
		if err := expectStillCut(l.Tick(ctx), "acl"); err != nil {
			t.Fatal(err)
		}
	}
	if calls := r.since(m); slices.Contains(calls, "Connect") || r.connectedNow() {
		t.Fatalf("a tick reconnected after a cut the state lost: %v", calls)
	}
	r.setNow(provedAt.Add(3 * time.Hour))
	if st := l.Tick(ctx); st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld {
		t.Fatalf("the resume did not lift the cut: %+v", st)
	}
}

// A cut, then a plan that cannot be read (Run retries), then the plan back and
// the change undone: the retry never lifts the cut.
func TestGuardRunCutHoldsAcrossAnUnreadablePlan(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	l := winvm.NewGuardLoop(r.d)
	l.Boot(ctx)
	ten := netip.MustParsePrefix("10.0.0.0/8")
	r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{ten})
	m := r.mark()
	if err := expectCut(r, l.Tick(ctx), r.since(m), "acl"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(r.dir, winvm.PlanFile), []byte(`{"version":99}`), 0o600)
	if st := l.Tick(ctx); !strings.Contains(st.Reason, "desired state") {
		t.Fatalf("an unreadable plan: %+v", st)
	}
	r.writePlan()
	if err := r.f.AddDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{ten}); err != nil {
		t.Fatal(err)
	}
	m = r.mark()
	for range 2 {
		if err := expectStillCut(l.Tick(ctx), "acl"); err != nil {
			t.Fatal(err)
		}
	}
	if calls := r.since(m); slices.Contains(calls, "Connect") || r.connectedNow() {
		t.Fatalf("a retry lifted the cut: %v", calls)
	}
}

// A readable plan that names no VM yet still takes the VM the guard knew off
// the network.
func TestGuardPlanWithoutIDsFailsClosed(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(p *winvm.Plan)
	}{
		{"no VM ID", func(p *winvm.Plan) { p.VMID = "" }},
		{"no adapter ID", func(p *winvm.Plan) { p.AdapterID = "" }},
		{"no switch ID", func(p *winvm.Plan) { p.SwitchID = "" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.edit(&r.p)
			r.writePlan()
			m := r.mark()
			st := r.d.Pass(context.Background())
			if !slices.Contains(r.since(m), "Disconnect") || r.connectedNow() {
				t.Fatalf("the VM stayed on the network: %v", r.since(m))
			}
			if st.Network != winvm.NetDisconnected || st.Fence != winvm.FenceCut || st.Reason == "" {
				t.Fatalf("status %+v", st)
			}
		})
	}
}

// An other line's exit that a tunnel route covers later keeps its row: the
// row setup placed is neither extra while the route is up nor missing once it
// goes, so the VM is never cut off for it.
func TestGuardExtraExitUnderATunnelRoute(t *testing.T) {
	ctx := context.Background()
	exit := netip.MustParseAddr("198.51.100.9")
	r := newRigWith(t, func(p *winvm.Plan) { p.ExtraExits = []netip.Addr{exit} }, false)
	if err := r.f.AddDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{netip.PrefixFrom(exit, 32)}); err != nil {
		t.Fatal(err)
	}
	if st := r.d.Boot(ctx); st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld {
		t.Fatalf("Boot with the exit's row: %+v", st)
	}
	r.f.SetRoute(winvm.Route{Prefix: netip.MustParsePrefix("198.51.100.0/24"), IfIndex: 15, NextHop: netip.IPv4Unspecified(), Metric: 5})
	st := r.d.Pass(ctx)
	if st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld {
		t.Fatalf("a tunnel route over the exit cut the VM off: %+v", st)
	}
	if !slices.Contains(r.acls(), netip.MustParsePrefix("198.51.100.0/24")) {
		t.Fatalf("the tunnel route got no Deny: %v", r.acls())
	}
}

// A row of the guard's own left one way by a remove that stopped halfway is
// still the guard's: the next try removes it and the VM comes back.
func TestGuardRunRemovesAOneWayLeftover(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	l := winvm.NewGuardLoop(r.d)
	l.Boot(ctx)
	old := netip.PrefixFrom(r.f.PublicIP, 32)
	r.setOwn(netip.MustParseAddr("198.51.100.77"))
	l.Tick(ctx)
	r.setNow(provedAt.Add(26 * time.Hour))
	r.f.FailNext("RemoveDeny", nil)
	if st := l.Tick(ctx); st.Network != winvm.NetDisconnected || !strings.Contains(st.Reason, "tries again") {
		t.Fatalf("after the failed remove: %+v", st)
	}
	if err := r.f.LeaveOneWay(r.p.VMID, old); err != nil {
		t.Fatal(err)
	}
	st := l.Tick(ctx)
	if st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld {
		t.Fatalf("not back after the one-way leftover: %+v", st)
	}
	l2, _ := r.f.ReadLive(ctx, r.p.Names, r.p.VMID, r.p.SwitchID)
	if slices.ContainsFunc(l2.ACLs, func(a winvm.ACL) bool { return a.Remote == old }) {
		t.Fatalf("the leftover is still there: %v", l2.ACLs)
	}
}

// cancelAfterConnect is the fake with a routine cut short right after the
// Connect: the reading after it ends the context, and a Disconnect on an ended
// context fails, as PowerShell does.
type cancelAfterConnect struct {
	*hvfake.HyperV
	cancel    context.CancelFunc
	connected *bool
}

func (h cancelAfterConnect) Connect(ctx context.Context, vmID, switchID, owner, adapterID string) error {
	*h.connected = true
	return h.HyperV.Connect(ctx, vmID, switchID, owner, adapterID)
}

func (h cancelAfterConnect) ReadLive(ctx context.Context, n winvm.Names, vmID, switchID string) (winvm.LiveState, error) {
	if *h.connected {
		h.cancel()
		return winvm.LiveState{}, ctx.Err()
	}
	return h.HyperV.ReadLive(ctx, n, vmID, switchID)
}

func (h cancelAfterConnect) Disconnect(ctx context.Context, vmID, owner, adapterID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.HyperV.Disconnect(ctx, vmID, owner, adapterID)
}

// A routine cut short after its Connect still undoes the Connect.
func TestGuardUndoesAConnectWhenCutShort(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := false
	r.d.HV = cancelAfterConnect{r.f, cancel, &connected}
	r.d.Boot(ctx)
	if !connected {
		t.Fatal("Boot did not connect, so this test proves nothing")
	}
	if r.connectedNow() {
		t.Fatal("the VM stayed on the network after a routine cut short")
	}
}

// A change on connect while the VM already runs: the status says
// disconnected, never connected, because the VM was taken off again.
func TestGuardChangeOnConnectWhileRunning(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	ten := netip.MustParsePrefix("10.0.0.0/8")
	r.d.HV = tamperOnConnect{r.f, func() { r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{ten}) }}
	st := r.d.Boot(ctx)
	if st.Network != winvm.NetDisconnected || st.Fence != winvm.FenceCut || r.connectedNow() {
		t.Fatalf("status %+v", st)
	}
}

// A cut whose Disconnect fails leaves the VM on the network: the status must
// say so plainly, never that the VM is cut off, and the next pass tries again.
func TestGuardCutWhoseDisconnectFailedSaysSo(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.f.RemoveDeny(ctx, r.p.VMID, r.p.Owner, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	r.f.FailNext("Disconnect", nil)
	st := r.d.Pass(ctx)
	if !r.connectedNow() || st.Network != winvm.NetConnected || st.Fence != winvm.FenceCut {
		t.Fatalf("status %+v", st)
	}
	if strings.Contains(strings.ToLower(st.Reason), "cut off") || !strings.Contains(st.Reason, "could not take the VM off the network") {
		t.Fatalf("reason %q", st.Reason)
	}
	m := r.mark()
	st = r.d.Pass(ctx)
	if !slices.Contains(r.since(m), "Disconnect") || r.connectedNow() || st.Network != winvm.NetDisconnected {
		t.Fatalf("the next pass did not take it off: %+v %v", st, r.since(m))
	}
}

// Under no proof lock, a Boot whose Disconnect fails twice (once before the
// check, once for keepOff) leaves the VM on the network unbooted: the next
// tick is the boot routine again, which starts with a Disconnect.
func TestGuardRunRetriesABootWhoseDisconnectsFailed(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.writePrefs(`{"keepOff":true,"approvedNetworks":[],"uploadLimitMbps":0}`)
	r.f.FailNth("Disconnect", 1, nil)
	r.f.FailNth("Disconnect", 2, nil)
	l := winvm.NewGuardLoop(r.d)
	st := l.Boot(ctx)
	if !r.connectedNow() {
		t.Fatalf("both disconnects failed, yet the adapter is off: %+v", st)
	}
	m := r.mark()
	l.Tick(ctx)
	if calls := r.since(m); len(calls) < 2 || calls[0] != "Inventory" || calls[1] != "Disconnect" {
		t.Fatalf("the tick after the failed boot is not the boot routine: %v", calls)
	}
}

func TestGuardReconnectsToRecordedSwitchAfterRename(t *testing.T) {
	r := newRig(t)
	if err := r.f.RenameSwitch(r.p.SwitchID, "my renamed switch"); err != nil {
		t.Fatal(err)
	}
	r.f.AddSwitch(r.p.Names.Switch, "Internal")
	if st := r.d.Boot(context.Background()); st.Network != winvm.NetConnected {
		t.Fatalf("the guard did not connect to the recorded switch: %+v", st)
	}
	if sw := adapterSwitch(t, r.f, r.p); sw != r.p.SwitchID {
		t.Fatalf("the adapter connected to %q, want %q", sw, r.p.SwitchID)
	}
}
