package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/filelock"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
	"github.com/GeiserX/CashPilot-Desktop/internal/imageupdates"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
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

// A daily run cut short (the app quits, or the process dies) does not count as
// the day's run: the day is stamped only when the run ends, so the apps it
// never reached are checked at the next start.
func TestADailyRunCutShortLeavesTheDayDue(t *testing.T) {
	app, tag := updatesUnderTest(t)
	if _, err := app.SaveImageUpdates(imageupdates.Notify, 0, nil); err != nil {
		t.Fatal(err)
	}
	app.Docker.SetImage(tag, buildTwo, "sha256:d2")
	before := app.cfg.Config().ImageUpdates.LastRun
	cut, cancel := context.WithCancel(context.Background())
	defer cancel()
	midRun := "unread"
	app.Docker.BeforeCall("POST /images/create", func() {
		// In the middle of the run: a process that died here left this stamp.
		midRun = app.cfg.Config().ImageUpdates.LastRun
		cancel()
	})
	app.runImageUpdates(cut, false)
	if midRun != before {
		t.Fatalf("the day was stamped %q before the run ended (was %q)", midRun, before)
	}
	if got := app.cfg.Config().ImageUpdates.LastRun; got != before {
		t.Fatalf("a cut run stamped the day: %q, was %q", got, before)
	}
	// The control: a run that ends stamps it.
	if got := app.runImageUpdates(context.Background(), false); got["honeygain"] != imageupdates.Available {
		t.Fatalf("the next daily run: %v; want it to run", got)
	}
	if got := app.cfg.Config().ImageUpdates.LastRun; got != imageupdates.Today(time.Now()) {
		t.Fatalf("a run that ended stamped %q", got)
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

// Check now takes the run before it answers: a run the helper holds is
// reported as busy, never acknowledged as started.
func TestCheckNowIsRefusedWhileAnotherRunHoldsTheLock(t *testing.T) {
	app, _ := updatesUnderTest(t)
	release, ok := filelock.Acquire(context.Background(), filepath.Join(app.cfg.DataDir(), "image_updates.lock"), 0)
	if !ok {
		t.Fatal("cannot take the lock file")
	}
	err := app.RunImageUpdates()
	release()
	if err == nil || !strings.Contains(err.Error(), i18n.T("go.imageupdates.running")) {
		t.Fatalf("Check now while the helper runs: %v; want the busy error", err)
	}
	imageUpdateRun.Lock()
	err = app.RunImageUpdates()
	imageUpdateRun.Unlock()
	if err == nil {
		t.Fatal("Check now while this process runs was acknowledged")
	}
}

// The window hears of a finished run only once both locks are free, so its
// reload never reads the finished run as running.
func TestTheRunIsAnnouncedAfterItsLocksAreFree(t *testing.T) {
	app, tag := updatesUnderTest(t)
	app.Docker.SetImage(tag, buildTwo, "sha256:d2")
	announced := make(chan bool, 2)
	saved := announceImageUpdates
	announceImageUpdates = func(a *App) {
		view, err := a.GetImageUpdates()
		free := err == nil && !view.Running
		if release, ok := filelock.Acquire(context.Background(), filepath.Join(a.cfg.DataDir(), "image_updates.lock"), 0); ok {
			release()
		} else {
			free = false
		}
		announced <- free
	}
	t.Cleanup(func() { announceImageUpdates = saved })

	app.runImageUpdates(context.Background(), true)
	if free := <-announced; !free {
		t.Fatal("the run was announced while it still held a lock")
	}
	if err := app.RunImageUpdates(); err != nil {
		t.Fatal(err)
	}
	select {
	case free := <-announced:
		if !free {
			t.Fatal("Check now was announced while it still held a lock")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Check now never announced its end")
	}
}

// An app holding NET_ADMIN or the GPU grant runs only the build the catalog
// pins, so it is never updated by itself; Notify only still reports it.
func TestAnAppWithNetworkRightsIsLeftToItsPin(t *testing.T) {
	app := newAppUnderTest(t)
	svc, ok := app.catalog.Get("mysterium")
	if !ok || !svc.Docker.FenceGated {
		t.Fatal("mysterium should be in the catalog and fence-gated")
	}
	if err := app.store.UpsertDeployment(store.Deployment{Slug: "mysterium", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if why := app.imageUpdateBlocker("mysterium", "", imageUpdateDrift{}); why != i18n.T("go.imageupdates.blocker.pinnedRights") {
		t.Fatalf("a fence-gated app: %q", why)
	}
	// The control: an app with no such rights is not held by it.
	if err := app.store.UpsertDeployment(store.Deployment{Slug: "honeygain", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if why := app.imageUpdateBlocker("honeygain", "", imageUpdateDrift{}); why != "" {
		t.Fatalf("an app with no extra rights: %q", why)
	}
}

// A custom app whose image declares a VOLUME outside its data path would start
// that folder empty in a new container, so it is left to its owner.
func TestACustomAppWithDataInAnImageVolumeIsLeftToTheOwner(t *testing.T) {
	settle, gap := imageUpdateSettle, imageupdates.Gap
	imageUpdateSettle, imageupdates.Gap = 1, 0
	t.Cleanup(func() { imageUpdateSettle, imageupdates.Gap = settle, gap })
	app := newAppUnderTest(t)
	deploy := func(name, image string) string {
		t.Helper()
		view, err := app.SaveCustomService("", services.CustomDefinition{Name: name, Image: image, DataPath: "/data"})
		if err != nil {
			t.Fatalf("SaveCustomService: %v", err)
		}
		app.Docker.SetImage(image, buildOne, "sha256:d1")
		if _, err := app.DeployService(view.Slug, nil, nil, nil); err != nil {
			t.Fatalf("DeployService: %v", err)
		}
		app.Docker.SetImage(image, buildTwo, "sha256:d2")
		return view.Slug
	}
	app.Docker.SetImageVolumes(buildOne, "/var/lib/mine")
	app.Docker.SetImageVolumes(buildTwo, "/var/lib/mine")
	loose := deploy("Loose", "org/loose:stable")
	app.Docker.SetImageVolumes(buildOne)
	app.Docker.SetImageVolumes(buildTwo)
	plain := deploy("Plain", "org/plain:stable")
	got := app.runImageUpdates(context.Background(), true)
	if got[loose] != "skipped" || got[plain] != imageupdates.Updated {
		t.Fatalf("outcomes %v; want %s skipped and %s updated", got, loose, plain)
	}
	view, _ := app.GetImageUpdates()
	for _, row := range view.History {
		if row.Slug == loose && !strings.Contains(row.Detail, "/var/lib/mine") {
			t.Fatalf("the reason does not name the folder: %q", row.Detail)
		}
	}
	// Mounts that could not be read are not clean: the replacement would take
	// an anonymous volume with the old container. The check reads the container
	// the update replaces, so another clean one of the app's (a copy set aside)
	// does not stand in for it.
	id, _ := app.Docker.Container("cashpilot-" + plain)
	if id.ID == "" {
		t.Fatal("the plain app has no container")
	}
	if why := app.imageUpdateBlocker(plain, id.ID, imageUpdateDrift{loose: map[string][]string{"ctr-set-aside": nil}}); why != i18n.T("go.imageupdates.blocker.mountsUnread") {
		t.Fatalf("a custom app whose container was not read: %q", why)
	}
	if why := app.imageUpdateBlocker(plain, id.ID, imageUpdateDrift{loose: map[string][]string{id.ID: nil}}); why != "" {
		t.Fatalf("a custom app read with no loose folder: %q", why)
	}
}
