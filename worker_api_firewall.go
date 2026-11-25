package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/filelock"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// The Windows Firewall rule for the server-commands API (worker_api.go). Windows
// blocks inbound connections to an app that has no rule, so the server cannot reach
// the API until one exists. On Windows the API does not listen at all before then:
// a first listen with no rule makes Windows show its own prompt, whose Allow makes a
// rule for every port and every address, and whose Cancel makes a Block rule that
// beats ours. CashPilot makes the rule only when the user presses the button that
// says what it opens, through the Windows admin prompt. The rule is inbound TCP on
// workerAPIPort, for this program only, on the Private profile only, from the
// private LAN and Tailscale ranges only. Leaving the server removes it.
// Other systems need no rule.

// serverCommandsRuleName is the rule's display name.
func serverCommandsRuleName() string {
	return fmt.Sprintf("CashPilot: server commands (TCP %d)", workerAPIPort)
}

// Windows permits duplicate display names. This is the Allow rule's unique name.
const serverCommandsRuleID = "CashPilot.Desktop.ServerCommands"

// psLiteral makes a PowerShell single-quoted literal. Inside one, PowerShell ends the
// string at ' and also at the curly quotes U+2018 to U+201B; each is escaped by
// doubling it. A user folder such as O’Brien has one.
func psLiteral(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		b.WriteRune(r)
		switch r {
		case '\'', '\u2018', '\u2019', '\u201A', '\u201B':
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// addRuleScript creates or updates the scoped Allow rule. It removes the inbound
// Block rules Windows holds for this program that cover
// TCP workerAPIPort, which Windows makes when someone presses Cancel on its own
// prompt: in Windows Firewall a Block rule wins over an Allow rule, so the scoped
// rule would do nothing beside one. Only a Block rule whose protocol is Any or TCP
// and whose local ports are Any or include the port can conflict, so every other
// Block rule for this program stays. The button that runs this says so before the
// user presses it.
func addRuleScript(name, program string) string {
	subnets := make([]string, 0, len(privateNets))
	for _, s := range privateSubnets() {
		subnets = append(subnets, psLiteral(s))
	}
	id := psLiteral(serverCommandsRuleID)
	scope := " -Enabled True -Direction Inbound -Action Allow -Protocol TCP -LocalPort " + fmt.Sprint(workerAPIPort) +
		" -Profile Private -RemoteAddress @(" + strings.Join(subnets, ",") + ") -Program " + psLiteral(program)
	return strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		blocksPortFilter,
		"Get-NetFirewallApplicationFilter -Program " + psLiteral(program) + " -ErrorAction SilentlyContinue |" +
			" Get-NetFirewallRule -ErrorAction SilentlyContinue |" +
			" Where-Object { $_.Direction -eq 'Inbound' -and $_.Action -eq 'Block' -and" +
			" (Test-CoversPort ($_ | Get-NetFirewallPortFilter) " + fmt.Sprint(workerAPIPort) + ") } | Remove-NetFirewallRule",
		"if (Get-NetFirewallRule -Name " + id + " -ErrorAction SilentlyContinue) {",
		"  Set-NetFirewallRule -Name " + id + scope + " | Out-Null",
		"} else {",
		"  New-NetFirewallRule -Name " + id + " -DisplayName " + psLiteral(name) + scope + " | Out-Null",
		"}",
		"exit 0",
	}, "\n")
}

// blocksPortFilter defines Test-CoversPort: whether a rule's port filter covers TCP
// on the port. Protocol is Any, TCP or its number 6; LocalPort is a list of Any, a
// port, a range such as 8000-9000, or a keyword such as RPC, which covers no plain
// port. These values are the firewall's own names, the same in every language.
const blocksPortFilter = `function Test-CoversPort($pf, [int]$port) {
  if ($null -eq $pf) { return $false }
  if (@('Any', 'TCP', '6') -notcontains [string]$pf.Protocol) { return $false }
  foreach ($p in @($pf.LocalPort)) {
    $p = [string]$p
    if ($p -eq 'Any' -or $p -eq [string]$port) { return $true }
    if ($p -match '^(\d+)-(\d+)$' -and [int]$Matches[1] -le $port -and $port -le [int]$Matches[2]) { return $true }
  }
  return $false
}`

// removeRuleScript deletes the rule; a rule that is already gone is no error.
func removeRuleScript() string {
	return strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"Get-NetFirewallRule -Name " + psLiteral(serverCommandsRuleID) + " -ErrorAction SilentlyContinue | Remove-NetFirewallRule",
		"exit 0",
	}, "\n")
}

// firewallRuleNeeded is true where the API needs a firewall rule. A var so tests
// can drive the Windows path on any system.
var firewallRuleNeeded = stdruntime.GOOS == "windows"

// thisProgram is this program's path. A var so tests can move the program.
var thisProgram = os.Executable

// Return presence independently of readiness. A disabled or edited rule is
// still present and must be removed on Leave.
var readServerCommandsFirewall = func() (runtime.NamedFirewallRule, error) {
	return runtime.ReadNamedFirewallRule(serverCommandsRuleID, workerAPIPort, privateSubnets())
}

func (a *App) serverCommandsFirewallStatus() string {
	if !firewallRuleNeeded {
		return ""
	}
	p, err := a.cfg.Pairing()
	if err != nil {
		return "unknown"
	}
	return firewallStatusFor(p)
}

func firewallStatusFor(p config.Pairing) string {
	actual, err := readServerCommandsFirewall()
	if err != nil {
		return "unknown"
	}
	if !actual.Found {
		return "absent"
	}
	if p.RemoveFirewall {
		return "removal-pending"
	}
	program, err := thisProgram()
	if err == nil && sameProgram(actual.Program, program) && sameProgram(p.FirewallProgram, program) {
		return "ready"
	}
	return "different"
}

func sameProgram(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// An admin prompt can outlive the window. Take the same lock as the Go writer
// after elevation so an outdated prompt can neither add nor remove a rule.
var runFirewallScript = func(script, path string, p config.Pairing) error {
	expect := "$pending.removeFirewall -eq $true"
	if p.FirewallProgram != "" {
		expect = "$pending.suspended -ne $true -and $pending.removeFirewall -ne $true -and $pending.firewallProgram -eq " + psLiteral(p.FirewallProgram)
	}
	guarded := strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$lock = [IO.File]::Open(" + psLiteral(path+".lock") + ", 'Open', 'ReadWrite', 'ReadWrite')",
		"try {",
		"  $lock.Lock(0, 1)",
		"  $pending = Get-Content -Raw -Encoding UTF8 -LiteralPath " + psLiteral(path) + " | ConvertFrom-Json",
		"  if ($pending.revision -cne " + psLiteral(p.Revision) + " -or -not (" + expect + ")) { throw 'The firewall request was superseded.' }",
		strings.TrimSuffix(script, "\nexit 0"),
		"} finally { $lock.Dispose() }",
		"exit 0",
	}, "\n")
	_, err := runtime.RunElevated(guarded)
	return err
}

func (a *App) AllowServerCommandsThroughFirewall() (ServerCommandsState, error) {
	if err := a.ready(); err != nil {
		return ServerCommandsState{}, err
	}
	if !firewallRuleNeeded {
		return a.serverCommandsState(), errors.New("this computer needs no firewall rule for it")
	}
	program, err := thisProgram()
	if err != nil {
		return a.serverCommandsState(), err
	}
	owner, ok := filelock.Acquire(context.Background(), a.cfg.PairingPath()+".firewall-allow", 0)
	if !ok {
		return a.serverCommandsState(), errors.New("a firewall rule request is already in progress")
	}
	defer owner()
	p, key, err := a.cfg.ReadPairingCredential(false)
	if err != nil {
		return a.serverCommandsState(), err
	}
	if p.RemoveFirewall {
		if _, err := a.probeFirewallRemoval(p.Revision); err != nil {
			return a.serverCommandsState(), err
		}
	}
	err = a.cfg.UpdatePairing(p.Revision, func(next *config.Pairing) error {
		if key.Worker == "" || next.Suspended || next.RemoveFirewall {
			return errors.New("join your CashPilot and finish any pending firewall removal before allowing the rule")
		}
		next.FirewallProgram = program
		p = *next
		return nil
	})
	if err != nil {
		return a.serverCommandsState(), err
	}
	runErr := runFirewallScript(addRuleScript(serverCommandsRuleName(), program), a.cfg.PairingPath(), p)
	// A successful Allow has no post-elevation save. The desired program was
	// saved before elevation and Windows supplies the result after it.
	err = a.cfg.UpdatePairing(p.Revision, func(next *config.Pairing) error {
		if next.Suspended || next.RemoveFirewall || next.FirewallProgram != program {
			return config.ErrPairingChanged
		}
		actual, err := readServerCommandsFirewall()
		if err != nil {
			return err
		}
		if !sameProgram(actual.Program, program) {
			return errors.New("Windows did not confirm the rule for this program")
		}
		return nil
	})
	a.syncWorkerAPI()
	if err != nil {
		return a.serverCommandsState(), fmt.Errorf("could not allow firewall rule %q: %w", serverCommandsRuleName(), errors.Join(runErr, err))
	}
	if a.ctx != nil {
		a.startUpstream(a.ctx)
	}
	return a.serverCommandsState(), nil
}

// probeFirewallRemoval reads Windows under the revision lock. A failed read
// retains the removal, and only an absent rule clears it.
func (a *App) probeFirewallRemoval(revision string) (pending bool, err error) {
	var readErr error
	err = a.cfg.UpdatePairing(revision, func(next *config.Pairing) error {
		if next.ServerURL != "" && !next.RemoveFirewall {
			return config.ErrPairingChanged
		}
		var actual runtime.NamedFirewallRule
		actual, readErr = readServerCommandsFirewall()
		pending = actual.Found || readErr != nil
		next.RemoveFirewall = pending
		return nil
	})
	return pending, errors.Join(err, readErr)
}

func (a *App) recoverServerCommandsFirewall() {
	if !firewallRuleNeeded {
		return
	}
	p, err := a.cfg.Pairing()
	if err == nil && p.Revision == "" && a.cfg.Config().Mode == config.ModeLead {
		actual, readErr := readServerCommandsFirewall()
		if actual.Found || readErr != nil {
			err = a.cfg.PublishPairing("", "", true, config.PairingCredential{})
			if err == nil {
				p, err = a.cfg.Pairing()
			}
		}
	}
	if err == nil && p.RemoveFirewall {
		_, err = a.probeFirewallRemoval(p.Revision)
	}
	if err != nil {
		log.Printf("server commands: could not recover Windows Firewall cleanup: %v", err)
	}
}

func (a *App) firewallCleanupStatus() string {
	if !firewallRuleNeeded {
		return ""
	}
	p, err := a.cfg.Pairing()
	if err != nil {
		return "Windows Firewall cleanup status is unknown."
	}
	if p.Revision == "" || (p.ServerURL != "" && !p.RemoveFirewall) {
		return ""
	}
	pending, err := a.probeFirewallRemoval(p.Revision)
	if err != nil {
		return "Windows Firewall could not be queried. Retry removal."
	}
	if pending {
		return "The CashPilot rule is still in Windows Firewall. Approve the administrator prompt to remove it."
	}
	return ""
}

func (a *App) RetryServerCommandsFirewallRemoval() error {
	if err := a.ready(); err != nil {
		return err
	}
	a.pairingMu.Lock()
	defer a.pairingMu.Unlock()
	return a.removeServerCommandsRule()
}

func (a *App) removeServerCommandsRule() error {
	if !firewallRuleNeeded {
		return nil
	}
	err := a.removeFirewallRule()
	if err != nil {
		return fmt.Errorf("removal of Windows Firewall rule %q is pending: %w", serverCommandsRuleName(), err)
	}
	return nil
}

func (a *App) removeFirewallRule() error {
	p, err := a.cfg.Pairing()
	if err != nil {
		return err
	}
	if p.Revision == "" && !a.joined() {
		if err := a.cfg.PublishPairing("", "", true, config.PairingCredential{}); err != nil {
			return err
		}
		p, err = a.cfg.Pairing()
		if err != nil {
			return err
		}
	}
	pending, err := a.probeFirewallRemoval(p.Revision)
	if err != nil || !pending {
		return err
	}
	runErr := runFirewallScript(removeRuleScript(), a.cfg.PairingPath(), p)
	pending, err = a.probeFirewallRemoval(p.Revision)
	if err == nil && !pending {
		return nil
	}
	if runErr == nil && err == nil {
		err = errors.New("Windows still has the rule")
	}
	return errors.Join(runErr, err)
}
