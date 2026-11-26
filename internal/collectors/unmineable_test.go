package collectors

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const unMineableURL = "GET https://api.unminable.com/v4/address/" + supportXMRFund + "?coin="

// unMineableSample is the web contract fixture: the pool's reply on 2026-11-26 for
// the Monero General Fund donation address with coin=XMR (chain list removed).
const unMineableSample = `{"success":true,"msg":"Ok","data":{"balance":"0.00071092","balance_payable":"0.00071092","precision":"8","payment_threshold":"0.03","mining_fee":"1","auto":false,"enabled":true,"maintenance_message":null,"enabled_auto_only":false,"uuid":"7a7812ea-6a3d-4932-b64d-f7cd1fcd4038","fresh":false,"network":"XMR","err_flags":{"payment_error":false,"missing_memo":false,"not_activated":false,"restricted":false}}}`

func unMineableCredentials(coin string) map[string]string {
	return map[string]string{unMineableAddressKey: supportXMRFund, unMineableCoinKey: coin}
}

func unMineableReply(data string) string {
	return `{"success":true,"msg":"Ok","data":` + data + `}`
}

func TestUnMineableReadsTheBalanceInTheChosenCoin(t *testing.T) {
	tests := []struct {
		name    string
		coin    string
		want    string
		body    string
		balance float64
	}{
		{"recorded XMR reply", " xmr ", "XMR", unMineableSample, 0.00071092},
		{"known address with nothing owed", "SHIB", "SHIB", unMineableReply(`{"balance":"0","payment_threshold":"150000","fresh":false}`), 0},
		{"exponent string", "DOGE", "DOGE", unMineableReply(`{"balance":"1e-7","payment_threshold":"30","fresh":false}`), 1e-7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []*http.Request
			stub := &stubTransport{routes: map[string]stubResponse{unMineableURL + tt.want: {body: tt.body}}}
			registry := newTestRegistry(t, stub)
			registry.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests = append(requests, req)
				return stub.RoundTrip(req)
			})}
			record, err := registry.Collect(context.Background(), "unmineable", map[string]string{
				unMineableAddressKey: " " + supportXMRFund + " ", unMineableCoinKey: tt.coin,
			})
			if err != nil || record.Error != "" || record.Platform != "unmineable" || record.Currency != tt.want || record.Balance != tt.balance {
				t.Fatalf("collect = %+v, %v; want %g %s", record, err, tt.balance, tt.want)
			}
			if len(requests) != 1 || requests[0].Method != http.MethodGet || requests[0].URL.Host != "api.unminable.com" ||
				requests[0].URL.Path != "/v4/address/"+supportXMRFund || requests[0].URL.RawQuery != "coin="+tt.want {
				t.Fatalf("requests = %v; want one address stats request for %s", requests, tt.want)
			}
			history := registry.store.ListDailyBalances(1)
			if len(history) != 1 || history[0].Balance != tt.balance || history[0].Currency != tt.want ||
				history[0].PoolAddressHash != poolAddressHash(tt.want+":"+supportXMRFund) {
				t.Fatalf("balance history = %+v; want one %s reading of %g", history, tt.want, tt.balance)
			}
		})
	}
}

func TestUnMineableFailuresAreNeverAZeroBalance(t *testing.T) {
	fastRetries(t)
	tests := []struct {
		name    string
		body    string
		status  int
		message string
		kind    string
	}{
		{name: "address never seen", body: unMineableReply(`{"balance":"0","payment_threshold":"0.03","fresh":true}`), message: "no record of this XMR address"},
		{name: "invalid address", status: http.StatusNotFound, body: `{"success":false,"msg":"Invalid address"}`, message: "does not accept this address for XMR", kind: "auth"},
		{name: "invalid coin", status: http.StatusNotFound, body: `{"success":false,"msg":"Invalid or missing coin"}`, message: "does not list the coin XMR", kind: "auth"},
		{name: "other refusal", status: http.StatusNotFound, body: `{"success":false,"msg":"Nope"}`, message: "HTTP 404"},
		{name: "refusal with HTTP 200", body: `{"success":false,"msg":"Nope"}`, message: "address stats", kind: "shape"},
		{name: "no fresh flag", body: unMineableReply(`{"balance":"0.1","payment_threshold":"0.03"}`), message: "valid balance", kind: "shape"},
		{name: "null fresh flag", body: unMineableReply(`{"balance":"0.1","payment_threshold":"0.03","fresh":null}`), message: "valid balance", kind: "shape"},
		{name: "no balance", body: unMineableReply(`{"payment_threshold":"0.03","fresh":false}`), message: "valid balance", kind: "shape"},
		{name: "number balance", body: unMineableReply(`{"balance":0.5,"payment_threshold":"0.03","fresh":false}`), message: "valid balance", kind: "shape"},
		{name: "negative balance", body: unMineableReply(`{"balance":"-1","payment_threshold":"0.03","fresh":false}`), message: "valid balance", kind: "shape"},
		{name: "NaN balance", body: unMineableReply(`{"balance":"NaN","payment_threshold":"0.03","fresh":false}`), message: "valid balance", kind: "shape"},
		{name: "infinite balance", body: unMineableReply(`{"balance":"Infinity","payment_threshold":"0.03","fresh":false}`), message: "valid balance", kind: "shape"},
		{name: "huge exponent", body: unMineableReply(`{"balance":"1e999","payment_threshold":"0.03","fresh":false}`), message: "valid balance", kind: "shape"},
		{name: "hex balance", body: unMineableReply(`{"balance":"0x1p-2","payment_threshold":"0.03","fresh":false}`), message: "valid balance", kind: "shape"},
		{name: "word balance", body: unMineableReply(`{"balance":"lots","payment_threshold":"0.03","fresh":false}`), message: "valid balance", kind: "shape"},
		{name: "no threshold", body: unMineableReply(`{"balance":"0.1","fresh":false}`), message: "valid balance", kind: "shape"},
		{name: "zero threshold", body: unMineableReply(`{"balance":"0.1","payment_threshold":"0","fresh":false}`), message: "valid balance", kind: "shape"},
		{name: "no data", body: `{"success":true,"msg":"Ok"}`, message: "address stats", kind: "shape"},
		{name: "list data", body: `{"success":true,"data":[]}`, message: "address stats", kind: "shape"},
		{name: "string success", body: `{"success":"yes","data":{"balance":"0.1","payment_threshold":"0.03","fresh":false}}`, message: "address stats", kind: "shape"},
		{name: "array", body: `[]`, message: "address stats", kind: "shape"},
		{name: "html", body: `<html>`, message: "address stats", kind: "shape"},
		{name: "API unavailable", status: http.StatusServiceUnavailable, message: "Try again shortly", kind: "transient"},
		{name: "rate limited", status: http.StatusTooManyRequests, message: "Try again shortly", kind: "transient"},
		{name: "public request refused", status: http.StatusForbidden, message: "HTTP 403", kind: "transient"},
		{name: "public request unauthorized", status: http.StatusUnauthorized, message: "HTTP 401", kind: "transient"},
		{name: "bad request", status: http.StatusBadRequest, message: "HTTP 400"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newTestRegistry(t, &stubTransport{routes: map[string]stubResponse{
				unMineableURL + "XMR": {body: tt.body, status: tt.status},
			}})
			record, err := registry.Collect(context.Background(), "unmineable", unMineableCredentials("XMR"))
			if err != nil || !strings.Contains(record.Error, tt.message) || record.ErrorKind != tt.kind {
				t.Fatalf("collect = %+v, %v; want error %q (%q)", record, err, tt.message, tt.kind)
			}
			if strings.Contains(record.Error, supportXMRFund) {
				t.Fatalf("error shows the payout address: %q", record.Error)
			}
			if got := registry.store.ListDailyBalances(1); len(got) != 0 {
				t.Fatalf("failed read entered the balance history: %+v", got)
			}
		})
	}
}

func TestUnMineableTransportErrorOmitsTheAddress(t *testing.T) {
	fastRetries(t)
	registry := newTestRegistry(t, &stubTransport{})
	calls := 0
	registry.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("connection reset")
	})}
	record, err := registry.Collect(context.Background(), "unmineable", unMineableCredentials("XMR"))
	if err != nil || record.ErrorKind != "transient" || !strings.Contains(record.Error, "Try again shortly") || calls != maxAttempts {
		t.Fatalf("collect = %+v, %v after %d requests", record, err, calls)
	}
	if strings.Contains(record.Error, supportXMRFund) || strings.Contains(record.Error, "unminable.com") {
		t.Fatalf("error shows the request URL: %q", record.Error)
	}
}

func TestUnMineableRejectsMalformedInputBeforeRequest(t *testing.T) {
	cases := []struct {
		coin, address, message string
	}{
		{"", supportXMRFund, "ticker"},
		{"X", supportXMRFund, "ticker"},
		{"XMR:other", supportXMRFund, "ticker"},
		{"ABCDEFGHIJK", supportXMRFund, "ticker"},
		{"X-R", supportXMRFund, "ticker"},
		{"XMR", "", "XMR payout address"},
		{"XMR", supportXMRFund + ".rig", "XMR payout address"},
		{"XMR", supportXMRFund + "#someone-else", "XMR payout address"},
		{"XMR", "XMR:" + supportXMRFund, "XMR payout address"},
		{"XMR", supportXMRFund + "?coin=SHIB", "XMR payout address"},
		{"XMR", "../" + supportXMRFund, "XMR payout address"},
		{"XMR", strings.Repeat("a", 19), "XMR payout address"},
		{"XMR", strings.Repeat("a", 129), "XMR payout address"},
	}
	for _, tc := range cases {
		transport := &countingTransport{}
		registry := NewRegistry(nil, WithHTTPClient(&http.Client{Transport: transport}))
		_, err := registry.collectUnMineable(context.Background(), map[string]string{unMineableAddressKey: tc.address, unMineableCoinKey: tc.coin})
		typed, ok := err.(collectorError)
		if !ok || typed.kind != "auth" || !strings.Contains(typed.message, tc.message) || transport.calls.Load() != 0 {
			t.Errorf("coin %q address %q: error %v with %d requests", tc.coin, tc.address, err, transport.calls.Load())
		}
		if ok && strings.Contains(typed.message, supportXMRFund) {
			t.Errorf("error shows the payout address: %q", typed.message)
		}
	}
}

// A coin switch is a new balance: the old coin's readings must not be its baseline.
func TestUnMineableCoinIsPartOfTheBalanceIdentity(t *testing.T) {
	xmr, _ := PoolAddressHash("unmineable", unMineableCredentials("XMR"))
	shib, _ := PoolAddressHash("unmineable", unMineableCredentials("SHIB"))
	lower, _ := PoolAddressHash("unmineable", unMineableCredentials(" xmr "))
	if xmr == "" || xmr == shib || xmr != lower {
		t.Fatalf("hashes: XMR %q, SHIB %q, lowercase xmr %q; want a coin switch to change it and case not to", xmr, shib, lower)
	}
	for _, creds := range []map[string]string{
		{unMineableAddressKey: supportXMRFund},
		{unMineableCoinKey: "XMR"},
	} {
		if hash, pool := PoolAddressHash("unmineable", creds); hash != "" || !pool {
			t.Errorf("PoolAddressHash(%v) = %q, %v; want no identity until both are set", creds, hash, pool)
		}
	}
}
