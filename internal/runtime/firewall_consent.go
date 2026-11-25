package runtime

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// A native binary's firewall_block rule is created with admin rights, so Windows
// shows its permission prompt. CashPilot never lets that prompt appear first: the
// app asks AdminStep before a deploy, shows its own screen saying what the prompt
// is for, and only after the person clicks Continue does it call ApproveAdminStep.
// A launch with no approval that would need the prompt stops with
// AdminApprovalNeeded instead of raising it, whichever path started it.

// AdminStepFirewallRule is the one admin step a deploy can need today: the
// outbound Windows Firewall rule of a native binary's firewall_block.
const AdminStepFirewallRule = "firewall-rule"

// adminApprovalTTL bounds how long a Continue click stays good. It covers the
// binary's download, which comes before the rule.
const adminApprovalTTL = 15 * time.Minute

// AdminStep says whether deploying a service now makes Windows show its admin
// prompt, and what that prompt changes.
type AdminStep struct {
	Needed bool `json:"needed"`
	// Kind is AdminStepFirewallRule when Needed.
	Kind string `json:"kind,omitempty"`
	// RuleName is the display name the rule gets in Windows Firewall.
	RuleName string `json:"ruleName,omitempty"`
	// Program is the binary the rule covers.
	Program string `json:"program,omitempty"`
	// Networks are the ranges the rule blocks the binary from.
	Networks []string `json:"networks,omitempty"`
}

// AdminApprovalNeeded is the error of a launch whose firewall rule is missing
// and whose admin prompt nobody approved in the app.
type AdminApprovalNeeded struct{ Slug string }

func (e *AdminApprovalNeeded) Error() string {
	return fmt.Sprintf("%s was not started: it needs a Windows Firewall rule, and adding that rule makes Windows ask for admin rights. CashPilot explains that prompt before it shows: open %s in Catalog and click Deploy", e.Slug, e.Slug)
}

// AdminStep reports whether deploying svc as slug on this machine would need the
// admin prompt: its binary for this OS declares firewall_block and the rule for
// that binary is not in place yet. It only reads; it never raises a prompt.
func (p *NativeProcessProvider) AdminStep(svc catalog.Service, slug string) (AdminStep, error) {
	bin, ok := svc.NativeBinaryFor(p.goos, p.goarch)
	if !ok || len(bin.FirewallBlock) == 0 {
		return AdminStep{}, nil
	}
	nets, err := validFirewallNetworks(bin.FirewallBlock)
	if err != nil {
		return AdminStep{}, err
	}
	rel := filepath.FromSlash(bin.Bin)
	if bin.Bin == "" || !filepath.IsLocal(rel) {
		return AdminStep{}, fmt.Errorf("refusing native binary path %q: it must stay within the service directory", bin.Bin)
	}
	// The same path ensureBinary extracts to, so the rule read is the one the
	// launch checks.
	program := filepath.Join(p.baseDir, slug, rel)
	ready, err := p.firewallReady(slug, program, nets)
	if err != nil {
		return AdminStep{}, err
	}
	if ready {
		return AdminStep{}, nil
	}
	return AdminStep{
		Needed:   true,
		Kind:     AdminStepFirewallRule,
		RuleName: firewallRuleName(slug, program),
		Program:  program,
		Networks: nets,
	}, nil
}

// ApproveAdminStep records that the person clicked Continue on the app's screen
// for slug's admin step. The next launch of slug may then raise the prompt, once.
func (p *NativeProcessProvider) ApproveAdminStep(slug string) {
	p.approvalMu.Lock()
	defer p.approvalMu.Unlock()
	if p.approvals == nil {
		p.approvals = map[string]time.Time{}
	}
	p.approvals[slug] = time.Now().Add(adminApprovalTTL)
}

// takeApproval uses up slug's approval, reporting whether one was still good.
func (p *NativeProcessProvider) takeApproval(slug string) bool {
	p.approvalMu.Lock()
	defer p.approvalMu.Unlock()
	until, ok := p.approvals[slug]
	delete(p.approvals, slug)
	return ok && time.Now().Before(until)
}

// firewallNext is what ensureFirewallBlock does once it has read the rule: create
// it (true), nothing (false, nil), or stop. A rule the firewall would ignore stops
// the launch, and a missing rule stops it too unless the admin prompt may show.
func firewallNext(st firewallState, slug, program string, nets []string, mayElevate bool, what string) (bool, error) {
	if why := st.notEnforced(); why != "" {
		return false, fmt.Errorf("%s, and %s, so the rule would do nothing. Fix that, or run %s in Docker", what, why, slug)
	}
	if st.matches(program, nets) {
		return false, nil
	}
	if !mayElevate {
		return false, &AdminApprovalNeeded{Slug: slug}
	}
	return true, nil
}

// firewallCreateFailed says why creating the rule failed. A No at the Windows
// prompt (ExitCancelled) is the person's choice, not a fault: it says so, that
// nothing else changed, and how to go ahead.
func firewallCreateFailed(slug string, nets []string, what string, err error) error {
	if code, ok := ExitCodeOf(err); ok && code == ExitCancelled {
		return fmt.Errorf("%s is not running: you chose No at the Windows permission prompt, so CashPilot did not add the Windows Firewall rule that blocks it from %s, and it never runs without that rule. Nothing else changed on this PC. To run it, click Deploy again and choose Yes at the prompt", slug, strings.Join(nets, ", "))
	}
	return fmt.Errorf("%s, and creating it failed (approve the Windows admin prompt and try again): %v", what, err)
}
