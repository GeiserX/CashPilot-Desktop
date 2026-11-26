package collectors

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// heroMinersAddress is the address in the web collector's tests.
const heroMinersAddress = "kaspa:qp5y3dq23g8nts03peqhvlq9xx4asd2mr84r22zqxqglkle7jf5qvrjsw5lkd"

func heroMinersURL(address string) string {
	return "GET https://kaspa.herominers.com/api/stats_address?" + url.Values{
		"address": {address}, "recentBlocksAmount": {"0"}, "longpoll": {"false"},
	}.Encode()
}

func fastRetries(t *testing.T) {
	t.Helper()
	previous := retryBaseWait
	retryBaseWait = time.Millisecond
	t.Cleanup(func() { retryBaseWait = previous })
}

func TestHeroMinersKaspaReadsOnlyTheUnpaidSompiBalance(t *testing.T) {
	ecdsa := "kaspa:" + strings.Repeat("q", 63)
	tests := []struct {
		name    string
		address string
		body    string
		balance float64
	}{
		// Recorded from the pool API on 2026-11-25 (the web contract fixture).
		{"recorded pool balance", heroMinersAddress, `{"stats":{"balance":"40828934717","paid":"20853646526889","lastShare":"1795640820","minPayoutLevel":"50000000000","hashrate":28976712690347}}`, 408.28934717},
		{"zero for a known address", heroMinersAddress, `{"stats":{"balance":"0"}}`, 0},
		{"one sompi", heroMinersAddress, `{"stats":{"balance":"1"}}`, 0.00000001},
		{"twenty digits", heroMinersAddress, `{"stats":{"balance":"99999999999999999999"}}`, 999999999999.99999999},
		{"63-character ECDSA address", ecdsa, `{"stats":{"balance":"184000000"}}`, 1.84},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []*http.Request
			stub := &stubTransport{routes: map[string]stubResponse{heroMinersURL(tt.address): {body: tt.body}}}
			registry := newTestRegistry(t, stub)
			registry.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests = append(requests, req)
				return stub.RoundTrip(req)
			})}
			record, err := registry.Collect(context.Background(), "herominers-kas", map[string]string{
				heroMinersKaspaAddressKey: " " + tt.address + " ",
			})
			if err != nil || record.Error != "" || record.Platform != "herominers-kas" || record.Currency != "KAS" || record.Balance != tt.balance {
				t.Fatalf("collect = %+v, %v; want %g KAS", record, err, tt.balance)
			}
			if len(requests) != 1 || requests[0].URL.Host != "kaspa.herominers.com" || requests[0].URL.Path != "/api/stats_address" {
				t.Fatalf("requests = %v; want one stats_address request", requests)
			}
			if got := requests[0].URL.Query(); len(got) != 3 || got.Get("address") != tt.address || got.Get("recentBlocksAmount") != "0" || got.Get("longpoll") != "false" {
				t.Fatalf("query = %v", got)
			}
			history := registry.store.ListDailyBalances(1)
			if len(history) != 1 || history[0].Balance != tt.balance || history[0].PoolAddressHash != poolAddressHash(tt.address) {
				t.Fatalf("balance history = %+v; want one reading of %g KAS", history, tt.balance)
			}
		})
	}
}

func TestHeroMinersKaspaFailuresSkipBalanceHistory(t *testing.T) {
	fastRetries(t)
	tests := []struct {
		name    string
		body    string
		status  int
		message string
		kind    string
	}{
		{name: "unseen address", body: `{"error":"Not found"}`, message: "no record of this address"},
		{name: "address rejected by the pool", body: `{"error":"Invalid address"}`, message: "full KAS payout address", kind: "auth"},
		{name: "other pool error with a balance", body: `{"error":"pool busy","stats":{"balance":"0"}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "null error with a balance", body: `{"error":null,"stats":{"balance":"0"}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "array", body: `[]`, message: "valid JSON", kind: "shape"},
		{name: "null", body: `null`, message: "JSON object", kind: "shape"},
		{name: "html", body: `<html>`, message: "valid JSON", kind: "shape"},
		{name: "no stats", body: `{}`, message: "valid unpaid balance", kind: "shape"},
		{name: "null stats", body: `{"stats":null}`, message: "valid unpaid balance", kind: "shape"},
		{name: "empty stats", body: `{"stats":{}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "null balance", body: `{"stats":{"balance":null}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "boolean balance", body: `{"stats":{"balance":true}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "number balance", body: `{"stats":{"balance":5}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "negative balance", body: `{"stats":{"balance":"-1"}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "fractional balance", body: `{"stats":{"balance":"1.4"}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "exponent balance", body: `{"stats":{"balance":"1e8"}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "empty balance", body: `{"stats":{"balance":""}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "21 digits", body: `{"stats":{"balance":"` + strings.Repeat("1", 21) + `"}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "API unavailable", status: http.StatusServiceUnavailable, message: "Try again shortly", kind: "transient"},
		{name: "public request refused", status: http.StatusForbidden, message: "HTTP 403", kind: "transient"},
		{name: "public request unauthorized", status: http.StatusUnauthorized, message: "HTTP 401", kind: "transient"},
		{name: "not found", status: http.StatusNotFound, message: "HTTP 404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newTestRegistry(t, &stubTransport{routes: map[string]stubResponse{
				heroMinersURL(heroMinersAddress): {body: tt.body, status: tt.status},
			}})
			record, err := registry.Collect(context.Background(), "herominers-kas", map[string]string{heroMinersKaspaAddressKey: heroMinersAddress})
			if err != nil || !strings.Contains(record.Error, tt.message) || record.ErrorKind != tt.kind {
				t.Fatalf("collect = %+v, %v; want error %q (%q)", record, err, tt.message, tt.kind)
			}
			if strings.Contains(record.Error, heroMinersAddress[6:]) {
				t.Fatalf("error shows the payout address: %q", record.Error)
			}
			if got := registry.store.ListDailyBalances(1); len(got) != 0 {
				t.Fatalf("failed read entered the balance history: %+v", got)
			}
		})
	}
}

func TestHeroMinersKaspaTransportErrorOmitsTheAddress(t *testing.T) {
	fastRetries(t)
	registry := newTestRegistry(t, &stubTransport{})
	calls := 0
	registry.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("connection reset")
	})}
	record, err := registry.Collect(context.Background(), "herominers-kas", map[string]string{heroMinersKaspaAddressKey: heroMinersAddress})
	if err != nil || record.ErrorKind != "transient" || !strings.Contains(record.Error, "Try again shortly") || calls != maxAttempts {
		t.Fatalf("collect = %+v, %v after %d requests", record, err, calls)
	}
	if strings.Contains(record.Error, heroMinersAddress[6:]) || strings.Contains(record.Error, "herominers.com") {
		t.Fatalf("error shows the request URL: %q", record.Error)
	}
}

func TestHeroMinersKaspaRejectsMalformedAddressBeforeRequest(t *testing.T) {
	for _, address := range []string{
		"",
		heroMinersAddress + ".rig",
		heroMinersAddress[6:],
		heroMinersAddress[:len(heroMinersAddress)-1],
		heroMinersAddress + "&longpoll=true",
		strings.ToUpper(heroMinersAddress),
		"kaspatest:" + heroMinersAddress[6:],
		"kaspa:" + strings.Repeat("q", 62),
		"kaspa:" + strings.Repeat("q", 64),
		"kaspa:" + strings.Repeat("b", 61),
	} {
		transport := &countingTransport{}
		registry := NewRegistry(nil, WithHTTPClient(&http.Client{Transport: transport}))
		_, err := registry.collectHeroMinersKaspa(context.Background(), map[string]string{heroMinersKaspaAddressKey: address})
		typed, ok := err.(collectorError)
		if !ok || typed.kind != "auth" || transport.calls.Load() != 0 {
			t.Errorf("collect error = %v with %d requests for invalid address %q", err, transport.calls.Load(), address)
		}
	}
}
