package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
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
	running, _ := m.runningRead(ctx, slug)
	return running
}

// runningRead is runningNow, with the error of a runtime that could not be read.
func (m *Manager) runningRead(ctx context.Context, slug string) (bool, error) {
	infos, err := m.providerForSlug(slug).List(ctx)
	if err != nil {
		return false, err
	}
	for _, info := range infos {
		if info.Slug == slug && info.Instance <= 1 {
			return info.Status == "running" || info.Status == "restarting", nil
		}
	}
	return false, nil
}

// copyStopKey is the key the owner's Stop of copy n (n >= 2) is recorded under. A
// slug has no '#', so it never meets a service's own key.
func copyStopKey(slug string, n int) string { return fmt.Sprintf("%s#%d", slug, n) }

// copyLogsMatcher is a provider that can filter a window of copy n's logs (Docker).
type copyLogsMatcher interface {
	CopyLogsMatching(ctx context.Context, slug string, n int, since time.Duration, match *regexp.Regexp, lines int) (string, error)
}

// proxiedLogsMatcher is a provider that can filter a window of a proxied copy's logs (Docker).
type proxiedLogsMatcher interface {
	ProxiedLogsMatching(ctx context.Context, slug string, proxyID int64, since time.Duration, match *regexp.Regexp, lines int) (string, error)
}

// copyRestarter is a provider that can restart copy n in place (Docker).
type copyRestarter interface {
	RestartCopy(ctx context.Context, slug string, n, stopTimeoutSeconds int) error
}

// proxiedRestarter is a provider that can restart a proxied copy's earner in place (Docker).
type proxiedRestarter interface {
	RestartProxied(ctx context.Context, slug string, proxyID int64, stopTimeoutSeconds int) error
}

// CopyLogsMatching is LogsMatching for one copy besides the service's first: copy
// instance (2 and up), or the copy through proxy proxyID when proxyID > 0, a routed
// service's included. It is health.CopyWindowReader.
func (m *Manager) CopyLogsMatching(ctx context.Context, slug string, instance int, proxyID int64, since time.Duration, match *regexp.Regexp, lines int) (string, error) {
	if proxyID > 0 {
		p, err := m.proxier()
		if err != nil {
			return "", err
		}
		reader, ok := p.(proxiedLogsMatcher)
		if !ok {
			return "", fmt.Errorf("this runtime cannot read a window of a proxied copy's logs")
		}
		return reader.ProxiedLogsMatching(ctx, slug, proxyID, since, match, lines)
	}
	if _, err := m.copier(slug, instance); err != nil {
		return "", err
	}
	reader, ok := m.providerForSlug(slug).(copyLogsMatcher)
	if !ok {
		return "", fmt.Errorf("this runtime cannot read a window of a copy's logs")
	}
	return reader.CopyLogsMatching(ctx, slug, instance, since, match, lines)
}

// RestartStuckCopy is RestartStuck for one copy besides the service's first (see
// CopyLogsMatching for which), with the same rules: never a copy the owner stopped
// at or after listedAt, never one that is not running now. The event it records
// names the copy. It is health.CopyRestarter; the health check holds the 6-hour
// wait, per copy.
func (m *Manager) RestartStuckCopy(ctx context.Context, slug string, instance int, proxyID int64, listedAt time.Time, reason string) error {
	if proxyID > 0 {
		return m.restartStuckProxied(ctx, slug, proxyID, reason)
	}
	copier, err := m.copier(slug, instance)
	if err != nil {
		return err
	}
	restarter, ok := copier.(copyRestarter)
	if !ok {
		return fmt.Errorf("this runtime cannot restart copy %d of %s", instance, slug)
	}
	key := copyStopKey(slug, instance)
	defer m.stops.lock(key)()
	if m.stops.since(key, listedAt) {
		log.Printf("services: left %s copy %d stopped: the owner stopped it", slug, instance)
		return nil
	}
	if !m.copyRunningNow(ctx, slug, instance) {
		log.Printf("services: left %s copy %d alone: it is not running now", slug, instance)
		return nil
	}
	if err := m.startAllowed(slug); err != nil {
		m.store.RecordEvent(slug, "restart_error", fmt.Sprintf("copy %d: %v", instance, err))
		return err
	}
	if err := restarter.RestartCopy(ctx, slug, instance, m.stopTimeout(slug)); err != nil {
		m.store.RecordEvent(slug, "restart_error", fmt.Sprintf("copy %d: %v", instance, err))
		return err
	}
	m.store.RecordEvent(slug, "auto_restarted", fmt.Sprintf("copy %d: %s", instance, reason))
	log.Printf("services: restarted %s copy %d by itself: %.300s", slug, instance, reason)
	return nil
}

// restartStuckProxied restarts the earner of slug's copy through proxy id where it
// is, under the copy's lock, which the owner's Stop and Start and the cap's stop
// also take. The runtime refuses a copy whose tunnel is stopped, which is how the
// owner and the cap stop one, so neither stop is undone; a capped proxy is refused
// here as well.
func (m *Manager) restartStuckProxied(ctx context.Context, slug string, id int64, reason string) error {
	mu := m.copyLock(id, slug)
	mu.Lock()
	defer mu.Unlock()
	_, s, err := m.loadProxy(id)
	if err != nil {
		return err
	}
	if s.CappedAt != "" {
		log.Printf("services: left %s through proxy %d alone: the proxy is at its traffic cap", slug, id)
		return nil
	}
	p, err := m.proxier()
	if err != nil {
		return err
	}
	restarter, ok := p.(proxiedRestarter)
	if !ok {
		return fmt.Errorf("this runtime cannot restart a proxied copy in place")
	}
	if err := restarter.RestartProxied(ctx, slug, id, m.stopTimeout(slug)); err != nil {
		// A refusal (the copy is down or its tunnel is stopped) is the runtime
		// leaving the copy as it is, not a failed restart. Anything else is one.
		var refused *runtime.ProxyError
		if !errors.As(err, &refused) {
			m.store.RecordEvent(slug, "restart_error", fmt.Sprintf("copy through proxy %d: %v", id, err))
		}
		return err
	}
	m.store.RecordEvent(slug, "auto_restarted", fmt.Sprintf("copy through proxy %d: %s", id, reason))
	log.Printf("services: restarted %s through proxy %d by itself: %.300s", slug, id, reason)
	return nil
}

// copyRunningNow reports whether the runtime lists copy n of slug as running or
// restarting right now. A failed listing is false: no restart on a guess.
func (m *Manager) copyRunningNow(ctx context.Context, slug string, n int) bool {
	infos, err := m.providerForSlug(slug).List(ctx)
	if err != nil {
		return false
	}
	for _, info := range infos {
		if info.Slug == slug && info.Instance == n {
			return info.Status == "running" || info.Status == "restarting"
		}
	}
	return false
}
