//go:build windows

package winvm

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func daclOf(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

// status.json: SYSTEM and Administrators write, Users read, nothing inherited.
// state.json and the proof lock: SYSTEM and Administrators only.
func TestGuardFilesAccessRules(t *testing.T) {
	dir := t.TempDir()
	if err := WriteStatus(dir, Status{Network: NetAbsent, Fence: FenceUnproven}); err != nil {
		t.Fatal(err)
	}
	if err := WriteProofLock(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	status := daclOf(t, filepath.Join(dir, StatusFile))
	for _, want := range []string{"D:P", "(A;;FA;;;SY)", "(A;;FA;;;BA)", "(A;;FR;;;BU)"} {
		if !strings.Contains(status, want) {
			t.Errorf("status.json DACL %s lacks %s", status, want)
		}
	}
	lock := daclOf(t, filepath.Join(dir, LockFile))
	if !strings.HasPrefix(lock, "D:P") || strings.Contains(lock, ";BU)") || strings.Contains(lock, ";WD)") || strings.Contains(lock, ";AU)") {
		t.Errorf("proof.lock DACL %s lets more than SYSTEM and Administrators in", lock)
	}
	// What this account wrote, it trusts: the guard runs as SYSTEM.
	if _, err := ReadProofLock(dir); err != nil {
		t.Fatalf("the lock this account wrote is not trusted: %v", err)
	}
}
