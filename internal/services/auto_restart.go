package services

import (
	"context"
	"log"
	"sync"
	"time"
)

// ownerStops orders the owner's Stop and an automatic restart of the same service
// (health signals with restart: true). Both take the slug's lock, and the restart
// leaves alone a service the owner stopped after the check that asked for it began.
type ownerStops struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
	at    map[string]time.Time
}

// lock takes slug's lock and returns its release.
func (o *ownerStops) lock(slug string) func() {
	o.mu.Lock()
	if o.locks == nil {
		o.locks = map[string]*sync.Mutex{}
	}
	l := o.locks[slug]
	if l == nil {
		l = &sync.Mutex{}
		o.locks[slug] = l
	}
	o.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// record notes that the owner stopped slug now. Call it under slug's lock.
func (o *ownerStops) record(slug string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.at == nil {
		o.at = map[string]time.Time{}
	}
	o.at[slug] = time.Now()
}

// since reports whether the owner stopped slug at or after t.
func (o *ownerStops) since(slug string, t time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	at, ok := o.at[slug]
	return ok && !at.Before(t)
}

// RestartStuck restarts a running service that a health signal with restart: true
// found stuck, such as a ProxyBase client that keeps dialing a dead relay. It does
// nothing when the owner stopped the service at or after listedAt, the moment the
// check that found it began: a Stop pressed while the logs were read must not be
// undone. It is health.Restarter; the health check holds the 6-hour wait.
func (m *Manager) RestartStuck(ctx context.Context, slug string, listedAt time.Time, reason string) error {
	defer m.stops.lock(slug)()
	if m.stops.since(slug, listedAt) {
		log.Printf("services: left %s stopped: the owner stopped it", slug)
		return nil
	}
	// The state the check started from can be older than listedAt (the dashboard
	// reads it from the last refresh), and a restart starts a stopped container.
	// Ask the runtime now, under the lock a Stop holds until it is done.
	if !m.runningNow(ctx, slug) {
		log.Printf("services: left %s alone: it is not running now", slug)
		return nil
	}
	if err := m.Restart(ctx, slug); err != nil {
		return err
	}
	m.store.RecordEvent(slug, "auto_restarted", reason)
	log.Printf("services: restarted %s by itself: %.300s", slug, reason)
	return nil
}

// runningNow reports whether the runtime lists slug's first copy as running or
// restarting right now. A failed listing is false: no restart on a guess.
func (m *Manager) runningNow(ctx context.Context, slug string) bool {
	infos, err := m.providerForSlug(slug).List(ctx)
	if err != nil {
		return false
	}
	for _, info := range infos {
		if info.Slug == slug && info.Instance <= 1 {
			return info.Status == "running" || info.Status == "restarting"
		}
	}
	return false
}
