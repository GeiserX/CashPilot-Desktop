package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/exchange"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

func approxEq(got, want float64) bool {
	return math.Abs(got-want) <= 1e-6
}

// TestComputeEarningsSummary pins the summary math end to end: cumulative daily
// balances are converted to the display currency, non-convertible reward points
// are excluded from the total, per-day deltas stay non-negative, and error /
// currency-mismatch rows are surfaced in the breakdown. keyring.MockInit is
// installed by TestMain in fleet_server_test.go, so the store stays in-memory.
func TestComputeEarningsSummary(t *testing.T) {
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager error: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open error: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded error: %v", err)
	}

	// Exchange service pointed at httptest servers: MYST=0.25 USD. The fiat feed
	// only needs to succeed for the refresh to populate the crypto cache; USD
	// display needs no specific fiat rate.
	cg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"mysterium":{"usd":0.25}}`)
	}))
	t.Cleanup(cg.Close)
	fr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"amount":1,"base":"USD","rates":{"EUR":0.9}}`)
	}))
	t.Cleanup(fr.Close)
	svc := exchange.NewService(
		exchange.WithBaseURLs(cg.URL, fr.URL),
		exchange.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}),
		exchange.WithCryptoIDs(map[string]string{"MYST": "mysterium"}),
	)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("exchange refresh error: %v", err)
	}

	// Seed three UTC days of CUMULATIVE balances (today, -1, -2). honeygain is USD
	// (convertible), mysterium is MYST (convertible via the 0.25 rate), grass is
	// GRASS (a non-convertible reward -> Points). A broken collector row for a
	// non-catalog platform exercises the error + currency-mismatch paths.
	ts := func(daysAgo, hour int) string {
		d := time.Now().UTC().AddDate(0, 0, -daysAgo)
		return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, time.UTC).Format(time.RFC3339)
	}
	seed := []store.EarningsRecord{
		{Platform: "honeygain", Balance: 1.00, Currency: "USD", CreatedAt: ts(2, 10)},
		{Platform: "honeygain", Balance: 1.50, Currency: "USD", CreatedAt: ts(1, 10)},
		{Platform: "honeygain", Balance: 2.00, Currency: "USD", CreatedAt: ts(0, 10)},
		{Platform: "mysterium", Balance: 4.0, Currency: "MYST", CreatedAt: ts(2, 10)},
		{Platform: "mysterium", Balance: 6.0, Currency: "MYST", CreatedAt: ts(1, 10)},
		{Platform: "mysterium", Balance: 8.0, Currency: "MYST", CreatedAt: ts(0, 10)},
		{Platform: "grass", Balance: 100.0, Currency: "GRASS", CreatedAt: ts(2, 10)},
		{Platform: "grass", Balance: 150.0, Currency: "GRASS", CreatedAt: ts(1, 10)},
		{Platform: "grass", Balance: 200.0, Currency: "GRASS", CreatedAt: ts(0, 10)},
		// A failed collector run for a service not in the catalog: it must appear
		// in the breakdown with its error and be marked non-comparable.
		{Platform: "brokencollector", Balance: 0, Currency: "USD", Error: "login failed", CreatedAt: ts(0, 11)},
	}
	for _, r := range seed {
		if _, err := st.SaveEarnings(r); err != nil {
			t.Fatalf("SaveEarnings(%+v) error: %v", r, err)
		}
	}

	app := &App{cfg: cfg, store: st, catalog: cat, exchange: svc, ctx: context.Background()}
	sum := app.computeEarningsSummary()

	// Total = honeygain latest (2.00 USD) + mysterium latest (8 MYST * 0.25 = 2.00 USD).
	// GRASS is excluded (non-convertible) and the error row has no successful daily
	// balance, so it contributes nothing.
	if !approxEq(sum.Total, 4.00) {
		t.Fatalf("Total = %v, want 4.00 (2.00 USD + 8 MYST @0.25)", sum.Total)
	}
	if sum.DisplayCurrency != "USD" {
		t.Fatalf("DisplayCurrency = %q, want USD", sum.DisplayCurrency)
	}

	// GRASS landed in Points (native units, latest day), never in the total.
	var grass *PointsBalance
	for i := range sum.Points {
		if sum.Points[i].Currency == "GRASS" {
			grass = &sum.Points[i]
		}
	}
	if grass == nil {
		t.Fatalf("expected a GRASS points balance, got points=%+v", sum.Points)
	}
	if !approxEq(grass.Balance, 200.0) {
		t.Fatalf("GRASS points balance = %v, want 200 (latest day)", grass.Balance)
	}

	// Per-day deltas use the latest-per-day cumulative balances and are clamped at
	// >= 0. Today's accrual: honeygain +0.50 USD and mysterium +2 MYST (0.50 USD).
	if sum.Today < 0 || sum.Month < 0 {
		t.Fatalf("expected non-negative Today/Month, got Today=%v Month=%v", sum.Today, sum.Month)
	}
	if !approxEq(sum.Today, 1.00) {
		t.Fatalf("Today = %v, want ~1.00 (honeygain +0.50 + mysterium +0.50)", sum.Today)
	}
	if len(sum.Daily) != 30 {
		t.Fatalf("expected 30 daily points, got %d", len(sum.Daily))
	}

	// Breakdown includes every latest record, including the error row.
	byPlat := map[string]ServiceEarning{}
	for _, se := range sum.Breakdown {
		byPlat[se.Platform] = se
	}

	broken, ok := byPlat["brokencollector"]
	if !ok {
		t.Fatalf("expected the error platform in the breakdown, got %+v", sum.Breakdown)
	}
	if broken.Error == "" {
		t.Fatalf("expected the error platform to surface its Error, got %+v", broken)
	}
	if broken.Cashout.Comparable {
		t.Fatalf("expected a non-catalog / currency-mismatch service to be Comparable=false, got %+v", broken.Cashout)
	}

	// A matched service discriminates: honeygain (USD balance vs USD cashout, min
	// 20) is comparable, convertible, 10% of the way, and not yet eligible.
	hg, ok := byPlat["honeygain"]
	if !ok {
		t.Fatal("expected honeygain in the breakdown")
	}
	if !hg.Cashout.Comparable {
		t.Fatalf("expected honeygain Comparable=true, got %+v", hg.Cashout)
	}
	if !hg.Convertible || !approxEq(hg.BalanceDisplay, 2.00) {
		t.Fatalf("expected honeygain convertible with BalanceDisplay 2.00, got %+v", hg)
	}
	if hg.Cashout.Eligible {
		t.Fatalf("honeygain at 2.00/20.00 must not be eligible, got %+v", hg.Cashout)
	}
	if !approxEq(hg.Cashout.Percent, 10.0) {
		t.Fatalf("honeygain cashout percent = %v, want 10 (2/20*100)", hg.Cashout.Percent)
	}

	// mysterium: MYST balance vs MYST cashout, min 4 -> 8/4 clamps to 100% and is
	// eligible; converts to 2.00 USD (8 * 0.25).
	my, ok := byPlat["mysterium"]
	if !ok {
		t.Fatal("expected mysterium in the breakdown")
	}
	if !my.Cashout.Comparable || !my.Cashout.Eligible {
		t.Fatalf("expected mysterium comparable+eligible (8 MYST >= 4 min), got %+v", my.Cashout)
	}
	if !approxEq(my.Cashout.Percent, 100.0) {
		t.Fatalf("mysterium cashout percent = %v, want 100 (clamped)", my.Cashout.Percent)
	}
	if !my.Convertible || !approxEq(my.BalanceDisplay, 2.00) {
		t.Fatalf("expected mysterium convertible with BalanceDisplay 2.00 (8*0.25), got %+v", my)
	}

	// RatesUpdated is populated after a successful refresh; rates are fresh.
	if sum.RatesStale {
		t.Fatal("expected RatesStale=false right after a successful refresh")
	}
	if sum.RatesUpdated == "" {
		t.Fatal("expected RatesUpdated to be set after a refresh")
	}
}

// newEarningsTestApp wires an App with an in-memory store and an exchange pointed
// at httptest servers serving the given CoinGecko / Frankfurter bodies. When
// refresh is true the cache is populated up front (so Stale() is false).
func newEarningsTestApp(t *testing.T, cgBody, frBody string, cryptoIDs map[string]string, refresh bool) (*App, *store.Store) {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager error: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open error: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded error: %v", err)
	}
	cg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cgBody)
	}))
	t.Cleanup(cg.Close)
	fr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, frBody)
	}))
	t.Cleanup(fr.Close)
	svc := exchange.NewService(
		exchange.WithBaseURLs(cg.URL, fr.URL),
		exchange.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}),
		exchange.WithCryptoIDs(cryptoIDs),
	)
	if refresh {
		if err := svc.Refresh(context.Background()); err != nil {
			t.Fatalf("exchange refresh error: %v", err)
		}
	}
	app := &App{cfg: cfg, store: st, catalog: cat, exchange: svc, ctx: context.Background()}
	return app, st
}

func seedEarnings(t *testing.T, st *store.Store, records ...store.EarningsRecord) {
	t.Helper()
	for _, r := range records {
		if _, err := st.SaveEarnings(r); err != nil {
			t.Fatalf("SaveEarnings(%+v) error: %v", r, err)
		}
	}
}

// atUTC formats a day at a fixed hour as RFC3339 (UTC), so date(created_at) in the
// store groups it under that calendar day.
func atUTC(d time.Time, hour int) string {
	return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// TestEarningsSummaryFirstObservationContributesZero pins the fix for both the
// first-observation inflation and the unreachable-baseline bug: a platform seen
// for the FIRST time today has no prior baseline, so its cumulative balance shows
// in Total but books 0 for Today and Month (it is not "earned" the day it first
// appears).
func TestEarningsSummaryFirstObservationContributesZero(t *testing.T) {
	app, st := newEarningsTestApp(t,
		`{"mysterium":{"usd":0.25}}`,
		`{"amount":1,"base":"USD","rates":{"EUR":0.9}}`,
		map[string]string{"MYST": "mysterium"}, true)

	now := time.Now().UTC()
	seedEarnings(t, st, store.EarningsRecord{Platform: "honeygain", Balance: 50.0, Currency: "USD", CreatedAt: atUTC(now, 10)})

	sum := app.computeEarningsSummary()
	if !approxEq(sum.Total, 50.0) {
		t.Fatalf("Total = %v, want 50.00 (latest cumulative still shows in Total)", sum.Total)
	}
	if !approxEq(sum.Today, 0) {
		t.Fatalf("Today = %v, want 0 (a first observation books no accrual, not its whole balance)", sum.Today)
	}
	if !approxEq(sum.Month, 0) {
		t.Fatalf("Month = %v, want 0 (no baseline before the first observation)", sum.Month)
	}
}

// TestEarningsSummaryCarryForwardAcrossGap proves asOf carries the last known
// cumulative balance across days with no observation, so accrual over a gap is the
// real delta — not the first-observation inflation and not zero.
func TestEarningsSummaryCarryForwardAcrossGap(t *testing.T) {
	app, st := newEarningsTestApp(t,
		`{"mysterium":{"usd":0.25}}`,
		`{"amount":1,"base":"USD","rates":{"EUR":0.9}}`,
		map[string]string{"MYST": "mysterium"}, true)

	now := time.Now().UTC()
	// Baseline 4 days ago (3.00), then a gap (no rows on -3/-2/-1), then today
	// (5.00). "yesterday" has no row, so asOf must carry 3.00 forward: Today is the
	// real +2.00, not 5.00 (first-obs inflation) and not 0.
	seedEarnings(t, st,
		store.EarningsRecord{Platform: "honeygain", Balance: 3.0, Currency: "USD", CreatedAt: atUTC(now.AddDate(0, 0, -4), 10)},
		store.EarningsRecord{Platform: "honeygain", Balance: 5.0, Currency: "USD", CreatedAt: atUTC(now, 10)},
	)

	sum := app.computeEarningsSummary()
	if !approxEq(sum.Today, 2.0) {
		t.Fatalf("Today = %v, want 2.00 (5.00 today minus 3.00 carried across the gap)", sum.Today)
	}
	if !approxEq(sum.Total, 5.0) {
		t.Fatalf("Total = %v, want 5.00", sum.Total)
	}
}

// TestEarningsSummaryMonthWindow pins concrete Month AND MonthChange by anchoring
// the seeds to the exact month boundaries the summary uses, so the result is
// deterministic regardless of the calendar date the test runs on. The baselines
// land in the fetched window only because fix 2 widened it to two months.
func TestEarningsSummaryMonthWindow(t *testing.T) {
	app, st := newEarningsTestApp(t,
		`{"mysterium":{"usd":0.25}}`,
		`{"amount":1,"base":"USD","rates":{"EUR":0.9}}`,
		map[string]string{"MYST": "mysterium"}, true)

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	beforeMonthStart := monthStart.AddDate(0, 0, -1)                       // last day of the previous month
	beforeLastMonthStart := monthStart.AddDate(0, -1, 0).AddDate(0, 0, -1) // last day of the month before that

	// last-month baseline: 2.00; this-month baseline: 3.00 (prev month earned 1.00);
	// today: 8.00 (this month earned 8.00 - 3.00 = 5.00).
	seedEarnings(t, st,
		store.EarningsRecord{Platform: "honeygain", Balance: 2.0, Currency: "USD", CreatedAt: atUTC(beforeLastMonthStart, 8)},
		store.EarningsRecord{Platform: "honeygain", Balance: 3.0, Currency: "USD", CreatedAt: atUTC(beforeMonthStart, 8)},
		store.EarningsRecord{Platform: "honeygain", Balance: 8.0, Currency: "USD", CreatedAt: atUTC(now, 8)},
	)

	sum := app.computeEarningsSummary()
	if !approxEq(sum.Month, 5.0) {
		t.Fatalf("Month = %v, want 5.00 (8.00 today minus 3.00 at the month baseline)", sum.Month)
	}
	// prevMonthEarned = 3.00 - 2.00 = 1.00; MonthChange = (5.00 - 1.00) / 1.00 * 100.
	if !approxEq(sum.MonthChange, 400.0) {
		t.Fatalf("MonthChange = %v, want 400 ((5-1)/1*100)", sum.MonthChange)
	}
}

// TestEarningsSummaryPointsClassificationDuringOutage proves a convertible
// currency that is momentarily unpriced (a rate outage for one token) is excluded
// from Total, is NOT surfaced as a reward point, and flags the rates stale — even
// though the exchange itself refreshed successfully (Stale() == false).
func TestEarningsSummaryPointsClassificationDuringOutage(t *testing.T) {
	// CoinGecko prices BTC only (NOT MYST); Frankfurter succeeds, so the refresh is
	// fully successful and Stale() is false, yet MYST has no live rate.
	app, st := newEarningsTestApp(t,
		`{"bitcoin":{"usd":50000}}`,
		`{"amount":1,"base":"USD","rates":{"EUR":0.9}}`,
		map[string]string{"BTC": "bitcoin"}, true)
	if app.exchange.Stale() {
		t.Fatal("precondition: exchange should be fresh after a successful refresh")
	}

	now := time.Now().UTC()
	seedEarnings(t, st,
		store.EarningsRecord{Platform: "mysterium", Balance: 6.0, Currency: "MYST", CreatedAt: atUTC(now.AddDate(0, 0, -1), 10)},
		store.EarningsRecord{Platform: "mysterium", Balance: 8.0, Currency: "MYST", CreatedAt: atUTC(now, 10)},
	)

	sum := app.computeEarningsSummary()
	if !approxEq(sum.Total, 0) {
		t.Fatalf("Total = %v, want 0 (MYST is unpriced during the outage -> excluded)", sum.Total)
	}
	for _, p := range sum.Points {
		if p.Currency == "MYST" {
			t.Fatalf("MYST must NOT be classified as a reward point, got points=%+v", sum.Points)
		}
	}
	if !sum.RatesStale {
		t.Fatal("expected RatesStale=true when a non-points currency cannot be priced")
	}
}

// fakeCollector is an injectable collectorRegistry for the scheduler tests. It
// records how many times each slug was collected, can be told to fail specific
// slugs, and tracks the maximum number of Collect calls in flight at once so the
// single-flight guard can be asserted. All shared state is mutex/atomic-guarded so
// the scheduler goroutine and the test observer stay race-free under -race.
type fakeCollector struct {
	mu        sync.Mutex
	supported map[string]bool
	fail      map[string]bool
	collected map[string]int

	inFlight atomic.Int32
	maxSeen  atomic.Int32
	hold     time.Duration
}

func newFakeCollector(supported, fail []string) *fakeCollector {
	f := &fakeCollector{
		supported: map[string]bool{},
		fail:      map[string]bool{},
		collected: map[string]int{},
	}
	for _, s := range supported {
		f.supported[s] = true
	}
	for _, s := range fail {
		f.fail[s] = true
	}
	return f
}

func (f *fakeCollector) Supports(slug string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.supported[slug]
}

func (f *fakeCollector) Collect(_ context.Context, slug string, _ map[string]string) (store.EarningsRecord, error) {
	// Track concurrent in-flight collects to prove the single-flight guard.
	cur := f.inFlight.Add(1)
	for {
		m := f.maxSeen.Load()
		if cur <= m || f.maxSeen.CompareAndSwap(m, cur) {
			break
		}
	}
	if f.hold > 0 {
		time.Sleep(f.hold)
	}
	f.inFlight.Add(-1)

	f.mu.Lock()
	f.collected[slug]++
	shouldFail := f.fail[slug]
	f.mu.Unlock()

	if shouldFail {
		// Mirror the real registry: the error is captured on the persisted record,
		// and a store/transport-style error is also returned to the caller.
		return store.EarningsRecord{Platform: slug, Error: "boom"}, fmt.Errorf("collector %s failed", slug)
	}
	return store.EarningsRecord{Platform: slug, Balance: 1, Currency: "USD"}, nil
}

func (f *fakeCollector) counts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int, len(f.collected))
	for k, v := range f.collected {
		out[k] = v
	}
	return out
}

// newSchedulerTestApp wires an App with a real in-memory store (seeded with the
// given deployment slugs) and the given fake collector. The store is real so the
// deployment iteration and credential lookup in collectAll are exercised for real;
// only the collector is faked. ctx is a plain background context, so emitEvent is a
// safe no-op (see emitEvent's doc) and the test never touches the Wails runtime.
func newSchedulerTestApp(t *testing.T, fake *fakeCollector, slugs ...string) (*App, context.Context, context.CancelFunc) {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager error: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open error: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, s := range slugs {
		if err := st.UpsertDeployment(store.Deployment{Slug: s}); err != nil {
			t.Fatalf("UpsertDeployment(%s) error: %v", s, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	app := &App{cfg: cfg, store: st, collectors: fake, ctx: ctx}
	return app, ctx, cancel
}

// TestSchedulerCollectsEachDeploymentOnTick pins the core Slice 4 behavior: the
// scheduler ticks on a short injected interval, runs collectAll on every tick, and
// collects each deployed service — and a collector that fails every run does NOT
// stop the others from being collected.
func TestSchedulerCollectsEachDeploymentOnTick(t *testing.T) {
	// svc-b's collector fails on every run; svc-a and svc-c must keep collecting.
	fake := newFakeCollector([]string{"svc-a", "svc-b", "svc-c"}, []string{"svc-b"})
	app, ctx, cancel := newSchedulerTestApp(t, fake, "svc-a", "svc-b", "svc-c")
	defer cancel()

	app.runScheduler(ctx, 5*time.Millisecond)
	t.Cleanup(app.stopScheduler)

	// Wait until every service has been collected at least twice: the first count
	// is the immediate initial run, so >=2 proves the ticker fired collectAll again
	// (i.e. collection really runs "on tick", not only once at start).
	deadline := time.Now().Add(5 * time.Second)
	for {
		c := fake.counts()
		if c["svc-a"] >= 2 && c["svc-b"] >= 2 && c["svc-c"] >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for repeated collection on tick, counts=%v", c)
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	app.stopScheduler() // returns only once the loop goroutine has exited (no leak)

	c := fake.counts()
	if c["svc-a"] < 2 || c["svc-c"] < 2 {
		t.Fatalf("a failing collector (svc-b) must not stop the others, counts=%v", c)
	}
	if c["svc-b"] < 2 {
		t.Fatalf("expected the failing collector to keep being attempted each cycle, counts=%v", c)
	}
}

// TestCollectAllSkipsUnsupportedAndContinuesOnError pins one full cycle in
// isolation: only deployed services with a native collector are collected (an
// unsupported deployment is skipped, not turned into an error row), and one failing
// collector does not abort the rest of the batch.
func TestCollectAllSkipsUnsupportedAndContinuesOnError(t *testing.T) {
	// svc-a/b/c have collectors (b fails); svc-x is deployed but unsupported.
	fake := newFakeCollector([]string{"svc-a", "svc-b", "svc-c"}, []string{"svc-b"})
	app, _, cancel := newSchedulerTestApp(t, fake, "svc-a", "svc-b", "svc-c", "svc-x")
	defer cancel()

	app.collectAll(context.Background())

	c := fake.counts()
	if c["svc-a"] != 1 || c["svc-b"] != 1 || c["svc-c"] != 1 {
		t.Fatalf("expected each supported service collected once (continuing past svc-b's error), counts=%v", c)
	}
	if _, ok := c["svc-x"]; ok {
		t.Fatalf("expected the unsupported deployment svc-x to be skipped, counts=%v", c)
	}
}

// TestCollectAllCollectsCredentialOnlyServicesAndDedups pins the imageless fix:
// collectAll collects the union of deployed slugs and slugs that merely have saved
// credentials. A supported service with credentials but NO deployment row (the
// imageless case: vast-ai, salad, grass, bytelixir) is collected — proving it no
// longer waits for a manual click. A service present in BOTH sets is collected
// exactly once (the collect count is the number of earnings rows one cycle writes,
// so ==1 means no duplicate row). The Supports gate still applies to credential-only
// slugs, so an unsupported one is skipped rather than turned into an error.
func TestCollectAllCollectsCredentialOnlyServicesAndDedups(t *testing.T) {
	// svc-both and svc-cred are supported; svc-unsup is NOT.
	fake := newFakeCollector([]string{"svc-both", "svc-cred"}, nil)
	// Only svc-both is deployed; svc-cred and svc-unsup exist solely as credentials.
	app, _, cancel := newSchedulerTestApp(t, fake, "svc-both")
	defer cancel()

	for _, slug := range []string{"svc-both", "svc-cred", "svc-unsup"} {
		if err := app.store.SaveCredentials(slug, map[string]string{"api_key": slug}); err != nil {
			t.Fatalf("SaveCredentials(%s) error: %v", slug, err)
		}
	}

	app.collectAll(context.Background())

	c := fake.counts()
	// New behavior: a supported credentials-only service (no deployment) collects.
	if c["svc-cred"] != 1 {
		t.Fatalf("expected the credentials-only supported service to be collected once, counts=%v", c)
	}
	// Dedup: a service in both the deployment and credential sets collects exactly
	// once per cycle (no duplicate earnings row).
	if c["svc-both"] != 1 {
		t.Fatalf("expected a service in both sets collected exactly once, counts=%v", c)
	}
	// The Supports gate still applies to credential-only slugs.
	if _, ok := c["svc-unsup"]; ok {
		t.Fatalf("expected the unsupported credentials-only slug to be skipped, counts=%v", c)
	}
}

// TestCollectAllSingleFlight pins the single-flight guard: many concurrent
// collectAll calls never overlap, so at most one Collect is ever in flight.
func TestCollectAllSingleFlight(t *testing.T) {
	fake := newFakeCollector([]string{"svc-a", "svc-b", "svc-c"}, nil)
	fake.hold = 2 * time.Millisecond // hold each collect so an overlap would be observable
	app, _, cancel := newSchedulerTestApp(t, fake, "svc-a", "svc-b", "svc-c")
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app.collectAll(context.Background())
		}()
	}
	wg.Wait()

	if got := fake.maxSeen.Load(); got != 1 {
		t.Fatalf("single-flight violated: max concurrent collects = %d, want 1", got)
	}
}
