package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"sync"

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

type workerFirewallState struct {
	mu         sync.Mutex
	generation uint64
	// Only one Allow may run through elevation or rollback at a time, so a stale
	// Allow cannot remove the rule that a newer Allow just installed.
	allowing          bool
	leftWhileAllowing bool
}

type serverCommandsRule struct {
	name    string
	program string
}

func (a *App) savedServerCommandsRule() serverCommandsRule {
	cfg := a.cfg.Config()
	return serverCommandsRule{strings.TrimSpace(cfg.ServerCommandsFirewallRule), cfg.ServerCommandsFirewallProgram}
}

func (a *App) saveServerCommandsRule(rule serverCommandsRule) error {
	return a.cfg.Update(func(c *config.AppConfig) {
		c.ServerCommandsFirewallRule = rule.name
		c.ServerCommandsFirewallProgram = rule.program
	})
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
	program, err := thisProgram()
	if err != nil {
		return a.serverCommandsState(), fmt.Errorf("could not find this program's path: %w", err)
	}
	firewall := &a.workerFirewall
	firewall.mu.Lock()
	if !a.joined() {
		firewall.mu.Unlock()
		return a.serverCommandsState(), errors.New("this computer is not joined to a CashPilot")
	}
	if firewall.allowing {
		firewall.mu.Unlock()
		return a.serverCommandsState(), errors.New("a firewall rule request is already in progress")
	}
	previous := a.savedServerCommandsRule()
	// The replacement and the old rule share an ID. Retire its saved path
	// before changing Windows so rollback can delete the rule even if config
	// writes fail after the change.
	if previous != (serverCommandsRule{}) {
		if err := a.saveServerCommandsRule(serverCommandsRule{}); err != nil {
			firewall.mu.Unlock()
			return a.serverCommandsState(), fmt.Errorf("could not clear the previous firewall record: %w", err)
		}
	}
	generation := firewall.generation
	firewall.allowing = true
	firewall.leftWhileAllowing = false
	firewall.mu.Unlock()
	defer func() {
		firewall.mu.Lock()
		firewall.allowing = false
		firewall.mu.Unlock()
	}()

	rule := serverCommandsRule{serverCommandsRuleName(), program}
	addErr := runFirewallScript(addRuleScript(rule.name, rule.program))
	if err := a.finishServerCommandsAllow(generation, previous, rule, addErr); err != nil {
		return a.serverCommandsState(), err
	}
	// Tell the server about the listener without waiting for the next heartbeat.
	if a.ctx != nil {
		a.startUpstream(a.ctx)
	}
	return a.serverCommandsState(), nil
}

func (a *App) finishServerCommandsAllow(generation uint64, previous, rule serverCommandsRule, addErr error) error {
	firewall := &a.workerFirewall
	firewall.mu.Lock()
	var rollbackErr error
	switch {
	case addErr != nil:
		rollbackErr = fmt.Errorf("the firewall rule was not made: %w", addErr)
		if previous.name == "" || !firewall.leftWhileAllowing {
			if previous.name != "" {
				rollbackErr = errors.Join(rollbackErr, a.restoreServerCommandsRule(previous))
			}
			firewall.mu.Unlock()
			return rollbackErr
		}
		// Leave deferred removal to this request. Windows still has the old rule.
		rule = previous
	case generation != firewall.generation || !a.joined():
		rollbackErr = errors.New("the pairing changed before the firewall rule was ready. Allow the rule again while joined")
	default:
		if err := a.saveServerCommandsRule(rule); err != nil {
			rollbackErr = fmt.Errorf("could not save the firewall rule: %w", err)
		} else {
			a.syncWorkerAPI()
			firewall.mu.Unlock()
			return nil
		}
	}
	firewall.mu.Unlock()
	return errors.Join(rollbackErr, a.deleteUnrecordedServerCommandsRule(rule))
}

// removeServerCommandsRule removes the rule CashPilot made. The caller holds
// workerFirewall.mu. An in-flight Allow owns cleanup of the rule it is replacing.
func (a *App) removeServerCommandsRule() error {
	rule := a.savedServerCommandsRule()
	if rule.name == "" {
		a.workerFirewall.leftWhileAllowing = a.workerFirewall.allowing
		return nil
	}
	if err := a.saveServerCommandsRule(serverCommandsRule{}); err != nil {
		return fmt.Errorf("could not clear the record for firewall rule %q: %w", rule.name, err)
	}
	return a.deleteUnrecordedServerCommandsRule(rule)
}

// deleteUnrecordedServerCommandsRule restores the record if Windows keeps the rule.
func (a *App) deleteUnrecordedServerCommandsRule(rule serverCommandsRule) error {
	if err := runFirewallScript(removeRuleScript()); err != nil {
		return errors.Join(fmt.Errorf("the firewall rule %q could not be removed: %w", rule.name, err), a.restoreServerCommandsRule(rule))
	}
	return nil
}

func (a *App) restoreServerCommandsRule(rule serverCommandsRule) error {
	if err := a.saveServerCommandsRule(rule); err != nil {
		return fmt.Errorf("could not record the installed firewall rule %q: %w", rule.name, err)
	}
	return nil
}
