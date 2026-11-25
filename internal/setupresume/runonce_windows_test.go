//go:build windows

package setupresume

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// testKey stands in for RunOnce: the tests never touch the real RunOnce key, so a
// test run can never make Windows start anything at the next sign-in.
const testKey = `Software\CashPilotTest`

func readValue(t *testing.T) (string, error) {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, testKey, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(ValueName)
	return v, err
}

func TestRunOnceWriterWritesAndRemovesTheValue(t *testing.T) {
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, testKey) })
	exe := filepath.Join(t.TempDir(), "CashPilot Desktop.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := arm(registry.CURRENT_USER, testKey, exe); err != nil {
		t.Fatalf("arm: %v", err)
	}
	got, err := readValue(t)
	if err != nil {
		t.Fatalf("the value was not written: %v", err)
	}
	if want := `"` + exe + `" --resume-setup`; got != want {
		t.Fatalf("value %q, want %q", got, want)
	}
	if err := deleteValue(registry.CURRENT_USER, testKey, ValueName); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := readValue(t); !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("the value is still there: %v", err)
	}
	// Removing what is not there is fine: a cancel after RunOnce already ran.
	if err := deleteValue(registry.CURRENT_USER, testKey, ValueName); err != nil {
		t.Fatalf("a second delete failed: %v", err)
	}
}

func TestRunOnceWriterRefusesAMissingApp(t *testing.T) {
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, testKey) })
	exe := filepath.Join(t.TempDir(), "gone.exe")
	if err := arm(registry.CURRENT_USER, testKey, exe); err == nil {
		t.Fatal("RunOnce was armed for an app that is not there")
	}
	if _, err := readValue(t); err == nil {
		t.Fatal("a value was written for a missing app")
	}
}
