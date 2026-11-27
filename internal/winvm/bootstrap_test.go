package winvm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEmbeddedScriptPins pins the SHA-256 of every script setup embeds, so a
// change to what runs as admin or goes into the guest's answer file is always
// a deliberate one: change the script, then the pin, in the same commit.
func TestEmbeddedScriptPins(t *testing.T) {
	for name, c := range map[string]struct {
		b    []byte
		want string
	}{
		"autounattend.tpl.xml": {[]byte(answerTemplate), "9a229b7ea47ed1dde5c8dd24a5ca847f6a02c56f00a54967e320cba1af62a8c4"},
		"setup.ps1":            {setupPS1, "4a78fd10e3166d2901760767dfee813116bbe75203566741bd371ca18b7bf49a"},
		"remove.ps1":           {removePS1, "86bbff0cdd377b44a88ced46de51ed18190cf927cdd3f747c57ba61764cc2570"},
	} {
		s := sha256.Sum256(c.b)
		if got := hex.EncodeToString(s[:]); got != c.want {
			t.Errorf("%s changed: sha256 %s, pinned %s", name, got, c.want)
		}
	}
	if b := SetupBootstrap(); b.SHA256 != "0A5BADA917FC0CE865C53BC1A7B9EEFDFDC27A668AE8813F454DB0C62EDDBE4E" || b.Name != "setup.ps1" {
		t.Errorf("SetupBootstrap = %s %s", b.Name, b.SHA256)
	}
	if b := RemoveBootstrap(); b.SHA256 != "9508D95914408741A7B98FF07C0E989D1DAD3B8DBD03C95F969515C93F007EE0" || b.Name != "remove.ps1" {
		t.Errorf("RemoveBootstrap = %s %s", b.Name, b.SHA256)
	}
}

// TestBootstrapsPreserveGuard runs both bootstraps with fake task cmdlets and
// real temporary files. It never opens Task Scheduler.
func TestBootstrapsPreserveGuard(t *testing.T) {
	const fakeTask = `
param($Bootstrap, $Dir, $Mode, $Owner)
$ErrorActionPreference = 'Stop'
Import-Module Microsoft.PowerShell.Utility
$env:CPD_TEST_MARKER = Join-Path $Dir 'task-touched'
$script:stopped = $false
function Get-ScheduledTask {
  param($TaskPath, $TaskName, $ErrorAction)
  $who = if ($Mode -eq 'foreign') { 'someone else' } else { 'CashPilot Secure VM guard. cashpilot-owner=' + $Owner }
  $state = if ($script:stopped) { 'Ready' } else { 'Running' }
  [pscustomobject]@{ Description = $who; State = $state }
}
function Stop-ScheduledTask {
  param($TaskPath, $TaskName, $InputObject)
  $script:stopped = $true
  Add-Content -LiteralPath $env:CPD_TEST_MARKER -Value 'stop'
}
function Start-ScheduledTask {
  param($TaskPath, $TaskName, $InputObject)
  $script:stopped = $false
  Add-Content -LiteralPath $env:CPD_TEST_MARKER -Value 'start'
}
function Start-Process {
  if ($Mode -eq 'rejected-params') { return [pscustomobject]@{ ExitCode = 2 } }
  throw 'injected launch failure'
}
# Simulate file identities for these cross-platform task and rollback tests.
$known = @{}
$guardPath = Join-Path (Join-Path $Dir 'guard') 'CashPilot-guard.exe'
$known[$guardPath] = (Get-FileHash -LiteralPath $guardPath).Hash
function Open-CPDFileJournal($root) { return $root }
function Write-CPDFileRecord($root, $rel, $id) {
  $known[(Join-Path $root $rel)] = $id
}
function Get-CPDFileID($root, $rel) {
  $p = Join-Path $root $rel
  if (Test-Path -LiteralPath $p) { return (Get-FileHash -LiteralPath $p).Hash }
}
function Copy-CPDFile($root, $rel, $src) {
  $p = Join-Path $root $rel
  Copy-Item -LiteralPath $src -Destination $p
  return (Get-FileHash -LiteralPath $p).Hash
}
function Remove-CPDFile($root, $rel, $id) {
  $p = Join-Path $root $rel
  if ($id -and (Test-Path -LiteralPath $p) -and (Get-FileHash -LiteralPath $p).Hash -eq $id) {
    Remove-Item -LiteralPath $p -Force
  }
}
$exe = Join-Path $Dir 'new.exe'
$params = Join-Path $Dir 'original.json'
$a = @{
  Exe = $exe; ExeHash = (Get-FileHash -LiteralPath $exe).Hash
  DataDir = $Dir; TaskPath = '\CashPilot\'; TaskName = 'VMGuard'
}
if ($Bootstrap.EndsWith('setup.ps1')) {
  $a.Params = $params
  $a.ParamsHash = (Get-FileHash -LiteralPath $params).Hash
  if ($Mode -eq 'bad-params') { $a.ParamsHash = '0' * 64 }
}
if ($Mode -eq 'bad-exe') { $a.ExeHash = '0' * 64 }
& $Bootstrap @a
exit $LASTEXITCODE
`
	for _, tc := range []struct {
		name    string
		boot    Bootstrap
		mode    string
		code    int
		touched string
	}{
		{"setup foreign task", SetupBootstrap(), "foreign", 1, ""},
		{"remove bootstrap leaves task checks to Go", bootstrap("remove.ps1", removePS1), "foreign", 1, ""},
		{"setup bad parameters", SetupBootstrap(), "bad-params", 13, ""},
		{"remove bad executable", RemoveBootstrap(), "bad-exe", 13, ""},
		{"setup failure after replacement", SetupBootstrap(), "launch-failure", 1, "stop\nstart"},
		{"setup role rejects params", SetupBootstrap(), "rejected-params", 2, "stop\nstart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			guard := filepath.Join(dir, "guard")
			if err := os.Mkdir(guard, 0o700); err != nil {
				t.Fatal(err)
			}
			plan, err := json.Marshal(map[string]any{
				"names": map[string]string{"dataDir": dir, "guardTask": ProductNames().GuardTask},
				"owner": tOwner, "guardTaskId": ProductNames().GuardTask,
			})
			if err != nil {
				t.Fatal(err)
			}
			boot := filepath.Join(dir, tc.boot.Name)
			oldExe := filepath.Join(guard, "CashPilot-guard.exe")
			for path, data := range map[string][]byte{
				boot: map[string][]byte{"setup.ps1": setupPS1, "remove.ps1": removePS1}[tc.boot.Name], oldExe: []byte("working copy"),
				filepath.Join(dir, "new.exe"):       []byte("replacement"),
				filepath.Join(dir, "original.json"): []byte("{}"),
				filepath.Join(dir, PlanFile):        plan,
			} {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			gotCode, output := runFakePowerShell(t, fakeTask, boot, dir, tc.mode, tOwner)
			old, _ := os.ReadFile(oldExe)
			touched, _ := os.ReadFile(filepath.Join(dir, "task-touched"))
			gotTouched := strings.TrimSpace(strings.ReplaceAll(string(touched), "\r\n", "\n"))
			if gotCode != tc.code || string(old) != "working copy" || gotTouched != tc.touched {
				t.Fatalf("exit=%d, guard=%q, task calls=%q\n%s", gotCode, old, gotTouched, output)
			}
		})
	}
}

func runFakePowerShell(t *testing.T, script string, args ...string) (int, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake.ps1")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := fakePowerShellCommand(t, ctx, append([]string{"-ExecutionPolicy", "Bypass", "-File", path}, args...)...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, out
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || ctx.Err() != nil {
		t.Fatalf("fake PowerShell: %v\n%s", err, out)
	}
	return exit.ExitCode(), out
}
