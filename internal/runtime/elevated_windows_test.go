//go:build windows

package runtime

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// TestRunElevatedLive runs real PowerShell through RunElevated. Unelevated it would
// show the admin prompt, so it runs only in an elevated process. GitHub's Windows
// runners are elevated, and there it must run: a skip in CI would hide that the
// Windows half was never tested.
func TestRunElevatedLive(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("expected an elevated process on the Windows CI runner")
		}
		t.Skip("not elevated: RunElevated would show the admin prompt")
	}
	sid, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}

	res, err := RunElevated("exit 0")
	if err != nil || res.RestartNeeded {
		t.Errorf("exit 0: %+v, %v", res, err)
	}
	for _, code := range []string{"3010", "1641"} {
		res, err := RunElevated("exit " + code)
		if err != nil || !res.RestartNeeded {
			t.Errorf("exit %s: %+v, %v; want success with a restart needed", code, res, err)
		}
	}
	if _, err := RunElevated("exit 5"); err == nil {
		t.Error("exit 5 was read as success")
	}
	// The script sees the SID read before elevation.
	if _, err := RunElevated("if ($CashPilotUserSid -ne '" + sid + "') { exit 7 }; exit 0"); err != nil {
		t.Errorf("the script did not get the signed-in SID %s: %v", sid, err)
	}
}

// TestElevatedLauncherLive runs the launcher an unelevated app uses, from this
// elevated process, where asking for admin rights shows no prompt. It proves the
// launcher starts System32's PowerShell with the script, hands back its exit
// code (3010 included) and passes the SID.
func TestElevatedLauncherLive(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("expected an elevated process on the Windows CI runner")
		}
		t.Skip("not elevated: the launcher would show the admin prompt")
	}
	sid, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	run := func(script string) (ElevatedResult, error) {
		args, err := ElevatedArgs(script, sid, false)
		if err != nil {
			t.Fatal(err)
		}
		_, err = powerShell(args...)
		return elevatedOutcome(err)
	}
	if res, err := run("exit 0"); err != nil || res.RestartNeeded {
		t.Errorf("exit 0: %+v, %v", res, err)
	}
	if res, err := run("exit 3010"); err != nil || !res.RestartNeeded {
		t.Errorf("exit 3010: %+v, %v", res, err)
	}
	if _, err := run("exit 5"); err == nil {
		t.Error("exit 5 came back as success")
	} else if code, ok := ExitCodeOf(err); !ok || code != 5 {
		t.Errorf("exit 5 came back as %d (%v)", code, ok)
	}
	if _, err := run("if ($CashPilotUserSid -ne '" + sid + "') { exit 7 }; if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { exit 8 }; exit 0"); err != nil {
		t.Errorf("the launched script lacked the SID or admin rights: %v", err)
	}
}
