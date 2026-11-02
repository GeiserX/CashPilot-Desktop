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

// Cashing out must not make CashPilot forget what a service earned. The balance
// total fell by $37.54 the morning EarnApp paid out; recorded cash-outs are the
// rest of the answer. Port of GeiserX/CashPilot#487 (tests/test_lifetime_earned.py).

// payoutApp is an App on a real store with MYST priced at 0.25 USD and EUR at 0.9.
func payoutApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatal(err)
	}
	cg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"mysterium":{"usd":0.25}}`)
	}))
	t.Cleanup(cg.Close)
	fr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"amount":1,"base":"USD","rates":{"EUR":0.9}}`)
	}))
	t.Cleanup(fr.Close)
	fx := exchange.NewService(
		exchange.WithBaseURLs(cg.URL, fr.URL),
		exchange.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}),
		exchange.WithCryptoIDs(map[string]string{"MYST": "mysterium"}),
	)
	if err := fx.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &App{cfg: cfg, store: st, catalog: cat, exchange: fx, ctx: context.Background()}
}

var payoutNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func TestARecordedCashOutCountsTowardEarnedAllTime(t *testing.T) {
	app := payoutApp(t)
	if _, err := app.store.SaveEarnings(store.EarningsRecord{Platform: "earnapp", Balance: 120, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	p, err := app.recordPayout("earnapp", 37.54, "", " PayPal ", "2026-01-15", payoutNow)
	if err != nil {
		t.Fatal(err)
	}
	if p.Currency != "USD" || p.Method != "PayPal" || p.PaidOn != "2026-01-15" || p.FxRateUSD == nil || *p.FxRateUSD != 1 {
		t.Fatalf("recorded %+v, want a priced USD cash-out dated 2026-01-15 by PayPal", p)
	}
	s := app.computeEarningsSummary(app.store.ListLatestEarnings())
	if s.PaidOut == nil || !approxEq(*s.PaidOut, 37.54) {
		t.Fatalf("PaidOut = %v, want 37.54", s.PaidOut)
	}
	if s.EarnedAllTime == nil || !approxEq(*s.EarnedAllTime, 157.54) {
		t.Fatalf("EarnedAllTime = %v, want 157.54 (120 balance + 37.54 cashed out)", s.EarnedAllTime)
	}
}

func TestNoCashOutsIsAKnownZero(t *testing.T) {
	app := payoutApp(t)
	s := app.computeEarningsSummary(nil)
	if s.PaidOut == nil || *s.PaidOut != 0 || s.EarnedAllTime == nil || *s.EarnedAllTime != 0 {
		t.Fatalf("PaidOut=%v EarnedAllTime=%v, want 0 and 0 on an empty ledger", s.PaidOut, s.EarnedAllTime)
	}
}

func TestPayoutsThatCannotBeReadAreUnknownNotZero(t *testing.T) {
	app := payoutApp(t)
	if _, err := app.store.SaveEarnings(store.EarningsRecord{Platform: "honeygain", Balance: 25, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	_ = app.store.Close() // every read now fails
	s := app.computeEarningsSummary(nil)
	if s.PaidOut != nil || s.EarnedAllTime != nil {
		t.Fatalf("PaidOut=%v EarnedAllTime=%v, want both unknown (nil)", s.PaidOut, s.EarnedAllTime)
	}
}

func TestABackdatedTokenCashOutIsNotPricedAtTodaysRate(t *testing.T) {
	app := payoutApp(t)
	old, err := app.recordPayout("mysterium", 500, "myst", "", "2026-01-15", payoutNow)
	if err != nil {
		t.Fatal(err)
	}
	today, err := app.recordPayout("mysterium", 100, "MYST", "", "", payoutNow)
	if err != nil {
		t.Fatal(err)
	}
	if old.FxRateUSD != nil {
		t.Fatalf("a backdated MYST cash-out was priced at %v, want unpriced", *old.FxRateUSD)
	}
	if today.FxRateUSD == nil || !approxEq(*today.FxRateUSD, 0.25) {
		t.Fatalf("today's MYST cash-out rate = %v, want 0.25", today.FxRateUSD)
	}
	s := app.computeEarningsSummary(nil)
	if s.PaidOut == nil || !approxEq(*s.PaidOut, 25) || s.UnpricedPayouts != 1 {
		t.Fatalf("PaidOut=%v Unpriced=%d, want 25 (100 MYST @0.25) and 1", s.PaidOut, s.UnpricedPayouts)
	}
}

func TestCashOutsAreShownInTheDisplayCurrency(t *testing.T) {
	app := payoutApp(t)
	if _, err := app.recordPayout("earnapp", 10, "USD", "", "", payoutNow); err != nil {
		t.Fatal(err)
	}
	if err := app.cfg.Update(func(c *config.AppConfig) { c.DisplayCurrency = "EUR" }); err != nil {
		t.Fatal(err)
	}
	s := app.computeEarningsSummary(nil)
	if s.PaidOut == nil || !approxEq(*s.PaidOut, 9) {
		t.Fatalf("PaidOut = %v, want 9 EUR (10 USD @0.9)", s.PaidOut)
	}
}

func TestARecordedCashOutIsValidated(t *testing.T) {
	app := payoutApp(t)
	cases := map[string]func() error{
		"unknown service": func() error { _, err := app.recordPayout("no-such-service", 5, "", "", "", payoutNow); return err },
		"zero amount":     func() error { _, err := app.recordPayout("earnapp", 0, "", "", "", payoutNow); return err },
		"negative amount": func() error { _, err := app.recordPayout("earnapp", -1, "", "", "", payoutNow); return err },
		"future date":     func() error { _, err := app.recordPayout("earnapp", 5, "", "", "2026-11-03", payoutNow); return err },
		"not a date":      func() error { _, err := app.recordPayout("earnapp", 5, "", "", "2026-02-30", payoutNow); return err },
	}
	for name, run := range cases {
		if run() == nil {
			t.Errorf("%s: accepted, want refused", name)
		}
	}
	if got, _ := app.store.ListPayouts(""); len(got) != 0 {
		t.Fatalf("refused cash-outs were stored: %+v", got)
	}
}

func TestAMistakenCashOutCanBeRemoved(t *testing.T) {
	app := payoutApp(t)
	p, err := app.recordPayout("earnapp", 5, "", "", "", payoutNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.removePayout(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.removePayout(p.ID); err == nil {
		t.Fatal("removing it twice succeeded, want an error")
	}
	if got, _ := app.store.ListPayouts("earnapp"); len(got) != 0 {
		t.Fatalf("still listed after removal: %+v", got)
	}
}
