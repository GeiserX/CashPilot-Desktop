//go:build windows

package winvm

import (
	"context"
	"encoding/base64"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"golang.org/x/sys/windows"
)

func runPS(t *testing.T, timeout time.Duration, script string) (string, uint32, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd, release, err := filePowerShellCommand(ctx, "", script)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("PowerShell ran past %v", timeout)
	}
	code, _ := runtime.ExitCodeOf(err)
	return string(out), code, err
}

func unicodeBase64(s string) string {
	units := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		b[2*i], b[2*i+1] = byte(u), byte(u>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// Windows PowerShell parses every script the app can run, with no errors.
func TestSetupScriptsParse(t *testing.T) {
	for name, s := range allScripts(t) {
		check := "$s = [Text.Encoding]::Unicode.GetString([Convert]::FromBase64String('" + unicodeBase64("$CashPilotUserSid = 'S-1-5-21-1-2-3-1001'\n"+s) + "'))\n" +
			"$errs = $null\n$null = [System.Management.Automation.Language.Parser]::ParseInput($s, [ref]$null, [ref]$errs)\n" +
			"if ($errs.Count -gt 0) { $errs | ForEach-Object { $_.Message }; exit 1 }\nexit 0"
		if out, code, err := runPS(t, time.Minute, check); err != nil {
			t.Errorf("%s does not parse (exit %d): %s", name, code, out)
		}
	}
}

// Bounded hands back a tool's exit code, and stops a tool that runs past its
// limit with ExitTimeout.
func TestBoundedStopsAToolThatHangs(t *testing.T) {
	_, code, _ := runPS(t, time.Minute, prelude()+"$code = Bounded (Join-Path ([Environment]::SystemDirectory) 'cmd.exe') @('/c','exit','7') 30\nexit $code")
	if code != 7 {
		t.Errorf("a tool that exits 7 came back as %d", code)
	}
	start := time.Now()
	_, code, _ = runPS(t, time.Minute, prelude()+"$code = Bounded (Join-Path ([Environment]::SystemDirectory) 'PING.EXE') @('-n','60','127.0.0.1') 2\nexit $code")
	if code != runtime.ExitTimeout {
		t.Errorf("a hung tool came back as %d, want %d", code, runtime.ExitTimeout)
	}
	if time.Since(start) > 30*time.Second {
		t.Errorf("the limit of 2 s took %v", time.Since(start))
	}
}

// The docker-users script, on a PC with no docker-users group, ends with
// CodeGroupMissing and changes nothing. A PC that has the group skips this: the
// test must never add the runner's account to a real group.
func TestDockerUsersScriptOnAPCWithoutTheGroup(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("expected an elevated process on the Windows CI runner")
		}
		t.Skip("not elevated")
	}
	out, _, _ := runPS(t, time.Minute, "if (Get-CimInstance Win32_Group -Filter \"LocalAccount=TRUE AND Name='docker-users'\") { 'present' } else { 'absent' }")
	if strings.TrimSpace(out) != "absent" {
		t.Skipf("this PC has a docker-users group (%s)", strings.TrimSpace(out))
	}
	s, _ := SetupScript(OpDockerUsers, false)
	_, err := runtime.RunElevated(s)
	if code, ok := runtime.ExitCodeOf(err); !ok || code != CodeGroupMissing {
		t.Fatalf("got %v, want exit 0x%08X", err, CodeGroupMissing)
	}
}

// The Server service script runs for real where the service already starts
// automatically and runs, so it changes nothing: it must end 0.
func TestLanmanScriptLive(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("expected an elevated process on the Windows CI runner")
		}
		t.Skip("not elevated")
	}
	out, _, _ := runPS(t, time.Minute, "$s = Get-Service LanmanServer; \"$($s.StartType)/$($s.Status)\"")
	if strings.TrimSpace(out) != "Automatic/Running" {
		t.Skipf("LanmanServer is %s here; the test changes nothing, so it runs only where it is Automatic and Running", strings.TrimSpace(out))
	}
	s, _ := SetupScript(OpLanmanServer, false)
	if res, err := runtime.RunElevated(s); err != nil || res.RestartNeeded {
		t.Fatalf("got %+v, %v", res, err)
	}
}

// The launch type read runs alone, with nothing after it that changes the PC. It
// never throws, and it hands back a number or says it could not read. The Windows
// runner is an administrator, so there it must read a number: the [wmiclass]
// chain it replaced never could on Windows PowerShell 5.1.
func TestHypervisorReadLive(t *testing.T) {
	script := prelude() + hypervisorRead + "if ($null -eq $launch) { 'could not read' } else { 'launch=' + $launch }\nexit 0\n"
	out, code, err := runPS(t, time.Minute, script)
	if err != nil {
		t.Fatalf("the read did not end cleanly (exit %d): %s", code, out)
	}
	got := strings.TrimSpace(out)
	if got != "could not read" && !regexp.MustCompile(`^launch=[0-9]+$`).MatchString(got) {
		t.Fatalf("the read answered %q, want launch=<number> or could not read", got)
	}
	if got == "could not read" && windows.GetCurrentProcessToken().IsElevated() && os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Fatal("an administrator on the Windows runner could not read hypervisorlaunchtype")
	}
	t.Log(got)
}
