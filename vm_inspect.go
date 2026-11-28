package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// vmInspectFlag is the read-only support role: it prints the Secure VM's live
// Hyper-V state, the host's network and the drift from a desired.json, as JSON,
// and changes nothing.
const vmInspectFlag = "vm-inspect"

// vmInspectTimeout bounds the whole reading.
const vmInspectTimeout = 3 * time.Minute

// vmInspectReport is what --vm-inspect prints. A reading that failed is null,
// with its error in Errors. Drops is null when no counter source answered,
// never 0.
type vmInspectReport struct {
	VM         string                `json:"vm"`
	VMID       string                `json:"vmId"`
	Live       *winvm.LiveState      `json:"live"`
	HostNet    *winvm.HostNet        `json:"hostNet"`
	Network    *winvm.CurrentNetwork `json:"network"`
	Drops      *winvm.DropSample     `json:"drops"`
	Usage      *winvm.Usage          `json:"usage"`
	Dynamic    *winvm.Dynamic        `json:"dynamic"`
	Changes    []winvm.Change        `json:"changes"`
	FullTunnel string                `json:"fullTunnel"`
	Collision  string                `json:"collision"`
	Notes      []string              `json:"notes"`
	Errors     map[string]string     `json:"errors"`
}

// runVMInspectMain is the --vm-inspect entry point. It returns the exit code.
func runVMInspectMain(planPath string) int {
	ctx, cancel := context.WithTimeout(context.Background(), vmInspectTimeout)
	defer cancel()
	return runVMInspect(ctx, winvm.NewHost(""), planPath, os.Stdout, os.Stderr)
}

// runVMInspect reads through r, which is only a winvm.Reader: nothing here can
// disconnect, connect or change anything. Exit codes: 0 when every reading
// worked, 1 when one failed (the report is still printed), 2 when the plan
// could not be read.
func runVMInspect(ctx context.Context, r winvm.Reader, planPath string, out, errOut io.Writer) int {
	p, err := loadPlanFile(planPath)
	if err != nil {
		fmt.Fprintln(errOut, "vm-inspect:", err)
		return 2
	}
	if p.VMID == "" || p.AdapterID == "" || p.SwitchID == "" {
		fmt.Fprintln(errOut, "vm-inspect: the plan has no VM, adapter or switch ID yet")
		return 2
	}
	rep := vmInspectReport{VM: p.Names.VM, VMID: p.VMID, Errors: map[string]string{}}
	fail := func(what string, err error) { rep.Errors[what] = err.Error() }

	if l, err := r.ReadLive(ctx, p.Names, p.VMID, p.SwitchID); err != nil {
		fail("live", err)
	} else {
		rep.Live = &l
	}
	if h, err := r.ReadHostNet(ctx, winvm.EgressSamples); err != nil {
		fail("hostNet", err)
	} else {
		rep.HostNet = &h
		d := winvm.Dynamic{
			HostAddrs: winvm.HostDenies(h, p.SwitchID),
			Tunnels:   winvm.TunnelPrefixes(h, p.SwitchID, winvm.StaticDeny()),
		}
		rep.Dynamic = &d
		_, rep.FullTunnel = winvm.FullTunnel(h, p.SwitchID)
		_, rep.Collision = winvm.SubnetCollision(h, p.SwitchID, p.Subnet)
	}
	if n, err := r.ReadNetwork(ctx); err != nil {
		fail("network", err)
	} else {
		rep.Network = &n
	}
	if s, err := r.DropCounters(ctx, p.VMID, p.AdapterID); err != nil {
		fail("drops", err)
	} else {
		rep.Drops = &s
	}
	if u, err := r.Usage(ctx, p.VMID); err != nil {
		fail("usage", err)
	} else {
		rep.Usage = &u
	}
	if rep.Live != nil && rep.Dynamic != nil {
		rep.Changes = winvm.Diff(p, *rep.Live, *rep.Dynamic)
		if rep.Changes == nil {
			rep.Changes = []winvm.Change{}
		}
	}
	rep.Notes = []string{"The own public IPs are not asked here, so their Deny rows show as extra ACL rows."}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		fmt.Fprintln(errOut, "vm-inspect:", err)
		return 1
	}
	if len(rep.Errors) > 0 {
		return 1
	}
	return 0
}

// loadPlanFile reads desired.json through LoadPlan's cap and strict checks.
func loadPlanFile(path string) (winvm.Plan, error) {
	f, err := os.Open(path)
	if err != nil {
		return winvm.Plan{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil {
		return winvm.Plan{}, err
	}
	return winvm.LoadPlan(b)
}
