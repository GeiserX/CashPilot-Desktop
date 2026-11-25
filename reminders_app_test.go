package main

import (
	"testing"
	"time"

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
// acted on here.
func TestRemindersSkippedWhileJoined(t *testing.T) {
	app := newPayloadTestApp(t, []store.Deployment{grassDeployment()})
	pinReminderDay(t, grassWarnDay)
	setFleetUpstreamURL(t, app, "https://hub.example/")

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

	// Leaving the server brings the local reminders back.
	setFleetUpstreamURL(t, app, "")
	if view, _ := app.GetReminders(); view.Joined || len(view.Items) != 1 {
		t.Fatalf("got %+v", view)
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
