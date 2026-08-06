package main

// CashPilot-Desktop-xjr, second half: showing the complete picture while paired.
//
// The first half uploads the history this machine collected alone. This shows
// the result -- the server's ACCOUNT-LEVEL figures for the platforms this
// machine runs -- and, just as importantly, stops showing it the moment the
// machine is no longer paired.
//
// Two rules run through every test here:
//
//   - **Absent is UNKNOWN, never zero.** A platform with no reading has never
//     been collected for. Rendering that as 0.00 reports a loss that did not
//     happen, and it does so most convincingly for the user who is worst off.
//   - **The figure is per PLATFORM, not per device.** If two machines run the
//     same service the provider reports one balance and nothing can split it.
//     Shared marks exactly where "this machine earned it" stops being true.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/fleetnet"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// pairedApp is an App configured as paired with serverURL.
func pairedApp(t *testing.T, serverURL string) *App {
	t.Helper()
	app := newPayloadTestApp(t, nil)
	if err := app.cfg.Update(func(c *config.AppConfig) { c.UpstreamURL = serverURL }); err != nil {
		t.Fatalf("pairing the test app: %v", err)
	}
	return app
}

func reportEarnings(t *testing.T, app *App, payload any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workers/earnings-import" {
			_ = json.NewEncoder(w).Encode(upstream.ImportResponse{Status: "ok"})
			return
		}
		body := map[string]any{"status": "ok", "worker_id": 1}
		if payload != nil {
			body["earnings"] = payload
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testClient(srv *httptest.Server) *upstream.Client {
	return &upstream.Client{
		HTTP:   srv.Client(),
		Policy: fleetnet.Policy{Mode: "private", AllowedHosts: []string{"127.0.0.1"}},
	}
}

func TestStandaloneShowsNoFleetView(t *testing.T) {
	// The default, and it must lose nothing: a Desktop that was never paired
	// shows its own numbers and says nothing about a fleet.
	app := newPayloadTestApp(t, nil)
	if got := app.fleetView(); got != nil {
		t.Fatalf("a standalone Desktop produced a fleet view: %+v", got)
	}
}

func TestPairedButNotYetReportedShowsNoFleetView(t *testing.T) {
	// Paired is not the same as informed. Until the server reports, the
	// fleet-wide figure is UNKNOWN -- and an empty panel reading 0.00 would be a
	// worse answer than no panel.
	app := pairedApp(t, "http://127.0.0.1:9")
	if got := app.fleetView(); got != nil {
		t.Fatalf("produced a fleet view before the server said anything: %+v", got)
	}
}

func TestAHeartbeatsFiguresBecomeTheFleetView(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	srv := reportEarnings(t, app, map[string]any{
		"window_days": 30,
		"currency":    "USD",
		"platforms": []map[string]any{
			{"slug": "grass", "usd": 4.25, "shared_with_other_workers": true},
			{"slug": "honeygain", "usd": 1.5, "shared_with_other_workers": false},
			{"slug": "storj", "usd": nil, "shared_with_other_workers": false},
		},
		"total_usd":                  5.75,
		"platforms_without_readings": []string{"storj"},
	})
	if err := app.cfg.Update(func(c *config.AppConfig) { c.UpstreamURL = srv.URL }); err != nil {
		t.Fatal(err)
	}
	app.upstream.mu.Lock()
	app.upstream.workerKey = "own-key"
	app.upstream.mu.Unlock()

	app.sendUpstream(context.Background(), testClient(srv), srv.URL, "enrol")

	view := app.fleetView()
	if view == nil {
		t.Fatal("the server reported figures and none were shown")
	}
	if view.WindowDays != 30 || view.Currency != "USD" {
		t.Fatalf("window/currency lost: %+v", view)
	}
	if view.TotalUSD == nil || *view.TotalUSD != 5.75 {
		t.Fatalf("TotalUSD = %v", view.TotalUSD)
	}
	if len(view.Platforms) != 3 {
		t.Fatalf("got %d platforms", len(view.Platforms))
	}
	if view.ReportedAt == "" {
		t.Fatal("no timestamp, so the UI cannot say how old the figure is")
	}
}

func TestAPlatformWithNoReadingStaysUnknown(t *testing.T) {
	// The single most important rule here. A nil USD must survive as nil all the
	// way to the frontend; the moment it becomes 0.0 the UI cannot tell "we have
	// no reading" from "you earned nothing".
	app := newPayloadTestApp(t, nil)
	srv := reportEarnings(t, app, map[string]any{
		"window_days":                30,
		"currency":                   "USD",
		"platforms":                  []map[string]any{{"slug": "storj", "usd": nil}},
		"total_usd":                  nil,
		"platforms_without_readings": []string{"storj"},
	})
	if err := app.cfg.Update(func(c *config.AppConfig) { c.UpstreamURL = srv.URL }); err != nil {
		t.Fatal(err)
	}
	app.upstream.mu.Lock()
	app.upstream.workerKey = "own-key"
	app.upstream.mu.Unlock()
	app.sendUpstream(context.Background(), testClient(srv), srv.URL, "enrol")

	view := app.fleetView()
	if view == nil {
		t.Fatal("no view")
	}
	if view.Platforms[0].USD != nil {
		t.Fatalf("an unknown reading became %v", *view.Platforms[0].USD)
	}
	if view.TotalUSD != nil {
		t.Fatalf("a total with nothing known became %v", *view.TotalUSD)
	}
	if len(view.WithoutReadings) != 1 || view.WithoutReadings[0] != "storj" {
		t.Fatalf("WithoutReadings = %v -- the user cannot see why the total is low", view.WithoutReadings)
	}
}

func TestASharedPlatformIsMarkedAsShared(t *testing.T) {
	// Two machines running Grass means the provider reports ONE balance. The UI
	// must not let the number imply this machine earned it.
	app := newPayloadTestApp(t, nil)
	srv := reportEarnings(t, app, map[string]any{
		"window_days": 30,
		"currency":    "USD",
		"platforms": []map[string]any{
			{"slug": "grass", "usd": 4.0, "shared_with_other_workers": true},
			{"slug": "honeygain", "usd": 1.0, "shared_with_other_workers": false},
		},
		"total_usd": 5.0,
	})
	if err := app.cfg.Update(func(c *config.AppConfig) { c.UpstreamURL = srv.URL }); err != nil {
		t.Fatal(err)
	}
	app.upstream.mu.Lock()
	app.upstream.workerKey = "own-key"
	app.upstream.mu.Unlock()
	app.sendUpstream(context.Background(), testClient(srv), srv.URL, "enrol")

	byslug := map[string]bool{}
	for _, p := range app.fleetView().Platforms {
		byslug[p.Slug] = p.Shared
	}
	if !byslug["grass"] {
		t.Fatal("a platform running on several workers was not marked shared")
	}
	if byslug["honeygain"] {
		t.Fatal("a platform on one worker was marked shared")
	}
}

func TestUnlinkingReturnsToTheLocalPictureAlone(t *testing.T) {
	// The behaviour that makes the whole design coherent, and the one the user
	// asked for in as many words. It works because the local rows were COPIED,
	// never moved -- so there is nothing to restore, only a view to switch back.
	app := newPayloadTestApp(t, nil)
	srv := reportEarnings(t, app, map[string]any{
		"window_days": 30, "currency": "USD",
		"platforms": []map[string]any{{"slug": "grass", "usd": 4.0}},
		"total_usd": 4.0,
	})
	if err := app.cfg.Update(func(c *config.AppConfig) { c.UpstreamURL = srv.URL }); err != nil {
		t.Fatal(err)
	}
	app.upstream.mu.Lock()
	app.upstream.workerKey = "own-key"
	app.upstream.mu.Unlock()
	app.sendUpstream(context.Background(), testClient(srv), srv.URL, "enrol")
	if app.fleetView() == nil {
		t.Fatal("precondition failed: no fleet view while paired")
	}

	// Unlink, exactly as clearing the field in Settings does.
	if err := app.cfg.Update(func(c *config.AppConfig) { c.UpstreamURL = "" }); err != nil {
		t.Fatal(err)
	}
	app.stopUpstream()

	if got := app.fleetView(); got != nil {
		t.Fatalf("an unlinked Desktop still shows the fleet figure: %+v", got)
	}
	app.upstream.mu.Lock()
	cached := app.upstream.fleetEarnings
	app.upstream.mu.Unlock()
	if cached != nil {
		t.Fatal("the server's figures outlived the pairing in memory")
	}
}

func TestAHeartbeatLandingDuringUnlinkDoesNotSurviveIt(t *testing.T) {
	// The ordering inside stopUpstream, and it is not a theoretical race.
	//
	// stopUpstream used to clear the cache and THEN cancel the loop. A heartbeat
	// already returned from the server -- merely not yet holding the mutex --
	// would write the OLD server's figures after the clear. Re-pair to a
	// DIFFERENT server and fleetView presents those figures under the new
	// server's URL, which is worse than showing nothing: the label makes the
	// wrong number look authoritative. (CodeRabbit, PR #116.)
	//
	// Driven deterministically rather than by racing goroutines and hoping: the
	// stand-in loop writes figures only once it observes the cancel, so the
	// write is GUARANTEED to land in the window the old code left open. A test
	// that merely raced would pass on the broken code most of the time.
	app := newPayloadTestApp(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done() // stopUpstream has cancelled; the old code has already cleared
		total := 99.0
		app.upstream.mu.Lock()
		app.upstream.fleetEarnings = &upstream.FleetEarnings{
			WindowDays: 30,
			Currency:   "USD",
			Platforms:  []upstream.FleetPlatform{{Slug: "grass", USD: &total}},
			TotalUSD:   &total,
		}
		app.upstream.fleetEarningsAt = time.Now().UTC()
		app.upstream.mu.Unlock()
	}()

	app.upstream.mu.Lock()
	app.upstream.cancel, app.upstream.done = cancel, done
	app.upstream.mu.Unlock()

	app.stopUpstream()

	app.upstream.mu.Lock()
	cached, at := app.upstream.fleetEarnings, app.upstream.fleetEarningsAt
	app.upstream.mu.Unlock()
	if cached != nil {
		t.Fatalf("a heartbeat that landed during unlink outlived it: %+v", cached)
	}
	if !at.IsZero() {
		t.Fatalf("the timestamp outlived the unlink: %v", at)
	}
}

func TestStopUpstreamStillClearsWhenNoLoopIsRunning(t *testing.T) {
	// The other branch. Moving the clear after the cancel makes it easy to leave
	// it behind an `if cancel != nil` early return, which would silently stop
	// clearing in the commonest case of all -- a Desktop that was never paired,
	// or one being stopped twice.
	app := newPayloadTestApp(t, nil)
	total := 4.0
	app.upstream.mu.Lock()
	app.upstream.fleetEarnings = &upstream.FleetEarnings{TotalUSD: &total}
	app.upstream.historyUnsupported = true
	app.upstream.mu.Unlock()

	app.stopUpstream() // no cancel, no done

	app.upstream.mu.Lock()
	cached, unsupported := app.upstream.fleetEarnings, app.upstream.historyUnsupported
	app.upstream.mu.Unlock()
	if cached != nil {
		t.Fatalf("stopUpstream left figures behind when no loop was running: %+v", cached)
	}
	if unsupported {
		t.Fatal("stopUpstream did not re-arm the import when no loop was running")
	}
}

func TestUnpairedConfigWinsEvenIfFiguresAreStillCached(t *testing.T) {
	// Two independent barriers stop an unlinked machine showing a fleet figure:
	// stopUpstream drops the cache, and fleetView refuses when no server is
	// configured. Only the SECOND one is under test here.
	//
	// Written because a negative control exposed the gap: deleting the config
	// check left every other test in this file passing, since clearing the cache
	// already covered them. A barrier nothing exercises is a barrier that will
	// be deleted as dead code by whoever touches this next.
	app := newPayloadTestApp(t, nil)
	app.upstream.mu.Lock()
	total := 4.0
	app.upstream.fleetEarnings = &upstream.FleetEarnings{
		WindowDays: 30,
		Currency:   "USD",
		Platforms:  []upstream.FleetPlatform{{Slug: "grass", USD: &total}},
		TotalUSD:   &total,
	}
	app.upstream.mu.Unlock()

	// No UpstreamURL: this machine is not paired, whatever is in memory.
	if got := app.fleetView(); got != nil {
		t.Fatalf("an unpaired Desktop showed a cached fleet figure: %+v", got)
	}
}

func TestAHeartbeatWithNoFiguresDoesNotBlankTheLastOnes(t *testing.T) {
	// One heartbeat that could not produce figures does not mean the account
	// earned nothing. Blanking on it would make the panel flicker to empty
	// whenever the server had a bad minute.
	app := newPayloadTestApp(t, nil)
	first := reportEarnings(t, app, map[string]any{
		"window_days": 30, "currency": "USD",
		"platforms": []map[string]any{{"slug": "grass", "usd": 4.0}},
		"total_usd": 4.0,
	})
	if err := app.cfg.Update(func(c *config.AppConfig) { c.UpstreamURL = first.URL }); err != nil {
		t.Fatal(err)
	}
	app.upstream.mu.Lock()
	app.upstream.workerKey = "own-key"
	app.upstream.mu.Unlock()
	app.sendUpstream(context.Background(), testClient(first), first.URL, "enrol")

	// The same server, now reporting nothing at all.
	silent := reportEarnings(t, app, nil)
	app.sendUpstream(context.Background(), testClient(silent), silent.URL, "enrol")

	view := app.fleetView()
	if view == nil || view.TotalUSD == nil || *view.TotalUSD != 4.0 {
		t.Fatalf("a silent heartbeat blanked the last known figures: %+v", view)
	}
}

func TestTheFleetViewIsInTheAppState(t *testing.T) {
	// It has to reach the frontend to be worth anything, and the field must
	// serialise as null (not omitted, not {}) when there is nothing to show --
	// the frontend branches on it.
	app := newPayloadTestApp(t, nil)
	raw, err := json.Marshal(AppState{Fleet: app.fleetView()})
	if err != nil {
		t.Fatal(err)
	}
	var round map[string]json.RawMessage
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatal(err)
	}
	value, present := round["fleet"]
	if !present {
		t.Fatal("AppState carries no `fleet` field")
	}
	if string(value) != "null" {
		t.Fatalf("standalone serialised as %s, want null", value)
	}
}
