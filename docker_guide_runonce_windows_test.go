//go:build windows

package main

import (
	"errors"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/setupresume"
	"golang.org/x/sys/windows/registry"
)

// readRealRunOnce reads the real HKCU RunOnce value; ok is false when it is
// not there.
func readRealRunOnce(t *testing.T) (string, bool) {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, setupresume.RunOnceKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("open RunOnce: %v", err)
	}
	defer k.Close()
	v, _, err := k.GetStringValue(setupresume.ValueName)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read RunOnce: %v", err)
	}
	return v, true
}

// guardRealRunOnce fails the test when the real RunOnce value differs after it
// from what it was before: the guide tests must only ever touch their fake.
func guardRealRunOnce(t *testing.T) {
	t.Helper()
	before, had := readRealRunOnce(t)
	t.Cleanup(func() {
		after, has := readRealRunOnce(t)
		if has != had || after != before {
			t.Errorf("the test changed the real RunOnce value: before %q (%v), after %q (%v)", before, had, after, has)
		}
	})
}
