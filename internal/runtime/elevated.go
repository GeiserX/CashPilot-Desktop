package runtime

import (
	"errors"
	"fmt"
	"regexp"
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
