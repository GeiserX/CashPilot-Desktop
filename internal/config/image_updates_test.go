package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A new install starts on Update automatically; an install from before app image
// updates starts on Off with the offer, written once, and a later choice stays.
func TestImageUpdatesDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", dir)
	fresh, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if s := fresh.Config().ImageUpdates; s == nil || s.Mode != ImageUpdatesAuto || s.Hour != ImageUpdatesDefaultHour || s.Offer {
		t.Fatalf("a new install starts on %+v; want auto at 04:00 with no offer", s)
	}
	again, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if s := again.Config().ImageUpdates; s == nil || s.Mode != ImageUpdatesAuto {
		t.Fatalf("the new install's choice was not written: %+v", s)
	}

	// The config file a release before image updates wrote.
	upgraded := t.TempDir()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", upgraded)
	if err := os.WriteFile(filepath.Join(upgraded, "config.json"), []byte(`{"displayCurrency":"EUR"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if s := old.Config().ImageUpdates; s == nil || s.Mode != ImageUpdatesOff || !s.Offer {
		t.Fatalf("an upgraded install reads %+v; want off with the offer", s)
	}
	if err := old.Update(func(c *AppConfig) { c.ImageUpdates = &ImageUpdates{Mode: "notify", Hour: 2} }); err != nil {
		t.Fatal(err)
	}
	later, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if s := later.Config().ImageUpdates; s == nil || s.Mode != "notify" || s.Hour != 2 || s.Offer {
		t.Fatalf("a later start changed the owner's choice: %+v", s)
	}
}
