package winvm

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

type exitErr uint32

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", uint32(e)) }
func (e exitErr) ExitCode() int { return int(uint32(e)) }

// longSID is as long as a local account SID gets.
const longSID = "S-1-5-21-4294967295-4294967295-4294967295-4294967295"

// allScripts is every script the app can run for a change: each admin change,
// with and without --web-download, and the winget window.
func allScripts(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{"docker-install (winget window)": DockerInstallScript}
	for op := range SetupStepFor {
		if !SetupElevated(op) {
			if _, err := SetupScript(op, false); err == nil {
				t.Errorf("%s runs as the user, yet has an admin script", op)
			}
			continue
		}
		for _, web := range []bool{false, true} {
			s, err := SetupScript(op, web)
			if err != nil {
				t.Fatalf("%s: %v", op, err)
			}
			out[fmt.Sprintf("%s web=%v", op, web)] = s
		}
	}
	return out
}

// forbidden are words no script the app runs may hold: accepting Docker's
// terms, installing silently, touching the user's WSL distros, adding anyone to
// Administrators, turning the hypervisor or a protection off, restarting the PC
// (a restart comes only from its own click), or running from a file.
var forbidden = []string{
	"--accept-license", "accept-package-agreements", "accept-source-agreements",
	"--silent", "--quiet", "--disable-interactivity",
	"--unregister", "--shutdown", "--terminate", "--set-default",
	"hypervisorlaunchtype off", "'off'",
	"S-1-5-32-544", "Administrators",
	"Restart-Computer", "Stop-Computer", "shutdown", "/f ",
	"-Force", "Disable-", "Set-MpPreference", "NetFirewall", "DeviceGuard",
	"Remove-", "Stop-Service", "VMSwitch", "NetNat", "Remove-VM",
	"$env:TEMP", "Add-Type", "Out-File", "Set-Content",
}

func hardLineViolations(script string) []string {
	var hits []string
	low := strings.ToLower(script)
	for _, bad := range forbidden {
		if strings.Contains(low, strings.ToLower(bad)) {
			hits = append(hits, bad)
		}
	}
	return hits
}

func TestNoSetupScriptCrossesAHardLine(t *testing.T) {
	scripts := allScripts(t)
	if len(scripts) < 11 {
		t.Fatalf("only %d scripts were checked", len(scripts))
	}
	for name, s := range scripts {
		if hits := hardLineViolations(s); len(hits) > 0 {
			t.Errorf("%s holds %q:\n%s", name, hits, s)
		}
	}
}

// The check above can fail: a winget line that accepts the license, a WSL line
// that unregisters a distro and a hypervisor line that turns it off are caught.
func TestTheHardLineCheckCanFail(t *testing.T) {
	wsl, _ := SetupScript(OpWSLInstall, false)
	hv, _ := SetupScript(OpHypervisorLaunch, false)
	for _, bad := range []string{
		strings.Replace(DockerInstallScript, "--override install", "--override 'install --accept-license'", 1),
		strings.Replace(DockerInstallScript, "--source winget", "--source winget --silent", 1),
		strings.Replace(wsl, "'--no-distribution'", "'--unregister','Ubuntu'", 1),
		strings.Replace(hv, "'auto'", "'off'", 1),
	} {
		if len(hardLineViolations(bad)) == 0 {
			t.Errorf("a script that crosses a hard line passed:\n%s", bad)
		}
	}
}

// winget's manifest adds --accept-license on its own, so the install replaces
// every installer switch: --override with plain "install".
func TestDockerInstallOverridesWingetsLicenseSwitch(t *testing.T) {
	for _, want := range []string{"--id Docker.DockerDesktop", "--exact", "--source winget", "--override install", "Read-Host"} {
		if !strings.Contains(DockerInstallScript, want) {
			t.Errorf("the winget script lacks %q", want)
		}
	}
}

func TestWSLInstallNeverInstallsADistro(t *testing.T) {
	for _, web := range []bool{false, true} {
		s, err := SetupScript(OpWSLInstall, web)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "@('--install','--no-distribution'") {
			t.Errorf("web=%v: no --no-distribution:\n%s", web, s)
		}
		if strings.Contains(s, "--distribution ") || strings.Contains(s, "'-d'") {
			t.Errorf("web=%v names a distro", web)
		}
		if got := strings.Contains(s, "'--web-download'"); got != web {
			t.Errorf("web=%v: --web-download present %v", web, got)
		}
		if !strings.Contains(s, `HKLM:\SOFTWARE\Policies\WSL`) {
			t.Error("the install does not read the WSL policy first")
		}
	}
	s, _ := SetupScript(OpWSLUpdate, true)
	if !strings.Contains(s, "@('--update','--web-download')") {
		t.Errorf("the update retry lacks --web-download:\n%s", s)
	}
}

// Every tool is System32's, and every slow one is bounded, ending ExitTimeout.
func TestScriptsRunSystemToolsWithALimit(t *testing.T) {
	for _, op := range []string{OpWSLInstall, OpWSLUpdate, OpHypervisorLaunch} {
		s, _ := SetupScript(op, false)
		if !strings.Contains(s, "[Environment]::SystemDirectory") || !strings.Contains(s, "$code = Bounded ") {
			t.Errorf("%s does not run a bounded System32 tool:\n%s", op, s)
		}
		if !strings.Contains(s, fmt.Sprintf("exit %d", runtime.ExitTimeout)) {
			t.Errorf("%s has no timeout exit", op)
		}
		limit := regexp.MustCompile(`\) (\d+)\n`).FindStringSubmatch(s)
		if limit == nil || limit[1] != fmt.Sprint(toolLimit(op)) || toolLimit(op) <= 0 {
			t.Errorf("%s: tool limit %v, want %d", op, limit, toolLimit(op))
		}
	}
	s, _ := SetupScript(OpHypervisorLaunch, false)
	if !strings.Contains(s, "@('/set','{current}','hypervisorlaunchtype','auto')") || !strings.Contains(s, "exit 3010") {
		t.Errorf("the hypervisor fix does not set auto and ask for a restart:\n%s", s)
	}
	s, _ = SetupScript(OpLanmanServer, false)
	if !strings.Contains(s, "Set-Service -Name LanmanServer -StartupType Automatic") || !strings.Contains(s, "Start-Service -Name LanmanServer") {
		t.Errorf("the Server service script:\n%s", s)
	}
}

func decodeLauncher(t *testing.T, args []string) string {
	t.Helper()
	if len(args) != 2 {
		t.Fatalf("no encoded script in %q", args)
	}
	b, err := base64.StdEncoding.DecodeString(args[1])
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(units))
}

// The SID is a parameter: the builder never writes one, the script reads
// $CashPilotUserSid, and RunElevated's front line is the only place a SID
// appears, after the SID grammar let it through.
func TestDockerUsersTakesTheSIDAsAParameter(t *testing.T) {
	s, err := SetupScript(OpDockerUsers, false)
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`S-1-5-\d`).MatchString(s) {
		t.Fatalf("the builder wrote a SID into the script:\n%s", s)
	}
	for _, want := range []string{"$CashPilotUserSid -notmatch", "SecurityIdentifier($CashPilotUserSid)", "/docker-users,group", "$group.Add($member)", "$group.IsMember($member)"} {
		if !strings.Contains(s, want) {
			t.Errorf("the docker-users script lacks %q", want)
		}
	}
	args, err := runtime.ElevatedArgs(s, longSID, true)
	if err != nil {
		t.Fatal(err)
	}
	full := decodeLauncher(t, args)
	if !strings.HasPrefix(full, "$CashPilotUserSid = '"+longSID+"'\n") || strings.Count(full, longSID) != 1 {
		t.Fatalf("the SID is not set once, first:\n%s", full)
	}
	for _, bad := range []string{"S-1-5-21-1'; Add-LocalGroupMember Administrators x; '", "Everyone", "S-1-5-32-544\n"} {
		if _, err := runtime.ElevatedArgs(s, bad, false); err == nil {
			t.Errorf("%q was accepted as the user's SID", bad)
		}
	}
}

// Every script, wrapped as RunElevated wraps it, fits one Windows command line.
func TestEveryScriptFitsOneCommandLine(t *testing.T) {
	for name, s := range allScripts(t) {
		for _, elevated := range []bool{false, true} {
			args, err := runtime.ElevatedArgs(s, longSID, elevated)
			if err != nil {
				t.Errorf("%s elevated=%v: %v", name, elevated, err)
				continue
			}
			n := 0
			for _, a := range args {
				n += len(a) + 1
			}
			if n > runtime.MaxCommandLine/2 {
				t.Errorf("%s elevated=%v: %d characters, too near the %d cap", name, elevated, n, runtime.MaxCommandLine)
			}
		}
	}
	// And the cap is enforced.
	if _, err := runtime.ElevatedArgs(strings.Repeat("#", 20000), longSID, false); err == nil {
		t.Error("a script past the command-line cap was accepted")
	}
}

func TestSetupOutcomeReadsExitCodes(t *testing.T) {
	restart := runtime.ElevatedResult{RestartNeeded: true, ExitCode: 3010}
	cases := []struct {
		name     string
		op       string
		res      runtime.ElevatedResult
		err      error
		timedOut bool
		web      bool
		state    string
		retry    string
	}{
		{"0 is done", OpLanmanServer, runtime.ElevatedResult{}, nil, false, false, OutcomeDone, ""},
		{"0 on docker-users asks to sign out", OpDockerUsers, runtime.ElevatedResult{}, nil, false, false, OutcomeSignOut, ""},
		{"3010 asks for a restart", OpWSLInstall, restart, nil, false, false, OutcomeRestart, ""},
		{"1641 asks for a restart", OpHypervisorLaunch, runtime.ElevatedResult{RestartNeeded: true, ExitCode: 1641}, nil, false, false, OutcomeRestart, ""},
		{"a declined prompt changed nothing", OpWSLInstall, runtime.ElevatedResult{}, exitErr(runtime.ExitCancelled), false, false, OutcomeCancelled, ""},
		{"the script's own timeout offers the direct download", OpWSLInstall, runtime.ElevatedResult{}, exitErr(runtime.ExitTimeout), false, false, OutcomeTimeout, RetryWebDownload},
		{"the app's timeout offers the direct download", OpWSLUpdate, runtime.ElevatedResult{}, errors.New("context deadline exceeded"), true, false, OutcomeTimeout, RetryWebDownload},
		{"a timeout after the direct download offers nothing more", OpWSLInstall, runtime.ElevatedResult{}, nil, true, true, OutcomeTimeout, ""},
		{"a Store failure offers the direct download", OpWSLInstall, runtime.ElevatedResult{}, exitErr(0x80072EE7), false, false, OutcomeFailed, RetryWebDownload},
		{"a Store failure after the direct download offers nothing more", OpWSLInstall, runtime.ElevatedResult{}, exitErr(0x80072EE7), false, true, OutcomeFailed, ""},
		{"an unknown failure is a failure with its code", OpWSLInstall, runtime.ElevatedResult{}, exitErr(5), false, false, OutcomeFailed, ""},
		{"the WSL policy stops", OpWSLInstall, runtime.ElevatedResult{}, exitErr(CodePolicy), false, false, OutcomePolicy, ""},
		{"a blocked program stops", OpWSLInstall, runtime.ElevatedResult{}, exitErr(1260), false, false, OutcomePolicy, ""},
		{"no group", OpDockerUsers, runtime.ElevatedResult{}, exitErr(CodeGroupMissing), false, false, OutcomeFailed, ""},
		{"no winget falls back to the page", OpDockerInstall, runtime.ElevatedResult{}, exitErr(CodeWingetGone), false, false, OutcomeFallback, ""},
		{"winget's restart-then-retry is a failure", OpDockerInstall, runtime.ElevatedResult{}, exitErr(WingetRebootForInstall), false, false, OutcomeFailed, ""},
		{"no PowerShell at all", OpLanmanServer, runtime.ElevatedResult{}, errors.New("exec: not found"), false, false, OutcomeFailed, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := SetupOutcome(c.op, c.res, c.err, c.timedOut, c.web)
			if o.State != c.state || o.Retry != c.retry {
				t.Fatalf("got %+v, want state %q retry %q", o, c.state, c.retry)
			}
			if strings.TrimSpace(o.Text) == "" {
				t.Fatal("no plain-words reason")
			}
			if o.State == OutcomeFailed || o.State == OutcomePolicy {
				if _, has := runtime.ExitCodeOf(c.err); has && o.Code == "" {
					t.Fatal("a failure with an exit code does not show it")
				}
			}
			if o.State == OutcomeFallback && o.URL != DockerDownloadURL {
				t.Fatalf("the fallback opens %q", o.URL)
			}
		})
	}
	if o := SetupOutcome(OpWSLInstall, runtime.ElevatedResult{}, exitErr(0x80070005), false, false); o.Code != "0x80070005" {
		t.Fatalf("an HRESULT shows as %q", o.Code)
	}
}

// Each change belongs to the row that offers it, and each row's change is one
// the app can make.
func TestEveryChangeIsOfferedByItsOwnRow(t *testing.T) {
	cases := map[string]struct {
		mutate func(*HostFacts)
		reason string
	}{
		OpWSLInstall:       {func(f *HostFacts) { f.VMPlatformFeature = No }, runtime.ReasonWSLMissing},
		OpWSLUpdate:        {func(f *HostFacts) { f.WSLVersion = "2.0.9.0" }, runtime.ReasonWSLOutdated},
		OpDockerInstall:    {func(f *HostFacts) { f.DockerDesktopInstalled = No }, runtime.ReasonNotInstalled},
		OpDockerUsers:      {func(f *HostFacts) { f.DockerUsersListed, f.DockerUsersToken = No, No }, runtime.ReasonAccessDenied},
		OpLinuxEngine:      {func(f *HostFacts) { f.DockerDesktopRunning = Yes }, runtime.ReasonWindowsContainers},
		OpHypervisorLaunch: {hypervisorOff, runtime.ReasonNotRunning},
		OpLanmanServer:     {func(f *HostFacts) { f.LanmanServer = "manual" }, runtime.ReasonNotRunning},
	}
	if len(cases) != len(SetupStepFor) {
		t.Fatalf("%d changes tested, %d exist", len(cases), len(SetupStepFor))
	}
	for op, c := range cases {
		f := goodPC()
		c.mutate(&f)
		derive(&f)
		s := stepOf(t, DockerSteps(f, c.reason, true), SetupStepFor[op])
		if s.State != StepNeedsYou || s.Action.Op != op {
			t.Errorf("%s: row %s is %q with op %q", op, s.ID, s.State, s.Action.Op)
		}
		if want := map[bool]string{true: ActionElevated, false: ActionRun}[SetupElevated(op)]; s.Action.Kind != want {
			t.Errorf("%s: kind %q, want %q", op, s.Action.Kind, want)
		}
		if SetupTimeout(op) <= 0 {
			t.Errorf("%s has no time limit", op)
		}
	}
	// docker-users is offered only on access denied.
	f := goodPC()
	f.DockerUsersListed, f.DockerUsersToken = No, No
	if hasStep(DockerSteps(f, runtime.ReasonNotRunning, true), "a8") {
		t.Error("docker-users is offered when nothing proved it is needed")
	}
}

// The hypervisor change reads the launch type as a number first and changes
// nothing when it is already Auto; that ending says so and asks for no restart.
func TestHypervisorChangeReadsTheLaunchTypeFirst(t *testing.T) {
	s, err := SetupScript(OpHypervisorLaunch, false)
	if err != nil {
		t.Fatal(err)
	}
	read := strings.Index(s, "-MethodName GetElement -Arguments @{ Type = [uint32]0x250000F0 }")
	skip := strings.Index(s, "if ($launch -eq 1) { exit "+psCode(CodeLaunchAuto)+" }")
	set := strings.Index(s, "'hypervisorlaunchtype','auto'")
	if read < 0 || skip < 0 || set < 0 || !(read < skip && skip < set) {
		t.Fatalf("the script does not read the launch type, then stop when it is Auto, before it sets it:\n%s", s)
	}
	// The [wmiclass] chain never read on Windows PowerShell 5.1: OpenStore hands
	// back a bare ManagementBaseObject, and OpenObject on it throws.
	if !strings.Contains(s, "Invoke-CimMethod -Namespace root/wmi -ClassName BcdStore -MethodName OpenStore") ||
		strings.Contains(s, "ManagementClass") || strings.Contains(s, "[wmiclass]") {
		t.Errorf("the script does not read the BCD store through the CIM cmdlets:\n%s", s)
	}
	if !strings.Contains(s, "{fa926493-6f1c-4193-a414-58f0b2456d1e}") {
		t.Errorf("the script does not read {current}")
	}
	o := SetupOutcome(OpHypervisorLaunch, runtime.ElevatedResult{}, exitErr(CodeLaunchAuto), false, false)
	if o.State == OutcomeRestart || o.State == OutcomeDone || !strings.Contains(o.Text, "already set to start at boot") {
		t.Errorf("already Auto ended %+v", o)
	}
}

// Already Auto is not a failure: the read worked and nothing needed changing, so
// the ending is a note with no error code.
func TestHypervisorAlreadyAutoIsANoteNotAFailure(t *testing.T) {
	o := SetupOutcome(OpHypervisorLaunch, runtime.ElevatedResult{}, exitErr(CodeLaunchAuto), false, false)
	if o.State != OutcomeNote || o.Code != "" {
		t.Errorf("already Auto ended %s with code %q, want %s and no code", o.State, o.Code, OutcomeNote)
	}
	if low := strings.ToLower(o.Text); !strings.Contains(low, "already") || !strings.Contains(low, "changed nothing") {
		t.Errorf("already Auto does not say it was already on and nothing changed: %s", o.Text)
	}
}

// The launch type read, which a live test runs alone on Windows, only reads: no
// bcdedit, no BCD method that sets or deletes, no exit.
func TestHypervisorReadChangesNothing(t *testing.T) {
	low := strings.ToLower(hypervisorRead)
	for _, w := range []string{"bcdedit", "setintegerelement", "setbooleanelement", "deleteelement", "-methodname set", "exit"} {
		if strings.Contains(low, w) {
			t.Errorf("the read part contains %q:\n%s", w, hypervisorRead)
		}
	}
}

// Only the install's own Windows features wait for a restart. The update turns on
// nothing, so a restart pending for something else never reads as "WSL needs a
// restart"; wsl.exe's own 3010 still does, through RunElevated.
func TestWSLUpdateIgnoresAnUnrelatedPendingRestart(t *testing.T) {
	for _, web := range []bool{false, true} {
		install, _ := SetupScript(OpWSLInstall, web)
		if !strings.Contains(install, "if (RebootPending) { exit 3010 }") {
			t.Errorf("web=%v: the install no longer asks for the restart its features need:\n%s", web, install)
		}
		update, _ := SetupScript(OpWSLUpdate, web)
		if strings.Contains(update, "if (RebootPending)") {
			t.Errorf("web=%v: the update turns any pending restart into its own:\n%s", web, update)
		}
		if !strings.Contains(update, "if ($code -ne 0) { exit $code }") || !strings.HasSuffix(update, "exit 0\n") {
			t.Errorf("web=%v: the update does not pass wsl.exe's own code through:\n%s", web, update)
		}
	}
}
