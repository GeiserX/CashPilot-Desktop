//go:build windows

package winvm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPinHostScriptDirectoryChecksAndHoldsACL(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("creating a directory owned by Administrators requires elevation")
	}
	base := t.TempDir()
	untrusted := filepath.Join(base, "untrusted")
	if err := os.Mkdir(untrusted, 0o700); err != nil {
		t.Fatal(err)
	}
	if release, err := pinHostScriptDirectory(untrusted); err == nil {
		release()
		t.Fatal("accepted a directory with an inherited ACL")
	}

	private := filepath.Join(base, "private")
	release, err := pinHostScriptDirectory(private)
	if err != nil {
		t.Fatal(err)
	}
	moveErr := os.Rename(base, base+"-moved")
	release()
	if !errors.Is(moveErr, windows.ERROR_SHARING_VIOLATION) && !errors.Is(moveErr, windows.ERROR_ACCESS_DENIED) {
		if moveErr == nil {
			_ = os.Rename(base+"-moved", base)
		}
		t.Fatalf("rename of an ancestor during the lease: %v", moveErr)
	}
	release, err = pinHostScriptDirectory(private)
	if err != nil {
		t.Fatalf("reopen the private directory: %v", err)
	}
	release()
}
