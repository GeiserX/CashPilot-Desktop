package main

// App image updates (internal/imageupdates): the daily run, the Settings card's
// bindings and the bell. The decisions live in the package; this file connects
// them to the store, the config, the deploy path and the runtime's check.

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/filelock"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
	"github.com/GeiserX/CashPilot-Desktop/internal/imageupdates"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// imageUpdateTick is how often the app looks whether the daily run is due.
var imageUpdateTick = 5 * time.Minute

// imageUpdateSettle is the check period of an update deploy, short in tests.
var imageUpdateSettle = imageupdates.SettleSeconds

// announceImageUpdates tells the window that a run finished; tests replace it
// to see when it fires.
var announceImageUpdates = func(a *App) { a.emitEvent("imageupdates:changed") }

// imageUpdateHistory is how many rows the Settings card shows.
const imageUpdateHistory = 200

// imageUpdateRun is the run of this process; the window and the background
// helper also share a lock file (runImageUpdates), so only one of them runs.
var imageUpdateRun sync.Mutex

// ImageUpdateApp is one app the setting applies to.
type ImageUpdateApp struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// ImageUpdateRow is one history row, with the app's name.
type ImageUpdateRow struct {
	store.ImageUpdate
	Name string `json:"name"`
}

// ImageUpdatesView is what the Settings card shows.
type ImageUpdatesView struct {
	Settings      config.ImageUpdates `json:"settings"`
	SettleSeconds int                 `json:"settleSeconds"`
	Apps          []ImageUpdateApp    `json:"apps"`
	History       []ImageUpdateRow    `json:"history"`
	Running       bool                `json:"running"`
}

// GetImageUpdates returns the setting, the apps it applies to and the history.
func (a *App) GetImageUpdates() (ImageUpdatesView, error) {
	if err := a.ready(); err != nil {
		return ImageUpdatesView{}, err
	}
	view := ImageUpdatesView{
		Settings:      imageupdates.Normalize(a.cfg.Config().ImageUpdates),
		SettleSeconds: imageUpdateSettle,
		Apps:          []ImageUpdateApp{},
		History:       []ImageUpdateRow{},
	}
	for _, dep := range a.store.ListDeployments() {
		view.Apps = append(view.Apps, ImageUpdateApp{Slug: dep.Slug, Name: a.serviceName(dep.Slug)})
	}
	sort.Slice(view.Apps, func(i, j int) bool { return view.Apps[i].Name < view.Apps[j].Name })
	rows, err := a.store.ListImageUpdates(imageUpdateHistory)
	if err != nil {
		return view, err
	}
	for _, row := range rows {
		view.History = append(view.History, ImageUpdateRow{ImageUpdate: row, Name: a.serviceName(row.Slug)})
	}
	if imageUpdateRun.TryLock() {
		imageUpdateRun.Unlock()
	} else {
		view.Running = true
	}
	return view, nil
}

// SaveImageUpdates saves the choice for every app, the hour and the choice per
// app ("" follows the one for every app). It keeps the date of the last run and
// ends the offer.
func (a *App) SaveImageUpdates(mode string, hour int, apps map[string]string) (config.ImageUpdates, error) {
	if err := a.ready(); err != nil {
		return config.ImageUpdates{}, err
	}
	var saved config.ImageUpdates
	var bad error
	// One read-modify-write under the config lock, so a run's date stamp and this
	// save never overwrite each other.
	err := a.cfg.Update(func(cfg *config.AppConfig) {
		current := imageupdates.Normalize(cfg.ImageUpdates)
		next, err := imageupdates.Save(current, mode, hour, apps)
		if err != nil {
			bad = err
			return
		}
		cfg.ImageUpdates, saved = &next, next
	})
	if bad != nil {
		return config.ImageUpdates{}, bad
	}
	return saved, err
}

// RunImageUpdates checks every app now, as the daily run does, in the
// background. It does not count as the day's run. It takes the run before it
// answers, so a busy run is reported and never acknowledged as started.
func (a *App) RunImageUpdates() error {
	if err := a.ready(); err != nil {
		return err
	}
	release, ok := a.reserveImageUpdates(a.ctx)
	if !ok {
		return errors.New(i18n.T("go.imageupdates.running"))
	}
	go func() {
		a.runReservedImageUpdates(a.ctx, true)
		release()
		announceImageUpdates(a)
	}()
	return nil
}

// reserveImageUpdates takes this process's run and the lock file that the
// window and the background helper share. release gives both back.
func (a *App) reserveImageUpdates(ctx context.Context) (release func(), ok bool) {
	if !imageUpdateRun.TryLock() {
		return nil, false
	}
	unlock, ok := filelock.Acquire(ctx, filepath.Join(a.cfg.DataDir(), "image_updates.lock"), 0)
	if !ok {
		imageUpdateRun.Unlock()
		return nil, false
	}
	return func() {
		unlock()
		imageUpdateRun.Unlock()
	}, true
}

// startImageUpdates looks every few minutes whether the daily run is due.
func (a *App) startImageUpdates(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(imageUpdateTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.runImageUpdates(ctx, false)
			}
		}
	}()
}

// runImageUpdates is the daily run (force false: only when it is due, and it
// stamps the day) or Check now (force true). Never panics, never overlaps.
// The event goes out after both locks are free, so the card never reads a
// finished run as running.
func (a *App) runImageUpdates(ctx context.Context, force bool) map[string]string {
	if ctx == nil || a.cfg == nil || a.store == nil || a.services == nil {
		return nil
	}
	release, ok := a.reserveImageUpdates(ctx)
	if !ok {
		return nil
	}
	outcomes, ran := a.runReservedImageUpdates(ctx, force)
	release()
	if ran {
		announceImageUpdates(a)
	}
	return outcomes
}

// runReservedImageUpdates is the run itself, with both locks held by the
// caller. ran is false when the daily run was not due.
func (a *App) runReservedImageUpdates(ctx context.Context, force bool) (outcomes map[string]string, ran bool) {
	settings := imageupdates.Normalize(a.cfg.Config().ImageUpdates)
	if !force {
		// A cheap look first: the config is written only on the day's run.
		if !imageupdates.AnythingOn(settings) || !imageupdates.Due(settings, time.Now()) {
			return nil, false
		}
		due, today := false, ""
		// Decide again from the file, under both locks: the window and the
		// helper never both run the day.
		if err := a.cfg.Update(func(cfg *config.AppConfig) {
			settings = imageupdates.Normalize(cfg.ImageUpdates)
			now := time.Now()
			due, today = imageupdates.AnythingOn(settings) && imageupdates.Due(settings, now), imageupdates.Today(now)
		}); err != nil {
			log.Printf("image updates: cannot read the day's run: %v", err)
			return nil, false
		}
		if !due {
			return nil, false
		}
		// The day counts as run only once the run ends, still under both locks.
		// A run cut short (the app quits, or the process dies) leaves the day
		// due, and the apps it never reached are checked at the next start.
		defer func() {
			if ctx.Err() != nil {
				return
			}
			if err := a.cfg.Update(func(cfg *config.AppConfig) {
				stamped := imageupdates.Normalize(cfg.ImageUpdates)
				stamped.LastRun = today
				cfg.ImageUpdates = &stamped
			}); err != nil {
				log.Printf("image updates: cannot save the day's run: %v", err)
			}
		}()
	}
	var slugs []string
	for _, dep := range a.store.ListDeployments() {
		slugs = append(slugs, dep.Slug)
	}
	sort.Strings(slugs)
	log.Printf("image updates: checking %d app(s)", len(slugs))
	outcomes = imageupdates.Run(ctx, a.imageUpdateHooks(), settings, slugs)
	for slug, outcome := range outcomes {
		if slices.Contains([]string{"current", imageupdates.Updated, "pinned", "off"}, outcome) {
			if err := a.store.ClearImageUpdates(slug); err != nil {
				log.Printf("image updates: %s: %v", slug, err)
			}
		}
	}
	return outcomes, true
}

func (a *App) imageUpdateHooks() imageupdates.Hooks {
	return imageupdates.Hooks{
		Check: func(ctx context.Context, slug string) (imageupdates.Check, bool, error) {
			check, ok, err := a.services.CheckImage(ctx, slug)
			if errors.Is(err, runtime.ErrNoContainer) {
				return imageupdates.Check{}, false, nil
			}
			if err != nil || !ok {
				return imageupdates.Check{}, false, err
			}
			return imageupdates.Check{
				Floating: check.Floating, LatestID: check.LatestID, LatestDigest: check.LatestDigest,
				RunningID: check.RunningID, RunningDigest: check.RunningDigest,
				ContainerID: check.ContainerID, Status: check.Status,
			}, true, nil
		},
		Blocker: func(ctx context.Context, slug, containerID string) string {
			// Read for each app, right after its check: an earlier app's update
			// takes minutes, and the owner can redeploy another app meanwhile.
			loose, err := a.services.LooseMounts(ctx)
			if err != nil {
				loose = nil
			}
			return a.imageUpdateBlocker(slug, containerID, imageUpdateDrift{found: a.services.ContainerDrift(ctx), loose: loose})
		},
		Deploy: func(_ context.Context, slug, containerID, newImage string, checkedAt time.Time) (imageupdates.DeployResult, bool, error) {
			var result imageupdates.DeployResult
			opts := services.DeployOptions{
				ExpectedContainer: &containerID,
				FollowTag:         true,
				SettleSeconds:     imageUpdateSettle,
				ExpectImage:       newImage,
				CheckedAt:         checkedAt,
				ImageUpdate: func(done *runtime.ImageUpdateResult) {
					result = imageupdates.DeployResult{NewImage: done.NewImage, OldKept: done.OldKept}
				},
			}
			// The Redeploy button's path: the saved settings, the image the
			// deployment follows, its wallet and its copies as they are.
			if _, _, err := a.deployService(slug, nil, opts, false); err != nil {
				var undone *runtime.ImageUpdateUndone
				return result, errors.As(err, &undone), err
			}
			return result, false, nil
		},
		Record: func(r imageupdates.Record) error {
			return a.store.RecordImageUpdate(store.ImageUpdate{
				Slug: r.Slug, Result: r.Result, OldImage: r.OldImage, NewImage: r.NewImage,
				OldDigest: r.OldDigest, NewDigest: r.NewDigest, Detail: r.Detail,
			})
		},
		Seen:  a.store.ImageUpdateSeen,
		Holds: &a.imageUpdateHolds,
	}
}

// imageUpdateDrift is how the running containers differ from what a redeploy
// builds (found, by slug), and the folders a new container would start empty
// (loose, by container ID). A container missing from loose was not read.
type imageUpdateDrift struct {
	found map[string][]string
	loose map[string][]string
}

// imageUpdateBlocker says why slug is not updated by itself, or "": the same
// cases the web app leaves to the owner.
func (a *App) imageUpdateBlocker(slug, containerID string, drift imageUpdateDrift) string {
	dep, ok, err := a.store.GetDeployment(slug)
	if err != nil || !ok {
		return i18n.T("go.imageupdates.blocker.noRecord")
	}
	svc, inCatalog := a.catalog.Get(slug)
	if inCatalog && catalog.IsRetired(svc.Status) {
		return i18n.T("go.imageupdates.blocker.retired")
	}
	if inCatalog && (svc.Docker.FenceGated || svc.Docker.GPU != nil) {
		// NET_ADMIN and the GPU grant trust the reviewed build the catalog pins
		// (DeployWallet), never whatever its tag names later.
		return i18n.T("go.imageupdates.blocker.pinnedRights")
	}
	if dep.CustomImage != "" {
		return i18n.T("go.imageupdates.blocker.chosenImage")
	}
	if dep.Instances > 1 {
		return i18n.T("go.imageupdates.blocker.copies", "n", dep.Instances)
	}
	if _, routed := a.services.Route(slug); routed {
		return i18n.T("go.imageupdates.blocker.proxied")
	}
	for _, proxied := range a.services.ProxiedCopies() {
		if proxied.Slug == slug {
			return i18n.T("go.imageupdates.blocker.proxied")
		}
	}
	if found := drift.found[slug]; len(found) > 0 {
		return i18n.T("go.imageupdates.blocker.drift", "what", strings.Join(found, "; "))
	}
	if inCatalog && (svc.Custom || catalog.IsCustomSlug(slug)) {
		// The container the update replaces, not another one of the app's.
		loose, read := drift.loose[containerID]
		if !read {
			// Unknown is not clean: the replacement would take an anonymous
			// volume with the old container.
			return i18n.T("go.imageupdates.blocker.mountsUnread")
		}
		if len(loose) > 0 {
			return i18n.T("go.imageupdates.blocker.imageVolume", "paths", strings.Join(loose, ", "))
		}
	}
	return ""
}

// imageUpdateNotifications is the bell's part: the newest row of each deployed
// app that a later check has not answered.
func (a *App) imageUpdateNotifications(deployments []store.Deployment) []Notification {
	if a.store == nil {
		return nil
	}
	rows, err := a.store.LatestImageUpdates()
	if err != nil {
		return nil
	}
	deployed := map[string]bool{}
	for _, dep := range deployments {
		deployed[dep.Slug] = true
	}
	var items []Notification
	for _, row := range rows {
		if row.Cleared || !deployed[row.Slug] {
			continue
		}
		name := a.serviceName(row.Slug)
		switch row.Result {
		case imageupdates.RolledBack:
			items = append(items, Notification{Level: "error", Title: i18n.T("go.imageupdates.notify.undone", "name", name), Message: row.Detail})
		case imageupdates.Failed:
			items = append(items, Notification{Level: "warning", Title: i18n.T("go.imageupdates.notify.failed", "name", name), Message: row.Detail})
		case imageupdates.Available:
			items = append(items, Notification{Level: "info", Title: i18n.T("go.imageupdates.notify.available", "name", name), Message: i18n.T("go.imageupdates.notify.availableMessage")})
		case imageupdates.Skipped:
			items = append(items, Notification{Level: "info", Title: i18n.T("go.imageupdates.notify.skipped", "name", name), Message: row.Detail})
		}
	}
	return items
}
