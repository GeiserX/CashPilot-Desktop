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
	spec := DeploySpec{Slug: "fwsvc", Service: svc, Env: stubEnv(marker)}

	_, err := p.Deploy(context.Background(), spec, nil)
	var need *AdminApprovalNeeded
	if !errors.As(err, &need) || need.Slug != "fwsvc" {
		t.Fatalf("Deploy with no approval: err = %v, want AdminApprovalNeeded", err)
	}
	if !slices.Equal(fw.elevate, []bool{false}) {
		t.Fatalf("mayElevate per firewall request = %v, want one false", fw.elevate)
	}
	time.Sleep(300 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the binary ran without its firewall rule")
	}
	if _, ok := p.readRegistry().Entries["fwsvc"]; ok {
		t.Fatal("a launch stopped for approval was recorded as deployed")
	}

	p.ApproveAdminStep("fwsvc")
	if _, err := p.Deploy(context.Background(), spec, nil); err != nil {
		t.Fatalf("Deploy after approval: %v", err)
	}
	if !slices.Equal(fw.elevate, []bool{false, true}) {
		t.Fatalf("mayElevate per firewall request = %v, want false then true", fw.elevate)
	}

	// The approval is used up: with the rule gone, a Start may not raise the prompt.
	if err := p.Stop(context.Background(), "fwsvc", 0); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	fw.inPlace = false
	if err := p.Start(context.Background(), "fwsvc"); !errors.As(err, &need) {
		t.Fatalf("Start with the rule gone and no new approval: err = %v, want AdminApprovalNeeded", err)
	}
	_ = p.Remove(context.Background(), "fwsvc", RemoveOptions{})
}

func TestAdminApprovalExpires(t *testing.T) {
	p := newTestNativeProvider(t)
	p.ApproveAdminStep("a")
	p.approvals["a"] = time.Now().Add(-time.Second)
	if p.takeApproval("a") {
		t.Fatal("an expired approval was taken")
	}
	p.ApproveAdminStep("b")
	if p.takeApproval("a") {
		t.Fatal("one slug's approval served another")
	}
	if !p.takeApproval("b") || p.takeApproval("b") {
		t.Fatal("an approval must serve exactly one launch")
	}
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
	p.firewallReady = func(slug, program string, nets []string) (bool, error) {
		reads = append(reads, program)
		return ready, nil
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
	off := firewallState{FirewallOff: true}
	if create, err := firewallNext(off, "mysterium", prog, nets, true, "w"); create || err == nil || errors.As(err, new(*AdminApprovalNeeded)) {
		t.Fatalf("firewall off: create=%v err=%v, want a refusal without a prompt", create, err)
	}
}

// THE RULE: a No at the Windows prompt reads as the person's choice, with what it
// left undone and how to go ahead; any other failure keeps its own reason.
func TestFirewallCreateFailedNamesTheDeclinedPrompt(t *testing.T) {
	nets := []string{"100.64.0.0/10", "169.254.0.0/16"}
	declined := firewallCreateFailed("mysterium", nets, "w", fmt.Errorf("%w: ", exitErr(ExitCancelled))).Error()
	for _, want := range []string{"mysterium is not running", "you chose No at the Windows permission prompt", "100.64.0.0/10, 169.254.0.0/16", "Nothing else changed", "click Deploy again and choose Yes"} {
		if !strings.Contains(declined, want) {
			t.Errorf("declined: %q lacks %q", declined, want)
		}
	}
	failed := firewallCreateFailed("mysterium", nets, "mysterium was not started", fmt.Errorf("%w: access denied", exitErr(1))).Error()
	if strings.Contains(failed, "chose No") || !strings.Contains(failed, "creating it failed") || !strings.Contains(failed, "access denied") {
		t.Errorf("failed: %q", failed)
	}
}
