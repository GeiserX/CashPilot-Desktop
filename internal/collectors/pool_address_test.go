package collectors

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

func TestPoolAddressHashMatchesCollectorNormalization(t *testing.T) {
	const bech32 = "bc1q4efd2vns8qwawg3nt4xxzs9053shh0q3x0tuu9"
	// A Base58 address is case-sensitive, so only Bech32 is lowercased.
	const legacy = "1BoatSLRHtKNngkdXEeobR76b53LETtpyT"
	tests := []struct {
		name  string
		slug  string
		creds map[string]string
		want  string
		pool  bool
	}{
		{"ocean Bech32 in any case", "ocean", map[string]string{oceanAddressKey: " " + strings.ToUpper(bech32) + " "}, poolAddressHash(bech32), true},
		{"ocean Base58 keeps its case", "ocean", map[string]string{oceanAddressKey: legacy}, poolAddressHash(legacy), true},
		{"supportxmr trims the address", "supportxmr", map[string]string{supportXMRAddressKey: " " + supportXMRFund + " "}, poolAddressHash(supportXMRFund), true},
		{"pool with no address yet", "supportxmr", map[string]string{supportXMRAddressKey: "  "}, "", true},
		{"service without a payout address", "honeygain", map[string]string{"HONEYGAIN_EMAIL": "a@b.c"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, pool := PoolAddressHash(tt.slug, tt.creds)
			if got != tt.want || pool != tt.pool {
				t.Fatalf("PoolAddressHash = %q, %v; want %q, %v", got, pool, tt.want, tt.pool)
			}
		})
	}
	if lower, _ := PoolAddressHash("ocean", map[string]string{oceanAddressKey: strings.ToLower(legacy)}); lower == poolAddressHash(legacy) {
		t.Fatal("a lowercased Base58 address is a different address and must not share its hash")
	}
}

// supportXMRReply answers the stats request with a recorded balance.
func supportXMRReply(req *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"lastHash":1795629323,"totalHashes":86313450020,"amtPaid":0,"amtDue":2633849853}`)),
		Request:    req,
	}
}

func TestCollectSavedDiscardsReadForReplacedAddress(t *testing.T) {
	replacement := "8" + supportXMRFund[1:]
	tests := []struct {
		name   string
		change func(*store.Store) error
	}{
		{"address replaced", func(st *store.Store) error {
			return st.SaveCredentials("supportxmr", map[string]string{supportXMRAddressKey: replacement})
		}},
		{"address removed", func(st *store.Store) error { return st.DeleteCredentials("supportxmr") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newTestRegistry(t, &stubTransport{})
			creds := map[string]string{supportXMRAddressKey: supportXMRFund}
			if err := registry.store.SaveCredentials("supportxmr", creds); err != nil {
				t.Fatal(err)
			}
			var changed atomic.Bool
			registry.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				// The user saves new settings while the pool request is in flight.
				if !changed.Swap(true) {
					if err := tt.change(registry.store); err != nil {
						return nil, err
					}
				}
				return supportXMRReply(req), nil
			})}

			record, err := registry.CollectSaved(context.Background(), "supportxmr", creds)
			if !errors.Is(err, ErrPayoutAddressChanged) || record != (store.EarningsRecord{}) {
				t.Fatalf("CollectSaved = %+v, %v; want the read discarded with ErrPayoutAddressChanged", record, err)
			}
			if got := registry.store.ListLatestEarnings(); len(got) != 0 {
				t.Fatalf("the read for the old address was saved: %+v", got)
			}
		})
	}

	// The next check with the saved address keeps its reading.
	registry := newTestRegistry(t, &stubTransport{})
	creds := map[string]string{supportXMRAddressKey: replacement}
	if err := registry.store.SaveCredentials("supportxmr", creds); err != nil {
		t.Fatal(err)
	}
	registry.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return supportXMRReply(req), nil
	})}
	record, err := registry.CollectSaved(context.Background(), "supportxmr", creds)
	if err != nil || record.Error != "" || record.Balance != 0.002633849853 || record.PoolAddressHash != poolAddressHash(replacement) {
		t.Fatalf("CollectSaved = %+v, %v; want the reading saved for the current address", record, err)
	}
	if got := registry.store.ListLatestEarnings(); len(got) != 1 || got[0].PoolAddressHash != poolAddressHash(replacement) {
		t.Fatalf("latest earnings = %+v; want one reading for the current address", got)
	}
}

// A service without a payout address saves like Collect, with no credential check.
func TestCollectSavedWithoutPayoutAddressSavesTheResult(t *testing.T) {
	registry := newTestRegistry(t, &stubTransport{})
	record, err := registry.CollectSaved(context.Background(), "not-a-service", map[string]string{"TOKEN": "x"})
	if err != nil || !strings.Contains(record.Error, "not ported yet") || record.PoolAddressHash != "" {
		t.Fatalf("CollectSaved = %+v, %v; want the collector error saved", record, err)
	}
	if got := registry.store.ListLatestEarnings(); len(got) != 1 || got[0].Platform != "not-a-service" {
		t.Fatalf("latest earnings = %+v; want the one saved row", got)
	}
}
