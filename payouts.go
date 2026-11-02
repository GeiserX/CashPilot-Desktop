package main

import (
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// The payout ledger: cash-outs the user records by hand, so cashing out stops
// looking like a loss. The balance total drops by every withdrawal; lifetime
// earned is that total plus what was cashed out. Port of GeiserX/CashPilot#487.
//
// Desktop does not detect payouts from balance drops, so every entry here is one
// the user typed: a cash-out made before CashPilot was installed, or any other.

// ListPayouts returns one service's recorded cash-outs, newest first.
func (a *App) ListPayouts(slug string) ([]store.Payout, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.store.ListPayouts(slug)
}

// RecordPayout records a cash-out for a catalog service. currency "" is the unit
// the service's balance is recorded in (then the catalog's cash-out currency,
// then USD); date "" is today, otherwise YYYY-MM-DD and not in the future.
func (a *App) RecordPayout(slug string, amount float64, currency, method, date string) (store.Payout, error) {
	if err := a.ready(); err != nil {
		return store.Payout{}, err
	}
	return a.recordPayout(slug, amount, currency, method, date, time.Now().UTC())
}

// RemovePayout removes a cash-out recorded by mistake.
func (a *App) RemovePayout(id int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.removePayout(id)
}

func (a *App) removePayout(id int64) error {
	removed, err := a.store.DeletePayout(id)
	if err != nil {
		return err
	}
	if !removed {
		return errors.New("no cash-out with that id")
	}
	return nil
}

func (a *App) recordPayout(slug string, amount float64, currency, method, date string, now time.Time) (store.Payout, error) {
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return store.Payout{}, fmt.Errorf("unknown service %q", slug)
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return store.Payout{}, errors.New("the amount must be more than zero")
	}
	method = strings.TrimSpace(method)
	if len(method) > 40 {
		return store.Payout{}, errors.New("the method can be at most 40 characters")
	}
	today := now.Format("2006-01-02")
	date = strings.TrimSpace(date)
	if date != "" {
		day, err := time.Parse("2006-01-02", date)
		if err != nil {
			return store.Payout{}, errors.New("the date must be a real day, YYYY-MM-DD")
		}
		if day.Format("2006-01-02") > today {
			return store.Payout{}, errors.New("the date cannot be in the future")
		}
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if len(currency) > 12 {
		return store.Payout{}, errors.New("the currency can be at most 12 characters")
	}
	if currency == "" {
		for _, rec := range a.store.ListLatestEarnings() {
			if rec.Platform == slug && rec.Error == "" {
				currency = rec.Currency
			}
		}
	}
	if currency == "" {
		currency = strings.ToUpper(strings.TrimSpace(svc.Cashout.Currency))
	}
	if currency == "" {
		currency = "USD"
	}
	// Only today's quote exists. A past cash-out in a token priced at it would be
	// restated by today's price, so it stays unpriced and is counted as such.
	var rate *float64
	if currency == "USD" {
		one := 1.0
		rate = &one
	} else if (date == "" || date == today) && a.exchange != nil {
		if r, ok := a.exchange.ToUSD(1, currency); ok {
			rate = &r
		}
	}
	return a.store.AddPayout(store.Payout{
		Platform:  slug,
		Amount:    amount,
		Currency:  currency,
		FxRateUSD: rate,
		Method:    method,
		PaidOn:    date,
	})
}

// addLifetime fills PaidOut, EarnedAllTime and UnpricedPayouts on a summary
// whose Total is already computed. A ledger that cannot be read, or a display
// currency with no rate, leaves both figures nil: 0 would claim nothing was ever
// cashed out. EarnedAllTime is also nil while the balance total is unknown.
func (a *App) addLifetime(summary *EarningsSummary) {
	payouts, err := a.store.ListPayouts("")
	if err != nil {
		log.Printf("payouts: could not total cash-outs, reporting them as unknown: %v", err)
		return
	}
	var paid float64
	unpriced := 0
	for _, p := range payouts {
		if p.FxRateUSD == nil {
			unpriced++
			continue
		}
		conv, ok := a.exchange.FromUSD(p.Amount*(*p.FxRateUSD), summary.DisplayCurrency)
		if !ok {
			return
		}
		paid += conv
	}
	summary.UnpricedPayouts = unpriced
	summary.PaidOut = &paid
	if summary.TotalKnown {
		all := summary.Total + paid
		summary.EarnedAllTime = &all
	}
}
