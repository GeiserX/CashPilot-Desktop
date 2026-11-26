//go:build windows

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// createNoWindow keeps PowerShell from flashing a console over the app.
const createNoWindow = 0x08000000

// powerShell returns stdout alone: PowerShell writes progress records to stderr as
// CLIXML even with nothing to report, which is not part of any answer. On failure
// the error carries stderr.
func powerShell(args ...string) ([]byte, error) {
	return powerShellContext(context.Background(), args...)
}

// powerShellContext is powerShell, stopped when ctx ends. It runs System32's
// Windows PowerShell by its full path (powerShellCommand), never from PATH.
func powerShellContext(ctx context.Context, args ...string) ([]byte, error) {
	cmd, err := powerShellCommand(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("could not find Windows PowerShell: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// %w keeps the *exec.ExitError reachable, so RunElevated can read the exit code.
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func readFirewallRule(name string) (firewallState, error) {
	out, err := powerShell("-EncodedCommand", psEncode(firewallCheckScript(name)))
	if err != nil {
		return firewallState{}, fmt.Errorf("could not read the Windows Firewall: %v", err)
	}
	return parseFirewallState(out)
}

// RunElevated runs a script with admin rights. An elevated app runs it directly;
// otherwise Windows shows its admin prompt, and a "no" there fails the call with
// exit code ExitCancelled (ElevatedArgs).
//
// The script gets $CashPilotUserSid (UserSIDVariable): the SID of the account
// signed in to the app, read here before the prompt, because the elevated script
// may run as a different account. An exit code of 3010 or 1641 returns
// RestartNeeded with no error.
func RunElevated(script string) (ElevatedResult, error) {
	return RunElevatedContext(context.Background(), script)
}

// RunElevatedContext is RunElevated that stops waiting when ctx ends. A script
// already running as admin cannot be stopped from here, so a script that runs a
// slow tool bounds that tool itself and exits ExitTimeout; ctx is the backstop.
func RunElevatedContext(ctx context.Context, script string) (ElevatedResult, error) {
	sid, err := currentUserSID()
	if err != nil {
		return ElevatedResult{}, fmt.Errorf("could not read which Windows account is signed in: %w", err)
	}
	args, err := ElevatedArgs(script, sid, windows.GetCurrentProcessToken().IsElevated())
	if err != nil {
		return ElevatedResult{}, err
	}
	_, err = powerShellContext(ctx, args...)
	if ctx.Err() != nil {
		return ElevatedResult{}, fmt.Errorf("the admin step did not finish in time: %w", ctx.Err())
	}
	return elevatedOutcome(err)
}

// currentUserSID is the SID of the account this process runs as.
func currentUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	if user.User.Sid == nil {
		return "", errors.New("the process token names no user")
	}
	return user.User.Sid.String(), nil
}

// ensureFirewallBlock makes sure the outbound block rule for program and nets is in
// place, asking for admin rights only when it is missing or differs. It fails when
// the rule cannot be confirmed, so the caller never starts the binary without it.
func ensureFirewallBlock(slug, program string, nets []string) error {
	return ensureFirewallBlockWith(slug, program, nets, true)
}

// ensureFirewallBlockWith is ensureFirewallBlock that raises the admin prompt only
// when mayElevate is set (the person approved it in the app). Otherwise a missing
// rule fails with AdminApprovalNeeded and Windows shows nothing.
func ensureFirewallBlockWith(slug, program string, nets []string, mayElevate bool) error {
	nets, err := validFirewallNetworks(nets)
	if err != nil {
		return err
	}
	return ensureFirewallRule(slug, program, nets, mayElevate, readFirewallRule, RunElevated)
}

// firewallReady reports whether the rule matches and whether it exists,
// reading it without admin rights. A firewall that would ignore the rule is an
// error, not "missing": no admin prompt can fix it, so the app must say what can.
func firewallReady(slug, program string, nets []string) (ready, found bool, err error) {
	what := fmt.Sprintf("%s was not started: it needs a Windows Firewall rule that blocks it from %s", slug, strings.Join(nets, ", "))
	st, err := readFirewallRule(firewallRuleName(slug, program))
	if err != nil {
		return false, false, fmt.Errorf("%s. CashPilot could not read the rule before the admin step: %w", what, err)
	}
	ready, err = firewallReadiness(st, program, nets, what)
	return ready, st.Found, err
}

// firewallBlockExists reads whether CashPilot's rule exists for the old binary.
func firewallBlockExists(slug, program string) (bool, error) {
	state, err := readFirewallRule(firewallRuleName(slug, program))
	return state.Found, err
}

// removeFirewallBlock deletes the rule for this program if it exists and this
// operation can ask for admin rights.
func removeFirewallBlock(slug, program string, mayElevate bool) error {
	exists, err := firewallBlockExists(slug, program)
	if err != nil || !exists {
		return err
	}
	if !mayElevate {
		return &AdminApprovalNeeded{Slug: slug}
	}
	name := firewallRuleName(slug, program)
	if _, err := RunElevated(firewallRemoveScript(name)); err != nil {
		return firewallRemoveFailed(name, err)
	}
	return nil
}

// ReadNamedFirewallRule reads one local firewall rule by its unique name.
func ReadNamedFirewallRule(name string, port int, subnets []string) (NamedFirewallRule, error) {
	script := `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
` + namedFirewallRuleScript(name)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	raw, err := powerShellContext(ctx, "-EncodedCommand", psEncode(script))
	if err != nil {
		return NamedFirewallRule{}, err
	}
	var rule struct {
		Found                                                        bool
		Program, Enabled, Direction, Action, Profile, Port, Protocol string
		Remote                                                       []string
	}
	if err := json.Unmarshal(raw, &rule); err != nil {
		return NamedFirewallRule{}, err
	}
	got := make([]string, 0, len(rule.Remote))
	for _, subnet := range rule.Remote {
		if prefix, err := netip.ParsePrefix(subnet); err == nil {
			got = append(got, prefix.Masked().String())
		} else if ipv4, ok := windowsRemoteAddresses(subnet); ok {
			got = append(got, ipv4...)
		}
	}
	slices.Sort(got)
	want := slices.Clone(subnets)
	slices.Sort(want)
	result := NamedFirewallRule{Found: rule.Found}
	if rule.Found && rule.Enabled == "True" && rule.Direction == "Inbound" && rule.Action == "Allow" &&
		rule.Profile == "Private" && rule.Port == strconv.Itoa(port) && (rule.Protocol == "TCP" || rule.Protocol == "6") &&
		len(got) == len(rule.Remote) && slices.Equal(got, want) {
		result.Program = rule.Program
	}
	return result, nil
}
