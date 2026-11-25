package e2e

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// buildFenced builds the product VM on the fake the way setup will.
func buildFenced(t *testing.T, f *hvfake.HyperV) winvm.Plan {
	t.Helper()
	return build(t, f, true)
}

func build(t *testing.T, f *hvfake.HyperV, staticARP bool) winvm.Plan {
	t.Helper()
	p, err := f.BuildFenced(staticARP)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func diffNow(t *testing.T, f *hvfake.HyperV, p winvm.Plan) []winvm.Change {
	t.Helper()
	ctx := context.Background()
	live, err := f.ReadLive(ctx, p.Names, p.VMID, p.SwitchID)
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.ReadHostNet(ctx, winvm.EgressSamples)
	if err != nil {
		t.Fatal(err)
	}
	d := winvm.Dynamic{HostAddrs: winvm.HostDenies(h, p.SwitchID), Tunnels: winvm.TunnelPrefixes(h, p.SwitchID, winvm.StaticDeny())}
	return winvm.Diff(p, live, d)
}

// fenceHolds is a check that depends on the fence itself, not on its config:
// the guest probes the host's LAN address, the LAN router, the gateway and
// the tailnet, and the check wants no arrival, nothing leaving the host, and
// one drop counted per probe. It is the kind of check the proof's green step
// makes.
func fenceHolds(t *testing.T, f *hvfake.HyperV, p winvm.Plan) error {
	t.Helper()
	ctx := context.Background()
	targets := []netip.AddrPort{
		netip.AddrPortFrom(f.LANIP, 40001),
		netip.AddrPortFrom(f.Router, 40002),
		netip.AddrPortFrom(p.Gateway, 40003),
		netip.AddrPortFrom(netip.MustParseAddr("100.64.0.9"), 40004),
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
			return fmt.Errorf("a probe arrived at %v", targets[i])
		}
	}
	for _, r := range recs {
		if r.Class == "physical" || r.Class == "tunnel" {
			return fmt.Errorf("a probe left the host through %s to %v", r.Class, r.Dst)
		}
	}
	if got := after.Out - before.Out; got != uint64(len(targets)) {
		return fmt.Errorf("the drop counter rose by %d, want %d", got, len(targets))
	}
	return nil
}

// The fake built the way setup will matches its own plan, and the fence holds.
// With the mutant on, the config compare still matches (the Deny rows are all
// listed), and only the fence-dependent check goes red. This is the positive
// control for every proof and guard test that runs on the fake.
func TestFakeHyperVMutantBreaksOnlyTheFence(t *testing.T) {
	f := hvfake.New()
	p := buildFenced(t, f)
	if cs := diffNow(t, f, p); len(cs) != 0 {
		t.Fatalf("the fake does not match its own plan: %+v", cs)
	}
	if err := fenceHolds(t, f, p); err != nil {
		t.Fatalf("the fence does not hold on the fake: %v", err)
	}
	f.SetMutant(true)
	if cs := diffNow(t, f, p); len(cs) != 0 {
		t.Fatalf("the mutant changed the config reading: %+v", cs)
	}
	if err := fenceHolds(t, f, p); err == nil {
		t.Fatal("with every Deny ignored the fence check still passed, so it proves nothing")
	} else {
		t.Logf("mutant on: %v", err)
	}
	f.SetMutant(false)
	if err := fenceHolds(t, f, p); err != nil {
		t.Fatalf("after the mutant: %v", err)
	}
}

// An Allow opens a red step, and pktmon sees the probe leave through the
// interface the host's route picks: the LAN router through the physical NIC,
// the tailnet through the tunnel. Same-prefix rows clash as on Hyper-V.
func TestFakeHyperVRedSteps(t *testing.T) {
	ctx := context.Background()
	f := hvfake.New()
	p := buildFenced(t, f)

	router := netip.PrefixFrom(f.Router, 32)
	if err := f.AddAllow(ctx, p.VMID, router); err != nil {
		t.Fatal(err)
	}
	capt, _ := f.Pktmon(ctx, []winvm.PktFilter{{Addr: f.Router, Proto: "TCP", Port: 22}})
	if _, err := f.Pktmon(ctx, nil); err == nil {
		t.Fatal("a second pktmon session started")
	}
	f.TCP(ctx, netip.AddrPortFrom(f.Router, 22), "tok")
	recs, _ := capt.Stop(ctx)
	if !slices.ContainsFunc(recs, func(r winvm.PktRecord) bool { return r.Class == "physical" && !r.Dropped }) {
		t.Fatalf("red: the probe to the router did not leave through the physical NIC: %+v", recs)
	}
	if err := f.RemoveAllow(ctx, p.VMID, router); err != nil {
		t.Fatal(err)
	}

	// The own-IP Deny must go before an Allow on the same prefix.
	gw := netip.PrefixFrom(p.Gateway, 32)
	if err := f.AddDeny(ctx, p.VMID, p.Owner, []netip.Prefix{gw}); err != nil {
		t.Fatal(err)
	}
	if err := f.AddAllow(ctx, p.VMID, gw); err == nil || !strings.Contains(err.Error(), "0x800700B7") {
		t.Fatalf("an Allow beside a Deny on one prefix: %v", err)
	}
	if err := f.RemoveDeny(ctx, p.VMID, p.Owner, []netip.Prefix{gw}); err != nil {
		t.Fatal(err)
	}

	// Red 4: L2 and L3 both open for the gateway, and the canary hears it.
	// With L3 back on, the same probe is stopped by the host firewall alone.
	c, _ := f.Canary(ctx, p.Gateway, 40010, netip.PrefixFrom(p.GuestIP, 32))
	defer c.Close()
	if err := f.AddAllow(ctx, p.VMID, gw); err != nil {
		t.Fatal(err)
	}
	f.TCP(ctx, netip.AddrPortFrom(p.Gateway, 40010), "red4")
	if len(c.Arrived()) != 0 {
		t.Fatal("L3 alone did not hold")
	}
	if err := f.SetL3(ctx, p.Names, p.Owner, false); err != nil {
		t.Fatal(err)
	}
	f.TCP(ctx, netip.AddrPortFrom(p.Gateway, 40010), "red4")
	if got := c.Arrived(); !slices.Equal(got, []string{"red4"}) {
		t.Fatalf("red 4: arrived %v", got)
	}

}

// The gateway ARP trap: the Deny over 172.16/12 also drops the guest's ARP for
// the gateway, so without the guest's static entry nothing reaches the
// internet. With it, the internet works and the gateway itself stays denied.
func TestFakeHyperVGatewayARP(t *testing.T) {
	ctx := context.Background()
	f := hvfake.New()
	build(t, f, false)
	if _, err := f.Egress(ctx); err == nil {
		t.Fatal("the guest reached the internet with no way to find the gateway")
	}
	f = hvfake.New()
	p := build(t, f, true)
	if got, err := f.Egress(ctx); err != nil || got != f.PublicIP {
		t.Fatalf("with the static entry: %v %v", got, err)
	}
	if err := fenceHolds(t, f, p); err != nil {
		t.Fatal(err)
	}
}

// The tailnet resolver answers only through an Allow, and a Deny is counted.
func TestFakeHyperVMagicDNS(t *testing.T) {
	ctx := context.Background()
	f := hvfake.New()
	p := buildFenced(t, f)
	resolver := netip.MustParseAddr("100.100.100.100")
	if _, err := f.DNS(ctx, resolver, "host.example.ts.net"); err == nil {
		t.Fatal("green: the tailnet resolver answered through the Deny")
	}
	if err := f.AddAllow(ctx, p.VMID, netip.PrefixFrom(resolver, 32)); err != nil {
		t.Fatal(err)
	}
	got, err := f.DNS(ctx, resolver, "host.example.ts.net")
	if err != nil || len(got) != 1 || !got[0].Is4() {
		t.Fatalf("red: %v %v", got, err)
	}
}

// The IPv6 path, red first: with ms_tcpip6 bound, the ::/0 Deny gone and L3
// off, the host's link-local canary hears the guest. With the stack unbound
// the frame is still delivered to the vNIC, and nothing is heard.
func TestFakeHyperVIPv6Stack(t *testing.T) {
	ctx := context.Background()
	f := hvfake.New()
	p := buildFenced(t, f)
	ll := f.VNICLinkLocal(p.Names.Switch)
	to := netip.AddrPortFrom(ll, 40020)
	c, _ := f.Canary(ctx, ll, to.Port(), netip.MustParsePrefix("fe80::/10"))
	defer c.Close()
	live, _ := f.ReadLive(ctx, p.Names, p.VMID, p.SwitchID)
	f.TCP(ctx, to, "v6")
	if len(c.Arrived()) != 0 {
		t.Fatal("the planned fence let IPv6 reach the host")
	}
	f.RemoveDeny(ctx, p.VMID, p.Owner, []netip.Prefix{netip.MustParsePrefix("::/0")})
	f.SetL3(ctx, p.Names, p.Owner, false)
	f.SetBinding(ctx, live.VNICIfIndex, "ms_tcpip6", true)
	f.TCP(ctx, to, "v6")
	if len(c.Arrived()) != 1 {
		t.Fatal("red: with the stack bound the probe was not heard, so the target is wrong")
	}
	f.SetBinding(ctx, live.VNICIfIndex, "ms_tcpip6", false)
	capt, _ := f.Pktmon(ctx, []winvm.PktFilter{{Addr: ll}})
	f.TCP(ctx, to, "v6")
	recs, _ := capt.Stop(ctx)
	if len(c.Arrived()) != 1 {
		t.Fatal("green: with the stack unbound the probe was heard")
	}
	if !slices.ContainsFunc(recs, func(r winvm.PktRecord) bool { return r.Class == "vnic" }) {
		t.Fatal("green proves nothing: the frame never reached the vNIC")
	}
}

// hv_sock: a registered listener for every VM accepts the guest; once its
// registration is gone it refuses, and the guard sees the new key while it is
// there.
func TestFakeHyperVHVSock(t *testing.T) {
	ctx := context.Background()
	f := hvfake.New()
	p := buildFenced(t, f)
	const svc = "0000BEEF-FACB-11E6-BD58-64006A7986D3"
	l, _ := f.HVListener(ctx, "00000000-0000-0000-0000-000000000000", svc, true)
	if ok, _ := f.HVSock(ctx, svc, "t"); !ok {
		t.Fatal("red: the registered listener refused")
	}
	if !slices.ContainsFunc(diffNow(t, f, p), func(c winvm.Change) bool { return c.Field == "gcs" }) {
		t.Fatal("the new registration is not drift")
	}
	l.Close()
	if ok, _ := f.HVSock(ctx, svc, "t"); ok {
		t.Fatal("green: the listener accepted after its registration was deleted")
	}
	if cs := diffNow(t, f, p); len(cs) != 0 {
		t.Fatalf("after cleanup: %+v", cs)
	}
}

// The fault hooks.
func TestFakeHyperVFaultHooks(t *testing.T) {
	ctx := context.Background()
	f := hvfake.New()
	p := buildFenced(t, f)

	boom := errors.New("boom")
	f.FailNext("ReadLive", boom)
	if _, err := f.ReadLive(ctx, p.Names, p.VMID, p.SwitchID); !errors.Is(err, boom) {
		t.Fatalf("FailNext: %v", err)
	}
	if _, err := f.ReadLive(ctx, p.Names, p.VMID, p.SwitchID); err != nil {
		t.Fatalf("FailNext fails only once: %v", err)
	}

	live, _ := f.ReadLive(ctx, p.Names, p.VMID, p.SwitchID)
	f.SetBinding(ctx, live.VNICIfIndex, "ms_rspndr", true)
	if !slices.ContainsFunc(diffNow(t, f, p), func(c winvm.Change) bool { return c.Field == "bindings" }) {
		t.Fatal("SetBinding: a binding back on is not drift")
	}
	f.SetBinding(ctx, live.VNICIfIndex, "ms_rspndr", false)

	f.SetGCS(append(live.GCS, "9D6C2B11-0000-4000-8000-000000000001"))
	if !slices.ContainsFunc(diffNow(t, f, p), func(c winvm.Change) bool { return c.Field == "gcs" }) {
		t.Fatal("SetGCS: a new key is not drift")
	}
	f.SetGCS(live.GCS)

	// A public route through the tunnel needs a Deny it does not have yet.
	f.SetRoute(winvm.Route{Prefix: netip.MustParsePrefix("198.51.100.0/24"), IfIndex: 15, NextHop: netip.IPv4Unspecified(), Metric: 5})
	if !slices.ContainsFunc(diffNow(t, f, p), func(c winvm.Change) bool { return c.Field == "acl" }) {
		t.Fatal("SetRoute: a new tunnel route did not ask for a Deny")
	}
	f.AddDeny(ctx, p.VMID, p.Owner, []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")})
	if cs := diffNow(t, f, p); len(cs) != 0 {
		t.Fatalf("with the tunnel Deny added: %+v", cs)
	}

	// Drop counters rise per denied datagram, and a reset reads as one.
	var tr winvm.DropTracker
	s, _ := f.DropCounters(ctx, p.VMID, p.AdapterID)
	tr.Add(s)
	f.UDP(ctx, netip.AddrPortFrom(f.LANIP, 53), "x", 100)
	s, _ = f.DropCounters(ctx, p.VMID, p.AdapterID)
	if d := tr.Add(s); d != 100 {
		t.Fatalf("100 denied datagrams counted %d", d)
	}
	f.ResetCounters()
	f.UDP(ctx, netip.AddrPortFrom(f.LANIP, 53), "x", 7)
	s, _ = f.DropCounters(ctx, p.VMID, p.AdapterID)
	if d := tr.Add(s); d != 7 {
		t.Fatalf("after a reset, 7 denied datagrams counted %d", d)
	}

	// Disconnect and the switch-port count Diff expects.
	f.Disconnect(ctx, p.VMID, p.Owner, p.AdapterID)
	if cs := diffNow(t, f, p); len(cs) != 0 {
		t.Fatalf("a disconnected VM with the right config: %+v", cs)
	}
	if !slices.Contains(f.Calls(), "Disconnect") {
		t.Fatal("the call log missed Disconnect")
	}
}
