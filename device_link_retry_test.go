package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// A device the provider does not know yet is registered later, not dropped.
// ProxyRack only knows a new UUID about five minutes after its client first
// connects, and /device/add answers "try again in 5 minutes" until then. The
// deploy used to make one attempt at that moment and never try again, so every
// new device stayed off the account and earned for nobody.

const retryUUID = "dev-1"

func retryApp(t *testing.T, register collectors.Registration) (*App, *fakeDevices) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertDeployment(store.Deployment{Slug: "linked", Name: "linked", Status: "running"}); err != nil {
		t.Fatalf("UpsertDeployment: %v", err)
	}
	if err := st.SaveCredentials("linked", map[string]string{"UUID": retryUUID}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	devices := &fakeDevices{fakeCollector: newFakeCollector(nil, nil), register: register}
	return &App{ctx: context.Background(), catalog: deviceLinkCatalog(t), collectors: devices, store: st}, devices
}

func pending(t *testing.T, app *App) []store.PendingDeviceLink {
	t.Helper()
	rows, err := app.store.ListPendingDeviceLinks()
	if err != nil {
		t.Fatalf("ListPendingDeviceLinks: %v", err)
	}
	return rows
}

// due stores a registration whose next try has come, first refused at firstAt.
func due(t *testing.T, app *App, firstAt time.Time, attempts int) {
	t.Helper()
	if err := app.store.SavePendingDeviceLink("linked", retryUUID, firstAt, firstAt); err != nil {
		t.Fatalf("SavePendingDeviceLink: %v", err)
	}
	if attempts > 0 {
		if err := app.store.UpdatePendingDeviceLink("linked", retryUUID, attempts, firstAt, false); err != nil {
			t.Fatalf("UpdatePendingDeviceLink: %v", err)
		}
	}
}

func TestADeviceTheProviderDoesNotKnowYetIsStoredToRetry(t *testing.T) {
	app, _ := retryApp(t, collectors.RegisterPending)
	before := time.Now()
	app.registerDevice("linked", map[string]string{"UUID": " " + retryUUID + " "})

	rows := pending(t, app)
	if len(rows) != 1 || rows[0].Slug != "linked" || rows[0].DeviceID != retryUUID || rows[0].GaveUp {
		t.Fatalf("want one live pending row for the device, got %+v", rows)
	}
	// The first retry waits out the provider's five minutes.
	if wait := rows[0].NextAt.Sub(before); wait < 5*time.Minute || wait > deviceLinkFirstRetry+time.Second {
		t.Fatalf("first retry in %v, want just over five minutes", wait)
	}
	if got := app.retryingDeviceIDs()["linked"]; got != retryUUID {
		t.Fatalf("retryingDevices[linked] = %q, want %s", got, retryUUID)
	}
	if _, ok := app.addedDeviceIDs()["linked"]; ok {
		t.Fatal("a device the provider does not know yet is not added")
	}
	// No "add it by hand" warning while the app is still adding it.
	app.collectors.(*fakeDevices).unlinked = map[string][]string{"linked": {retryUUID}}
	if n := app.unlinkedDeviceNotifications([]store.Deployment{{Slug: "linked"}}); len(n) != 0 {
		t.Fatalf("a device being retried got the manual-step warning: %+v", n)
	}
}

func TestARetryThatSucceedsMarksTheDeviceAdded(t *testing.T) {
	app, devices := retryApp(t, collectors.RegisterAdded)
	now := time.Now()
	due(t, app, now.Add(-10*time.Minute), 0)

	app.retryDeviceLinks(context.Background(), now)

	if len(devices.asked) != 1 {
		t.Fatalf("want one add, asked %v", devices.asked)
	}
	if rows := pending(t, app); len(rows) != 0 {
		t.Fatalf("an added device is no longer pending, got %+v", rows)
	}
	if got := app.addedDeviceIDs()["linked"]; got != retryUUID {
		t.Fatalf("addedDevices[linked] = %q, want %s", got, retryUUID)
	}
}

func TestARetryTheProviderStillRefusesBacksOff(t *testing.T) {
	app, _ := retryApp(t, collectors.RegisterPending)
	now := time.Now()
	due(t, app, now.Add(-10*time.Minute), 0)

	app.retryDeviceLinks(context.Background(), now)

	rows := pending(t, app)
	if len(rows) != 1 || rows[0].Attempts != 1 || rows[0].GaveUp {
		t.Fatalf("want one live row after one attempt, got %+v", rows)
	}
	if wait := rows[0].NextAt.Sub(now.Truncate(time.Second)); wait != 2*deviceLinkFirstRetry {
		t.Fatalf("next try in %v, want %v", wait, 2*deviceLinkFirstRetry)
	}

	// The doubling stops at the cap.
	if err := app.store.UpdatePendingDeviceLink("linked", retryUUID, 9, now, false); err != nil {
		t.Fatal(err)
	}
	app.retryDeviceLinks(context.Background(), now)
	if wait := pending(t, app)[0].NextAt.Sub(now.Truncate(time.Second)); wait != deviceLinkMaxRetry {
		t.Fatalf("next try in %v, want the %v cap", wait, deviceLinkMaxRetry)
	}
}

func TestAfterADayItGivesUpAndTellsTheUser(t *testing.T) {
	app, devices := retryApp(t, collectors.RegisterPending)
	devices.unlinked = map[string][]string{"linked": {retryUUID}}
	now := time.Now()
	due(t, app, now.Add(-25*time.Hour), 8)

	app.retryDeviceLinks(context.Background(), now)
	app.retryDeviceLinks(context.Background(), now.Add(time.Hour))

	if rows := pending(t, app); len(rows) != 1 || !rows[0].GaveUp {
		t.Fatalf("want the row marked given up, got %+v", rows)
	}
	if len(devices.asked) != 1 {
		t.Fatalf("a given-up device is not tried again, asked %v", devices.asked)
	}
	if _, ok := app.retryingDeviceIDs()["linked"]; ok {
		t.Fatal("a given-up device is no longer shown as retrying")
	}
	var found *Notification
	for _, n := range app.deviceLinkGaveUpNotifications() {
		found = &n
	}
	if found == nil || found.Level != "warning" || !strings.Contains(found.Message, retryUUID) ||
		!strings.Contains(found.Message, "Open the link.") || !strings.Contains(found.Message, "https://example.test/r/"+retryUUID) {
		t.Fatalf("want a warning naming the device, its step and link, got %+v", found)
	}
	if n := app.unlinkedDeviceNotifications([]store.Deployment{{Slug: "linked"}}); len(n) != 0 {
		t.Fatalf("one notice per device, but the generic warning fired too: %+v", n)
	}
	// The user adds it by hand: the provider's next check says it is on the account.
	devices.unlinked = nil
	if n := app.deviceLinkGaveUpNotifications(); len(n) != 0 {
		t.Fatalf("a device now on the account still warned: %+v", n)
	}
}

func TestAServiceThatIsRemovedIsDroppedWithoutAsking(t *testing.T) {
	app, devices := retryApp(t, collectors.RegisterAdded)
	now := time.Now()
	due(t, app, now.Add(-10*time.Minute), 0)
	if err := app.store.DeleteDeployment("linked"); err != nil {
		t.Fatal(err)
	}

	app.retryDeviceLinks(context.Background(), now)

	if len(devices.asked) != 0 {
		t.Fatalf("the provider must not be asked about a device that is gone, asked %v", devices.asked)
	}
	if rows := pending(t, app); len(rows) != 0 {
		t.Fatalf("want the row dropped, got %+v", rows)
	}
}

func TestCredentialsSavedWithoutARedeployKeepTheRunningDevicesRetry(t *testing.T) {
	app, devices := retryApp(t, collectors.RegisterAdded)
	now := time.Now()
	due(t, app, now.Add(-10*time.Minute), 0)
	// The old container still runs dev-1; only the saved form changed.
	if err := app.store.SaveCredentials("linked", map[string]string{"UUID": "dev-2"}); err != nil {
		t.Fatal(err)
	}

	app.retryDeviceLinks(context.Background(), now)

	if len(devices.asked) != 0 {
		t.Fatalf("the saved id is not the running one, so nothing can be added; asked %v", devices.asked)
	}
	if rows := pending(t, app); len(rows) != 1 || rows[0].DeviceID != retryUUID {
		t.Fatalf("the running device's retry must stay, got %+v", rows)
	}
	if _, ok := app.retryingDeviceIDs()["linked"]; ok {
		t.Fatal("the form's id is not being retried, so the deploy output must not say it is")
	}
}

func TestARedeployWithAnotherDeviceDropsTheOldRetry(t *testing.T) {
	app, _ := retryApp(t, collectors.RegisterFailed)
	due(t, app, time.Now().Add(-10*time.Minute), 0)

	app.registerDevice("linked", map[string]string{"UUID": "dev-2"})

	if rows := pending(t, app); len(rows) != 0 {
		t.Fatalf("the replaced device's retry must go, got %+v", rows)
	}
}

func TestARedeployAfterGivingUpGetsANewDayOfRetries(t *testing.T) {
	app, devices := retryApp(t, collectors.RegisterPending)
	old := time.Now().Add(-25 * time.Hour)
	due(t, app, old, 8)
	app.retryDeviceLinks(context.Background(), time.Now()) // gives up

	app.registerDevice("linked", map[string]string{"UUID": retryUUID}) // the redeploy
	rows := pending(t, app)
	app.retryDeviceLinks(context.Background(), time.Now()) // not due yet

	if len(rows) != 1 || rows[0].GaveUp || rows[0].Attempts != 0 || !rows[0].FirstAt.After(old) || !rows[0].NextAt.After(time.Now()) {
		t.Fatalf("want a fresh window, got %+v", rows)
	}
	if len(devices.asked) != 2 {
		t.Fatalf("the give-up try and the redeploy, nothing more; asked %v", devices.asked)
	}
}

func TestADeployThatAddsTheDeviceClearsAPendingRetry(t *testing.T) {
	app, _ := retryApp(t, collectors.RegisterPending)
	app.registerDevice("linked", map[string]string{"UUID": retryUUID})
	app.collectors.(*fakeDevices).register = collectors.RegisterAdded
	app.registerDevice("linked", map[string]string{"UUID": retryUUID})
	if rows := pending(t, app); len(rows) != 0 {
		t.Fatalf("an added device is no longer pending, got %+v", rows)
	}
}

func TestTheRetryRunsOnItsOwnLoop(t *testing.T) {
	app, devices := retryApp(t, collectors.RegisterAdded)
	due(t, app, time.Now().Add(-10*time.Minute), 0)
	app.startDeviceLinkRetry(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for len(pending(t, app)) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	app.stopDeviceLinkRetry()
	if len(devices.asked) != 1 || len(pending(t, app)) != 0 {
		t.Fatalf("the loop's first pass should add the due device, asked %v", devices.asked)
	}
}
