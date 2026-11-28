package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/imageupdates"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// The app image updates against the fake Docker: the decisions of
// internal/imageupdates, run through the real deploy path, the store and the
// config, as the daily run and Check now use them.

const (
	buildOne   = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	buildTwo   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	buildThree = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

// updatesUnderTest deploys honeygain from its pin, with the tag the pin names on
// the same build, and makes the check period and the gap short.
func updatesUnderTest(t *testing.T) (*appUnderTest, string) {
	t.Helper()
	settle, gap := imageUpdateSettle, imageupdates.Gap
	imageUpdateSettle, imageupdates.Gap = 1, 0
	t.Cleanup(func() { imageUpdateSettle, imageupdates.Gap = settle, gap })
	app := newAppUnderTest(t)
	svc, ok := app.catalog.Get("honeygain")
	if !ok {
		t.Fatal("honeygain is not in the catalog")
	}
	pin := runtime.ImageForArch(svc.Docker, "amd64")
	tag, floating := runtime.FollowRef(svc, "amd64")
	if !floating || !strings.Contains(pin, "@sha256:") {
		t.Fatalf("honeygain should be pinned by digest and follow a tag: pin %q, tag %q", pin, tag)
	}
	app.Docker.SetImage(pin, buildOne, "sha256:d1")
	app.Docker.SetImage(tag, buildOne, "sha256:d1")
	creds := map[string]string{"HONEYGAIN_EMAIL": "someone@example.test", "HONEYGAIN_PASSWORD": "not-a-real-password"}
	if _, err := app.DeployService("honeygain", creds, nil, nil); err != nil {
		t.Fatalf("DeployService: %v", err)
	}
	return app, tag
}

func running(t *testing.T, app *appUnderTest) (id, build string) {
	t.Helper()
	c, ok := app.Docker.Container("cashpilot-honeygain")
	if !ok || c.State != "running" {
		t.Fatalf("honeygain does not run: %+v", c)
	}
	return c.ID, c.ImageID
}

func rows(t *testing.T, app *appUnderTest) []string {
	t.Helper()
	view, err := app.GetImageUpdates()
	if err != nil {
		t.Fatalf("GetImageUpdates: %v", err)
	}
	var out []string
	for _, r := range view.History {
		out = append(out, r.Result)
	}
	return out
}

func TestANewInstallUpdatesAutomaticallyAtFourOClock(t *testing.T) {
	app := newAppUnderTest(t)
	s := imageupdates.Normalize(app.cfg.Config().ImageUpdates)
	if s.Mode != imageupdates.Auto || s.Hour != 4 || s.Offer {
		t.Fatalf("a new install starts on %+v; want auto at 04:00 with no offer", s)
	}
}

func TestAnUnchangedTagChangesNothing(t *testing.T) {
	app, _ := updatesUnderTest(t)
	id, build := running(t, app)
	for range 2 {
		got := app.runImageUpdates(context.Background(), true)
		if got["honeygain"] != "current" {
			t.Fatalf("outcome %v; want current", got)
		}
	}
	if newID, newBuild := running(t, app); newID != id || newBuild != build {
		t.Fatal("an unchanged tag recreated the container")
	}
	if r := rows(t, app); len(r) != 0 {
		t.Fatalf("an unchanged tag wrote rows: %v", r)
	}
}

func TestANewBuildIsInstalledAndABrokenOneUndoneAndHeld(t *testing.T) {
	app, tag := updatesUnderTest(t)
	oldID, _ := running(t, app)

	// The tag moves: the update installs it.
	app.Docker.SetImage(tag, buildTwo, "sha256:d2")
	if got := app.runImageUpdates(context.Background(), true); got["honeygain"] != imageupdates.Updated {
		t.Fatalf("outcome %v; want updated", got)
	}
	updatedID, build := running(t, app)
	if updatedID == oldID || build != buildTwo {
		t.Fatalf("the container runs %s; want the new build in a new container", build)
	}
	if r := rows(t, app); len(r) != 1 || r[0] != imageupdates.Updated {
		t.Fatalf("rows %v", r)
	}
	// Check now is not the day's run.
	if last := app.cfg.Config().ImageUpdates.LastRun; last != "" {
		t.Fatalf("Check now stamped the day: %q", last)
	}

	// A broken build: Docker restarts it, the update is undone, and held.
	app.Docker.SetImage(tag, buildThree, "sha256:d3")
	app.Docker.SetCrashing(buildThree, true)
	if got := app.runImageUpdates(context.Background(), true); got["honeygain"] != imageupdates.RolledBack {
		t.Fatalf("outcome %v; want rolled_back", got)
	}
	if id, b := running(t, app); id != updatedID || b != buildTwo {
		t.Fatalf("the previous container is not back: %s on %s", id, b)
	}
	notes := app.imageUpdateNotifications(app.store.ListDeployments())
	if len(notes) != 1 || notes[0].Level != "error" || !strings.Contains(notes[0].Message, "restarted by Docker") {
		t.Fatalf("the bell: %+v", notes)
	}
	created := len(app.Docker.Created())
	for range 3 {
		if got := app.runImageUpdates(context.Background(), true); got["honeygain"] != "held" {
			t.Fatalf("outcome %v; want held", got)
		}
	}
	if n := len(app.Docker.Created()); n != created {
		t.Fatalf("a held build made %d more containers", n-created)
	}
	// The tag moving back to the running build answers the bell.
	app.Docker.SetImage(tag, buildTwo, "sha256:d2")
	app.runImageUpdates(context.Background(), true)
	if notes := app.imageUpdateNotifications(app.store.ListDeployments()); len(notes) != 0 {
		t.Fatalf("the bell still shows %+v", notes)
	}
}

func TestNotifyOnlyChangesNoContainerAndTheDailyRunRunsOnce(t *testing.T) {
	app, tag := updatesUnderTest(t)
	id, _ := running(t, app)
	if _, err := app.SaveImageUpdates(imageupdates.Notify, 0, nil); err != nil {
		t.Fatal(err)
	}
	app.Docker.SetImage(tag, buildTwo, "sha256:d2")
	if got := app.runImageUpdates(context.Background(), false); got["honeygain"] != imageupdates.Available {
		t.Fatalf("outcome %v; want available", got)
	}
	if got := app.cfg.Config().ImageUpdates.LastRun; got != imageupdates.Today(time.Now()) {
		t.Fatalf("the daily run stamped %q", got)
	}
	if got := app.runImageUpdates(context.Background(), false); got != nil {
		t.Fatalf("a second daily run the same day ran: %v", got)
	}
	if newID, _ := running(t, app); newID != id {
		t.Fatal("Notify only recreated the container")
	}
	notes := app.imageUpdateNotifications(app.store.ListDeployments())
	if len(notes) != 1 || notes[0].Level != "info" {
		t.Fatalf("the bell: %+v", notes)
	}
}

func TestOffChecksNothing(t *testing.T) {
	app, tag := updatesUnderTest(t)
	if _, err := app.SaveImageUpdates(imageupdates.Off, 0, nil); err != nil {
		t.Fatal(err)
	}
	app.Docker.SetImage(tag, buildTwo, "")
	app.Docker.ResetCalls()
	if got := app.runImageUpdates(context.Background(), false); got != nil {
		t.Fatalf("Off ran: %v", got)
	}
	for _, call := range app.Docker.Calls() {
		if strings.Contains(call, "/images/create") {
			t.Fatalf("Off pulled: %v", app.Docker.Calls())
		}
	}
	// Check now with Off checks nothing either.
	if got := app.runImageUpdates(context.Background(), true); got["honeygain"] != "off" {
		t.Fatalf("Check now with Off: %v", got)
	}
}

func TestAStoppedAppIsNotStartedByAnUpdate(t *testing.T) {
	app, tag := updatesUnderTest(t)
	if err := app.services.Stop(context.Background(), "honeygain"); err != nil {
		t.Fatal(err)
	}
	app.Docker.SetImage(tag, buildTwo, "")
	if got := app.runImageUpdates(context.Background(), true); got["honeygain"] != "skipped" {
		t.Fatalf("outcome %v; want skipped", got)
	}
	if c, _ := app.Docker.Container("cashpilot-honeygain"); c.State == "running" {
		t.Fatal("an update started a stopped app")
	}
}

func TestAnUpgradedInstallIsOfferedAndStaysOff(t *testing.T) {
	app := newAppUnderTest(t)
	// The config as a release before image updates wrote it.
	if err := app.cfg.Update(func(c *config.AppConfig) { c.ImageUpdates = nil }); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	s := imageupdates.Normalize(reloaded.Config().ImageUpdates)
	if s.Mode != imageupdates.Off || !s.Offer {
		t.Fatalf("an upgraded install reads %+v; want off with the offer", s)
	}
}

// A Stop after the check refuses the update: the owner's Stop is never undone.
func TestAStopAfterTheCheckRefusesTheUpdate(t *testing.T) {
	app, tag := updatesUnderTest(t)
	id, _ := running(t, app)
	app.Docker.SetImage(tag, buildTwo, "")
	checkedAt := time.Now()
	if err := app.services.Stop(context.Background(), "honeygain"); err != nil {
		t.Fatal(err)
	}
	_, rolledBack, err := app.imageUpdateHooks().Deploy(context.Background(), "honeygain", id, buildTwo, checkedAt)
	if err == nil || rolledBack || !strings.Contains(err.Error(), "stopped after the check") {
		t.Fatalf("an update after a Stop: rolled back %v, %v", rolledBack, err)
	}
	if c, _ := app.Docker.Container("cashpilot-honeygain"); c.ID != id || c.State == "running" {
		t.Fatalf("the update touched the stopped app: %+v", c)
	}
}
