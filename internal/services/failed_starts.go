package services

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// StartRetry is how long the app waits between two tries to start the same container
// again (StartFailedStarts). The web worker's START_RETRY_SECONDS.
const StartRetry = 5 * time.Minute

// failedStartLister is the slice of a provider that can say which earners Docker
// could not start (runtime.DockerProvider.FailedStarts). The native runtime has no
// daemon that starts anything on its own, so it does not implement it.
type failedStartLister interface {
	FailedStarts(ctx context.Context) ([]runtime.FailedStart, error)
}

// failedStartState is when StartFailedStarts last tried each container, by id.
type failedStartState struct {
	// running lets one pass run at a time: a Start waits for its service's deploy
	// lock, which a deploy holds through its image pull, so a pass can take long.
	running sync.Mutex
	mu      sync.Mutex
	triedAt map[string]time.Time
}

// startRetryNow is the clock StartFailedStarts reads; a test moves it.
var startRetryNow = time.Now

// StartFailedStarts starts again every earner Docker means to run but could not
// start, and returns their names. Docker tries a container with a restarting policy
// once, when its daemon starts, and never again if that start fails; after an
// unclean shutdown such an earner stayed down until someone started it by hand (see
// internal/runtime/failed_starts.go for how one is recognised).
//
// Each try goes through Start or StartCopy, so a restore that did not finish and
// CASHPILOT_PROXIED_ONLY hold the earner here as they hold a Start from the
// dashboard, and a fence-gated container starts only behind the fence. One try per
// container every StartRetry; a container that is no longer listed is forgotten.
// The app runs this every runtime.FenceGuard, in its own goroutine so a Start that
// waits on a deploy never holds up the fence. A pass that finds another still
// running returns at once. Never fails.
func (m *Manager) StartFailedStarts(ctx context.Context) []string {
	if !m.failedStarts.running.TryLock() {
		return nil
	}
	defer m.failedStarts.running.Unlock()
	kinds := make([]string, 0, len(m.providers))
	for kind := range m.providers {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	var failed []runtime.FailedStart
	listed := map[runtime.Provider]bool{}
	for _, kind := range kinds {
		p := m.providers[kind]
		lister, ok := p.(failedStartLister)
		// A provider registered under two kinds is asked once.
		if !ok || listed[p] {
			continue
		}
		listed[p] = true
		found, err := lister.FailedStarts(ctx)
		if err != nil {
			// The daemon is gone; the next pass looks again. Forget nothing, or
			// a daemon that comes and goes would let every pass try at once.
			return nil
		}
		failed = append(failed, found...)
	}

	now := startRetryNow()
	m.failedStarts.mu.Lock()
	if m.failedStarts.triedAt == nil {
		m.failedStarts.triedAt = map[string]time.Time{}
	}
	seen := make(map[string]bool, len(failed))
	var due []runtime.FailedStart
	for _, f := range failed {
		seen[f.ID] = true
		if last, ok := m.failedStarts.triedAt[f.ID]; ok && now.Sub(last) < StartRetry {
			continue
		}
		m.failedStarts.triedAt[f.ID] = now
		due = append(due, f)
	}
	for id := range m.failedStarts.triedAt {
		if !seen[id] {
			delete(m.failedStarts.triedAt, id)
		}
	}
	m.failedStarts.mu.Unlock()

	var started []string
	for _, f := range due {
		// A routed service runs as its proxy's copy, and Start would start that copy,
		// not this container.
		if _, routed := m.Route(f.Slug); routed {
			continue
		}
		// The interval counts from the try itself, not from the start of the pass:
		// a Start before this one may have waited on a deploy.
		m.failedStarts.mu.Lock()
		m.failedStarts.triedAt[f.ID] = startRetryNow()
		m.failedStarts.mu.Unlock()
		var err error
		if f.Instance > 1 {
			err = m.StartCopy(ctx, f.Slug, f.Instance)
		} else {
			err = m.Start(ctx, f.Slug)
		}
		// A refusal or a second failure is already recorded as start_error.
		if err != nil {
			continue
		}
		m.store.RecordEvent(f.Slug, "start_retried", fmt.Sprintf("%s: Docker could not start it (%s)", f.Name, f.Error))
		started = append(started, f.Name)
	}
	return started
}
