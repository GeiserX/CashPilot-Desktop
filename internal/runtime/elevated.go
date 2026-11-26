package runtime

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
)

// ElevatedResult is how an admin script run through RunElevated ended, when it
// succeeded. RestartNeeded is set when the script exited 3010
// (ERROR_SUCCESS_REBOOT_REQUIRED: done, effective after a restart) or 1641
// (ERROR_SUCCESS_REBOOT_INITIATED). Both are successes that ask for a restart, so
// the caller shows "restart to finish" and never "failed".
type ElevatedResult struct {
	RestartNeeded bool `json:"restartNeeded"`
	ExitCode      int  `json:"exitCode"`
}

const (
	exitRebootRequired  = 3010
	exitRebootInitiated = 1641
)

// Exit codes an admin script, or the launcher in front of it, ends with.
const (
	// ExitCancelled is ERROR_CANCELLED: the user said no at the Windows admin
	// prompt, so nothing ran.
	ExitCancelled = 1223
	// ExitTimeout is ERROR_TIMEOUT: a script stopped a tool that ran past its
	// time limit.
	ExitTimeout = 1460
)

// MaxCommandLine is the longest command line CreateProcess takes, in UTF-16
// units. An admin script travels inside one, base64-encoded twice over (once for
// the launcher, once inside it), so it must stay well under this.
const MaxCommandLine = 32767

// ElevatedArgs is what RunElevated passes to System32's powershell.exe, by its
// full path, after -NoProfile -NonInteractive: the script with the signed-in
// user's SID in front, encoded.
// An elevated app runs it directly. Otherwise a short launcher asks Windows for
// admin rights and starts System32's powershell.exe with the script, so the admin
// side never runs anything from a folder the user can write to. It refuses a
// script whose command line would pass MaxCommandLine.
func ElevatedArgs(script, userSID string, alreadyElevated bool) ([]string, error) {
	full, err := elevatedScript(script, userSID)
	if err != nil {
		return nil, err
	}
	enc := psEncode(full)
	args := []string{"-EncodedCommand", enc}
	if !alreadyElevated {
		args = []string{"-Command", elevatedLauncher(enc)}
	}
	// PowerShell's full path (at most MAX_PATH, 260), its two fixed flags, the
	// spaces and the quotes Go adds around an argument with spaces.
	n := 260 + len(` -NoProfile -NonInteractive`) + 2
	for _, a := range args {
		n += 1 + len(utf16.Encode([]rune(a))) + 2
	}
	if n > MaxCommandLine {
		return nil, fmt.Errorf("the admin script is too long for one Windows command line (%d of %d characters)", n, MaxCommandLine)
	}
	return args, nil
}

// elevatedLauncher asks Windows for admin rights and runs the encoded script in
// System32's powershell.exe, hidden, then exits with the script's exit code.
// A "no" at the prompt makes Process.Start throw a Win32Exception whose code is
// ERROR_CANCELLED (1223); the launcher exits with that code, so the caller can
// tell "you said no" from "it failed" without reading any translated text.
func elevatedLauncher(enc string) string {
	return strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$ProgressPreference = 'SilentlyContinue'",
		"$si = New-Object System.Diagnostics.ProcessStartInfo",
		"$si.FileName = Join-Path ([Environment]::SystemDirectory) 'WindowsPowerShell\\v1.0\\powershell.exe'",
		"$si.Arguments = '-NoProfile -NonInteractive -EncodedCommand " + enc + "'",
		"$si.Verb = 'runas'",
		"$si.UseShellExecute = $true",
		"$si.WindowStyle = 'Hidden'",
		"try { $p = [System.Diagnostics.Process]::Start($si) } catch { $e = $_.Exception; while ($e -and -not ($e -is [System.ComponentModel.Win32Exception])) { $e = $e.InnerException }; if ($e) { exit $e.NativeErrorCode }; exit 1 }",
		"if ($null -eq $p) { exit 1 }",
		"$p.WaitForExit()",
		"exit $p.ExitCode",
	}, "; ")
}

// ExitCodeOf is the exit code an error from RunElevated carries, as Windows
// stores it (an unsigned 32-bit value, so an HRESULT reads as 0x8...). ok is
// false when the error carries none: PowerShell could not start, or the wait
// was cut short.
func ExitCodeOf(err error) (code uint32, ok bool) {
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		return uint32(exit.ExitCode()), true
	}
	return 0, false
}

// UserSIDVariable is the PowerShell variable RunElevated sets, at the top of every
// admin script, to the SID of the account signed in to the app. When a standard
// account approves the admin prompt with an administrator's password, the script
// runs as that administrator: $env:USERNAME, HKCU and whoami all name the
// administrator. A script that adds "the current user" to a group, or writes that
// user's settings, must use this SID instead.
const UserSIDVariable = "CashPilotUserSid"

var sidPattern = regexp.MustCompile(`^S-1-[0-9]+(-[0-9]+){1,15}$`)

// elevatedScript puts the signed-in user's SID in front of script. The SID is
// checked against the SID grammar before it reaches PowerShell, so nothing but
// digits and dashes is ever placed inside the quotes.
func elevatedScript(script, userSID string) (string, error) {
	if !sidPattern.MatchString(userSID) {
		return "", fmt.Errorf("%q is not a Windows account SID", userSID)
	}
	return "$" + UserSIDVariable + " = '" + userSID + "'\n" + script, nil
}

// elevatedOutcome reads how the elevated PowerShell ended. A nil error is exit 0.
// An exit code of 3010 or 1641 is a success that needs a restart. Any other error
// is returned unchanged.
func elevatedOutcome(err error) (ElevatedResult, error) {
	if err == nil {
		return ElevatedResult{}, nil
	}
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		switch code := exit.ExitCode(); code {
		case exitRebootRequired, exitRebootInitiated:
			return ElevatedResult{RestartNeeded: true, ExitCode: code}, nil
		}
	}
	return ElevatedResult{}, err
}

// SecureVMStub runs a verified bootstrap file while holding leases on the
// recorded data and guard directories.
type SecureVMStub struct {
	DataDir      string
	Script       string // where the app wrote the bootstrap
	ScriptName   string // setup.ps1 or remove.ps1
	ScriptSHA256 string // uppercase hex
	// Args are the bootstrap's named parameters, in order.
	Args [][2]string
}

// ExitBadHash is ERROR_INVALID_DATA: a copied file did not have the SHA-256
// the stub expected, so nothing ran.
const ExitBadHash = 13

var (
	stubPath  = regexp.MustCompile(`^[A-Za-z]:\\[^"'` + "`" + `$;&|<>*?\r\n]{0,240}$`)
	stubHash  = regexp.MustCompile(`^[0-9A-F]{64}$`)
	stubName  = regexp.MustCompile(`^[a-z]+\.ps1$`)
	stubParam = regexp.MustCompile(`^[A-Z][A-Za-z]{0,31}$`)
	stubValue = regexp.MustCompile(`^[^"'` + "`" + `$;&|<>*?\r\n]{0,260}$`)
)

// Build writes the stub. Every value is checked against a strict grammar
// before it is put between single quotes, so none can end the quote.
func (s SecureVMStub) Build() (string, error) {
	for what, p := range map[string]string{"data folder": s.DataDir, "script": s.Script} {
		if !stubPath.MatchString(p) || strings.Contains(p, "..") {
			return "", fmt.Errorf("the %s %q is not an absolute Windows path", what, p)
		}
	}
	if !stubName.MatchString(s.ScriptName) {
		return "", fmt.Errorf("script name %q is not allowed", s.ScriptName)
	}
	if !stubHash.MatchString(s.ScriptSHA256) {
		return "", errors.New("the script hash is not 64 uppercase hex digits")
	}
	var args []string
	for _, a := range s.Args {
		if !stubParam.MatchString(a[0]) || !stubValue.MatchString(a[1]) {
			return "", fmt.Errorf("bootstrap argument %q is not allowed", a[0])
		}
		args = append(args, "-"+a[0], "'"+a[1]+"'")
	}
	return strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$ProgressPreference = 'SilentlyContinue'",
		"trap { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }",
		// A parent PowerShell 7 leaves its own module folders in PSModulePath,
		// and Windows PowerShell then loads PowerShell 7's Security module and
		// fails on its type data. Use only Windows PowerShell's own modules.
		"$env:PSModulePath = $env:ProgramFiles + '\\WindowsPowerShell\\Modules;' + [Environment]::SystemDirectory + '\\WindowsPowerShell\\v1.0\\Modules'",
		SecureVMDirectoryPS,
		"$bytes = [IO.File]::ReadAllBytes('" + s.Script + "')",
		"$sha = [Security.Cryptography.SHA256]::Create()",
		"try { $hash = [BitConverter]::ToString($sha.ComputeHash($bytes)).Replace('-', '') } finally { $sha.Dispose() }",
		"if ($hash -cne '" + s.ScriptSHA256 + "') { exit " + fmt.Sprint(ExitBadHash) + " }",
		"$data = '" + s.DataDir + "'",
		"$create = $" + fmt.Sprint(s.ScriptName != "remove.ps1"),
		"$root = Open-CPDDirectory $data $create 'cashpilot-data'",
		"try {",
		"  $guard = Open-CPDDirectory (Join-Path $data 'guard') $create 'cashpilot-guard'",
		"  try {",
		"    $path = Join-Path (Join-Path $data 'guard') ('.' + [guid]::NewGuid().ToString('N') + '." + s.ScriptName + "')",
		"    $file = [IO.File]::Open($path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write)",
		"    try {",
		"      try { $file.Write($bytes, 0, $bytes.Length) } finally { $file.Dispose() }",
		"      $locked = [IO.File]::Open($path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)",
		"      try {",
		"        if ((Get-FileHash -InputStream $locked -Algorithm SHA256).Hash -cne '" + s.ScriptSHA256 + "') { exit " + fmt.Sprint(ExitBadHash) + " }",
		"        $ps = Join-Path ([Environment]::SystemDirectory) 'WindowsPowerShell\\v1.0\\powershell.exe'",
		"        & $ps -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $path " + strings.Join(args, " "),
		"      } finally { $locked.Dispose() }",
		"    } finally { [IO.File]::Delete($path) }",
		"  } finally { $guard.Dispose() }",
		"} finally { $root.Dispose() }",
		"exit $LASTEXITCODE",
	}, "\n"), nil
}
