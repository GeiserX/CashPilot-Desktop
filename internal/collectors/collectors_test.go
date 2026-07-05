package collectors

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// roundTripFunc adapts a function into an http.RoundTripper so a Registry can be
// driven against canned responses without a live network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestCollectAnyoneAlwaysEmitsTokenCurrency pins fix 2: collectAnyone must ALWAYS
// report the raw ANYONE token count and never flip its currency to USD (the old
// code priced via CoinGecko on success and only then reported "USD", which
// corrupted the summary because computeEarningsSummary collapses each platform to
// a single currency). The exchange layer prices ANYONE, so pricing here is gone.
func TestCollectAnyoneAlwaysEmitsTokenCurrency(t *testing.T) {
	var hosts []string
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		// AO dry-run reward: 1.5e18 base units == 1.5 ANYONE tokens.
		body := `{"Messages":[{"Data":"1500000000000000000"}]}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})
	r := &Registry{http: &http.Client{Transport: rt}}

	res, err := r.collectAnyone(context.Background(), map[string]string{"ANYONE_FINGERPRINTS": "fp1"})
	if err != nil {
		t.Fatalf("collectAnyone error: %v", err)
	}
	if res.Currency != "ANYONE" {
		t.Fatalf("Currency = %q, want ANYONE (the collector must never price to USD)", res.Currency)
	}
	if res.Balance != 1.5 {
		t.Fatalf("Balance = %v, want 1.5 (raw token count, 1.5e18 / 1e18)", res.Balance)
	}
	for _, h := range hosts {
		if strings.Contains(h, "coingecko") {
			t.Fatalf("collector must no longer price via CoinGecko; it contacted %q", h)
		}
	}
}

// TestDoRawCapsBodySize pins fix 4: doRaw must cap the response body at 8 MiB via
// io.LimitReader so a hostile/huge body cannot OOM the app.
func TestDoRawCapsBodySize(t *testing.T) {
	const capBytes = 8 << 20
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(make([]byte, capBytes+4096))),
			Header:     make(http.Header),
		}, nil
	})
	r := &Registry{http: &http.Client{Transport: rt}}

	raw, status, _, err := r.doRaw(context.Background(), "GET", "https://example.test/", nil, nil, nil)
	if err != nil {
		t.Fatalf("doRaw error: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(raw) != capBytes {
		t.Fatalf("doRaw read %d bytes, want it capped at %d", len(raw), capBytes)
	}
}

func TestParseBytelixirBalance(t *testing.T) {
	balance, ok := parseBytelixirBalance(`<span>$</span>0.04<span class="text-2xs">025</span>`)
	if !ok {
		t.Fatal("expected balance to parse")
	}
	if balance != 0.04025 {
		t.Fatalf("unexpected balance: %f", balance)
	}
}

func TestParsePacketStreamBalance(t *testing.T) {
	balance, ok := parsePacketStreamBalance(`<h3>Balance</h3><div><h2 class="x">$1.23</h2></div>`)
	if !ok {
		t.Fatal("expected balance to parse")
	}
	if balance != 1.23 {
		t.Fatalf("unexpected balance: %f", balance)
	}
}

func TestStorjPayoutUSD(t *testing.T) {
	balance := storjPayoutUSD(map[string]any{
		"currentMonth": map[string]any{
			"egressBandwidthPayout":   float64(100),
			"egressRepairAuditPayout": float64(50),
			"diskSpacePayout":         float64(25),
		},
	})
	if balance != 1.75 {
		t.Fatalf("unexpected balance: %f", balance)
	}
}
