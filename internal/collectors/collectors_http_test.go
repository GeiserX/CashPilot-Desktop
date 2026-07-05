package collectors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/zalando/go-keyring"
)

// This file exercises the REAL HTTP-parse path of the JSON-balance collectors —
// request build -> auth round-trip -> balance JSON decode -> Result{Balance,
// Currency,Error} — with zero network. Every collector funnels through
// r.http.Do(req) (collectors.go doRaw), so a Registry built with a custom
// http.Client{Transport: stubTransport} sees canned responses keyed by the
// request's method+URL. Each table case pins the exact Balance computed from the
// numbers in its canned body plus the Currency, and the failure cases pin that an
// HTTP error status or malformed JSON surfaces as an error (never a panic).

// stubResponse is one canned HTTP reply. status 0 is treated as 200. headers are
// added to the response Header (e.g. Set-Cookie) so cookie-driven collectors can
// be stubbed too; JSON-balance collectors here don't need them but the field keeps
// the seam general.
type stubResponse struct {
	status  int
	body    string
	headers map[string]string
}

// stubTransport is an http.RoundTripper that answers from a route table instead of
// the network. Keys are "METHOD URL" (e.g. "GET https://host/path").
type stubTransport struct {
	routes map[string]stubResponse
}

// RoundTrip matches req against the route table and never touches the network.
// Matching order:
//  1. exact "METHOD URL" key (URL includes any query string);
//  2. longest route key that is a prefix of "METHOD URL" — so a route registered
//     without a query string still matches "...?appid=...&version=..."; and
//  3. longest route whose URL part is a substring of the request URL (same method).
//
// Longest-match keeps selection deterministic even though Go map iteration order
// is random. An unmapped call returns HTTP 599 with an empty JSON body so it
// surfaces as a clear failure rather than a panic or a real network hit.
func (t *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.String()
	if resp, ok := t.routes[key]; ok {
		return resp.toResponse(req), nil
	}
	bestLen := -1
	var best stubResponse
	for rk, rv := range t.routes {
		if strings.HasPrefix(key, rk) && len(rk) > bestLen {
			bestLen, best = len(rk), rv
		}
	}
	if bestLen >= 0 {
		return best.toResponse(req), nil
	}
	for rk, rv := range t.routes {
		method, urlPart, found := strings.Cut(rk, " ")
		if found && req.Method == method && strings.Contains(req.URL.String(), urlPart) && len(rk) > bestLen {
			bestLen, best = len(rk), rv
		}
	}
	if bestLen >= 0 {
		return best.toResponse(req), nil
	}
	return stubResponse{status: 599, body: "{}"}.toResponse(req), nil
}

func (s stubResponse) toResponse(req *http.Request) *http.Response {
	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	header := make(http.Header)
	for k, v := range s.headers {
		header.Add(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Request:    req,
	}
}

// newTestRegistry builds a Registry wired to stub over a temp store, using the
// exact keyring.MockInit()+store.Open(t.TempDir()) setup the existing collector
// tests use (see TestCollectDispatch). The JSON-balance collectors never touch the
// store, but building the full Registry keeps this faithful to production wiring.
func newTestRegistry(t *testing.T, stub *stubTransport) *Registry {
	t.Helper()
	keyring.MockInit()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return &Registry{store: st, http: &http.Client{Transport: stub}}
}

// TestCollectorsHTTPParse drives each JSON-balance collector's real parse path
// against canned responses: six collectors on the success path (exact Balance +
// Currency), five on an HTTP-error path, and two on a malformed-JSON path.
func TestCollectorsHTTPParse(t *testing.T) {
	type collectFn func(*Registry, context.Context, map[string]string) (Result, error)

	cases := []struct {
		name         string
		collect      collectFn
		env          map[string]string
		creds        map[string]string
		routes       map[string]stubResponse
		wantErr      bool
		wantPlatform string
		wantBalance  float64
		wantCurrency string
	}{
		// ---- success paths (6 collectors) ----
		{
			name:    "honeygain success",
			collect: (*Registry).collectHoneygain,
			creds:   map[string]string{"HONEYGAIN_EMAIL": "user@example.test", "HONEYGAIN_PASSWORD": "pw"},
			routes: map[string]stubResponse{
				"POST https://dashboard.honeygain.com/api/v1/users/tokens":  {body: `{"data":{"access_token":"hg-access-token"}}`},
				"GET https://dashboard.honeygain.com/api/v1/users/balances": {body: `{"data":{"payout":{"usd_cents":1250}}}`},
			},
			wantPlatform: "honeygain",
			wantBalance:  12.5, // usd_cents 1250 / 100
			wantCurrency: "USD",
		},
		{
			name:    "repocket success",
			collect: (*Registry).collectRepocket,
			env:     map[string]string{"CASHPILOT_REPOCKET_FIREBASE_KEY": "test-firebase-key"},
			creds:   map[string]string{"REPOCKET_EMAIL": "user@example.test", "REPOCKET_PASSWORD": "pw"},
			routes: map[string]stubResponse{
				// registered without ?key=... — the collector appends the firebase key
				// as a query string, exercising the prefix fallback in RoundTrip.
				"POST https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword": {body: `{"idToken":"repocket-id-token"}`},
				"GET https://api.repocket.com/api/reports/current":                           {body: `{"centsCredited":625}`},
			},
			wantPlatform: "repocket",
			wantBalance:  6.25, // centsCredited 625 / 100
			wantCurrency: "USD",
		},
		{
			name:    "vast-ai success sums per-machine earnings",
			collect: (*Registry).collectVast,
			creds:   map[string]string{"VAST_API_KEY": "vast-key"},
			routes: map[string]stubResponse{
				// machine1 = gpu+sto+bwu+bwd = 2.0+0.5+0.25+0.25 = 3.0; machine2 = 1.0+0.5 = 1.5; sum = 4.5.
				"GET https://console.vast.ai/api/v0/users/me/machine-earnings": {body: `{"per_machine":[{"gpu_earn":2.0,"sto_earn":0.5,"bwu_earn":0.25,"bwd_earn":0.25},{"gpu_earn":1.0,"sto_earn":0.5}]}`},
			},
			wantPlatform: "vast-ai",
			wantBalance:  4.5,
			wantCurrency: "USD",
		},
		{
			name:    "traffmonetizer success",
			collect: (*Registry).collectTraffmonetizer,
			creds:   map[string]string{"TRAFFMONETIZER_TOKEN": "tm-token"},
			routes: map[string]stubResponse{
				"GET https://data.traffmonetizer.com/api/app_user/get_balance": {body: `{"data":{"balance":7.25}}`},
			},
			wantPlatform: "traffmonetizer",
			wantBalance:  7.25,
			wantCurrency: "USD",
		},
		{
			name:    "earnfm success",
			collect: (*Registry).collectEarnFM,
			env:     map[string]string{"CASHPILOT_EARNFM_SUPABASE_ANON_KEY": "anon-key"},
			creds:   map[string]string{"EARNFM_EMAIL": "user@example.test", "EARNFM_PASSWORD": "pw"},
			routes: map[string]stubResponse{
				// registered without ?grant_type=password — prefix fallback matches it.
				"POST https://sb.earn.fm/auth/v1/token":             {body: `{"access_token":"earnfm-access-token"}`},
				"GET https://api.earn.fm/v2/harvester/view_balance": {body: `{"data":{"totalBalance":3.5}}`},
			},
			wantPlatform: "earnfm",
			wantBalance:  3.5,
			wantCurrency: "USD",
		},
		{
			name:    "iproyal success",
			collect: (*Registry).collectIPRoyal,
			creds:   map[string]string{"IPROYALPAWNS_EMAIL": "user@example.test", "IPROYALPAWNS_PASSWORD": "pw"},
			routes: map[string]stubResponse{
				"POST https://api.pawns.app/api/v1/users/tokens":              {body: `{"access_token":"pawns-access-token"}`},
				"GET https://api.pawns.app/api/v1/users/me/balance-dashboard": {body: `{"balance":8.75}`},
			},
			wantPlatform: "iproyal",
			wantBalance:  8.75,
			wantCurrency: "USD",
		},

		// ---- HTTP-error paths (5 collectors) ----
		{
			name:    "honeygain login HTTP 401 errors",
			collect: (*Registry).collectHoneygain,
			creds:   map[string]string{"HONEYGAIN_EMAIL": "user@example.test", "HONEYGAIN_PASSWORD": "pw"},
			routes: map[string]stubResponse{
				"POST https://dashboard.honeygain.com/api/v1/users/tokens": {status: 401, body: `{"error":"invalid credentials"}`},
			},
			wantErr: true,
		},
		{
			name:    "traffmonetizer balance HTTP 500 errors",
			collect: (*Registry).collectTraffmonetizer,
			creds:   map[string]string{"TRAFFMONETIZER_TOKEN": "tm-token"},
			routes: map[string]stubResponse{
				"GET https://data.traffmonetizer.com/api/app_user/get_balance": {status: 500, body: `{}`},
			},
			wantErr: true,
		},
		{
			name:    "iproyal login HTTP 500 errors",
			collect: (*Registry).collectIPRoyal,
			creds:   map[string]string{"IPROYALPAWNS_EMAIL": "user@example.test", "IPROYALPAWNS_PASSWORD": "pw"},
			routes: map[string]stubResponse{
				"POST https://api.pawns.app/api/v1/users/tokens": {status: 500, body: `{}`},
			},
			wantErr: true,
		},
		{
			name:    "repocket report HTTP 500 errors",
			collect: (*Registry).collectRepocket,
			env:     map[string]string{"CASHPILOT_REPOCKET_FIREBASE_KEY": "test-firebase-key"},
			creds:   map[string]string{"REPOCKET_EMAIL": "user@example.test", "REPOCKET_PASSWORD": "pw"},
			routes: map[string]stubResponse{
				"POST https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword": {body: `{"idToken":"repocket-id-token"}`},
				"GET https://api.repocket.com/api/reports/current":                           {status: 500, body: `{}`},
			},
			wantErr: true,
		},
		{
			name:    "earnfm balance HTTP 401 errors",
			collect: (*Registry).collectEarnFM,
			env:     map[string]string{"CASHPILOT_EARNFM_SUPABASE_ANON_KEY": "anon-key"},
			creds:   map[string]string{"EARNFM_EMAIL": "user@example.test", "EARNFM_PASSWORD": "pw"},
			routes: map[string]stubResponse{
				"POST https://sb.earn.fm/auth/v1/token":             {body: `{"access_token":"earnfm-access-token"}`},
				"GET https://api.earn.fm/v2/harvester/view_balance": {status: 401, body: `{}`},
			},
			wantErr: true,
		},

		// ---- malformed-JSON paths (2 collectors) ----
		{
			name:    "honeygain malformed balance JSON errors, not panics",
			collect: (*Registry).collectHoneygain,
			creds:   map[string]string{"HONEYGAIN_EMAIL": "user@example.test", "HONEYGAIN_PASSWORD": "pw"},
			routes: map[string]stubResponse{
				"POST https://dashboard.honeygain.com/api/v1/users/tokens":  {body: `{"data":{"access_token":"hg-access-token"}}`},
				"GET https://dashboard.honeygain.com/api/v1/users/balances": {body: `{"data":{"payout":`}, // truncated -> invalid JSON
			},
			wantErr: true,
		},
		{
			name:    "vast-ai malformed earnings JSON errors, not panics",
			collect: (*Registry).collectVast,
			creds:   map[string]string{"VAST_API_KEY": "vast-key"},
			routes: map[string]stubResponse{
				"GET https://console.vast.ai/api/v0/users/me/machine-earnings": {body: `{"per_machine":[`}, // truncated -> invalid JSON
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			r := newTestRegistry(t, &stubTransport{routes: tc.routes})

			res, err := tc.collect(r, context.Background(), tc.creds)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil (result: %+v)", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Platform != tc.wantPlatform {
				t.Errorf("Platform = %q, want %q", res.Platform, tc.wantPlatform)
			}
			if res.Balance != tc.wantBalance {
				t.Errorf("Balance = %v, want %v", res.Balance, tc.wantBalance)
			}
			if res.Currency != tc.wantCurrency {
				t.Errorf("Currency = %q, want %q", res.Currency, tc.wantCurrency)
			}
			if res.Error != "" {
				t.Errorf("Result.Error = %q, want empty on the success path", res.Error)
			}
		})
	}
}
