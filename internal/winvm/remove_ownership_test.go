package winvm_test

import (
	"context"
	"errors"
	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

func TestRemoveLeavesForeignNATWithSameNameAndSubnet(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	p := readPlan(t, dir)
	f.AddForeignNAT(p.Names.NAT, p.Subnet)
	res := remove(t, f, dir)
	if !slices.Contains(f.Objects(), "nat "+p.Names.NAT+" "+p.Subnet.String()) ||
		!strings.Contains(strings.Join(res.Left, "\n"), "the NAT "+p.Names.NAT) {
		t.Fatalf("Remove did not keep and list the replacement NAT: %+v", res)
	}
}

func TestRepairRefusesReplacementNAT(t *testing.T) {
	f := hvfake.New()
	dir := build(t, f)
	p := readPlan(t, dir)
	f.AddForeignNAT(p.Names.NAT, p.Subnet)
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
	res := remove(t, f, dir)
	if !strings.Contains(strings.Join(f.Objects(), "\n"), "pktmon=true") ||
		!strings.Contains(strings.Join(res.Left, "\n"), "pktmon session") {
		t.Fatalf("Remove did not keep and list the unverified capture: %+v", res)
	}
}
