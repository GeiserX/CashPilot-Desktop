package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/fleetnet"
	stdruntime "runtime"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
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
// Two directions of traffic, and they are not the same thing. Every heartbeat
// reports what this machine is RUNNING. Once — the first time the server
// confirms this worker — it also hands over the earnings history this Desktop
// collected BEFORE it was paired, so the fleet view does not begin on the day
// of pairing with every earlier day missing from the total.
//
// The push is a copy, never a migration: the local rows are read and left
// exactly where they are. That is what lets an unlinked Desktop go on showing
// precisely what it earned on its own, and it is why re-pairing is safe.
// Ongoing collection is still the server's job — it collects centrally from
// provider accounts and returns the result in the heartbeat response.

// upstreamState is the pairing loop's stop handle plus the cached credential.
type upstreamState struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	workerKey string
	// fleetEarnings is the last figures the server reported for the platforms
	// this machine runs, or nil for UNKNOWN. Held in memory rather than stored:
	// it is the server's view, not ours, and a figure that outlived the pairing
	// would be a stale claim about an account we no longer talk to.
	fleetEarnings *upstream.FleetEarnings
	// fleetEarningsAt is when that arrived, so the UI can say how fresh it is
	// instead of implying it is live.
	fleetEarningsAt time.Time
	// historyUnsupported records that this server answered 404 to an earnings
	// import, i.e. it predates the endpoint. In MEMORY rather than in config on
	// purpose: it must stop the retry now, but an upgraded server has to be
	// tried again, and restarting Desktop or re-saving settings is the natural
	// point to find out. Persisting it would make one old server a permanent
	// verdict.
	historyUnsupported bool
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
	// Re-arm the earnings import. startUpstream calls this first, so every
	// restart -- and every unpair -- gives an upgraded server another chance,
	// which is exactly where a user who just upgraded theirs would expect it.
	a.upstream.historyUnsupported = false
	// Drop the server's figures. Unpairing must return this machine to showing
	// only what it earned on its own, and leaving the fleet-wide number on
	// screen after the link is gone would be a claim about an account we no
	// longer talk to. Cleared here rather than in startUpstream because
	// startUpstream returns early when standalone -- which is precisely the
	// unpair case.
	a.upstream.fleetEarnings, a.upstream.fleetEarningsAt = nil, time.Time{}
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

	// Whatever the server reported about the platforms this machine runs. An
	// absent block leaves the previous figures alone rather than blanking them:
	// one heartbeat that could not produce figures does not mean the account
	// earned nothing.
	if fleet, err := upstream.ParseEarnings(resp.Earnings); err != nil {
		log.Printf("upstream: %v", err)
	} else if fleet != nil {
		a.upstream.mu.Lock()
		a.upstream.fleetEarnings, a.upstream.fleetEarningsAt = fleet, time.Now().UTC()
		a.upstream.mu.Unlock()
	}

	// Only once we are CONFIRMED: the server refuses an import from a worker
	// still presenting the shared enrolment key, so attempting it mid-enrolment
	// would just log a 403 every minute.
	if upstream.Confirmed(workerKey, resp.WorkerKey) {
		a.pushHistoryOnce(ctx, client, serverURL, workerKey)
	}
}

// pushHistoryOnce hands the server the earnings this Desktop recorded before it
// was paired, the first time it is confirmed with that server.
//
// Nothing is deleted or moved: the local rows are read, not migrated. That is
// what lets an unlinked Desktop still show exactly what it earned on its own,
// and it is why re-pairing to the same server is safe.
func (a *App) pushHistoryOnce(ctx context.Context, client *upstream.Client, serverURL, workerKey string) {
	target := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	cfg := a.cfg.Config()
	if strings.TrimRight(strings.TrimSpace(cfg.UpstreamHistoryPushedTo), "/") == target {
		return
	}
	a.upstream.mu.Lock()
	unsupported := a.upstream.historyUnsupported
	a.upstream.mu.Unlock()
	if unsupported {
		return
	}

	readings := historyReadings(a.store.ListDailyBalances(upstreamHistoryDays))
	clientID := a.upstreamPayload().ClientID
	imported, skipped := 0, 0
	for _, batch := range upstream.ChunkReadings(readings, upstream.ImportChunk) {
		resp, err := client.Import(ctx, target, workerKey, upstream.ImportPayload{ClientID: clientID, Readings: batch})
		if err != nil {
			// A server without the endpoint is not a transient failure, and it
			// is not the marker's business either -- recording the marker would
			// mean an upgraded server never receives the history. Stop asking
			// for this run instead; a restart or a settings save tries again.
			if errors.Is(err, upstream.ErrImportUnsupported) {
				a.upstream.mu.Lock()
				a.upstream.historyUnsupported = true
				a.upstream.mu.Unlock()
				log.Printf("upstream: %s is too old to accept this machine's earnings history (needs CashPilot v1.16.0 or newer); not retrying until restart", target)
				return
			}
			// Do NOT record the marker: a partial push must be retried on the
			// next heartbeat, and the import is idempotent so re-sending the
			// batches that did land costs nothing but a round trip.
			if ctx.Err() == nil {
				log.Printf("upstream: could not hand %s this machine's earnings history: %v", target, err)
			}
			return
		}
		imported += resp.Imported
		skipped += len(resp.Skipped)
	}

	// Recorded even when there was nothing to send. A Desktop paired before it
	// ever collected anything has no history to hand over, and re-asking every
	// minute forever would be the same wasted work as re-sending one.
	//
	// Update, not Save: the import above is network work that can take seconds,
	// and Save writes the WHOLE config -- so saving the snapshot read at the top
	// of this function would silently discard anything the user changed on the
	// settings screen while the upload was in flight. (CodeRabbit, PR #115.)
	if err := a.cfg.Update(func(c *config.AppConfig) { c.UpstreamHistoryPushedTo = target }); err != nil {
		log.Printf("upstream: pushed %d earnings reading(s) but could not record it, so it will be re-sent: %v", imported, err)
		return
	}
	if len(readings) == 0 {
		return
	}
	if skipped > 0 {
		// Surfaced rather than swallowed: a skip means the server's catalog does
		// not know that slug, so those days are absent from the fleet total and
		// a silent drop would look identical to a complete import.
		log.Printf("upstream: handed %s %d earnings reading(s); %d were not recognised and were skipped", target, imported, skipped)
		return
	}
	log.Printf("upstream: handed %s this machine's earnings history (%d reading(s))", target, imported)
}

// upstreamHistoryDays matches the server's own retention window, so nothing is
// sent that the server would purge on its next daily pass anyway.
const upstreamHistoryDays = 400

// historyReadings converts the local daily balances into the server's shape.
//
// The FX rate is deliberately left ABSENT rather than filled from today's
// rates: Desktop does not record what a currency was worth on a past day, and
// stamping a historical reading with the current rate would misprice it
// confidently. Absent means unknown, which is the truth.
func historyReadings(balances []store.DailyBalance) []upstream.ImportReading {
	out := make([]upstream.ImportReading, 0, len(balances))
	for _, b := range balances {
		slug := strings.TrimSpace(b.Platform)
		day := strings.TrimSpace(b.Day)
		if slug == "" || day == "" {
			continue
		}
		out = append(out, upstream.ImportReading{
			Slug:     slug,
			Balance:  b.Balance,
			Date:     day,
			Currency: strings.TrimSpace(b.Currency),
		})
	}
	return out
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

// upstreamEnrolmentKey returns the stored pairing key for the settings form.
//
// A read error yields "" rather than propagating: GetSettingsState builds the
// whole environment list, and failing it entirely would make the settings screen
// unopenable because ONE optional field could not be read. The pairing loop
// treats the same error as a hard stop, which is where it matters -- there, a
// locked keychain must not be mistaken for "no key" and trigger a re-enrolment.
func (a *App) upstreamEnrolmentKey() string {
	key, err := config.UpstreamEnrolmentKey(a.cfg.AppDir())
	if err != nil {
		return ""
	}
	return key
}

// FleetView is what the dashboard shows while this Desktop is paired: the
// server's account-level figures for the platforms this machine runs.
//
// Nil means show the local numbers alone. That covers standalone (the default),
// a pairing whose server has not reported yet, and — importantly — the moment
// after unlinking, which is the behaviour that makes the whole design coherent:
// a machine that stops being paired goes back to showing exactly what it earned
// by itself, because its own rows were never moved.
type FleetView struct {
	ServerURL string `json:"serverUrl"`
	// ReportedAt is when the server last said this, in RFC3339. The UI shows it
	// rather than implying the figure is live: the heartbeat is on a timer, and
	// a number with no age reads as current when it may be an hour old.
	ReportedAt string `json:"reportedAt"`
	// WindowDays is the period the figures cover, straight from the server, so
	// the UI never has to guess whether it is showing 7 days or 30.
	WindowDays int                 `json:"windowDays"`
	Currency   string              `json:"currency"`
	Platforms  []FleetViewPlatform `json:"platforms"`
	// TotalUSD is nil when NOTHING is known. The server sums only platforms it
	// has readings for, and omits the total entirely when there are none —
	// rendering that as 0.00 would report a loss that did not happen.
	TotalUSD *float64 `json:"totalUsd"`
	// WithoutReadings names the platforms this machine runs that the server has
	// no figure for at all. Surfaced rather than hidden: those are usually a
	// missing collector or credentials nobody entered, and they are the reason
	// the total is lower than the user expects.
	WithoutReadings []string `json:"withoutReadings"`
}

// FleetViewPlatform is one platform's account-level figure.
type FleetViewPlatform struct {
	Slug string   `json:"slug"`
	USD  *float64 `json:"usd"`
	// Shared marks a platform more than one worker on the fleet runs. That is
	// exactly when "this machine earned it" stops being true, and the UI has to
	// say so rather than let the number imply otherwise.
	Shared bool `json:"shared"`
}

// fleetView returns the server's figures, or nil when there is nothing to show.
func (a *App) fleetView() *FleetView {
	serverURL := strings.TrimRight(strings.TrimSpace(a.cfg.Config().UpstreamURL), "/")
	if serverURL == "" {
		return nil // standalone -- the default, and the state after unlinking
	}
	a.upstream.mu.Lock()
	fleet, at := a.upstream.fleetEarnings, a.upstream.fleetEarningsAt
	a.upstream.mu.Unlock()
	if fleet == nil {
		// Paired, but the server has not reported. UNKNOWN, not zero.
		return nil
	}

	platforms := make([]FleetViewPlatform, 0, len(fleet.Platforms))
	for _, p := range fleet.Platforms {
		platforms = append(platforms, FleetViewPlatform{Slug: p.Slug, USD: p.USD, Shared: p.Shared})
	}
	currency := strings.TrimSpace(fleet.Currency)
	if currency == "" {
		currency = "USD"
	}
	return &FleetView{
		ServerURL:       serverURL,
		ReportedAt:      at.Format(time.RFC3339),
		WindowDays:      fleet.WindowDays,
		Currency:        currency,
		Platforms:       platforms,
		TotalUSD:        fleet.TotalUSD,
		WithoutReadings: fleet.PlatformsWithoutReadings,
	}
}
