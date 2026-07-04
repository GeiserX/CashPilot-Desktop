package main

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
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
