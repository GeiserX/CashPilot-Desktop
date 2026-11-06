package main

import (
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
)

// recordIsolationOnWhileBlocked runs save in the background while the test holds the
// lock save waits on, records "isolation on" the way the fence does (cfg.Update) in
// that window, then lets save finish.
func recordIsolationOnWhileBlocked(t *testing.T, app *App, lock, unlock func(), save func() error) {
	t.Helper()
	lock()
	done := make(chan error, 1)
	go func() { done <- save() }()
	// Long enough for save to take its reads and reach the lock.
	time.Sleep(200 * time.Millisecond)
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

// THE RULE: a Settings save writes only the fields the form owns. The fence records
// "isolation on" in the background; a save that wrote back its whole earlier snapshot
// would turn that memory off again.
func TestSaveSettingsKeepsTheFencesMemory(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	recordIsolationOnWhileBlocked(t, app, app.upstream.mu.Lock, app.upstream.mu.Unlock, func() error {
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
	recordIsolationOnWhileBlocked(t, app, app.activity.writeMu.Lock, app.activity.writeMu.Unlock, func() error {
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
