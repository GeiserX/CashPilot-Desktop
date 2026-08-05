package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/fleetnet"
	stdruntime "runtime"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// Optional pairing: report this Desktop to a CashPilot server as a worker.
//
// Desktop was only ever a hub — it RECEIVES heartbeats and has no outbound
// direction — so running it alongside a CashPilot server meant owning two hubs
// that each believed they were the source of truth. This is the missing
// direction, and it is entirely opt-in: an empty UpstreamURL means standalone,
// which stays the default and loses nothing.
//
// It reports what this machine is RUNNING. It does not send earnings, and does
// not touch Desktop's local earnings history: the server collects earnings
// centrally from provider accounts and returns them in the heartbeat response.
// What should happen to Desktop's existing local history when a user pairs is a
// genuinely open question and is deliberately not answered here.

// upstreamState is the pairing loop's stop handle plus the cached credential.
type upstreamState struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	workerKey string
}

// startUpstream begins heartbeating when paired, and is a no-op when not.
//
// Idempotent: calling it again (after the user edits settings) stops the
// previous loop first, so changing the URL cannot leave two loops reporting
// under the same identity.
func (a *App) startUpstream(ctx context.Context) {
	a.stopUpstream()

	cfg := a.cfg.Config()
	serverURL := strings.TrimSpace(cfg.UpstreamURL)
	if serverURL == "" {
		return // standalone — the default
	}

	enrol, err := config.UpstreamEnrolmentKey(a.cfg.AppDir())
	if err != nil {
		// A locked keychain is NOT "no key". Re-enrolling would orphan this
		// machine's existing worker identity on the server, so refuse and say so.
		log.Printf("upstream: cannot read the enrolment key, not pairing: %v", err)
		return
	}
	workerKey, err := config.UpstreamWorkerKey(a.cfg.AppDir())
	if err != nil {
		log.Printf("upstream: cannot read the worker key, not pairing: %v", err)
		return
	}
	if upstream.AuthToken(workerKey, enrol) == "" {
		log.Printf("upstream: %s is configured but no key is stored; not pairing", serverURL)
		return
	}

	interval := time.Duration(cfg.UpstreamIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = time.Duration(config.DefaultUpstreamIntervalMinutes) * time.Minute
	}

	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	a.upstream.mu.Lock()
	a.upstream.cancel, a.upstream.done, a.upstream.workerKey = cancel, done, workerKey
	a.upstream.mu.Unlock()

	client := upstream.New(fleetnet.Policy{
		Mode:          cfg.WorkerURLPolicy,
		AllowedHosts:  cfg.WorkerAllowedHosts,
		AllowMetadata: cfg.WorkerAllowMetadata,
	})

	go func() {
		defer close(done)
		a.sendUpstream(loopCtx, client, serverURL, enrol) // enrol promptly, not after one interval
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				a.sendUpstream(loopCtx, client, serverURL, enrol)
			}
		}
	}()
}

// stopUpstream cancels the pairing loop and waits for it to exit. Idempotent.
func (a *App) stopUpstream() {
	a.upstream.mu.Lock()
	cancel, done := a.upstream.cancel, a.upstream.done
	a.upstream.cancel, a.upstream.done = nil, nil
	a.upstream.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

// sendUpstream posts one heartbeat and persists a newly issued worker key.
func (a *App) sendUpstream(ctx context.Context, client *upstream.Client, serverURL, enrolKey string) {
	a.upstream.mu.Lock()
	workerKey := a.upstream.workerKey
	a.upstream.mu.Unlock()

	resp, err := client.Send(ctx, serverURL, upstream.AuthToken(workerKey, enrolKey), a.upstreamPayload())
	if err != nil {
		// Never fatal: a phone or laptop is offline often, and an unreachable
		// server must not stop Desktop from working standalone.
		if ctx.Err() == nil {
			log.Printf("upstream: %v", err)
		}
		return
	}
	if next := upstream.KeyToPersist(workerKey, resp.WorkerKey); next != "" {
		if err := config.SetUpstreamWorkerKey(a.cfg.AppDir(), next); err != nil {
			// Losing the issued key is recoverable — the server re-delivers it
			// until the worker authenticates with it — but it must be visible,
			// because until it sticks every heartbeat re-enrols.
			log.Printf("upstream: could not persist the issued worker key: %v", err)
			return
		}
		a.upstream.mu.Lock()
		a.upstream.workerKey = next
		a.upstream.mu.Unlock()
		log.Printf("upstream: enrolled with %s and stored this machine's own key", serverURL)
	}
}

// upstreamPayload describes what this machine is running, in the server's shape.
func (a *App) upstreamPayload() upstream.Payload {
	host := runtime.DeviceHostname()

	// The machine's HOSTNAME, not cfg.HostnamePrefix. That setting is the
	// CONTAINER name prefix ("containers are named <prefix>-<service>") and it
	// defaults to "cashpilot", so using it here would report every paired
	// Desktop on every fleet as a worker called "cashpilot". A Docker worker
	// defaults its own CASHPILOT_WORKER_NAME to the hostname for the same
	// reason. Caught by a test, not by reading.
	name := host

	items := make([]upstream.Item, 0)
	for _, dep := range a.store.ListDeployments() {
		if strings.TrimSpace(dep.Slug) == "" {
			continue
		}
		items = append(items, upstream.Item{Slug: dep.Slug, Name: dep.Name, Status: dep.Status})
	}

	return upstream.Payload{
		Name: name,
		// client_id is the STABLE identity the server keys a worker on. The
		// hostname is used rather than the display name because renaming the
		// machine in settings must not enrol a second worker and split this
		// device's history in two.
		ClientID:   host,
		Containers: items,
		Apps:       []upstream.Item{},
		SystemInfo: upstream.SystemInfo{
			OS:         stdruntime.GOOS,
			Arch:       stdruntime.GOARCH,
			Hostname:   host,
			DeviceType: "desktop",
			// Injected at link time from the release tag; empty on an unstamped
			// local build. The field is omitempty, so empty is sent as ABSENT and
			// the server records UNKNOWN rather than a match -- never a guess,
			// because a wrong version hides exactly the stale install this is
			// meant to reveal.
			Version: AppVersion(),
		},
	}
}
