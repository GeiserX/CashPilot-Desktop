package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"sync"
	"time"

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
	if firewallRuleNeeded {
		// Read the confirmed fields and the journal in the same turn. A
		// replacement could otherwise clear the journal between the two reads.
		release, err := a.lockServerCommandsFirewall()
		if err != nil {
			return false
		}
		defer release()
	}
	if _, err := a.cfg.Reload(); err != nil {
		return false
	}
	cfg := a.cfg.Config()
	pending, err := a.cfg.ReadFirewallChange()
	if err != nil || pending != nil {
		return false
	}
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

// runFirewallScript rechecks the journal under the rule lock after Windows
// grants elevation. An admin prompt can outlive the Desktop that raised it.
var runFirewallScript = func(script, journalPath string, change config.FirewallChange) error {
	guarded := strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$lock = [IO.File]::Open(" + psLiteral(journalPath+".lock") + ", 'Open', 'ReadWrite', 'ReadWrite')",
		"try {",
		"  $lock.Lock(0, 1)",
		"  $pending = Get-Content -Raw -Encoding UTF8 -LiteralPath " + psLiteral(journalPath) + " | ConvertFrom-Json",
		"  if ($pending.id -ne " + psLiteral(change.ID) + ") { throw 'The firewall request was superseded.' }",
		strings.TrimSuffix(script, "\nexit 0"),
		"  $rule = Get-NetFirewallRule -PolicyStore PersistentStore | Where-Object { $_.Name -eq " + psLiteral(serverCommandsRuleID) + " }",
		"  $program = ''",
		"  if ($null -ne $rule) {",
		"    $program = [string]($rule | Get-NetFirewallApplicationFilter).Program",
		"    if ($program -eq '') { throw 'The Windows Firewall rule has no program filter.' }",
		"  }",
		"  if ($program -ne " + psLiteral(change.NewProgram) + ") { throw 'Windows could not confirm the firewall change.' }",
		"} finally { $lock.Dispose() }",
		"exit 0",
	}, "\n")
	_, err := runtime.RunElevated(guarded)
	return err
}

var readServerCommandsFirewallProgram = func() (string, error) {
	return runtime.FirewallRuleProgram(serverCommandsRuleID)
}

type workerFirewallState struct {
	mu         sync.Mutex
	generation uint64
	allowing   bool
}

type serverCommandsRule struct {
	name    string
	program string
}

func (a *App) savedServerCommandsRule() serverCommandsRule {
	cfg := a.cfg.Config()
	return serverCommandsRule{strings.TrimSpace(cfg.ServerCommandsFirewallRule), cfg.ServerCommandsFirewallProgram}
}

func (a *App) confirmServerCommandsRule(rule serverCommandsRule) error {
	if err := a.cfg.Update(func(c *config.AppConfig) {
		c.ServerCommandsFirewallRule = rule.name
		c.ServerCommandsFirewallProgram = rule.program
	}); err != nil {
		return err
	}
	return a.cfg.SaveFirewallChange(nil)
}

func (a *App) lockServerCommandsFirewall() (func(), error) {
	release, ok := filelock.Acquire(context.Background(), a.cfg.FirewallChangePath()+".lock", 10*time.Second)
	if !ok {
		return nil, errors.New("could not lock the server commands firewall record")
	}
	return release, nil
}

func (a *App) beginFirewallChange(previous serverCommandsRule, program, action string) (config.FirewallChange, error) {
	name := previous.name
	if name == "" {
		name = serverCommandsRuleName()
	}
	change := config.FirewallChange{
		ID: rand.Text(), Name: name, OldProgram: previous.program, Action: action, NewProgram: program,
	}
	if err := a.cfg.SaveFirewallChange(&change); err != nil {
		return change, fmt.Errorf("could not record the pending firewall change: %w", err)
	}
	return change, nil
}

// reconcileServerCommandsRule records the actual rule after an interrupted
// request. The caller holds the rule lock, so a late admin script either ran
// before this check or sees that its request was cleared.
func (a *App) reconcileServerCommandsRule() error {
	pending, err := a.cfg.ReadFirewallChange()
	if err != nil || pending == nil {
		return err
	}
	program, err := readServerCommandsFirewallProgram()
	if err != nil {
		return fmt.Errorf("could not check firewall rule %q: %w", pending.Name, err)
	}
	var rule serverCommandsRule
	if program != "" {
		rule = serverCommandsRule{pending.Name, program}
	}
	if err := a.confirmServerCommandsRule(rule); err != nil {
		return fmt.Errorf("could not reconcile firewall rule %q: %w", pending.Name, err)
	}
	return nil
}

func (a *App) recoverServerCommandsFirewall() {
	if !firewallRuleNeeded {
		return
	}
	pending, err := a.cfg.ReadFirewallChange()
	if err == nil && pending != nil {
		// The helper can start while the window is waiting for Admin. Leave
		// the live request to its owner. A crash releases the owner lock.
		owner, ok := filelock.Acquire(context.Background(), a.cfg.FirewallChangePath()+".active", 0)
		if !ok {
			return
		}
		defer owner()
		release, lockErr := a.lockServerCommandsFirewall()
		if lockErr == nil {
			defer release()
			err = a.reconcileServerCommandsRule()
		} else {
			err = lockErr
		}
	}
	if err != nil {
		a.emitError("server-commands", err)
	}
}

// AllowServerCommandsThroughFirewall makes the rule after the user presses the
// button that names what it opens.
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
	owner, ok := filelock.Acquire(context.Background(), a.cfg.FirewallChangePath()+".active", 0)
	if !ok {
		return a.serverCommandsState(), errors.New("a firewall rule request is already in progress")
	}
	defer owner()

	firewall := &a.workerFirewall
	firewall.mu.Lock()
	a.refreshConfig()
	if !a.joined() {
		firewall.mu.Unlock()
		return a.serverCommandsState(), errors.New("this computer is not joined to a CashPilot")
	}
	change, err := a.prepareServerCommandsAllow(program)
	if err != nil {
		firewall.mu.Unlock()
		return a.serverCommandsState(), err
	}
	generation := firewall.generation
	firewall.allowing = true
	firewall.mu.Unlock()
	a.syncWorkerAPI()

	addErr := runFirewallScript(addRuleScript(change.Name, program), a.cfg.FirewallChangePath(), change)
	firewall.mu.Lock()
	err = a.finishServerCommandsAllow(generation, change, addErr)
	firewall.allowing = false
	firewall.mu.Unlock()
	a.syncWorkerAPI()
	if err != nil {
		return a.serverCommandsState(), err
	}
	if a.ctx != nil {
		a.startUpstream(a.ctx)
	}
	return a.serverCommandsState(), nil
}

func (a *App) prepareServerCommandsAllow(program string) (config.FirewallChange, error) {
	release, err := a.lockServerCommandsFirewall()
	if err != nil {
		return config.FirewallChange{}, err
	}
	defer release()
	if err := a.reconcileServerCommandsRule(); err != nil {
		return config.FirewallChange{}, err
	}
	return a.beginFirewallChange(a.savedServerCommandsRule(), program, "allow")
}

// finishServerCommandsAllow confirms the result or records a removal before
// rolling it back. The caller holds workerFirewall.mu.
func (a *App) finishServerCommandsAllow(generation uint64, change config.FirewallChange, addErr error) error {
	release, err := a.lockServerCommandsFirewall()
	if err != nil {
		return err
	}
	pending, err := a.cfg.ReadFirewallChange()
	if err != nil || pending == nil || pending.ID != change.ID {
		release()
		return errors.Join(err, errors.New("the firewall request was superseded"))
	}
	a.refreshConfig()
	changed := generation != a.workerFirewall.generation || !a.joined()
	rule := serverCommandsRule{change.Name, change.NewProgram}
	switch {
	case addErr != nil:
		err = fmt.Errorf("the firewall rule was not made: %w", addErr)
		probeErr := a.reconcileServerCommandsRule()
		rule = a.savedServerCommandsRule()
		if probeErr != nil || !changed || rule.name == "" {
			release()
			return errors.Join(err, probeErr)
		}
	case changed:
		err = errors.New("the pairing changed before the firewall rule was ready. Allow the rule again while joined")
	default:
		if saveErr := a.confirmServerCommandsRule(rule); saveErr == nil {
			release()
			return nil
		} else {
			err = fmt.Errorf("could not save the firewall rule: %w", saveErr)
		}
	}
	removal, recordErr := a.beginFirewallChange(rule, "", "remove")
	release()
	if recordErr != nil {
		return errors.Join(err, recordErr)
	}
	return errors.Join(err, a.finishServerCommandsRemoval(removal))
}

// removeServerCommandsRule runs after the listener closes. A same-process Allow
// does its own cleanup when the admin prompt finishes. Leave reports it pending.
func (a *App) removeServerCommandsRule() error {
	if a.workerFirewall.allowing {
		return errors.New("the Windows Firewall request is still in progress")
	}
	if owner, ok := filelock.Acquire(context.Background(), a.cfg.FirewallChangePath()+".active", 0); ok {
		defer owner()
	}
	release, err := a.lockServerCommandsFirewall()
	if err != nil {
		return err
	}
	if err := a.reconcileServerCommandsRule(); err != nil {
		release()
		return err
	}
	rule := a.savedServerCommandsRule()
	if rule.name == "" {
		release()
		return nil
	}
	change, err := a.beginFirewallChange(rule, "", "remove")
	release()
	if err != nil {
		return err
	}
	return a.finishServerCommandsRemoval(change)
}

// A failed or interrupted removal can leave a record after the computer has
// already switched modes. A repeated Leave still has work to do in that case.
func (a *App) retryServerCommandsRemoval() (LeaveResult, error) {
	a.workerFirewall.mu.Lock()
	defer a.workerFirewall.mu.Unlock()
	pending, err := a.cfg.ReadFirewallChange()
	if err != nil {
		return LeaveResult{}, err
	}
	if pending == nil && a.savedServerCommandsRule().name == "" {
		return LeaveResult{}, errors.New("this computer already runs everything here")
	}
	if err := a.removeServerCommandsRule(); err != nil {
		return LeaveResult{FirewallError: err.Error()}, nil
	}
	return LeaveResult{}, nil
}

func (a *App) finishServerCommandsRemoval(change config.FirewallChange) error {
	removeErr := runFirewallScript(removeRuleScript(), a.cfg.FirewallChangePath(), change)
	release, err := a.lockServerCommandsFirewall()
	if err != nil {
		return errors.Join(removeErr, err)
	}
	defer release()
	pending, err := a.cfg.ReadFirewallChange()
	if err != nil || pending == nil || pending.ID != change.ID {
		return errors.Join(err, removeErr, errors.New("the firewall removal was superseded"))
	}
	if removeErr != nil {
		removeErr = fmt.Errorf("the firewall rule %q could not be removed: %w", change.Name, removeErr)
		if err := a.reconcileServerCommandsRule(); err != nil {
			return errors.Join(removeErr, fmt.Errorf("could not record the installed firewall rule %q: %w", change.Name, err))
		}
		if a.savedServerCommandsRule().name == "" {
			return nil
		}
		return removeErr
	}
	if err := a.confirmServerCommandsRule(serverCommandsRule{}); err != nil {
		return fmt.Errorf("could not save removal of firewall rule %q: %w", change.Name, err)
	}
	return nil
}
