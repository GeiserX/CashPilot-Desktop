package collectors

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// twoMinersAddress is the address in the web collector tests.
const twoMinersAddress = "0x00109777bdc6d6a658097ed6b6167569ee1f4ec1"

const twoMinersURL = "GET https://etc.2miners.com/api/accounts/" + twoMinersAddress

func TestTwoMinersETCReadsOnlyTheUnpaidBalance(t *testing.T) {
	tests := []struct {
		name    string
		address string
		body    string
		balance float64
	}{
		// Recorded from the pool API on 2026-11-25 (the web contract fixture).
		{"recorded pool balance", twoMinersAddress, `{"stats":{"balance":48277005,"immature":459255,"lastShare":1795641243,"paid":163385117517,"pending":123456}}`, 0.048277005},
		{"zero for a known address", twoMinersAddress, `{"stats":{"balance":0,"lastShare":1}}`, 0},
		{"one pool unit", twoMinersAddress, `{"stats":{"balance":1,"lastShare":1}}`, 0.000000001},
		{"checksum case is lowercased", "0x" + strings.ToUpper(twoMinersAddress[2:]), `{"stats":{"balance":2000000000,"lastShare":1}}`, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []*http.Request
			stub := &stubTransport{routes: map[string]stubResponse{twoMinersURL: {body: tt.body}}}
			registry := newTestRegistry(t, stub)
			registry.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests = append(requests, req)
				return stub.RoundTrip(req)
			})}
			record, err := registry.Collect(context.Background(), "2miners-etc", map[string]string{
				twoMinersETCAddressKey: " " + tt.address + " ",
			})
			if err != nil || record.Error != "" || record.Platform != "2miners-etc" || record.Currency != "ETC" || record.Balance != tt.balance {
				t.Fatalf("collect = %+v, %v; want %g ETC", record, err, tt.balance)
			}
			if len(requests) != 1 || requests[0].Method != http.MethodGet || requests[0].URL.String() != "https://etc.2miners.com/api/accounts/"+twoMinersAddress {
				t.Fatalf("requests = %v; want one lowercase account request", requests)
			}
			history := registry.store.ListDailyBalances(1)
			if len(history) != 1 || history[0].Balance != tt.balance || history[0].PoolAddressHash != poolAddressHash(twoMinersAddress) {
				t.Fatalf("balance history = %+v; want one reading of %g ETC", history, tt.balance)
			}
		})
	}
}

func TestTwoMinersETCFailuresSkipBalanceHistory(t *testing.T) {
	fastRetries(t)
	tests := []struct {
		name    string
		body    string
		status  int
		message string
		kind    string
	}{
		{name: "unseen address", status: http.StatusNotFound, message: "no record of this address"},
		{name: "unseen address in an older reply", body: `{"stats":{"lastShare":0,"balance":0,"paid":0}}`, message: "no record of this address"},
		// Recorded on 2026-11-26: a seen address can answer 200 with no balance key.
		{name: "seen address without a balance", body: `{"stats":{"lastShare":1794105329}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "pool error with a balance", body: `{"error":"busy","stats":{"balance":1,"lastShare":1}}`, message: "account stats", kind: "shape"},
		{name: "null error with a balance", body: `{"error":null,"stats":{"balance":1,"lastShare":1}}`, message: "account stats", kind: "shape"},
		{name: "array", body: `[]`, message: "valid JSON", kind: "shape"},
		{name: "null", body: `null`, message: "account stats", kind: "shape"},
		{name: "html", body: `<html>`, message: "valid JSON", kind: "shape"},
		{name: "no stats", body: `{}`, message: "account stats", kind: "shape"},
		{name: "null stats", body: `{"stats":null}`, message: "account stats", kind: "shape"},
		{name: "list stats", body: `{"stats":[]}`, message: "account stats", kind: "shape"},
		{name: "empty stats", body: `{"stats":{}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "balance without a share time", body: `{"stats":{"balance":0}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "string balance", body: `{"stats":{"balance":"0","lastShare":1}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "negative balance", body: `{"stats":{"balance":-1,"lastShare":1}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "boolean balance", body: `{"stats":{"balance":true,"lastShare":1}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "fractional balance", body: `{"stats":{"balance":1.5,"lastShare":1}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "exponent balance", body: `{"stats":{"balance":1e9,"lastShare":1}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "balance past 64 bits", body: `{"stats":{"balance":18446744073709551616,"lastShare":1}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "boolean share time", body: `{"stats":{"balance":0,"lastShare":false}}`, message: "valid unpaid balance", kind: "shape"},
		{name: "API unavailable", status: http.StatusServiceUnavailable, message: "Try again shortly", kind: "transient"},
		{name: "public request refused", status: http.StatusForbidden, message: "HTTP 403", kind: "transient"},
		{name: "public request unauthorized", status: http.StatusUnauthorized, message: "HTTP 401", kind: "transient"},
		{name: "bad request", status: http.StatusBadRequest, message: "HTTP 400"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newTestRegistry(t, &stubTransport{routes: map[string]stubResponse{
				twoMinersURL: {body: tt.body, status: tt.status},
			}})
			record, err := registry.Collect(context.Background(), "2miners-etc", map[string]string{twoMinersETCAddressKey: twoMinersAddress})
			if err != nil || !strings.Contains(record.Error, tt.message) || record.ErrorKind != tt.kind {
				t.Fatalf("collect = %+v, %v; want error %q (%q)", record, err, tt.message, tt.kind)
			}
			if strings.Contains(strings.ToLower(record.Error), twoMinersAddress[2:]) {
				t.Fatalf("error shows the payout address: %q", record.Error)
			}
			if got := registry.store.ListDailyBalances(1); len(got) != 0 {
				t.Fatalf("failed read entered the balance history: %+v", got)
			}
		})
	}
}

func TestTwoMinersETCTransportErrorOmitsTheAddress(t *testing.T) {
	fastRetries(t)
	registry := newTestRegistry(t, &stubTransport{})
	calls := 0
	registry.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("connection reset")
	})}
	record, err := registry.Collect(context.Background(), "2miners-etc", map[string]string{twoMinersETCAddressKey: twoMinersAddress})
	if err != nil || record.ErrorKind != "transient" || !strings.Contains(record.Error, "Try again shortly") || calls != maxAttempts {
		t.Fatalf("collect = %+v, %v after %d requests", record, err, calls)
	}
	if strings.Contains(record.Error, twoMinersAddress[2:]) || strings.Contains(record.Error, "2miners.com") {
		t.Fatalf("error shows the request URL: %q", record.Error)
	}
}

func TestTwoMinersETCRejectsMalformedAddressBeforeRequest(t *testing.T) {
	for _, address := range []string{
		"",
		twoMinersAddress + ".rig",
		twoMinersAddress[2:],
		twoMinersAddress[:len(twoMinersAddress)-1],
		twoMinersAddress + "0",
		twoMinersAddress + "?extra=",
		"0x" + strings.Repeat("g", 40),
		"0X" + twoMinersAddress[2:],
		"bc1qabcd",
		"../" + twoMinersAddress,
	} {
		transport := &countingTransport{}
		registry := NewRegistry(nil, WithHTTPClient(&http.Client{Transport: transport}))
		_, err := registry.collectTwoMinersETC(context.Background(), map[string]string{twoMinersETCAddressKey: address})
		typed, ok := err.(collectorError)
		if !ok || typed.kind != "auth" || transport.calls.Load() != 0 {
			t.Errorf("collect error = %v with %d requests for invalid address %q", err, transport.calls.Load(), address)
		}
	}
}
