package main

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
)

// recordIsolationOnWhileBlocked runs save in the background while the test holds the
// lock save waits on, records "isolation on" the way the fence does (cfg.Update) in
// that window, then lets save finish.
func recordIsolationOnWhileBlocked(t *testing.T, app *App, saver string, lock, unlock func(), save func() error) {
	t.Helper()
	lock()
	done := make(chan error, 1)
	go func() { done <- save() }()
	// save has taken its reads once it waits on the lock. A fixed sleep could let the
	// record land before those reads, and the test would pass with the bug back.
	if !waitBlockedOnMutex(saver, 10*time.Second) {
		unlock()
		t.Fatalf("%s never reached the lock", saver)
	}
	if err := app.cfg.Update(func(c *config.AppConfig) { c.IsolationOn = true }); err != nil {
		unlock()
		t.Fatalf("Update: %v", err)
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("save: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("save did not finish")
	}
}

// waitBlockedOnMutex reports whether a goroutine running fn is parked on a mutex,
// polling the goroutine stacks until timeout.
func waitBlockedOnMutex(fn string, timeout time.Duration) bool {
	buf := make([]byte, 1<<20)
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		stacks := string(buf[:runtime.Stack(buf, true)])
		for _, g := range strings.Split(stacks, "\n\n") {
			if strings.Contains(g, "sync.(*Mutex).Lock(") && strings.Contains(g, fn) {
				return true
			}
		}
	}
	return false
}

// THE RULE: a Settings save writes only the fields the form owns. The fence records
// "isolation on" in the background; a save that wrote back its whole earlier snapshot
// would turn that memory off again.
func TestSaveSettingsKeepsTheFencesMemory(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	recordIsolationOnWhileBlocked(t, app, ".(*App).SaveSettings(", app.upstream.mu.Lock, app.upstream.mu.Unlock, func() error {
		// A new pairing address clears the old worker key under upstream.mu, after
		// the form was read.
		_, err := app.SaveSettings(map[string]string{"upstreamUrl": "http://cashpilot.example", "displayCurrency": "EUR"})
		return err
	})
	got := app.cfg.Config()
	if !got.IsolationOn {
		t.Error("a Settings save put back isolation off over the fence's record")
	}
	if got.UpstreamURL != "http://cashpilot.example" || got.DisplayCurrency != "EUR" {
		t.Errorf("the form's own fields were not saved: %q %q", got.UpstreamURL, got.DisplayCurrency)
	}
}

// The same for the Activity switch, which saves under its own write lock.
func TestSetActivityEnabledKeepsTheFencesMemory(t *testing.T) {
	app := newActivityTestApp(t)
	recordIsolationOnWhileBlocked(t, app, ".(*App).SetActivityEnabled(", app.activity.writeMu.Lock, app.activity.writeMu.Unlock, func() error {
		_, err := app.SetActivityEnabled(true)
		return err
	})
	got := app.cfg.Config()
	if !got.IsolationOn {
		t.Error("the Activity switch put back isolation off over the fence's record")
	}
	if !got.ActivityEnabled {
		t.Error("the Activity switch was not saved")
	}
}
