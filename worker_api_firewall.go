package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
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

// firewallReady reports whether the API may listen: no rule is needed here, or
// CashPilot made it for this program.
func (a *App) firewallReady() bool {
	return !firewallRuleNeeded || a.firewallRuleMade()
}

// thisProgram is this program's path. A var so tests can move the program.
var thisProgram = os.Executable

// firewallRuleMade reports whether CashPilot made the rule for the program running
// now. A rule made for another path allows nothing to this one: listening anyway
// would make Windows show its own prompt, the case the rule exists to avoid. The
// button then shows again, and pressing it replaces the rule.
func (a *App) firewallRuleMade() bool {
	cfg := a.cfg.Config()
	if strings.TrimSpace(cfg.ServerCommandsFirewallRule) == "" {
		return false
	}
	program, err := thisProgram()
	if err != nil || strings.TrimSpace(cfg.ServerCommandsFirewallProgram) == "" {
		return false
	}
	// Windows paths ignore case.
	return strings.EqualFold(filepath.Clean(cfg.ServerCommandsFirewallProgram), filepath.Clean(program))
}

// runFirewallScript runs an admin script. A var so tests never raise a prompt.
var runFirewallScript = func(script string) error {
	_, err := runtime.RunElevated(script)
	return err
}

// AllowServerCommandsThroughFirewall makes the rule, after the user pressed the button
// that names what it opens. It is refused unless this computer is joined.
func (a *App) AllowServerCommandsThroughFirewall() (ServerCommandsState, error) {
	if err := a.ready(); err != nil {
		return ServerCommandsState{}, err
	}
	if !firewallRuleNeeded {
		return a.serverCommandsState(), errors.New("this computer needs no firewall rule for it")
	}
	if !a.joined() {
		return a.serverCommandsState(), errors.New("this computer is not joined to a CashPilot")
	}
	program, err := thisProgram()
	if err != nil {
		return a.serverCommandsState(), fmt.Errorf("could not find this program's path: %w", err)
	}
	name := serverCommandsRuleName()
	if err := runFirewallScript(addRuleScript(name, program)); err != nil {
		return a.serverCommandsState(), fmt.Errorf("the firewall rule was not made: %w", err)
	}
	if err := a.cfg.Update(func(c *config.AppConfig) {
		c.ServerCommandsFirewallRule = name
		c.ServerCommandsFirewallProgram = program
	}); err != nil {
		return a.serverCommandsState(), err
	}
	// The rule exists, so the API listens now, and the next heartbeat, sent now,
	// reports its URL.
	a.syncWorkerAPI()
	if a.ctx != nil {
		a.startUpstream(a.ctx)
	}
	return a.serverCommandsState(), nil
}

// removeServerCommandsRule removes the rule CashPilot made, if any. A failure keeps
// the rule's name saved and is reported to the user, who can delete it by that name.
func (a *App) removeServerCommandsRule() error {
	name := strings.TrimSpace(a.cfg.Config().ServerCommandsFirewallRule)
	if name == "" {
		return nil
	}
	if err := runFirewallScript(removeRuleScript()); err != nil {
		return fmt.Errorf("the firewall rule %q was not removed: %w", name, err)
	}
	return a.cfg.Update(func(c *config.AppConfig) {
		c.ServerCommandsFirewallRule = ""
		c.ServerCommandsFirewallProgram = ""
	})
}
