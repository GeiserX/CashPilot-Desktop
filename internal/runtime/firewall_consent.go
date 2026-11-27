package runtime

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// AdminStepFirewallRule is the Windows Firewall change a native deploy can need.
const AdminStepFirewallRule = "firewall-rule"

// AdminStep says whether deploying a service now makes Windows show its admin
// prompt, and what that prompt changes.
type AdminStep struct {
	Needed bool `json:"needed"`
	// Kind is AdminStepFirewallRule when Needed.
	Kind string `json:"kind,omitempty"`
	// RuleName is the display name the rule gets in Windows Firewall.
	RuleName string `json:"ruleName,omitempty"`
	// ReplaceRule means a rule with RuleName already exists and will be recreated.
	ReplaceRule bool `json:"replaceRule,omitempty"`
	// Program is the binary the rule covers.
	Program string `json:"program,omitempty"`
	// Networks are the ranges the rule blocks the binary from.
	Networks []string `json:"networks,omitempty"`
	// RemoveRuleName is the rule from the previous deployment, if it must go.
	RemoveRuleName string `json:"removeRuleName,omitempty"`
}

// AdminApprovalNeeded is the error of a launch whose firewall rule is missing
// and whose admin prompt nobody approved in the app. Name is the service's
// display name, the one Catalog shows; the slug stands in until a caller that
// knows the name fills it.
type AdminApprovalNeeded struct {
	Slug string
	Name string
}

func (e *AdminApprovalNeeded) Error() string {
	name := e.Name
	if name == "" {
		name = e.Slug
	}
	return i18n.T("go.runtime.firewall.approvalNeeded", "name", name)
}

// NameAdminApproval gives an AdminApprovalNeeded in err the display name, so its
// message points at what Catalog shows. Any other error comes back as it was.
func NameAdminApproval(err error, name string) error {
	var need *AdminApprovalNeeded
	if name != "" && errors.As(err, &need) && need.Name == "" {
		need.Name = name
	}
	return err
}

// AdminStep reads what the deploy must change in Windows Firewall.
func (p *NativeProcessProvider) AdminStep(svc catalog.Service, slug string) (AdminStep, error) {
	bin, ok := svc.NativeBinaryFor(p.goos, p.goarch)
	if !ok {
		return AdminStep{}, nil
	}
	rel := filepath.FromSlash(bin.Bin)
	if bin.Bin == "" || !filepath.IsLocal(rel) {
		return AdminStep{}, fmt.Errorf("refusing native binary path %q: it must stay within the service directory", bin.Bin)
	}
	program := filepath.Join(p.baseDir, slug, rel)
	oldProgram := obsoleteFirewallProgram(p.readRegistry().Entries[slug], program, bin.FirewallBlock)
	return p.firewallAdminStep(slug, program, bin.FirewallBlock, oldProgram)
}

func obsoleteFirewallProgram(prev *nativeRegistryEntry, program string, nets []string) string {
	if prev != nil && len(prev.FirewallBlock) > 0 && (len(nets) == 0 || prev.BinPath != program) {
		return prev.BinPath
	}
	return ""
}

func (p *NativeProcessProvider) firewallAdminStep(slug, program string, nets []string, oldProgram string) (AdminStep, error) {
	var step AdminStep
	if len(nets) > 0 {
		nets, err := validFirewallNetworks(nets)
		if err != nil {
			return AdminStep{}, err
		}
		ready, found, err := p.firewallReady(slug, program, nets)
		if err != nil {
			return AdminStep{}, err
		}
		if !ready {
			step.RuleName = firewallRuleName(slug, program)
			step.ReplaceRule = found
			step.Program = program
			step.Networks = nets
		}
	}
	if oldProgram != "" {
		exists, err := p.firewallExists(slug, oldProgram)
		if err != nil {
			return AdminStep{}, err
		}
		if exists {
			step.RemoveRuleName = firewallRuleName(slug, oldProgram)
		}
	}
	step.Needed = step.RuleName != "" || step.RemoveRuleName != ""
	if step.Needed {
		step.Kind = AdminStepFirewallRule
	}
	return step, nil
}

// approvedBy compares the change explained on the setup card with the change
// this deploy needs now. A changed binary or set of networks needs a new approval.
func (step AdminStep) approvedBy(approval *AdminStep) bool {
	return approval != nil && step.Needed && approval.Needed &&
		step.Kind == approval.Kind && step.RuleName == approval.RuleName &&
		step.RemoveRuleName == approval.RemoveRuleName && step.ReplaceRule == approval.ReplaceRule &&
		step.Program == approval.Program && slices.Equal(step.Networks, approval.Networks)
}

// ensureFirewallRule checks a rule before and after the Windows admin step.
func ensureFirewallRule(slug, program string, nets []string, mayElevate bool, read func(string) (firewallState, error), elevate func(string) (ElevatedResult, error)) error {
	name := firewallRuleName(slug, program)
	what := i18n.T("go.runtime.firewall.what", "slug", slug, "networks", strings.Join(nets, ", "))
	st, err := read(name)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("go.runtime.firewall.readBeforeFailed", "what", what), err)
	}
	create, err := firewallNext(st, slug, program, nets, mayElevate, what)
	if err != nil || !create {
		return err
	}
	if _, err := elevate(firewallCreateScript(name, program, nets)); err != nil {
		return firewallCreateFailed(slug, nets, what, err)
	}
	st, err = read(name)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("go.runtime.firewall.verifyFailed", "what", what), err)
	}
	return verifyCreatedFirewallRule(st, program, nets, what)
}

// firewallNext is what ensureFirewallBlock does once it has read the rule: create
// it (true), nothing (false, nil), or stop. A rule the firewall would ignore stops
// the launch, and a missing rule stops it too unless the admin prompt may show.
func firewallNext(st firewallState, slug, program string, nets []string, mayElevate bool, what string) (bool, error) {
	if why := st.notEnforced(); why != "" {
		return false, firewallNotEnforced(what, why)
	}
	if st.matches(program, nets) {
		return false, nil
	}
	if !mayElevate {
		return false, &AdminApprovalNeeded{Slug: slug}
	}
	return true, nil
}

// firewallReadiness is firewallReady once the rule is read: whether it is in
// place, or why no admin prompt could make it count.
func firewallReadiness(st firewallState, program string, nets []string, what string) (bool, error) {
	if why := st.notEnforced(); why != "" {
		return false, firewallNotEnforced(what, why)
	}
	return st.matches(program, nets), nil
}

// firewallNotEnforced is the refusal when Windows Firewall would ignore the rule.
// It names the fix and nothing else: the service runs as a program on Windows,
// so there is no other way to run it from this card.
func firewallNotEnforced(what, why string) error {
	return errors.New(i18n.T("go.runtime.firewall.notEnforced", "what", what, "why", why))
}

// firewallCreateFailed says why creating the rule failed. A No at the Windows
// prompt (ExitCancelled) is the person's choice, not a fault: it says so, what it
// left stopped, and how to go ahead. The prompt shows after a redeploy stopped
// the running instance, so the text never claims nothing changed.
func firewallCreateFailed(slug string, nets []string, what string, err error) error {
	if code, ok := ExitCodeOf(err); ok && code == ExitCancelled {
		return errors.New(i18n.T("go.runtime.firewall.declined", "slug", slug, "networks", strings.Join(nets, ", ")))
	}
	return errors.New(i18n.T("go.runtime.firewall.createFailed", "what", what, "error", err))
}

func firewallRemoveFailed(name string, err error) error {
	if code, ok := ExitCodeOf(err); ok && code == ExitCancelled {
		return errors.New(i18n.T("go.runtime.firewall.removeDeclined", "rule", strconv.Quote(name)))
	}
	return fmt.Errorf("%s: %w", i18n.T("go.runtime.firewall.removeFailed", "rule", strconv.Quote(name), "command", psQuote(name)), err)
}

func verifyCreatedFirewallRule(st firewallState, program string, nets []string, what string) error {
	if !st.matches(program, nets) {
		return errors.New(i18n.T("go.runtime.firewall.notInPlace", "what", what))
	}
	// The firewall can be switched off while the admin prompt waits.
	if why := st.notEnforced(); why != "" {
		return errors.New(i18n.T("go.runtime.firewall.switchedOff", "what", what, "why", why))
	}
	return nil
}
