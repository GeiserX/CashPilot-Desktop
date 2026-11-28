package winvm

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// The consented changes of the Docker checklist: the scripts that make them and
// how each ending reads. Each change runs only after its confirm screen, from a
// click, and only while its row still asks for it (the app re-reads the PC
// first). The scripts are fixed text built here; the only things that vary are
// a time limit and the --web-download switch, both chosen by the app. The
// signed-in user's SID never enters this text: RunElevated sets
// $CashPilotUserSid in front of it, read before the admin prompt and checked
// against the SID grammar there.
//
// Every script reports through its exit code, never through text, because
// Windows translates what its tools print (the test PC is Spanish).

// SetupStepFor maps each change to the checklist row that offers it.
var SetupStepFor = map[string]string{
	OpWSLInstall:       "a3",
	OpWSLUpdate:        "a4",
	OpDockerInstall:    "a5",
	OpDockerUsers:      "a8",
	OpLinuxEngine:      "a9",
	OpHypervisorLaunch: "a10",
	OpLanmanServer:     "a12",
}

// SetupTimeout is how long each change may run. A WSL install downloads its
// package; the Docker install waits on the user in winget's window.
func SetupTimeout(op string) time.Duration {
	switch op {
	case OpWSLInstall, OpWSLUpdate:
		return 15 * time.Minute
	case OpDockerInstall:
		return 60 * time.Minute
	case OpLinuxEngine:
		return 3 * time.Minute
	}
	return 2 * time.Minute
}

// SetupElevated says whether a change needs admin rights, and so goes through
// RunElevated. The Docker install runs as the user (its installer asks Windows
// itself), and the switch to Linux containers is the user's own Docker setting.
func SetupElevated(op string) bool {
	switch op {
	case OpWSLInstall, OpWSLUpdate, OpDockerUsers, OpHypervisorLaunch, OpLanmanServer:
		return true
	}
	return false
}

// Exit codes the scripts define. Bit 29 marks an application's own code, so
// none of these can be mistaken for a Windows error.
const (
	CodePolicy       uint32 = 0x20000001 // a policy on this PC blocks WSL
	CodeToolMissing  uint32 = 0x20000002 // wsl.exe or bcdedit.exe is not in System32
	CodeBadSID       uint32 = 0x20000003 // $CashPilotUserSid is not a SID
	CodeGroupMissing uint32 = 0x20000004 // the docker-users group does not exist
	CodeNotAdded     uint32 = 0x20000005 // the account is still not in docker-users
	CodeService      uint32 = 0x20000006 // the Server service could not be set or started
	CodeWingetGone   uint32 = 0x20000007 // winget is not on this PC
	CodeLaunchAuto   uint32 = 0x20000008 // hypervisorlaunchtype is already auto: nothing to change
)

// Windows codes the endings read.
const (
	codeAccessDisabledByPolicy uint32 = 1260       // ERROR_ACCESS_DISABLED_BY_POLICY
	hrAccessDisabledByPolicy   uint32 = 0x800704EC // the same, as an HRESULT
	hrMemberInAlias            uint32 = 0x80070562 // ERROR_MEMBER_IN_ALIAS: already a member
)

// winget's own results for an install that touches a restart, from its return
// code list
// (https://github.com/microsoft/winget-cli/blob/master/doc/windows/package-manager/winget/returnCodes.md).
// winget turns the installer's own exit code into one of these, so the Docker
// install never ends 3010 or 1641 itself.
const (
	// WingetRebootToFinish is APPINSTALLER_CLI_ERROR_INSTALL_REBOOT_REQUIRED_TO_FINISH:
	// "Restart your PC to finish installation."
	WingetRebootToFinish uint32 = 0x8A150109
	// WingetRebootForInstall is APPINSTALLER_CLI_ERROR_INSTALL_REBOOT_REQUIRED_FOR_INSTALL:
	// "Installation failed. Restart your PC then try again."
	WingetRebootForInstall uint32 = 0x8A15010A
	// WingetRebootInitiated is APPINSTALLER_CLI_ERROR_INSTALL_REBOOT_INITIATED:
	// "Your PC will restart to finish installation."
	WingetRebootInitiated uint32 = 0x8A15010B
)

// wslDownloadCodes are failures of the Microsoft Store or Windows Update path
// that WSL downloads through by default. Its --web-download switch fetches the
// package from GitHub instead, so these get the offer to try that.
var wslDownloadCodes = map[uint32]bool{
	0x80072EE2: true, // WININET_E_TIMEOUT
	0x80072EE7: true, // WININET_E_NAME_NOT_RESOLVED
	0x80072EFD: true, // WININET_E_CANNOT_CONNECT
	0x80072F8F: true, // WININET_E_DECODING_FAILED (clock or TLS)
	0x80070422: true, // ERROR_SERVICE_DISABLED: the Store or Update service is off
	0x80240438: true, // WU_E_PT_ENDPOINT_UNREACHABLE
	0x8024500C: true, // WU_E_REDIRECTOR_CONNECT_POLICY: Windows Update blocked
	0x803F8001: true, // the Store cannot offer the app
}

// psCode writes a code as the signed decimal PowerShell compares an HResult
// (an Int32) against.
func psCode(code uint32) string { return strconv.Itoa(int(int32(code))) }

// scriptPrelude is shared by every admin script: stop on errors, and three
// helpers. ExitWith ends with the Windows code inside an error, or fallback.
// Bounded runs a tool hidden, and stops it with ExitTimeout when it runs past
// its limit (only the tool itself, never a child it started). RebootPending
// reads Component Based Servicing's marker, a registry key, not text.
const scriptPrelude = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
function ExitWith($err, [int]$fallback) {
  $e = $err.Exception
  while ($e) {
    if ($e -is [System.ComponentModel.Win32Exception]) { exit $e.NativeErrorCode }
    if ($e -is [System.Runtime.InteropServices.COMException]) { exit $e.HResult }
    $e = $e.InnerException
  }
  exit $fallback
}
function Bounded([string]$exe, [string[]]$argv, [int]$seconds) {
  $p = Start-Process -FilePath $exe -ArgumentList $argv -WindowStyle Hidden -PassThru
  $null = $p.Handle
  if (-not $p.WaitForExit($seconds * 1000)) { try { $p.Kill() } catch { }; exit ` + "%d" + ` }
  $p.WaitForExit()
  return $p.ExitCode
}
function RebootPending { Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending' }
`

func prelude() string { return fmt.Sprintf(scriptPrelude, runtime.ExitTimeout) }

// toolLimit is the tool's own limit inside a script: the change's time limit
// less a minute, so the script stops the tool before the app stops waiting.
func toolLimit(op string) int {
	return int((SetupTimeout(op) - time.Minute) / time.Second)
}

// SetupScript builds the admin script for op. webDownload adds --web-download
// to the WSL changes, for a retry after the Store path failed or hung.
func SetupScript(op string, webDownload bool) (string, error) {
	switch op {
	case OpWSLInstall:
		return wslScript([]string{"--install", "--no-distribution"}, webDownload, toolLimit(op), true), nil
	case OpWSLUpdate:
		return wslScript([]string{"--update"}, webDownload, toolLimit(op), false), nil
	case OpDockerUsers:
		return prelude() + dockerUsersScript, nil
	case OpLanmanServer:
		return prelude() + fmt.Sprintf(lanmanScript, CodeService, CodeService, CodeService), nil
	case OpHypervisorLaunch:
		return prelude() + fmt.Sprintf(hypervisorScript, CodeToolMissing, psCode(CodeLaunchAuto), toolLimit(op)), nil
	}
	return "", fmt.Errorf("%q is not a change CashPilot makes with admin rights", op)
}

// wslScript runs System32's wsl.exe with args. The install first reads the WSL
// policy (HKLM\SOFTWARE\Policies\WSL, AllowWSL 0 is "WSL is not allowed"), so a
// managed PC stops with CodePolicy before anything runs. After the install, a
// pending Component Based Servicing restart turns success into 3010: the install
// turns on Windows features, which finish at the next start. The update turns on
// nothing, so a pending restart there belongs to something else (often Windows
// Update) and is not the update's: only wsl.exe's own 3010 or 1641 asks for one.
func wslScript(args []string, webDownload bool, seconds int, install bool) string {
	if webDownload {
		args = append(args, "--web-download")
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + a + "'"
	}
	var b strings.Builder
	b.WriteString(prelude())
	if install {
		fmt.Fprintf(&b, `$policy = Get-ItemProperty -Path 'HKLM:\SOFTWARE\Policies\WSL' -Name AllowWSL -ErrorAction SilentlyContinue
if ($policy -and $policy.AllowWSL -eq 0) { exit %s }
`, psCode(CodePolicy))
	}
	fmt.Fprintf(&b, `$wsl = Join-Path ([Environment]::SystemDirectory) 'wsl.exe'
if (-not (Test-Path -LiteralPath $wsl)) { exit %s }
$code = Bounded $wsl @(%s) %d
if ($code -ne 0) { exit $code }
`, psCode(CodeToolMissing), strings.Join(quoted, ","), seconds)
	if install {
		b.WriteString("if (RebootPending) { exit 3010 }\n")
	}
	b.WriteString("exit 0\n")
	return b.String()
}

// dockerUsersScript adds the signed-in account, by its SID, to the local group
// docker-users. The group has no well-known SID, so it is found by its name
// (Docker's installer creates it with that name in every language), through
// ADSI's WinNT provider, which calls the Net API (NetLocalGroupAddMembers).
// Nothing is compiled or written to disk. Already a member counts as done, and
// the script reads the membership back before it says done.
var dockerUsersScript = fmt.Sprintf(`if ($CashPilotUserSid -notmatch '^S-1-[0-9]+(-[0-9]+){1,15}$') { exit %[1]s }
try { $sid = New-Object System.Security.Principal.SecurityIdentifier($CashPilotUserSid) } catch { exit %[1]s }
$member = 'WinNT://' + $sid.Value
try { $group = [ADSI]('WinNT://' + [Environment]::MachineName + '/docker-users,group'); $null = $group.Path; $null = $group.Name } catch { exit %[2]s }
if (-not $group.Name) { exit %[2]s }
try { $group.Add($member) } catch {
  $e = $_.Exception
  while ($e -and -not ($e -is [System.Runtime.InteropServices.COMException])) { $e = $e.InnerException }
  if (-not ($e -and $e.HResult -eq %[3]s)) { ExitWith $_ 1 }
}
if (-not $group.IsMember($member)) { exit %[4]s }
exit 0
`, psCode(CodeBadSID), psCode(CodeGroupMissing), psCode(hrMemberInAlias), psCode(CodeNotAdded))

// lanmanScript sets the Server service to start automatically, starts it, and
// checks that it runs. Status is an enum name, never translated.
const lanmanScript = `try { Set-Service -Name LanmanServer -StartupType Automatic } catch { ExitWith $_ %d }
try { Start-Service -Name LanmanServer } catch { ExitWith $_ %d }
if ((Get-Service -Name LanmanServer).Status -ne 'Running') { exit %d }
exit 0
`

// hypervisorRead reads the hypervisorlaunchtype of the boot entry Windows
// started from into $launch, as a number, or leaves $launch $null when it cannot
// read it. It reads through the BCD WMI provider, not bcdedit's translated text:
// element 0x250000F0 (BcdOSLoaderInteger_HypervisorLaunchType) of {current},
// whose fixed GUID is {fa926493-6f1c-4193-a414-58f0b2456d1e}; 1 is Auto
// (https://learn.microsoft.com/en-us/previous-versions/windows/desktop/bcd/bcdosloaderelementtypes).
// It goes through the CIM cmdlets, which hand back each out parameter as an
// instance the next call can run a method on. The [wmiclass] chain handed back
// a bare ManagementBaseObject from OpenStore, so OpenObject threw on Windows
// PowerShell 5.1 and the read never worked. An entry that opens but has no such
// element reads as 0: nothing in it tells Windows to start its hypervisor. It
// only reads, and never throws.
const hypervisorRead = `$launch = $null
try {
  $opened = Invoke-CimMethod -Namespace root/wmi -ClassName BcdStore -MethodName OpenStore -Arguments @{ File = '' }
  if ($opened.ReturnValue) {
    $entry = Invoke-CimMethod -InputObject $opened.Store -MethodName OpenObject -Arguments @{ Id = '{fa926493-6f1c-4193-a414-58f0b2456d1e}' }
    if ($entry.ReturnValue) {
      $element = Invoke-CimMethod -InputObject $entry.Object -MethodName GetElement -Arguments @{ Type = [uint32]0x250000F0 }
      if ($element.ReturnValue) { $launch = [int64]$element.Element.Integer } else { $launch = 0 }
    }
  }
} catch { $launch = $null }
`

// hypervisorScript sets the boot entry Windows starts from to launch its
// hypervisor again. It reads the entry's hypervisorlaunchtype first
// (hypervisorRead): already Auto ends CodeLaunchAuto and changes nothing. A read
// that fails goes on to the change. It only ever sets "auto". The change takes
// effect at the next start, so success is 3010.
const hypervisorScript = `$bcd = Join-Path ([Environment]::SystemDirectory) 'bcdedit.exe'
if (-not (Test-Path -LiteralPath $bcd)) { exit %d }
` + hypervisorRead + `if ($launch -eq 1) { exit %s }
$code = Bounded $bcd @('/set','{current}','hypervisorlaunchtype','auto') %d
if ($code -ne 0) { exit $code }
exit 3010
`

// DockerInstallScript runs winget in a window the user sees, as the user, to
// install Docker Desktop. winget's manifest for Docker Desktop adds
// --accept-license to every install (its Custom switch), which would accept
// Docker's terms for the user, so --override replaces every installer switch
// with plain "install": the installer opens its own window and the user accepts
// the terms in Docker Desktop. winget's own questions are answered by the user
// in the window. A PC with no winget exits CodeWingetGone, and the app opens
// the download page instead.
var DockerInstallScript = fmt.Sprintf(`$Host.UI.RawUI.WindowTitle = 'CashPilot: install Docker Desktop'
$winget = Get-Command winget.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $winget) { exit %s }
Write-Host 'CashPilot asked winget to install Docker Desktop.'
Write-Host 'Answer winget in this window. The Docker Desktop installer then asks Windows for permission and opens its own window.'
Write-Host 'You accept Docker''s terms yourself, in Docker Desktop. CashPilot never accepts them for you.'
Write-Host ''
& $winget.Source install --id Docker.DockerDesktop --exact --source winget --override install
$code = $LASTEXITCODE
Write-Host ''
Read-Host 'Press Enter to close this window and go back to CashPilot'
exit $code
`, psCode(CodeWingetGone))

// Outcome states of a change.
const (
	OutcomeDone      = "done"      // made; the checklist reads the PC again
	OutcomeRestart   = "restart"   // made; Windows needs a restart to finish
	OutcomeSignOut   = "signout"   // made; the account must sign out and back in
	OutcomeFailed    = "failed"    // not made; Text says why
	OutcomeTimeout   = "timeout"   // ran past its time limit
	OutcomeCancelled = "cancelled" // the user said no at the Windows prompt
	OutcomePolicy    = "policy"    // a policy on this PC blocks it; stop and explain
	OutcomeFallback  = "fallback"  // the app cannot do it here; Text says how by hand
	OutcomeNote      = "note"      // nothing needed changing; Text says why
)

// RetryWebDownload is the retry a WSL change offers after a Store failure.
const RetryWebDownload = "web-download"

// Outcome is how a change ended, in plain words.
type Outcome struct {
	State string `json:"state"`
	Text  string `json:"text"`
	// Code is the exit code of a failure, as 0x%08X; "" otherwise.
	Code string `json:"code,omitempty"`
	// Retry is RetryWebDownload when a WSL change may work through GitHub.
	Retry string `json:"retry,omitempty"`
	// URL is the page a fallback opens.
	URL string `json:"url,omitempty"`
}

// SetupOutcome reads how a change ended: res and err are what the run returned,
// timedOut is true when the app stopped waiting, and webDownload whether this
// run already used --web-download. It reads exit codes only.
func SetupOutcome(op string, res runtime.ElevatedResult, err error, timedOut, webDownload bool) Outcome {
	wsl := op == OpWSLInstall || op == OpWSLUpdate
	retry := func(o Outcome) Outcome {
		if wsl && !webDownload {
			o.Retry = RetryWebDownload
		}
		return o
	}
	code, hasCode := runtime.ExitCodeOf(err)
	if timedOut || (hasCode && code == runtime.ExitTimeout) {
		mins := int(SetupTimeout(op) / time.Minute)
		text := i18n.T("go.winvm.outcome.timeout", "minutes", mins, "check", checkAgainLabel())
		if op == OpDockerInstall {
			text = i18n.T("go.winvm.outcome.timeoutWinget", "minutes", mins, "check", checkAgainLabel())
		} else if wsl {
			text = i18n.T("go.winvm.outcome.timeoutWSL", "minutes", mins, "check", checkAgainLabel())
		}
		return retry(Outcome{State: OutcomeTimeout, Text: text})
	}
	if err == nil {
		if res.RestartNeeded {
			return Outcome{State: OutcomeRestart, Text: i18n.T("go.winvm.outcome.restart")}
		}
		if op == OpDockerUsers {
			return Outcome{State: OutcomeSignOut, Text: i18n.T("go.winvm.outcome.signOut")}
		}
		return Outcome{State: OutcomeDone, Text: i18n.T("go.winvm.outcome.done")}
	}
	if !hasCode {
		return Outcome{State: OutcomeFailed, Text: i18n.T("go.winvm.outcome.runFailed", "error", err.Error())}
	}
	hex := fmt.Sprintf("0x%08X", code)
	switch code {
	case runtime.ExitCancelled:
		return Outcome{State: OutcomeCancelled, Text: i18n.T("go.winvm.outcome.cancelled")}
	case CodePolicy, codeAccessDisabledByPolicy, hrAccessDisabledByPolicy:
		return Outcome{State: OutcomePolicy, Code: hex, Text: i18n.T("go.winvm.outcome.policy")}
	case CodeToolMissing:
		return Outcome{State: OutcomeFailed, Code: hex, Text: i18n.T("go.winvm.outcome.toolMissing")}
	case CodeGroupMissing:
		return Outcome{State: OutcomeFailed, Code: hex, Text: i18n.T("go.winvm.outcome.groupMissing")}
	case CodeBadSID, CodeNotAdded:
		return Outcome{State: OutcomeFailed, Code: hex, Text: i18n.T("go.winvm.outcome.notAdded")}
	case CodeService:
		return Outcome{State: OutcomeFailed, Code: hex, Text: i18n.T("go.winvm.outcome.service")}
	case CodeLaunchAuto:
		return Outcome{State: OutcomeNote, Text: i18n.T("go.winvm.outcome.launchAuto")}
	case WingetRebootForInstall:
		return Outcome{State: OutcomeFailed, Code: hex, Text: i18n.T("go.winvm.outcome.wingetReboot", "button", i18n.T("go.winvm.guide.a5.action"))}
	case CodeWingetGone:
		return Outcome{State: OutcomeFallback, URL: DockerDownloadURL, Text: i18n.T("go.winvm.outcome.wingetGone", "check", checkAgainLabel())}
	}
	if wsl && wslDownloadCodes[code] {
		return retry(Outcome{State: OutcomeFailed, Code: hex, Text: i18n.T("go.winvm.outcome.wslDownload", "code", hex)})
	}
	return Outcome{State: OutcomeFailed, Code: hex, Text: i18n.T("go.winvm.outcome.code", "code", hex)}
}
