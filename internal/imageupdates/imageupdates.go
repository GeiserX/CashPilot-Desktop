// Package imageupdates installs new builds of the app images Desktop runs, once a
// day, and undoes a build that fails. It mirrors the web app's app/image_updates.py.
//
// Most catalog apps follow a tag. Desktop pins each one by digest
// (catalog-overlay/image-pins.yml), and a deploy pulls that pinned build. The pin
// stays the known-good build that a new deploy and the Deploy button use. With
// updates on, a run pulls the tag the pin names; when that tag points at another
// build, it redeploys the app on the tag (runtime.DeploySpec.FollowTag), watches
// the new container for SettleSeconds and puts the old one back if it fails. A
// build that failed is never tried again here; the next new build is.
//
// The decisions live here, behind Hooks, so they can be tested without Docker.
package imageupdates

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// The three choices.
const (
	Off    = config.ImageUpdatesOff
	Notify = "notify"
	Auto   = config.ImageUpdatesAuto
)

// Modes are the valid choices.
var Modes = []string{Off, Notify, Auto}

// What a run can record for one app.
const (
	Updated    = "updated"
	RolledBack = "rolled_back"
	Failed     = "failed"
	Available  = "available"
	Skipped    = "skipped"
)

// SettleSeconds is how long the new container is watched before the old one goes.
const SettleSeconds = 120

// Gap is the pause after an update, so two apps never restart at once.
var Gap = 60 * time.Second

// Normalize is s with anything unreadable replaced by a safe value: an unknown mode
// is off, an hour outside 0-23 is the default hour. nil is off.
func Normalize(s *config.ImageUpdates) config.ImageUpdates {
	if s == nil {
		return config.ImageUpdates{Mode: Off, Hour: config.ImageUpdatesDefaultHour}
	}
	out := config.ImageUpdates{Mode: s.Mode, Hour: s.Hour, Offer: s.Offer, LastRun: s.LastRun}
	if !slices.Contains(Modes, out.Mode) {
		out.Mode = Off
	}
	if out.Hour < 0 || out.Hour > 23 {
		out.Hour = config.ImageUpdatesDefaultHour
	}
	for slug, mode := range s.Services {
		if slug != "" && slices.Contains(Modes, mode) {
			if out.Services == nil {
				out.Services = map[string]string{}
			}
			out.Services[slug] = mode
		}
	}
	return out
}

// EffectiveMode is the choice for one app: its own, else the one for every app.
func EffectiveMode(s config.ImageUpdates, slug string) string {
	if mode, ok := s.Services[slug]; ok && mode != "" {
		return mode
	}
	if s.Mode == "" {
		return Off
	}
	return s.Mode
}

// AnythingOn says whether any app could be checked: not every choice is off.
func AnythingOn(s config.ImageUpdates) bool {
	if s.Mode != Off && s.Mode != "" {
		return true
	}
	for _, mode := range s.Services {
		if mode != Off {
			return true
		}
	}
	return false
}

// Today is the local date a run is recorded under.
func Today(now time.Time) string { return now.Format(time.DateOnly) }

// Due says whether the daily run should start now: the hour has come and today's
// run has not happened.
func Due(s config.ImageUpdates, now time.Time) bool {
	return now.Hour() >= s.Hour && s.LastRun != Today(now)
}

// Check is what a check of one app found (runtime.ImageCheck, without Docker).
type Check struct {
	// Floating is false when the app names no tag to follow.
	Floating      bool
	LatestID      string
	LatestDigest  string
	RunningID     string
	RunningDigest string
	ContainerID   string
	Status        string
}

// Record is one history row.
type Record struct {
	Slug      string
	Result    string
	OldImage  string
	NewImage  string
	OldDigest string
	NewDigest string
	Detail    string
}

// DeployResult is what an update deploy did with the old image.
type DeployResult struct {
	NewImage string
	// OldKept says why the old image stays on disk, "" when it went.
	OldKept string
}

// Hooks are what a run needs from the app, passed in so the decisions stay testable.
type Hooks struct {
	// Check pulls the app's tag and compares builds. ok is false for an app that
	// cannot be checked here at all (a native process, no container).
	Check func(ctx context.Context, slug string) (check Check, ok bool, err error)
	// Blocker says why the app will not be updated by itself, or "".
	Blocker func(ctx context.Context, slug, containerID string) string
	// Deploy runs the update deploy of the container containerID, installing only
	// the build newImage. checkedAt is when the check began: a Stop since then
	// refuses the update. rolledBack says the new build failed its check period
	// and the old container is back.
	Deploy func(ctx context.Context, slug, containerID, newImage string, checkedAt time.Time) (result DeployResult, rolledBack bool, err error)
	// Record writes one history row.
	Record func(Record) error
	// Seen says whether a row with one of results already names newImage for slug.
	Seen func(slug, newImage string, results ...string) (bool, error)
	// Sleep waits between updates; nil waits for real.
	Sleep func(ctx context.Context, d time.Duration)
	// Holds keeps the builds undone while the app runs; nil keeps none.
	Holds *Holds
}

// Holds are the builds undone while the app runs, by app. The history row is
// what holds a build, and Holds covers a row that could not be written: the
// build stays held, and the next check writes the row again.
type Holds struct {
	mu   sync.Mutex
	rows map[string]Record
}

func (h *Holds) add(rec Record) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rows == nil {
		h.rows = map[string]Record{}
	}
	h.rows[rec.Slug+"\x00"+rec.NewImage] = rec
}

func (h *Holds) get(slug, image string) (Record, bool) {
	if h == nil {
		return Record{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	rec, ok := h.rows[slug+"\x00"+image]
	return rec, ok
}

// UpdateService checks one app and acts on mode. It returns what happened, for the
// log and the tests: off, pinned, unknown, current, held, available, skipped,
// rolled_back, failed or updated.
func UpdateService(ctx context.Context, h Hooks, slug, mode string) (string, error) {
	if mode == Off {
		return "off", nil
	}
	checkedAt := time.Now()
	check, ok, err := h.Check(ctx, slug)
	if err != nil {
		return "", err
	}
	if !ok {
		return "unknown", nil
	}
	if !check.Floating {
		return "pinned", nil
	}
	if check.LatestID == "" || check.ContainerID == "" {
		return "unknown", nil
	}
	if check.RunningID == check.LatestID {
		return "current", nil
	}
	rec := Record{Slug: slug, OldImage: check.RunningID, NewImage: check.LatestID, OldDigest: check.RunningDigest, NewDigest: check.LatestDigest}
	// A build that already failed here is never tried again; a newer one is.
	if undone, ok := h.Holds.get(slug, check.LatestID); ok {
		// Its row may not be written yet: write it now, and redeploy nothing.
		if seen, err := h.Seen(slug, check.LatestID, RolledBack); err != nil || seen {
			return "held", err
		}
		return "held", h.Record(undone)
	}
	if held, err := h.Seen(slug, check.LatestID, RolledBack); err != nil || held {
		return "held", err
	}
	if mode == Notify {
		return "available", recordOnce(h, rec, Available)
	}
	why := h.Blocker(ctx, slug, check.ContainerID)
	if why == "" && check.Status != "running" {
		why = i18n.T("go.imageupdates.notRunning")
	}
	if why != "" {
		rec.Detail = why
		return "skipped", recordOnce(h, rec, Skipped)
	}
	result, rolledBack, err := h.Deploy(ctx, slug, check.ContainerID, check.LatestID, checkedAt)
	if err != nil && !rolledBack && result.NewImage != "" {
		// The new build passed and runs; a later step of the deploy failed.
		rec.Result, rec.NewImage = Updated, result.NewImage
		rec.Detail = i18n.T("go.imageupdates.laterStep", "error", err.Error())
		if recErr := h.Record(rec); recErr != nil {
			return Updated, errors.Join(err, recErr)
		}
		return Updated, err
	}
	if err != nil {
		rec.Result, rec.Detail = Failed, err.Error()
		if rolledBack {
			rec.Result = RolledBack
			// Held at once: a row that cannot be written must not free the build.
			h.Holds.add(rec)
		}
		if recErr := h.Record(rec); recErr != nil {
			// Both reasons reach the log: the deploy's and the history's.
			return rec.Result, errors.Join(err, recErr)
		}
		return rec.Result, nil
	}
	rec.Result = Updated
	if result.NewImage != "" {
		rec.NewImage = result.NewImage
	}
	if result.OldKept != "" {
		rec.Detail = i18n.T("go.imageupdates.oldKept", "why", result.OldKept)
	}
	log.Printf("image updates: updated %s from %s to %s", slug, Short(rec.OldDigest), Short(rec.NewDigest))
	return Updated, h.Record(rec)
}

// recordOnce writes rec with result unless a row already names its new build.
func recordOnce(h Hooks, rec Record, result string) error {
	seen, err := h.Seen(rec.Slug, rec.NewImage, result)
	if err != nil || seen {
		return err
	}
	rec.Result = result
	return h.Record(rec)
}

// Run checks each app in turn; a pause follows each real update. One app's
// failure never stops the others. It returns what happened per app.
func Run(ctx context.Context, h Hooks, s config.ImageUpdates, slugs []string) map[string]string {
	outcomes := make(map[string]string, len(slugs))
	for _, slug := range slugs {
		if ctx.Err() != nil {
			break
		}
		outcome, err := UpdateService(ctx, h, slug, EffectiveMode(s, slug))
		// The pause follows every deploy, whatever its row or its error says:
		// Docker may have replaced the container before a later step failed.
		recreated := outcome == Updated || outcome == RolledBack || outcome == Failed
		if err != nil {
			log.Printf("image updates: check of %s failed: %v", slug, err)
			outcome = "error"
		}
		outcomes[slug] = outcome
		if recreated {
			sleep(ctx, h, Gap)
		}
	}
	return outcomes
}

func sleep(ctx context.Context, h Hooks, d time.Duration) {
	if h.Sleep != nil {
		h.Sleep(ctx, d)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// Short is the first 12 hex digits of a digest or image ID.
func Short(digest string) string {
	_, hex, found := strings.Cut(digest, ":")
	if !found {
		hex = digest
	}
	if len(hex) > 12 {
		hex = hex[:12]
	}
	return hex
}

// Save is the user's new choice applied to the stored setting: LastRun stays, the
// offer goes, and an app set to its default is dropped from the overrides.
func Save(current config.ImageUpdates, mode string, hour int, services map[string]string) (config.ImageUpdates, error) {
	if !slices.Contains(Modes, mode) {
		return current, fmt.Errorf("unknown mode %q", mode)
	}
	if hour < 0 || hour > 23 {
		return current, fmt.Errorf("hour %d is not 0-23", hour)
	}
	out := config.ImageUpdates{Mode: mode, Hour: hour, LastRun: current.LastRun}
	for _, slug := range slices.Sorted(maps.Keys(services)) {
		choice := services[slug]
		if choice == "" {
			continue
		}
		if !slices.Contains(Modes, choice) {
			return current, fmt.Errorf("unknown mode %q for %s", choice, slug)
		}
		if out.Services == nil {
			out.Services = map[string]string{}
		}
		out.Services[slug] = choice
	}
	return out, nil
}
