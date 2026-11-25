package winvm_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// TestGuestProbeOverPowerShellDirect drives the product GuestProbe against
// the fake's PowerShell Direct: it checks every value before it reaches the
// guest, and reads the guest's answers.
func TestGuestProbeOverPowerShellDirect(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	if _, err := winvm.Build(context.Background(), buildDeps(t, f, dir), setupParams()); err != nil {
		t.Fatal(err)
	}
	p := readPlan(t, dir)
	f.GuestAnswers = map[string]any{
		"probe-tcp":    map[string]any{"sent": true, "result": "timeout"},
		"probe-dns":    map[string]any{"answers": []string{"100.64.0.5"}},
		"probe-egress": map[string]any{"ip": "203.0.113.7"},
		"probe-hvsock": map[string]any{"connected": false, "result": "refused"},
	}
	g := winvm.NewGuestProbe(f, p.VMID, p.Owner, winvm.Cred{User: "cpadmin", Password: setupParams().AdminPassword})
	ctx := context.Background()
	if sent, err := g.TCP(ctx, netip.MustParseAddrPort("192.0.2.10:45000"), "cpd-0123456789abcdef"); err != nil || !sent {
		t.Fatalf("TCP: %v %v", sent, err)
	}
	if as, err := g.DNS(ctx, netip.MustParseAddr("100.100.100.100"), "host.example.ts.net"); err != nil || len(as) != 1 {
		t.Fatalf("DNS: %v %v", as, err)
	}
	if ip, err := g.Egress(ctx); err != nil || ip != f.PublicIP {
		t.Fatalf("Egress: %v %v", ip, err)
	}
	if ok, err := g.HVSock(ctx, "0000AAAA-FACB-11E6-BD58-64006A7986D3", "cpd-1"); err != nil || ok {
		t.Fatalf("HVSock: %v %v", ok, err)
	}
	if err := g.RawFrame(ctx, 0x88b5, "FF-FF-FF-FF-FF-FF", nil, 1); err != winvm.ErrUnsupported {
		t.Fatalf("RawFrame: %v", err)
	}
	m := len(f.Calls())
	for name, call := range map[string]func() error{
		"token with a quote": func() error {
			_, err := g.TCP(ctx, netip.MustParseAddrPort("192.0.2.10:45000"), "a'b")
			return err
		},
		"port 0": func() error { _, err := g.TCP(ctx, netip.MustParseAddrPort("192.0.2.10:0"), "t"); return err },
		"DNS name with a space": func() error {
			_, err := g.DNS(ctx, netip.MustParseAddr("100.100.100.100"), "a b")
			return err
		},
		"hv_sock service not a GUID": func() error { _, err := g.HVSock(ctx, "x", "t"); return err },
		"UDP of 0 datagrams":         func() error { return g.UDP(ctx, netip.MustParseAddrPort("192.0.2.10:53"), "t", 0) },
	} {
		if call() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(f.Calls()) != m {
		t.Fatalf("a refused probe reached the guest: %v", f.Calls()[m:])
	}
}

func TestGuestHVSockRejectsInconclusiveResults(t *testing.T) {
	ctx := context.Background()
	f := hvfake.New()
	dir := t.TempDir()
	if _, err := winvm.Build(ctx, buildDeps(t, f, dir), setupParams()); err != nil {
		t.Fatal(err)
	}
	g := winvm.NewGuestProbe(f, readPlan(t, dir).VMID, readPlan(t, dir).Owner, winvm.Cred{User: "cpadmin", Password: setupParams().AdminPassword})
	for _, result := range []string{"timeout", "refused:10049", "socket-error:10060", ""} {
		t.Run(result, func(t *testing.T) {
			f.GuestAnswers = map[string]any{"probe-hvsock": map[string]any{"connected": false, "result": result}}
			if _, err := g.HVSock(ctx, "0000AAAA-FACB-11E6-BD58-64006A7986D3", "cpd-1"); err == nil {
				t.Fatalf("the guest result %q counted as an explicit refusal", result)
			}
		})
	}
}
