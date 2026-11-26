package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
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

// upstreamState holds the pairing loop and the last server readings.
type upstreamState struct {
	// run serialises startUpstream and stopUpstream, so the config sync and a
	// join or a Settings save that restart the loop at the same moment cannot
	// leave two loops running.
	run sync.Mutex
	// linkURL is the address startUpstream last ran for ("" when it found none),
	// so syncConfig can tell when the saved address moved under it.
	linkURL string
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
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
	// fleetExits are the public IPs of the fleet's boxes from the server's last
	// heartbeat reply, which the LAN fence drops (fenceOtherExits). fleetExitsKnown is
	// false until the first reply since the loop started: the fence then keeps the
	// drops its table has rather than lose another line's.
	fleetExits      []string
	fleetExitsKnown bool
	// stallsJudged is whether the server's last answer came from a CashPilot that
	// judges this computer's copies for traffic stalls from the counters each
	// heartbeat carries: v2.10.0 or newer, the release that added both the stall
	// check and fleet_exits, so its answer carries the fleet_exits key (stallServer).
	stallsJudged bool
	// The pairing's health, for the dashboard and Settings (pairingStatus), so a
	// failing link shows in the app and not only in the log. statusURL is the
	// server these belong to: a status from another server is not shown.
	statusURL   string
	lastAttempt time.Time
	lastOK      time.Time
	lastFailure upstream.Failure
}

// recordPairing notes the outcome of one heartbeat to serverURL.
func (a *App) recordPairing(serverURL string, failure upstream.Failure) {
	now := time.Now().UTC()
	a.upstream.mu.Lock()
	defer a.upstream.mu.Unlock()
	if a.upstream.statusURL != serverURL {
		a.upstream.statusURL, a.upstream.lastOK = serverURL, time.Time{}
	}
	a.upstream.lastAttempt, a.upstream.lastFailure = now, failure
	if failure.Code == "" {
		a.upstream.lastOK = now
	}
}

// startUpstream begins heartbeating when paired, and is a no-op when not.
//
// Idempotent: calling it again (after the user edits settings) stops the
// previous loop first, so changing the URL cannot leave two loops reporting
// under the same identity.
func (a *App) startUpstream(ctx context.Context) {
	a.upstream.run.Lock()
	defer a.upstream.run.Unlock()
	a.stopUpstreamLocked()

	cfg := a.cfg.Config()
	serverURL := strings.TrimSpace(cfg.UpstreamURL)
	a.upstream.mu.Lock()
	a.upstream.linkURL = serverURL
	a.upstream.mu.Unlock()
	if serverURL == "" {
		return // standalone — the default
	}

	interval := heartbeatInterval(cfg.UpstreamIntervalMinutes)

	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	a.upstream.mu.Lock()
	a.upstream.cancel, a.upstream.done = cancel, done
	a.upstream.mu.Unlock()

	client := upstream.New()

	go func() {
		defer close(done)
		a.sendUpstream(loopCtx, client, serverURL, "")
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				if !a.linkStillSaved(serverURL) {
					// The other process (the window or the helper) left or moved
					// this server. Sending now would keep this computer online on
					// the server's Fleet page, and a refused key would enrol it
					// again after the user removed it there. The fleet's exits
					// belong to that server too: drop them now, not at the next
					// config sync, so the fence never uses another server's list.
					a.upstream.mu.Lock()
					a.upstream.fleetExits, a.upstream.fleetExitsKnown = nil, false
					a.upstream.mu.Unlock()
					return
				}
				a.sendUpstream(loopCtx, client, serverURL, "")
			}
		}
	}()
}

// stopUpstream cancels the pairing loop and waits for it to exit. Idempotent.
func (a *App) stopUpstream() {
	a.upstream.run.Lock()
	defer a.upstream.run.Unlock()
	a.stopUpstreamLocked()
}

// linkStillSaved reads the config file again and reports whether it still names
// serverURL. The window and the helper each run this loop from their own copy
// of the config, so a join or a switch made in one reaches the other only from
// the file.
func (a *App) linkStillSaved(serverURL string) bool {
	a.refreshConfig()
	return strings.TrimSpace(a.cfg.Config().UpstreamURL) == serverURL
}

// heartbeatInterval is how often the pairing loop sends, from the saved
// interval in minutes. A var so tests can shorten it.
var heartbeatInterval = func(minutes int) time.Duration {
	if minutes <= 0 {
		minutes = config.DefaultUpstreamIntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// configSyncInterval is how often syncConfig looks for a change the other
// process saved. A var so tests can shorten it.
var configSyncInterval = time.Minute

// startConfigSync keeps this process in step with a join or a switch saved by
// the other one: the window and the background helper (--daemon) each run the
// collection scheduler and the pairing loop from their own copy of the config.
func (a *App) startConfigSync(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(configSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.syncConfig(ctx)
			}
		}
	}()
}

// syncConfig reads the config file again and, when the saved server address no
// longer matches the one the pairing loop runs for, starts or stops the loop to
// match. Collection needs nothing more: collectAll reads the file at each tick.
func (a *App) syncConfig(ctx context.Context) {
	if a.cfg == nil || ctx.Err() != nil {
		return
	}
	a.refreshConfig()
	want := strings.TrimSpace(a.cfg.Config().UpstreamURL)
	a.upstream.mu.Lock()
	have := a.upstream.linkURL
	a.upstream.mu.Unlock()
	if want != have {
		a.startUpstream(ctx)
	}
}

func (a *App) stopUpstreamLocked() {
	a.upstream.mu.Lock()
	cancel, done := a.upstream.cancel, a.upstream.done
	a.upstream.cancel, a.upstream.done = nil, nil
	a.upstream.mu.Unlock()

	// STOP THE LOOP BEFORE DROPPING WHAT IT CACHES. Clearing first leaves a
	// window in which an in-flight heartbeat -- already returned from the
	// server, merely not yet holding the mutex -- writes the OLD server's
	// figures after the clear. Re-pair to a different server and fleetView would
	// then present those figures under the NEW server's URL, which is worse than
	// showing nothing: the label makes the wrong number look authoritative.
	// (CodeRabbit, PR #116.)
	if cancel != nil {
		cancel()
		if done != nil {
			<-done
		}
	}

	a.upstream.mu.Lock()
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
	// The fleet's exits belong to the server we no longer talk to; a new pairing
	// starts from unknown again.
	a.upstream.fleetExits, a.upstream.fleetExitsKnown = nil, false
	a.upstream.stallsJudged = false
	a.upstream.mu.Unlock()
}

// sendUpstream reports to the saved pairing. Join owns the worker key, even
// when an older server sends a key back to a periodic heartbeat.
func (a *App) sendUpstream(ctx context.Context, client *upstream.Client, serverURL, enrolKey string) {
	pairing, credential, err := a.cfg.ReadPairingCredential(a.window)
	if err != nil {
		a.recordPairing(serverURL, upstream.Failure{Code: "keychain", Message: "CashPilot cannot read the saved key. Unlock the keychain or join again."})
		return
	}
	if pairing.Revision == "" {
		credential.Worker, err = config.LegacyWorkerKey(a.cfg.AppDir(), serverURL)
	} else if pairing.Suspended || pairing.ServerURL != serverURL {
		return
	} else {
		enrolKey = credential.Enrolment
	}
	token := upstream.AuthToken(credential.Worker, enrolKey)
	if err != nil || token == "" {
		a.recordPairing(serverURL, upstream.Failure{Code: "no-key", Message: "No key is available for your CashPilot. Enter the enrolment key in Settings or join again."})
		return
	}

	payload := a.upstreamPayload()
	// The fence's own reading, cached for an hour: the server hands it to the fleet's
	// other boxes, whose fences then drop this line too.
	payload.SystemInfo.EgressIP = a.egressIP(ctx)
	// Where the server sends its commands (worker_api.go); "" leaves this computer
	// report-only on the server, as before.
	if pairing.Revision != "" && credential.Worker != "" {
		payload.URL = a.workerAPIURL(serverURL)
	}
	// Every copy with its traffic counters, read in this process's turn, which ends
	// once the server answered (heartbeat_traffic.go).
	release := func() {}
	if a.services != nil {
		list, proxied := heartbeatReads(a)
		release = a.withLiveUnits(ctx, &payload, list, proxied)
	}
	current, readErr := a.cfg.Pairing()
	if readErr != nil || current.Revision != pairing.Revision || current.Suspended {
		release()
		return
	}
	resp, err := client.Send(ctx, serverURL, token, payload)
	release()
	if a.suspendRefusedPairing(pairing.Revision, serverURL, err) || ctx.Err() != nil {
		return
	}
	current, readErr = a.cfg.Pairing()
	if readErr != nil || current.Revision != pairing.Revision || current.Suspended {
		return
	}
	if err != nil {
		log.Printf("upstream: %v", err)
		a.recordPairing(serverURL, upstream.Explain(err, upstream.Host(serverURL), payload.Name))
		return
	}
	_, _, judged := upstream.ParseFleetExits(resp.FleetExits)
	a.upstream.mu.Lock()
	a.upstream.stallsJudged = judged
	a.upstream.mu.Unlock()
	a.recordPairing(serverURL, upstream.Failure{})
	// Apply the fleet's exits for this pairing.
	if a.applyFleetExits(resp.FleetExits) && a.fence != nil {
		a.fence.ReconcileFence(ctx)
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
	if upstream.Confirmed(credential.Worker, resp.WorkerKey) {
		a.pushHistoryOnce(ctx, client, serverURL, credential.Worker, pairing.Revision)
	}
}

const refusedPairingMessage = "Your CashPilot refused this computer's key. In Settings, switch to Run everything here and join again."

// A response can only suspend the revision that sent the request.
func (a *App) suspendRefusedPairing(revision, serverURL string, err error) bool {
	if !errors.Is(err, upstream.ErrUnauthorized) {
		return false
	}
	if revision != "" {
		if err := a.cfg.UpdatePairing(revision, func(p *config.Pairing) error {
			p.Suspended = true
			return nil
		}); err != nil {
			if errors.Is(err, config.ErrPairingChanged) {
				return true
			}
			log.Printf("upstream: could not save the pairing suspension: %v", err)
		}
		a.syncWorkerAPI()
		return true
	}
	a.recordPairing(serverURL, upstream.Failure{Code: "unauthorized", Message: refusedPairingMessage})
	return true
}

// pushHistoryOnce hands the server the earnings this Desktop recorded before it
// was paired, the first time it is confirmed with that server.
//
// Nothing is deleted or moved: the local rows are read, not migrated. That is
// what lets an unlinked Desktop still show exactly what it earned on its own,
// and it is why re-pairing to the same server is safe.
func (a *App) pushHistoryOnce(ctx context.Context, client *upstream.Client, serverURL, workerKey, revision string) {
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
		if a.suspendRefusedPairing(revision, serverURL, err) {
			return
		}
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

// applyFleetExits takes the fleet's exits from a heartbeat reply. It reports whether
// the list the fence drops changed, so the fence must be applied again.
//
// The rule is the web worker's (worker_api._apply_fleet_exits_reply). A null list comes
// from a server that could not read its fleet this time: the list stays as it is,
// unknown after a start, so the table keeps its drops. A reply without the key comes
// from a server that predates the list: no other exits while nothing is held. Once a
// list is held, a missing key keeps it. Only a server rolled back to before the list
// sends that, and clearing would let every earner reach the owner's other lines again,
// while keeping costs at most a stale drop until the next start or a new list.
func (a *App) applyFleetExits(raw json.RawMessage) bool {
	ips, ok, sent := upstream.ParseFleetExits(raw)
	a.upstream.mu.Lock()
	defer a.upstream.mu.Unlock()
	switch {
	case ok:
		ips = runtime.ExitTargets(ips)
	case !sent:
		if a.upstream.fleetExitsKnown {
			return false
		}
		ips = nil
	default:
		return false
	}
	if a.upstream.fleetExitsKnown && slices.Equal(ips, a.upstream.fleetExits) {
		return false
	}
	a.upstream.fleetExits, a.upstream.fleetExitsKnown = ips, true
	return true
}

// fenceOtherExits is the owner's other public IPs for the LAN fence, and whether the
// list is complete: the fleet's exits from the CashPilot server while paired, and
// those of the workers that report to this Desktop as a hub. Phones are left out: one
// on mobile data reports its carrier's address.
func (a *App) fenceOtherExits() ([]string, bool) {
	var ips []string
	complete := true
	paired := a.cfg != nil && strings.TrimSpace(a.cfg.Config().UpstreamURL) != ""
	a.upstream.mu.Lock()
	if paired {
		ips = append(ips, a.upstream.fleetExits...)
		complete = a.upstream.fleetExitsKnown
	}
	a.upstream.mu.Unlock()
	if a.store == nil {
		return ips, complete
	}
	devices, err := a.store.FleetDevices()
	if err != nil {
		// Not "no workers": the fence keeps the drops it has.
		return ips, false
	}
	for _, d := range devices {
		if d.Kind != "mobile" {
			ips = append(ips, d.EgressIP)
		}
	}
	return ips, complete
}

// egressIP is this machine's public IPv4, "" when unknown or without a fence.
func (a *App) egressIP(ctx context.Context) string {
	if a.fence == nil {
		return ""
	}
	return a.fence.EgressIP(ctx)
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

	// The stored first copies, without counters. sendUpstream replaces them with a
	// live reading of every copy (withLiveUnits) when it can take one.
	items := make([]upstream.Container, 0)
	for _, dep := range a.store.ListDeployments() {
		if strings.TrimSpace(dep.Slug) == "" {
			continue
		}
		items = append(items, upstream.Container{Slug: dep.Slug, Name: dep.Name, Status: dep.Status})
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
			Version:   AppVersion(),
			GPUGrants: a.gpuGrants(),
		},
	}
}

// gpuGrants is what the heartbeat reports as this computer's GPU grants: the
// server checks it before it deploys a GPU entry here. nil without a catalog.
func (a *App) gpuGrants() map[string]catalog.GPUSpec {
	if a.catalog == nil {
		return nil
	}
	return catalog.GPUGrants(a.catalog.List())
}

// upstreamEnrolmentKey returns the stored pairing key for the settings form.
//
// A read error yields "" rather than propagating: GetSettingsState builds the
// whole environment list, and failing it entirely would make the settings screen
// unopenable because ONE optional field could not be read. The pairing loop
// treats the same error as a hard stop, which is where it matters -- there, a
// locked keychain must not be mistaken for "no key" and trigger a re-enrolment.
func (a *App) upstreamEnrolmentKey() string {
	p, key, err := a.cfg.ReadPairingCredential(a.window)
	if err != nil {
		return ""
	}
	if p.Revision == "" {
		enrol, _ := config.LegacyEnrolmentKey(a.cfg.AppDir())
		return enrol
	}
	return key.Enrolment
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
