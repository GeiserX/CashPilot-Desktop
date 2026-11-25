package main

import (
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// pinReminderDay makes "today" a fixed day for the reminders.
func pinReminderDay(t *testing.T, day time.Time) {
	t.Helper()
	prev := reminderNow
	reminderNow = func() time.Time { return day }
	t.Cleanup(func() { reminderNow = prev })
}

func grassDeployment() store.Deployment {
	return store.Deployment{Slug: "grass", ContainerID: "c1", Name: "cashpilot-grass", Image: "img", Status: "running", Runtime: "existing-docker"}
}

// Grass's Stage 2 claim closes on 22 Jan 2027 and warns 60 days ahead.
var grassWarnDay = time.Date(2026, 12, 1, 10, 0, 0, 0, time.Local)

func TestRemindersShowForADeployedService(t *testing.T) {
	app := newPayloadTestApp(t, []store.Deployment{grassDeployment()})
	pinReminderDay(t, grassWarnDay)

	view, err := app.GetReminders()
	if err != nil {
		t.Fatal(err)
	}
	if view.Joined || view.Unknown || len(view.Items) != 1 {
		t.Fatalf("got %+v", view)
	}
	it := view.Items[0]
	if it.Slug != "grass" || it.Rule != "claim-stage2-usdc" || it.Due != "2027-01-22" || it.Stage != "d60" || it.URL != "https://app.grass.io/dashboard" {
		t.Fatalf("got %+v", it)
	}
}

func TestRemindersOnlyForServicesSetUp(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	pinReminderDay(t, grassWarnDay)
	view, err := app.GetReminders()
	if err != nil || len(view.Items) != 0 {
		t.Fatalf("nothing is set up, got %+v, %v", view, err)
	}
}

// While joined, the server owns the reminders: none are evaluated, shown or
// acted on here. Joined is Mode follower, and a join or a switch that the other
// process (the window or the background helper) saved holds at the next read.
func TestRemindersSkippedWhileJoined(t *testing.T) {
	app := newPayloadTestApp(t, []store.Deployment{grassDeployment()})
	pinReminderDay(t, grassWarnDay)
	if view, _ := app.GetReminders(); view.Joined || len(view.Items) != 1 {
		t.Fatalf("a lead shows its reminders, got %+v", view)
	}

	// The other process joins, on its own Manager of the same config file.
	other, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Update(func(c *config.AppConfig) { c.Mode = config.ModeFollower; c.UpstreamURL = "https://hub.example/" }); err != nil {
		t.Fatal(err)
	}

	view, err := app.GetReminders()
	if err != nil {
		t.Fatal(err)
	}
	if !view.Joined || view.ServerURL != "https://hub.example" || len(view.Items) != 0 {
		t.Fatalf("got %+v", view)
	}
	if _, err := app.ReminderAction("grass", "claim-stage2-usdc", "done", "", 0); err == nil {
		t.Fatal("an action while joined must be refused")
	}
	if rows, _ := app.store.ListObligationState(); len(rows) != 0 {
		t.Fatalf("nothing may be stored while joined, got %+v", rows)
	}

	// Switching back to lead brings the local reminders back.
	if err := other.Update(func(c *config.AppConfig) { c.Mode = config.ModeLead; c.UpstreamURL = "" }); err != nil {
		t.Fatal(err)
	}
	if view, _ := app.GetReminders(); view.Joined || len(view.Items) != 1 {
		t.Fatalf("got %+v", view)
	}
}

// Joined is the mode, not a saved server address: a lead keeps its reminders.
func TestRemindersFollowTheModeNotTheServerAddress(t *testing.T) {
	app := newPayloadTestApp(t, []store.Deployment{grassDeployment()})
	pinReminderDay(t, grassWarnDay)
	if err := app.cfg.Update(func(c *config.AppConfig) { c.Mode = config.ModeLead; c.UpstreamURL = "https://hub.example" }); err != nil {
		t.Fatal(err)
	}
	if view, _ := app.GetReminders(); view.Joined || len(view.Items) != 1 {
		t.Fatalf("a lead shows its reminders, got %+v", view)
	}
}

// A service that an online worker reporting here runs is in use. One that only
// an offline worker runs is not.
func TestRemindersForServicesAnOnlineWorkerRuns(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	pinReminderDay(t, grassWarnDay)
	seen := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := app.store.UpsertFleetDevice(store.FleetDevice{Name: "pc-online", Kind: "worker", Status: "online", LastSeen: seen, Services: []string{"grass"}}); err != nil {
		t.Fatal(err)
	}
	offline, err := app.store.UpsertFleetDevice(store.FleetDevice{Name: "pc-offline", Kind: "worker", Status: "offline", LastSeen: seen, Services: []string{"mysterium"}})
	if err != nil {
		t.Fatal(err)
	}

	view, err := app.GetReminders()
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 1 || view.Items[0].Slug != "grass" {
		t.Fatalf("want only the online worker's grass, got %+v", view.Items)
	}

	// The same worker online again: its service gets its reminder.
	offline.Status = "online"
	if _, err := app.store.UpsertFleetDevice(offline); err != nil {
		t.Fatal(err)
	}
	view, err = app.GetReminders()
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, it := range view.Items {
		slugs[it.Slug] = true
	}
	if !slugs["grass"] || !slugs["mysterium"] {
		t.Fatalf("want grass and mysterium once both workers are online, got %+v", view.Items)
	}
}

func TestReminderDoneHidesItAndSticks(t *testing.T) {
	app := newPayloadTestApp(t, []store.Deployment{grassDeployment()})
	pinReminderDay(t, grassWarnDay)

	view, err := app.ReminderAction("grass", "claim-stage2-usdc", "done", "2027-01-22", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 0 {
		t.Fatalf("Done did not hide it: %+v", view.Items)
	}
	pinReminderDay(t, time.Date(2027, 1, 21, 10, 0, 0, 0, time.Local))
	if view, _ := app.GetReminders(); len(view.Items) != 0 {
		t.Fatalf("a done claim came back: %+v", view.Items)
	}
	if _, err := app.ReminderAction("grass", "claim-stage2-usdc", "explode", "", 0); err == nil {
		t.Fatal("an unknown action must be refused")
	}
}

// The clock reads the store's own earnings history and stores its start.
func TestRemindersReadTheEarningsHistory(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	now := time.Now()
	day := func(n int) string { return now.UTC().AddDate(0, 0, -n).Format(time.RFC3339) }
	for _, r := range []store.EarningsRecord{
		{Platform: "honeygain", Balance: 1, Currency: "USD", CreatedAt: day(150)},
		{Platform: "honeygain", Balance: 2, Currency: "USD", CreatedAt: day(120)},
		{Platform: "honeygain", Balance: 2, Currency: "USD", CreatedAt: day(1)},
	} {
		if _, err := app.store.SaveEarnings(r); err != nil {
			t.Fatal(err)
		}
	}
	pinReminderDay(t, now)

	// Removed, but a balance above 0 keeps the inactivity clock running.
	view, err := app.GetReminders()
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 1 || view.Items[0].Rule != "keep-active-6-months" || view.Items[0].Stage != "d90" {
		t.Fatalf("got %+v", view.Items)
	}
	start := now.UTC().AddDate(0, 0, -120).Format("2006-01-02")
	if view.Items[0].Occurrence != start {
		t.Fatalf("the clock starts at the last increase %s, got %s", start, view.Items[0].Occurrence)
	}
	rows, err := app.store.ListObligationState()
	if err != nil || len(rows) != 1 || rows[0].Rule != "keep-active-6-months" || rows[0].AnchorAt != start {
		t.Fatalf("the clock start was not stored: %+v, %v", rows, err)
	}
}
