package winvm_test

import (
	"context"
	"errors"
	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// replacementPrefix is a /29 next to the recorded one. Windows keys a NAT's
// ID by its name ("name;0"), so a replacement with the same name and prefix
// is identical to ours in every field the provider reports. A replacement on
// another prefix is the one Remove and Repair can tell apart.
func replacementPrefix(t *testing.T, p winvm.Plan) netip.Prefix {
	t.Helper()
	a := p.Subnet.Addr().As4()
	a[2]++
	return netip.PrefixFrom(netip.AddrFrom4(a), p.Subnet.Bits())
}

func TestRemoveLeavesForeignNATWithSameNameOnAnotherSubnet(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	p := readPlan(t, dir)
	other := replacementPrefix(t, p)
	f.AddForeignNAT(p.Names.NAT, other)
	res := remove(t, f, dir)
	if !slices.Contains(f.Objects(), "nat "+p.Names.NAT+" "+other.String()) ||
		!strings.Contains(strings.Join(res.Left, "\n"), "the NAT "+p.Names.NAT) {
		t.Fatalf("Remove did not keep and list the replacement NAT: %+v", res)
	}
}

func TestRepairRefusesReplacementNAT(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	p := readPlan(t, dir)
	f.AddForeignNAT(p.Names.NAT, replacementPrefix(t, p))
	before := f.Objects()
	if _, err := winvm.Build(context.Background(), buildDeps(t, f, dir), setupParams()); !errors.Is(err, winvm.ErrNotOwned) {
		t.Fatalf("Repair = %v, want ErrNotOwned", err)
	}
	diffObjects(t, before, f.Objects())
}

func TestRemoveLeavesReplacementGuardian(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	f.SetGuardian(true)
	res := remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), "guardian=true") ||
		!strings.Contains(strings.Join(res.Left, "\n"), "vTPM guardian") {
		t.Fatalf("Remove did not keep and list the replacement guardian: %+v", res)
	}
}

func TestClearProofLeavesCaptureFromAnotherInstall(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	p := readPlan(t, dir)
	f.LeaveProofLeftovers(p.Names, p.Owner[:len(p.Owner)-1])
	capture := filepath.Join(dir, "proof", "pktmon-1", "capture.etl")
	if err := os.MkdirAll(filepath.Dir(capture), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capture, []byte("capture evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), "pktmon=true") ||
		!strings.Contains(strings.Join(res.Left, "\n"), "pktmon session") {
		t.Fatalf("Remove did not keep and list the unverified capture: %+v", res)
	}
	if _, err := os.Stat(capture); err != nil {
		t.Fatalf("Remove touched the unverified session's capture: %v", err)
	}
}
