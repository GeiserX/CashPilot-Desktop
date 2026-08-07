package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/exchange"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// summaryFor builds a real App against an in-memory store and an exchange service
// wired to httptest feeds, then returns the computed summary. fiatRates is the
// literal Frankfurter `rates` object, so a test can withhold a currency to model a
// rate outage rather than mocking the exchange package.
func summaryFor(t *testing.T, display string, fiatRates string, seed []store.EarningsRecord) EarningsSummary {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager error: %v", err)
	}
	if err := cfg.Update(func(c *config.AppConfig) { c.DisplayCurrency = display }); err != nil {
		t.Fatalf("cfg.Update error: %v", err)
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
		_, _ = io.WriteString(w, `{"mysterium":{"usd":0.25}}`)
	}))
	t.Cleanup(cg.Close)
	fr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"amount":1,"base":"USD","rates":`+fiatRates+`}`)
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
	for _, r := range seed {
		if _, err := st.SaveEarnings(r); err != nil {
			t.Fatalf("SaveEarnings(%+v) error: %v", r, err)
		}
	}
	app := &App{cfg: cfg, store: st, catalog: cat, exchange: svc, ctx: context.Background()}
	return app.computeEarningsSummary(app.store.ListLatestEarnings())
}

func daysAgoTS(daysAgo, hour int) string {
	d := time.Now().UTC().AddDate(0, 0, -daysAgo)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// TestTotalKnownSeparatesUnpriceableFromZero pins the distinction the dashboard
// headline depends on: a total of 0 is a real measurement for a user who has
// earned nothing, and a fabrication for a user whose balances could not be
// priced. Both produce Total == 0, so the number alone cannot be trusted and
// TotalKnown is what the UI must branch on.
//
// The unpriceable case is not exotic. ToDisplay routes every balance through
// USD, so a single missing DISPLAY-currency rate -- one failed fiat fetch --
// makes every platform unpriceable at once, for every non-USD user.
func TestTotalKnownSeparatesUnpriceableFromZero(t *testing.T) {
	realMoney := []store.EarningsRecord{
		{Platform: "honeygain", Balance: 1.00, Currency: "USD", CreatedAt: daysAgoTS(1, 10)},
		{Platform: "honeygain", Balance: 250.00, Currency: "USD", CreatedAt: daysAgoTS(0, 10)},
	}

	tests := []struct {
		name        string
		display     string
		fiatRates   string
		seed        []store.EarningsRecord
		wantKnown   bool
		wantStale   bool
		wantTotalGT float64 // require Total strictly greater; -1 to require exactly 0
	}{
		{
			// The defect. 250.00 USD of real earnings, no JPY rate, so every
			// contribution is dropped and Total falls back to Go's zero value.
			name:        "display currency has no rate",
			display:     "JPY",
			fiatRates:   `{"EUR":0.9}`,
			seed:        realMoney,
			wantKnown:   false,
			wantStale:   true,
			wantTotalGT: -1,
		},
		{
			// Control: same money, a display currency that IS priced.
			name:        "display currency is priced",
			display:     "EUR",
			fiatRates:   `{"EUR":0.9}`,
			seed:        realMoney,
			wantKnown:   true,
			wantStale:   false,
			wantTotalGT: 0,
		},
		{
			// The case that stops the fix from over-reaching: a brand new
			// install genuinely IS at zero, and that zero must stay known or
			// every new user is told their total is unavailable.
			name:        "no earnings at all is a real zero",
			display:     "USD",
			fiatRates:   `{"EUR":0.9}`,
			seed:        nil,
			wantKnown:   true,
			wantStale:   false,
			wantTotalGT: -1,
		},
		{
			// A partial sum stays KNOWN and is flagged stale. An understated
			// real figure is still a measurement, and blanking it would throw
			// away the priced services to describe the unpriced one.
			name:      "one platform unpriceable, another priced",
			display:   "USD",
			fiatRates: `{"EUR":0.9}`,
			seed: append(append([]store.EarningsRecord{}, realMoney...),
				store.EarningsRecord{Platform: "nosana", Balance: 5, Currency: "NOS", CreatedAt: daysAgoTS(0, 10)}),
			wantKnown:   true,
			wantStale:   true,
			wantTotalGT: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sum := summaryFor(t, tc.display, tc.fiatRates, tc.seed)
			if sum.TotalKnown != tc.wantKnown {
				t.Errorf("TotalKnown = %v, want %v (Total=%v, RatesStale=%v)",
					sum.TotalKnown, tc.wantKnown, sum.Total, sum.RatesStale)
			}
			if sum.RatesStale != tc.wantStale {
				t.Errorf("RatesStale = %v, want %v", sum.RatesStale, tc.wantStale)
			}
			if tc.wantTotalGT < 0 {
				if sum.Total != 0 {
					t.Errorf("Total = %v, want exactly 0", sum.Total)
				}
			} else if sum.Total <= tc.wantTotalGT {
				t.Errorf("Total = %v, want > %v", sum.Total, tc.wantTotalGT)
			}
		})
	}
}

// TestTotalKnownFalseHidesRealMoney is the one that states the user-visible
// consequence outright, so a future change that "simplifies" TotalKnown away
// fails against the symptom rather than the mechanism: the summary reports a
// total of zero while the breakdown still holds 250.00 of real balance.
func TestTotalKnownFalseHidesRealMoney(t *testing.T) {
	sum := summaryFor(t, "JPY", `{"EUR":0.9}`, []store.EarningsRecord{
		{Platform: "honeygain", Balance: 1.00, Currency: "USD", CreatedAt: daysAgoTS(1, 10)},
		{Platform: "honeygain", Balance: 250.00, Currency: "USD", CreatedAt: daysAgoTS(0, 10)},
	})

	if sum.Total != 0 {
		t.Fatalf("precondition: Total = %v, want 0 (the unpriceable case)", sum.Total)
	}
	var found bool
	for _, b := range sum.Breakdown {
		if b.Platform == "honeygain" {
			found = true
			if b.Balance != 250.00 {
				t.Errorf("breakdown balance = %v, want 250.00", b.Balance)
			}
		}
	}
	if !found {
		t.Fatal("honeygain missing from breakdown")
	}
	if sum.TotalKnown {
		t.Error("TotalKnown = true while a total of 0 sits above 250.00 of real balance; " +
			"the dashboard would state zero as a measured fact")
	}
}
