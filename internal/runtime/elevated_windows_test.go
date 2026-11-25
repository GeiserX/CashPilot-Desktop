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
