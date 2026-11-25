package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The window waits while GetAppState answers errStillStarting, and only while the
// text is exactly STILL_STARTING in frontend/src/render/boot.ts. If the two drift,
// a slow first start shows a dead failed page again.
func TestStillStartingMatchesFrontend(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("frontend", "src", "render", "boot.ts"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`export const STILL_STARTING = "([^"]*)";`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("frontend/src/render/boot.ts has no STILL_STARTING constant")
	}
	if got, want := string(m[1]), errStillStarting.Error(); got != want {
		t.Fatalf("frontend STILL_STARTING = %q, Go errStillStarting = %q", got, want)
	}
}

// Before startCore has set the app up, the bindings the first screen calls answer
// errStillStarting, so the window waits.
func TestBindingsAnswerStillStartingBeforeStartup(t *testing.T) {
	a := NewApp()
	if _, err := a.GetAppState(); !errors.Is(err, errStillStarting) {
		t.Fatalf("GetAppState before startup = %v, want errStillStarting", err)
	}
	if _, err := a.SetupResume(); !errors.Is(err, errStillStarting) {
		t.Fatalf("SetupResume before startup = %v, want errStillStarting", err)
	}
}

// A start that failed for good is reported as that failure, not as still
// starting, so the window shows it at once instead of waiting a minute.
func TestFailedStartIsNotStillStarting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	if err := a.startCore(context.Background()); err == nil {
		t.Fatal("startCore with a broken config.json succeeded")
	}
	_, err := a.GetAppState()
	if err == nil || errors.Is(err, errStillStarting) || err.Error() == errStillStarting.Error() {
		t.Fatalf("GetAppState after a failed start = %v, want the start's own error", err)
	}
	if !strings.HasPrefix(err.Error(), "config: ") {
		t.Fatalf("GetAppState after a failed start = %q, want it to name the config", err)
	}
}
