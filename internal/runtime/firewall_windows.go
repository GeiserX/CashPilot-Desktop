//go:build windows

package runtime

import (
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
	cmd := exec.Command("powershell.exe", append([]string{"-NoProfile", "-NonInteractive"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
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

// runElevated runs a script with admin rights. An elevated app runs it directly;
// otherwise Windows shows its admin prompt, and a "no" there fails the call. The
// wrapper stops on error: Start-Process reports a declined prompt, or a session with
// no desktop to show one on, as a non-terminating error, after which $p is null and
// a bare "exit $p.ExitCode" would exit 0.
func runElevated(script string) error {
	enc := psEncode(script)
	var err error
	if windows.GetCurrentProcessToken().IsElevated() {
		_, err = powerShell("-EncodedCommand", enc)
	} else {
		_, err = powerShell("-Command", "$ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue'; $p = Start-Process -FilePath powershell.exe -Verb RunAs -WindowStyle Hidden -Wait -PassThru "+
			"-ArgumentList '-NoProfile','-NonInteractive','-EncodedCommand','"+enc+"'; if ($null -eq $p) { exit 1 }; exit $p.ExitCode")
	}
	return err
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
	if err := runElevated(firewallCreateScript(name, program, nets)); err != nil {
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
	if err := runElevated(firewallRemoveScript(name)); err != nil {
		return fmt.Errorf("the Windows Firewall rule %q is still there (approve the Windows admin prompt and remove again, or delete it yourself with Remove-NetFirewallRule -DisplayName %s): %v", name, psQuote(name), err)
	}
	return nil
}
