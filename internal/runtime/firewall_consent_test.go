package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// THE RULE: Windows never shows the admin prompt for a firewall rule before the
// person clicked Continue on CashPilot's own screen. A deploy with no approval
// stops with AdminApprovalNeeded, runs nothing and records nothing. One approval
// lets one launch raise the prompt; a later launch needs a new one.
func TestNativeDeployNeverRaisesTheAdminPromptUnapproved(t *testing.T) {
	p := newTestNativeProvider(t)
	var fw firewallCalls
	fw.install(p)
	art := tarGzArtifact(t, stubBinName(), stubBinaryBytes(t))
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("fwsvc", url, sha256hex(art), "tar.gz")
	svc.Native.Binaries[0].FirewallBlock = []string{"100.64.0.0/10"}
	svc.Name = "Firewall Service"
	spec := DeploySpec{Slug: "fwsvc", Service: svc, Env: stubEnv(marker)}

	_, err := p.Deploy(context.Background(), spec, nil)
	var need *AdminApprovalNeeded
	if !errors.As(err, &need) || need.Slug != "fwsvc" {
		t.Fatalf("Deploy with no approval: err = %v, want AdminApprovalNeeded", err)
	}
	// It points at what Catalog shows: the display name, never the slug.
	if msg := err.Error(); !strings.HasPrefix(msg, "Firewall Service was not started") || !strings.Contains(msg, "open Firewall Service in Catalog") || strings.Contains(msg, "fwsvc") {
		t.Fatalf("AdminApprovalNeeded = %q, want the display name and not the slug", msg)
	}
	if len(fw.elevate) != 0 {
		t.Fatalf("mayElevate per firewall request = %v, want no request: the deploy stops on the read", fw.elevate)
	}

	time.Sleep(300 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the binary ran without its firewall rule")
	}
	if _, ok := p.readRegistry().Entries["fwsvc"]; ok {
		t.Fatal("a launch stopped for approval was recorded as deployed")
	}

	approved := approveNativeDeploy(t, p, spec)
	if _, err := p.Deploy(context.Background(), approved, nil); err != nil {
		t.Fatalf("Deploy after approval: %v", err)
	}
	if !slices.Equal(fw.elevate, []bool{true}) {
		t.Fatalf("mayElevate per firewall request = %v, want one true", fw.elevate)
	}
	running := p.readRegistry().Entries["fwsvc"]
	if running == nil || running.PID == 0 {
		t.Fatalf("registry entry after the approved deploy = %+v, want a running pid", running)
	}

	// The approval is used up. With the rule gone (a catalog change to the
	// networks makes it differ), a redeploy with no new approval is refused before
	// it stops the running node, so the node keeps running.
	fw.inPlace = false
	if _, err := p.Deploy(context.Background(), spec, nil); !errors.As(err, &need) {
		t.Fatalf("redeploy with the rule gone and no new approval: err = %v, want AdminApprovalNeeded", err)
	}
	if e := p.readRegistry().Entries["fwsvc"]; e == nil || e.PID != running.PID || !p.slugAlive("fwsvc") {
		t.Fatalf("a refused redeploy stopped the running node: entry = %+v", e)
	}

	// A Start may not raise the prompt either.
	if err := p.Stop(context.Background(), "fwsvc", 0); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	approved = approveNativeDeploy(t, p, spec) // Continue applies to the pending Deploy.
	if err := p.Start(context.Background(), "fwsvc"); !errors.As(err, &need) {
		t.Fatalf("Start with the rule gone and approval for another call: err = %v, want AdminApprovalNeeded", err)
	}
	if !slices.Equal(fw.elevate, []bool{true, false}) {
		t.Fatalf("mayElevate per firewall request = %v, want Deploy true, Start false", fw.elevate)
	}
	if _, err := p.Deploy(context.Background(), approved, nil); err != nil {
		t.Fatalf("approved deploy after separate Start: %v", err)
	}
	_ = p.Remove(context.Background(), "fwsvc", RemoveOptions{})
}

func approveNativeDeploy(t *testing.T, p *NativeProcessProvider, spec DeploySpec) DeploySpec {
	t.Helper()
	step, err := p.AdminStep(spec.Service, spec.Slug)
	if err != nil || !step.Needed {
		t.Fatalf("AdminStep = %+v, %v, want approval", step, err)
	}
	spec.AdminApproval = &step
	return spec
}

// THE RULE: AdminStep says the prompt is coming exactly when the launch would
// raise it: a Windows binary with firewall_block whose rule is not in place. It
// names the rule and the program the launch would use, and it only reads.
func TestAdminStepNamesTheRuleTheLaunchWouldCreate(t *testing.T) {
	p := newTestNativeProvider(t)
	p.goos, p.goarch = "windows", "amd64"
	svc := catalog.Service{Name: "MystNodes", Slug: "mysterium", Native: catalog.NativeConfig{Binaries: []catalog.NativeBinary{
		{OS: "windows", Arch: "amd64", Bin: "myst.exe", FirewallBlock: []string{"100.64.0.0/10", "169.254.0.0/16"}},
		{OS: "linux", Arch: "amd64", Bin: "myst"},
	}}}
	var reads []string
	ready := false
	p.firewallReady = func(slug, program string, nets []string) (bool, bool, error) {
		reads = append(reads, program)
		return ready, ready, nil
	}

	step, err := p.AdminStep(svc, "mysterium")
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(p.baseDir, "mysterium", "myst.exe")
	if !step.Needed || step.Kind != AdminStepFirewallRule || step.Program != program ||
		step.RuleName != firewallRuleName("mysterium", program) ||
		!slices.Equal(step.Networks, []string{"100.64.0.0/10", "169.254.0.0/16"}) {
		t.Fatalf("step = %+v, want the firewall rule for %s", step, program)
	}

	ready = true
	if step, err := p.AdminStep(svc, "mysterium"); err != nil || step.Needed {
		t.Fatalf("with the rule in place: step = %+v, err = %v, want none", step, err)
	}

	// A firewall that would ignore the rule is no admin step: no prompt can fix
	// it, so the card shows the reason instead of its consent screen.
	p.firewallReady = func(slug, program string, nets []string) (bool, bool, error) {
		return false, false, firewallNotEnforced("mysterium was not started", firewallState{FirewallOff: true}.notEnforced())
	}
	if step, err := p.AdminStep(svc, "mysterium"); err == nil || step.Needed || !strings.Contains(err.Error(), "Windows Firewall is off") {
		t.Fatalf("firewall off: step = %+v, err = %v, want the reason and no step", step, err)
	}
	p.firewallReady = func(slug, program string, nets []string) (bool, bool, error) {
		reads = append(reads, program)
		return ready, ready, nil
	}

	// macOS and Linux binaries declare no rule: no step, and nothing is read.
	reads = nil
	p.goos = "linux"
	if step, err := p.AdminStep(svc, "mysterium"); err != nil || step.Needed || len(reads) != 0 {
		t.Fatalf("linux: step = %+v, err = %v, reads = %q, want none", step, err, reads)
	}

	p.goos = "windows"
	svc.Native.Binaries[0].Bin = "../myst.exe"
	if _, err := p.AdminStep(svc, "mysterium"); err == nil {
		t.Fatal("a binary path outside the service folder was accepted")
	}
}

func TestFirewallNextAsksBeforeCreating(t *testing.T) {
	prog := `C:\Users\u\AppData\Roaming\CashPilot\native\mysterium\myst.exe`
	nets := []string{"100.64.0.0/10"}
	missing := firewallState{}
	if create, err := firewallNext(missing, "mysterium", prog, nets, false, "w"); create || !errors.As(err, new(*AdminApprovalNeeded)) {
		t.Fatalf("missing rule, no approval: create=%v err=%v, want AdminApprovalNeeded", create, err)
	}
	if create, err := firewallNext(missing, "mysterium", prog, nets, true, "w"); !create || err != nil {
		t.Fatalf("missing rule, approved: create=%v err=%v, want create", create, err)
	}
	good := firewallState{Found: true, Enabled: true, Direction: 2, Profiles: 0x7FFFFFFF, Program: prog, Remote: "100.64.0.0/255.192.0.0"}
	if create, err := firewallNext(good, "mysterium", prog, nets, false, "w"); create || err != nil {
		t.Fatalf("rule in place: create=%v err=%v, want nothing to do", create, err)
	}
	// The read AdminStep makes: missing, in place, or a firewall that would
	// ignore it (an error, so the card says so and shows no consent screen).
	if ready, err := firewallReadiness(missing, prog, nets, "w"); ready || err != nil {
		t.Fatalf("readiness, missing rule: ready=%v err=%v, want not ready", ready, err)
	}
	if ready, err := firewallReadiness(good, prog, nets, "w"); !ready || err != nil {
		t.Fatalf("readiness, rule in place: ready=%v err=%v, want ready", ready, err)
	}
	ignored := good
	ignored.LocalIgnored = true
	if ready, err := firewallReadiness(ignored, prog, nets, "w"); ready || err == nil || !strings.Contains(err.Error(), "group policy") {
		t.Fatalf("readiness, rule ignored: ready=%v err=%v, want the reason", ready, err)
	}
	off := firewallState{FirewallOff: true}
	if create, err := firewallNext(off, "mysterium", prog, nets, true, "w"); create || err == nil || errors.As(err, new(*AdminApprovalNeeded)) {
		t.Fatalf("firewall off: create=%v err=%v, want a refusal without a prompt", create, err)
	} else if strings.Contains(err.Error(), "Docker") || !strings.Contains(err.Error(), "Windows Firewall is off") {
		// A program that runs natively on Windows has no Docker route from the card.
		t.Fatalf("firewall off: %q, want the reason and no Docker advice", err)
	}
}

// THE RULE: a No at the Windows prompt reads as the person's choice, with what it
// left undone and how to go ahead; any other failure keeps its own reason.
func TestFirewallCreateFailedNamesTheDeclinedPrompt(t *testing.T) {
	nets := []string{"100.64.0.0/10", "169.254.0.0/16"}
	declined := firewallCreateFailed("mysterium", nets, "w", fmt.Errorf("%w: ", exitErr(ExitCancelled))).Error()
	for _, want := range []string{"mysterium is not running", "you chose No at the Windows permission prompt", "100.64.0.0/10, 169.254.0.0/16", "If it was running before this deploy, CashPilot stopped it for the deploy, and it stays stopped", "click Deploy again and choose Yes"} {
		if !strings.Contains(declined, want) {
			t.Errorf("declined: %q lacks %q", declined, want)
		}
	}
	// The prompt shows after a redeploy stopped the running node, so a No did
	// change something: the text never says otherwise.
	if strings.Contains(declined, "Nothing else changed") || strings.Contains(declined, "nothing changed") {
		t.Errorf("declined: %q claims nothing changed, but a redeploy stopped the running node first", declined)
	}
	failed := firewallCreateFailed("mysterium", nets, "mysterium was not started", fmt.Errorf("%w: access denied", exitErr(1))).Error()
	if strings.Contains(failed, "chose No") || !strings.Contains(failed, "creating it failed") || !strings.Contains(failed, "access denied") {
		t.Errorf("failed: %q", failed)
	}
}

// slugAlive reports whether slug's managed child is alive.
func (p *NativeProcessProvider) slugAlive(slug string) bool {
	p.mu.Lock()
	mp := p.procs[slug]
	p.mu.Unlock()
	return mp != nil && p.procAlive(mp)
}

func TestFirewallDisabledAfterAdminStepReportsCreation(t *testing.T) {
	program := "myst.exe"
	nets := []string{"100.64.0.0/10"}
	state := firewallState{Found: true, Enabled: true, Direction: 2, Profiles: 0x7FFFFFFF,
		Program: program, Remote: "100.64.0.0/255.192.0.0", FirewallOff: true}
	err := verifyCreatedFirewallRule(state, program, nets, "MystNodes was not started")
	if err == nil || strings.Contains(err.Error(), "was not asked") ||
		!strings.Contains(err.Error(), "after the admin step") || !strings.Contains(err.Error(), "Windows Firewall is off") {
		t.Fatalf("post-admin firewall check: %v, want the reason and the step that already ran", err)
	}
}

func TestAdminStepIdentifiesSameNameFirewallReplacement(t *testing.T) {
	p := newTestNativeProvider(t)
	p.firewallReady = func(string, string, []string) (bool, bool, error) { return false, true, nil }
	step, err := p.firewallAdminStep("mysterium", "myst.exe", []string{"100.64.0.0/10"}, "")
	if err != nil || !step.Needed {
		t.Fatalf("step = %+v, %v", step, err)
	}
	if !step.ReplaceRule {
		t.Fatalf("step = %+v; want a same-name replacement", step)
	}
	approval := step
	approval.ReplaceRule = false
	if step.approvedBy(&approval) || !step.approvedBy(&step) {
		t.Fatal("approval to add a rule must not approve a replacement")
	}
}

func TestFirewallReadFailureNamesWhetherAdminStepRan(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprintf("after=%t", after), func(t *testing.T) {
			ran := false
			err := ensureFirewallRule("mysterium", "myst.exe", []string{"100.64.0.0/10"}, true,
				func(string) (firewallState, error) {
					if ran || !after {
						return firewallState{}, errors.New("read failed")
					}
					return firewallState{}, nil
				},
				func(string) (ElevatedResult, error) {
					ran = true
					return ElevatedResult{}, nil
				})
			phase := "before the admin step"
			if after {
				phase = "after the admin step"
			}
			if ran != after || err == nil || !strings.Contains(err.Error(), phase) {
				t.Fatalf("admin step ran = %v, error = %v; want %s", ran, err, phase)
			}
		})
	}
}
