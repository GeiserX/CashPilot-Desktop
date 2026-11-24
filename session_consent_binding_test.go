package main

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
)

// newConsentTestApp has one manual-only provider (a desktop app, no image) and one
// container provider.
func newConsentTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/depin/wipter.yml": {Data: []byte(
			"name: Wipter\nslug: wipter\ncategory: depin\nstatus: active\nplatforms: [windows, macos, linux]\n" +
				"docker:\n  image: \"\"\n")},
		"services/bandwidth/keeper.yml": {Data: []byte(
			"name: Identity Keeper\nslug: keeper\ncategory: bandwidth\nstatus: active\n" +
				"docker:\n  image: keeper/image:1.0.0\n")},
	})
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded: %v", err)
	}
	if svc, _ := cat.Get("wipter"); !svc.ManualOnly {
		t.Fatal("CONTROL: the test catalog's wipter is not manual-only")
	}
	return &App{cfg: cfg, catalog: cat}
}

// THE RULE: accepting records the text version and the date for that one app, in
// the config, and the binding hands back what is now recorded.
func TestAcceptSessionRiskRecordsVersionAndDate(t *testing.T) {
	app := newConsentTestApp(t)
	before := time.Now().UTC().Add(-time.Second)

	got, err := app.AcceptSessionRisk("wipter", "2026-11-24")
	if err != nil {
		t.Fatalf("AcceptSessionRisk: %v", err)
	}
	entry, ok := got["wipter"]
	if !ok || entry.TextVersion != "2026-11-24" {
		t.Fatalf("returned %+v, want wipter at version 2026-11-24", got)
	}
	if entry.AcceptedAt.Before(before) || entry.AcceptedAt.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("accepted at %v, want about now", entry.AcceptedAt)
	}
	if stored := app.cfg.Config().SessionConsent["wipter"]; stored != entry {
		t.Errorf("config holds %+v, binding returned %+v", stored, entry)
	}
	if len(got) != 1 {
		t.Errorf("one accept recorded %d consents: %+v", len(got), got)
	}

	// A new text version replaces the entry: the wizard compares the version, so
	// the old one must not linger next to it.
	again, err := app.AcceptSessionRisk("wipter", "2027-01-01")
	if err != nil {
		t.Fatalf("second AcceptSessionRisk: %v", err)
	}
	if again["wipter"].TextVersion != "2027-01-01" || len(again) != 1 {
		t.Errorf("after a new version: %+v", again)
	}
}

// A consent is per app and only for an app the user installs by hand: an unknown
// slug, a container service or a missing version records nothing.
func TestAcceptSessionRiskRefusesWhatItCannotMean(t *testing.T) {
	app := newConsentTestApp(t)
	cases := map[string][2]string{
		"unknown service":    {"nope", "2026-11-24"},
		"container service":  {"keeper", "2026-11-24"},
		"missing version":    {"wipter", "  "},
		"oversized version":  {"wipter", strings.Repeat("v", maxConsentVersion+1)},
		"control in version": {"wipter", "2026\n11"},
		"empty slug":         {"", "2026-11-24"},
	}
	for name, args := range cases {
		if _, err := app.AcceptSessionRisk(args[0], args[1]); err == nil {
			t.Errorf("%s: AcceptSessionRisk(%q, %q) succeeded", name, args[0], args[1])
		}
	}
	if n := len(app.cfg.Config().SessionConsent); n != 0 {
		t.Errorf("refused accepts still recorded %d consents: %+v", n, app.cfg.Config().SessionConsent)
	}
}

// The way out: revoking deletes the entry and leaves the other apps' consents. It
// works for a slug the catalog no longer lists, and twice in a row.
func TestRevokeSessionRiskDeletesOnlyThatApp(t *testing.T) {
	app := newConsentTestApp(t)
	if _, err := app.AcceptSessionRisk("wipter", "2026-11-24"); err != nil {
		t.Fatal(err)
	}
	// A consent left from a service the catalog dropped since.
	if err := app.cfg.Update(func(c *config.AppConfig) {
		c.SessionConsent = map[string]config.SessionConsent{
			"wipter": c.SessionConsent["wipter"],
			"gone":   {TextVersion: "old", AcceptedAt: time.Now().UTC()},
		}
	}); err != nil {
		t.Fatal(err)
	}

	got, err := app.RevokeSessionRisk("wipter")
	if err != nil {
		t.Fatalf("RevokeSessionRisk: %v", err)
	}
	if _, still := got["wipter"]; still {
		t.Errorf("wipter is still consented after revoke: %+v", got)
	}
	if _, kept := got["gone"]; !kept {
		t.Errorf("revoking wipter dropped another app's consent: %+v", got)
	}
	if _, err := app.RevokeSessionRisk("wipter"); err != nil {
		t.Errorf("a second revoke failed: %v", err)
	}
	got, err = app.RevokeSessionRisk("gone")
	if err != nil {
		t.Fatalf("revoking a service the catalog no longer lists: %v", err)
	}
	if len(got) != 0 || app.cfg.Config().SessionConsent != nil {
		t.Errorf("after the last revoke: returned %+v, config %+v", got, app.cfg.Config().SessionConsent)
	}
}

// Config() hands out the stored map. An accept or revoke that edited it in place
// would change a copy a caller already holds, and race every reader of it.
func TestSessionConsentNeverEditsAMapACallerHolds(t *testing.T) {
	app := newConsentTestApp(t)
	if _, err := app.AcceptSessionRisk("wipter", "2026-11-24"); err != nil {
		t.Fatal(err)
	}
	held := app.cfg.Config().SessionConsent
	if _, err := app.RevokeSessionRisk("wipter"); err != nil {
		t.Fatal(err)
	}
	if _, ok := held["wipter"]; !ok {
		t.Error("revoke deleted from a map an earlier Config() returned")
	}
	returned, err := app.AcceptSessionRisk("wipter", "2026-11-24")
	if err != nil {
		t.Fatal(err)
	}
	delete(returned, "wipter")
	if _, ok := app.cfg.Config().SessionConsent["wipter"]; !ok {
		t.Error("editing the map the binding returned changed the stored config")
	}
}

// Before Startup has loaded the config and catalog, the bindings refuse instead of
// panicking on a nil manager.
func TestSessionConsentBeforeStartup(t *testing.T) {
	var app App
	if _, err := app.AcceptSessionRisk("wipter", "2026-11-24"); err == nil {
		t.Error("AcceptSessionRisk succeeded before startup")
	}
	if _, err := app.RevokeSessionRisk("wipter"); err == nil {
		t.Error("RevokeSessionRisk succeeded before startup")
	}
}
