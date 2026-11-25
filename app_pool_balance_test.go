package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

func testPoolAddressHash(address string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(address)))
}

func TestPoolMonthlyAccrualAcrossAddressChanges(t *testing.T) {
	app, st := newEarningsTestApp(t,
		`{"monero":{"usd":200}}`,
		`{"amount":1,"base":"USD","rates":{"EUR":0.9}}`,
		map[string]string{"XMR": "monero"}, true)
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastMonthStart := monthStart.AddDate(0, -1, 0)

	// Last month earned 2 + 8 + 6 USD across three separate periods. The
	// second period has two readings on the same day. The third period
	// returns to the first address with a new opening balance.
	for _, reading := range []struct {
		day     int
		address string
		balance float64
	}{
		{-1, "first", 0.01},
		{0, "first", 0.02},
		{1, "second", 0.10},
		{1, "second", 0.14},
		{3, "first", 0.04},
		{4, "first", 0.07},
	} {
		seedEarnings(t, st, store.EarningsRecord{
			Platform: "supportxmr", Balance: reading.balance, Currency: "XMR",
			PoolAddressHash: reading.address, CreatedAt: atUTC(lastMonthStart.AddDate(0, 0, reading.day), 10),
		})
	}
	// This month keeps one address. Carry the last reading forward and use
	// the net change of 0.03 XMR (6 USD).
	seedEarnings(t, st, store.EarningsRecord{
		Platform: "supportxmr", Balance: 0.10, Currency: "XMR",
		PoolAddressHash: "first", CreatedAt: atUTC(now, 10),
	})
	summary := app.computeEarningsSummary(st.ListLatestEarnings())
	if !approxEq(summary.Month, 6) || !approxEq(summary.MonthChange, -62.5) {
		t.Fatalf("Month = %g, MonthChange = %g, want 6 and -62.5%% (last month earned 16)", summary.Month, summary.MonthChange)
	}
}

func TestPoolMonthlyAccrualResumesAfterAddressChange(t *testing.T) {
	app, st := newEarningsTestApp(t,
		`{"monero":{"usd":200}}`,
		`{"amount":1,"base":"USD","rates":{"EUR":0.9}}`,
		map[string]string{"XMR": "monero"}, true)
	now := time.Now().UTC()
	if now.Day() == 1 {
		t.Skip("two daily readings are needed in the current month")
	}
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	seedEarnings(t, st,
		store.EarningsRecord{Platform: "supportxmr", Balance: 0.01, Currency: "XMR", PoolAddressHash: "first", CreatedAt: atUTC(first.AddDate(0, 0, -1), 10)},
		store.EarningsRecord{Platform: "supportxmr", Balance: 0.09, Currency: "XMR", PoolAddressHash: "second", CreatedAt: atUTC(first, 10)},
		store.EarningsRecord{Platform: "supportxmr", Balance: 0.10, Currency: "XMR", PoolAddressHash: "second", CreatedAt: atUTC(now, 10)},
	)
	if got := app.computeEarningsSummary(st.ListLatestEarnings()).Month; !approxEq(got, 2) {
		t.Fatalf("Month = %g, want 2 from the new address after its opening balance", got)
	}
}

func TestPoolTotalUsesCurrentPayoutAddress(t *testing.T) {
	app := newAppUnderTest(t)
	app.Providers.SetCryptoUSD("monero", 200)
	if err := app.exchange.Refresh(app.ctx); err != nil {
		t.Fatal(err)
	}
	const slug, key = "supportxmr", "SUPPORTXMR_ADDRESS"
	first, second := "4"+strings.Repeat("A", 94), "8"+strings.Repeat("B", 94)
	save := func(address string) {
		t.Helper()
		if err := app.SaveCredentials(slug, map[string]string{key: address}); err != nil {
			t.Fatal(err)
		}
	}
	checkTotal := func(want float64) {
		t.Helper()
		summary, err := app.GetEarningsSummary()
		if err != nil || !approxEq(summary.Total, want) {
			t.Errorf("GetEarningsSummary total = %g, err = %v, want %g", summary.Total, err, want)
		}
		state, err := app.GetAppState()
		if err != nil || !approxEq(state.Summary.Total, want) {
			t.Errorf("GetAppState total = %g, err = %v, want %g", state.Summary.Total, err, want)
		}
	}
	now := time.Now().UTC()
	oldReading := store.EarningsRecord{Platform: slug, Balance: 0.09, Currency: "XMR",
		PoolAddressHash: testPoolAddressHash(first), CreatedAt: atUTC(now.AddDate(0, 0, -1), 10)}
	save(first)
	seedEarnings(t, app.store, oldReading)
	checkTotal(18)
	save(second)
	checkTotal(0)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: slug, Error: "no record of this address", Currency: "USD", CreatedAt: atUTC(now, 9)})
	checkTotal(0)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: slug, Balance: 0.02, Currency: "XMR",
		PoolAddressHash: testPoolAddressHash(second), CreatedAt: atUTC(now, 10)})
	oldReading.CreatedAt = atUTC(now, 11)
	seedEarnings(t, app.store, oldReading)
	checkTotal(4)
}

type poolTestTransport func(*http.Request) (*http.Response, error)

func (f poolTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestPoolCollectionDiscardsReplacedAddress(t *testing.T) {
	for _, path := range []string{"post-deploy", "manual", "scheduled helper"} {
		t.Run(path, func(t *testing.T) {
			app := newAppUnderTest(t)
			const slug, key = "supportxmr", "SUPPORTXMR_ADDRESS"
			first, second := "4"+strings.Repeat("A", 94), "8"+strings.Repeat("B", 94)
			if err := app.SaveCredentials(slug, map[string]string{key: first}); err != nil {
				t.Fatal(err)
			}
			runner := app.App
			if path == "scheduled helper" {
				peer, err := store.Open(app.cfg.DataDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = peer.Close() })
				runner = &App{ctx: app.ctx, cfg: app.cfg, store: peer}
			}
			started := make(chan string, 1)
			release := make(chan struct{})
			ctx, cancel := context.WithTimeout(app.ctx, 5*time.Second)
			defer cancel()
			client := &http.Client{Transport: poolTestTransport(func(req *http.Request) (*http.Response, error) {
				started <- req.URL.Path
				select {
				case <-release:
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(
						`{"lastHash":1,"totalHashes":1,"amtPaid":0,"amtDue":10000000000}`))}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})}
			runner.collectors = collectors.NewRegistry(runner.store, collectors.WithHTTPClient(client))
			done := make(chan struct{})
			go func() {
				defer close(done)
				switch path {
				case "post-deploy":
					runner.collectOne(ctx, slug)
				case "manual":
					_, _ = runner.CollectService(slug)
				case "scheduled helper":
					runner.collectAll(ctx)
				}
			}()
			// Always unblock and join the collector before the store closes.
			defer func() {
				close(release)
				<-done
				if got := app.store.ListLatestEarnings(); len(got) != 0 {
					t.Errorf("stored a response for the replaced payout address: %+v", got)
				}
			}()
			select {
			case requested := <-started:
				if requested != "/api/miner/"+first+"/stats" {
					t.Fatalf("request used the wrong initial address: %q", requested)
				}
			case <-ctx.Done():
				t.Fatal("collector did not start")
			}
			if err := app.SaveCredentials(slug, map[string]string{key: second}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
