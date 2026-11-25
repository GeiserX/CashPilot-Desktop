//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"

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

// powerShellContext is powerShell, stopped when ctx ends.
func powerShellContext(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", append([]string{"-NoProfile", "-NonInteractive"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
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
	nets, err := validFirewallNetworks(nets)
	if err != nil {
		return err
	}
	name := firewallRuleName(slug, program)
	what := fmt.Sprintf("%s was not started: it needs a Windows Firewall rule that blocks it from %s", slug, strings.Join(nets, ", "))
	st, err := readFirewallRule(name)
	if err != nil {
		return fmt.Errorf("%s, and %v", what, err)
	}
	if why := st.notEnforced(); why != "" {
		return fmt.Errorf("%s, and %s, so the rule would do nothing. Fix that, or run %s in Docker", what, why, slug)
	}
	if st.matches(program, nets) {
		return nil
	}
	if _, err := RunElevated(firewallCreateScript(name, program, nets)); err != nil {
		return fmt.Errorf("%s, and creating it failed (approve the Windows admin prompt and try again): %v", what, err)
	}
	st, err = readFirewallRule(name)
	if err != nil {
		return fmt.Errorf("%s, and %v", what, err)
	}
	if !st.matches(program, nets) {
		return fmt.Errorf("%s, and the rule is still not in place after creating it", what)
	}
	// The firewall can be switched off while the admin prompt waits.
	if why := st.notEnforced(); why != "" {
		return fmt.Errorf("%s, and %s, so the rule would do nothing. Fix that, or run %s in Docker", what, why, slug)
	}
	return nil
}

// removeFirewallBlock deletes the rule for this program, asking for admin rights
// only when it exists.
func removeFirewallBlock(slug, program string) error {
	name := firewallRuleName(slug, program)
	st, err := readFirewallRule(name)
	if err != nil {
		return err
	}
	if !st.Found {
		return nil
	}
	if _, err := RunElevated(firewallRemoveScript(name)); err != nil {
		return fmt.Errorf("the Windows Firewall rule %q is still there (approve the Windows admin prompt and remove again, or delete it yourself with Remove-NetFirewallRule -DisplayName %s): %v", name, psQuote(name), err)
	}
	return nil
}
