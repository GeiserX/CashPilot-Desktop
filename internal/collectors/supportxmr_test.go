package collectors

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const supportXMRFund = "44AFFq5kSiGBoZ4NMDwYtN18obc8AemS33DBLWs3H7otXft3XjrpDtQGv7SqSsaBYBb98uNbr2VBBEt7f2wfn3RVGQBEP3A"

func TestSupportXMRBalance(t *testing.T) {
	tests := []struct {
		name    string
		address string
		body    string
		balance float64
	}{
		{"recorded pool balance", supportXMRFund, `{"lastHash":1795629323,"totalHashes":86313450020,"amtPaid":934095340542,"amtDue":2633849853,"txnCount":0}`, 0.002633849853},
		{"subaddress with one piconero due", "8" + supportXMRFund[1:], `{"lastHash":0,"totalHashes":0,"amtPaid":0,"amtDue":1}`, 0.000000000001},
		{"integrated address with last share", "4" + strings.Repeat("A", 105), `{"lastHash":1795629323,"totalHashes":0,"amtPaid":0,"amtDue":0}`, 0},
		{"zero with hashes", supportXMRFund, `{"lastHash":0,"totalHashes":1,"amtPaid":0,"amtDue":0}`, 0},
		{"zero after a payment", supportXMRFund, `{"lastHash":0,"totalHashes":0,"amtPaid":1,"amtDue":0,"txnCount":0}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newTestRegistry(t, &stubTransport{routes: map[string]stubResponse{
				"GET https://www.supportxmr.com/api/miner/" + tt.address + "/stats": {body: tt.body},
			}})
			record, err := registry.Collect(context.Background(), "supportxmr", map[string]string{
				"SUPPORTXMR_ADDRESS": " " + tt.address + " ",
			})
			if err != nil || record.Error != "" || record.Platform != "supportxmr" || record.Currency != "XMR" || record.Balance != tt.balance {
				t.Fatalf("collect = %+v, %v; want %g XMR", record, err, tt.balance)
			}
			history := registry.store.ListDailyBalances(1)
			if len(history) != 1 || history[0].Balance != tt.balance || history[0].PoolAddressHash != poolAddressHash(tt.address) {
				t.Fatalf("balance history = %+v; want one reading of %g XMR", history, tt.balance)
			}
		})
	}
}

func TestSupportXMRFailuresSkipBalanceHistory(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		status  int
		message string
		kind    string
	}{
		{name: "unseen", body: `{"lastHash":0,"totalHashes":0,"amtPaid":0,"amtDue":0,"txnCount":9}`, message: "no record of this address"},
		{name: "missing due", body: `{"lastHash":0,"totalHashes":1,"amtPaid":0}`, message: "invalid balance or mining history", kind: "shape"},
		{name: "missing history", body: `{"amtDue":0,"amtPaid":0,"lastHash":0}`, message: "invalid balance or mining history", kind: "shape"},
		{name: "null paid", body: `{"lastHash":0,"totalHashes":0,"amtPaid":null,"amtDue":0}`, message: "invalid balance or mining history", kind: "shape"},
		{name: "negative due", body: `{"lastHash":0,"totalHashes":1,"amtPaid":0,"amtDue":-1}`, message: "invalid balance or mining history", kind: "shape"},
		{name: "string due", body: `{"lastHash":0,"totalHashes":1,"amtPaid":0,"amtDue":"0"}`, message: "invalid balance or mining history", kind: "shape"},
		{name: "fractional due", body: `{"lastHash":0,"totalHashes":1,"amtPaid":0,"amtDue":0.5}`, message: "invalid balance or mining history", kind: "shape"},
		{name: "boolean history", body: `{"lastHash":true,"totalHashes":1,"amtPaid":0,"amtDue":0}`, message: "invalid balance or mining history", kind: "shape"},
		{name: "array", body: `[]`, message: "expected a JSON object", kind: "shape"},
		{name: "API unavailable", status: http.StatusServiceUnavailable, message: "Try again shortly", kind: "transient"},
		{name: "address rejected", status: http.StatusNotFound, message: "SupportXMR returned HTTP 404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newTestRegistry(t, &stubTransport{routes: map[string]stubResponse{
				"GET https://www.supportxmr.com/api/miner/" + supportXMRFund + "/stats": {body: tt.body, status: tt.status},
			}})
			record, err := registry.Collect(context.Background(), "supportxmr", map[string]string{"SUPPORTXMR_ADDRESS": supportXMRFund})
			if err != nil || !strings.Contains(record.Error, tt.message) || record.ErrorKind != tt.kind {
				t.Fatalf("collect = %+v, %v; want error %q (%q)", record, err, tt.message, tt.kind)
			}
			if got := registry.store.ListDailyBalances(1); len(got) != 0 {
				t.Fatalf("failed read entered the balance history: %+v", got)
			}
		})
	}
}

func TestSupportXMRRejectsMalformedAddressBeforeRequest(t *testing.T) {
	for _, address := range []string{"", supportXMRFund + ".rig", supportXMRFund + "+50000", supportXMRFund[:94], "4/" + supportXMRFund[2:]} {
		transport := &countingTransport{}
		registry := NewRegistry(nil, WithHTTPClient(&http.Client{Transport: transport}))
		_, err := registry.collectSupportXMR(context.Background(), map[string]string{"SUPPORTXMR_ADDRESS": address})
		typed, ok := err.(collectorError)
		if !ok || typed.kind != "auth" || transport.calls.Load() != 0 {
			t.Errorf("collect error = %v with %d requests for invalid address %q", err, transport.calls.Load(), address)
		}
	}
}
