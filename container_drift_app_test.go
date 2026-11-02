package main

import (
	"testing"
)

// Ignore on a "needs fixing" warning mutes that service until the catalog changes a
// field the check judges; a mute taken at an older catalog no longer counts.
func TestIgnoreDriftMutesOnlyThatServiceAtTheCurrentCatalog(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	found := func() map[string][]string {
		return map[string][]string{"earnapp": {"missing NET_RAW"}, "honeygain": {"has no mount at /data"}}
	}
	if err := app.IgnoreDrift("earnapp"); err != nil {
		t.Fatalf("IgnoreDrift: %v", err)
	}
	got := filterIgnoredDrift(found(), app.cfg.Config().DriftIgnored, app.catalog)
	if _, muted := got["earnapp"]; muted || len(got["honeygain"]) != 1 {
		t.Fatalf("after Ignore on earnapp: %v, want honeygain only", got)
	}

	// The catalog has changed a judged field since the mute: the warning is back.
	stale := map[string]string{"earnapp": "0000000000000000"}
	if got := filterIgnoredDrift(found(), stale, app.catalog); len(got["earnapp"]) != 1 {
		t.Fatalf("a mute at an older catalog still hides the warning: %v", got)
	}

	// A second Ignore keeps the first.
	if err := app.IgnoreDrift("honeygain"); err != nil {
		t.Fatalf("IgnoreDrift: %v", err)
	}
	if got := filterIgnoredDrift(found(), app.cfg.Config().DriftIgnored, app.catalog); len(got) != 0 {
		t.Fatalf("after both Ignores: %v, want nothing", got)
	}

	if err := app.IgnoreDrift("no-such-service"); err == nil {
		t.Fatal("IgnoreDrift accepted a slug the catalog does not have")
	}
}

// CASHPILOT_DRIFT_WARNINGS turns the check off, and back on.
func TestTheDriftWarningSwitch(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	setting := func() string {
		state, err := app.GetSettingsState()
		if err != nil {
			t.Fatalf("GetSettingsState: %v", err)
		}
		for _, e := range state.Environment {
			if e.Key == "CASHPILOT_DRIFT_WARNINGS" {
				return e.Value
			}
		}
		t.Fatal("CASHPILOT_DRIFT_WARNINGS is not in the settings")
		return ""
	}
	if got := setting(); got != "true" {
		t.Fatalf("default = %q, want true", got)
	}
	for _, off := range []string{"false", "off", "0", "no"} {
		if _, err := app.SaveSettings(map[string]string{"driftWarnings": off}); err != nil {
			t.Fatalf("SaveSettings(%q): %v", off, err)
		}
		if got := setting(); got != "false" || app.containerDrift() != nil {
			t.Fatalf("after %q the setting reads %q and the check ran", off, got)
		}
		if _, err := app.SaveSettings(map[string]string{"driftWarnings": "true"}); err != nil {
			t.Fatalf("SaveSettings(true): %v", err)
		}
		if got := setting(); got != "true" {
			t.Fatalf("turning it back on reads %q", got)
		}
	}
	// A save that does not mention it leaves it alone.
	if _, err := app.SaveSettings(map[string]string{"driftWarnings": "false"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveSettings(map[string]string{"timezone": "Europe/Madrid"}); err != nil {
		t.Fatal(err)
	}
	if got := setting(); got != "false" {
		t.Fatalf("an unrelated save turned the warnings back on: %q", got)
	}
}
