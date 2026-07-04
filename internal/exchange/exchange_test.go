package exchange

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const (
	coinGeckoBody   = `{"mysterium":{"usd":0.25}}`
	frankfurterBody = `{"amount":1,"base":"USD","rates":{"EUR":0.9,"GBP":0.8}}`
)

// toggleHandler serves a canned JSON body but can be flipped to return HTTP 500
// to exercise the stale-graceful path. It is safe for concurrent use.
type toggleHandler struct {
	body string
	fail atomic.Bool
}

func (h *toggleHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if h.fail.Load() {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, h.body)
}

// errRoundTripper fails every request at the transport layer.
type errRoundTripper struct{}

func (errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("transport boom")
}

func approx(got, want float64) bool {
	return math.Abs(got-want) <= 1e-6
}

// newTestService wires a Service to two httptest servers, using only MYST so
// the crypto cache is exactly {"MYST": 0.25} after a refresh.
func newTestService(t *testing.T) (*Service, *toggleHandler, *toggleHandler) {
	t.Helper()
	cg := &toggleHandler{body: coinGeckoBody}
	fr := &toggleHandler{body: frankfurterBody}
	cgSrv := httptest.NewServer(cg)
	frSrv := httptest.NewServer(fr)
	t.Cleanup(cgSrv.Close)
	t.Cleanup(frSrv.Close)

	svc := NewService(
		WithBaseURLs(cgSrv.URL, frSrv.URL),
		WithHTTPClient(&http.Client{Timeout: 5 * time.Second}),
		WithCryptoIDs(map[string]string{"MYST": "mysterium"}),
	)
	return svc, cg, fr
}

func TestRefreshAndConvert(t *testing.T) {
	svc, _, _ := newTestService(t)

	if !svc.Stale() {
		t.Fatal("expected Stale() == true before the first refresh")
	}

	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}

	if svc.Stale() {
		t.Fatal("expected Stale() == false after a successful refresh")
	}

	// ToUSD: crypto multiplies (4 * 0.25 == 1.0).
	if got, ok := svc.ToUSD(4, "MYST"); !ok || !approx(got, 1.0) {
		t.Fatalf("ToUSD(4, MYST) = (%v, %v), want (1.0, true)", got, ok)
	}
	// ToUSD: fiat divides (10 / 0.9).
	if got, ok := svc.ToUSD(10, "EUR"); !ok || !approx(got, 10.0/0.9) {
		t.Fatalf("ToUSD(10, EUR) = (%v, %v), want (~11.11, true)", got, ok)
	}
	// ToUSD: USD is identity.
	if got, ok := svc.ToUSD(5, "USD"); !ok || !approx(got, 5) {
		t.Fatalf("ToUSD(5, USD) = (%v, %v), want (5, true)", got, ok)
	}
	// Reward points are non-convertible.
	if got, ok := svc.ToUSD(1, "GRASS"); ok {
		t.Fatalf("ToUSD(1, GRASS) = (%v, %v), want ok=false", got, ok)
	}
	// Unknown token is non-convertible.
	if got, ok := svc.ToUSD(1, "DOGE"); ok {
		t.Fatalf("ToUSD(1, DOGE) = (%v, %v), want ok=false", got, ok)
	}

	// ToDisplay routes crypto -> USD -> fiat: 4 MYST -> 1 USD -> 0.9 EUR.
	if got, ok := svc.ToDisplay(4, "MYST", "EUR"); !ok || !approx(got, 0.9) {
		t.Fatalf("ToDisplay(4, MYST, EUR) = (%v, %v), want (0.9, true)", got, ok)
	}
	// FromUSD into crypto divides: 1 USD / 0.25 == 4 MYST.
	if got, ok := svc.FromUSD(1, "MYST"); !ok || !approx(got, 4) {
		t.Fatalf("FromUSD(1, MYST) = (%v, %v), want (4, true)", got, ok)
	}

	// Convertible reflects membership.
	for _, c := range []string{"USD", "EUR", "MYST"} {
		if !svc.Convertible(c) {
			t.Fatalf("Convertible(%q) = false, want true", c)
		}
	}
	for _, c := range []string{"GRASS", "DOGE"} {
		if svc.Convertible(c) {
			t.Fatalf("Convertible(%q) = true, want false", c)
		}
	}

	// Snapshot is a deep copy that reflects the fetched rates.
	snap := svc.Snapshot()
	if !approx(snap.CryptoUSD["MYST"], 0.25) || !approx(snap.Fiat["EUR"], 0.9) || !approx(snap.Fiat["USD"], 1.0) {
		t.Fatalf("Snapshot rates mismatch: %+v", snap)
	}
	if snap.LastUpdated == "" {
		t.Fatal("Snapshot.LastUpdated should be set after a refresh")
	}
	snap.Fiat["EUR"] = 999 // mutate the copy...
	if got, ok := svc.ToUSD(10, "EUR"); !ok || !approx(got, 10.0/0.9) {
		t.Fatalf("Snapshot mutation leaked into the cache: ToUSD(10, EUR) = (%v, %v)", got, ok)
	}
}

func TestStaleGracefulOnHTTP500(t *testing.T) {
	svc, cg, fr := newTestService(t)

	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh returned error: %v", err)
	}

	// A 500 from CoinGecko must keep the last-good cache and return an error.
	cg.fail.Store(true)
	if err := svc.Refresh(context.Background()); err == nil {
		t.Fatal("expected Refresh to error on CoinGecko HTTP 500")
	}
	assertCachePreserved(t, svc)

	// A 500 from Frankfurter (crypto healthy again) is also fully rejected.
	cg.fail.Store(false)
	fr.fail.Store(true)
	if err := svc.Refresh(context.Background()); err == nil {
		t.Fatal("expected Refresh to error on Frankfurter HTTP 500")
	}
	assertCachePreserved(t, svc)
}

func TestRefreshTransportErrorKeepsUSD(t *testing.T) {
	svc := NewService(
		WithBaseURLs("http://example.invalid", "http://example.invalid"),
		WithHTTPClient(&http.Client{Transport: errRoundTripper{}}),
		WithCryptoIDs(map[string]string{"MYST": "mysterium"}),
	)

	if err := svc.Refresh(context.Background()); err == nil {
		t.Fatal("expected Refresh to return an error when the transport fails")
	}
	if !svc.Stale() {
		t.Fatal("cache should be stale after an all-failing refresh")
	}
	// USD is intrinsic and must convert even with an empty cache.
	if got, ok := svc.ToUSD(5, "USD"); !ok || !approx(got, 5) {
		t.Fatalf("ToUSD(5, USD) = (%v, %v), want (5, true)", got, ok)
	}
	if got, ok := svc.FromUSD(5, "USD"); !ok || !approx(got, 5) {
		t.Fatalf("FromUSD(5, USD) = (%v, %v), want (5, true)", got, ok)
	}
	// Anything requiring a fetched rate is non-convertible.
	if _, ok := svc.ToUSD(1, "EUR"); ok {
		t.Fatal("ToUSD(1, EUR) should be non-convertible with an empty cache")
	}
}

func TestEnsureFreshRefreshesOnce(t *testing.T) {
	svc, _, _ := newTestService(t)

	// First call populates the cache (was never fetched).
	svc.EnsureFresh(context.Background())
	if svc.Stale() {
		t.Fatal("EnsureFresh should have populated the cache")
	}
	before := svc.Snapshot().LastUpdated

	// Second call within CacheTTL must be a no-op (no re-fetch).
	svc.EnsureFresh(context.Background())
	if after := svc.Snapshot().LastUpdated; after != before {
		t.Fatalf("EnsureFresh refetched inside CacheTTL: %q -> %q", before, after)
	}
}

// assertCachePreserved checks the last-good rates survived a failed refresh.
func assertCachePreserved(t *testing.T, svc *Service) {
	t.Helper()
	if got, ok := svc.ToUSD(4, "MYST"); !ok || !approx(got, 1.0) {
		t.Fatalf("stale cache lost MYST: ToUSD(4, MYST) = (%v, %v)", got, ok)
	}
	if got, ok := svc.ToUSD(10, "EUR"); !ok || !approx(got, 10.0/0.9) {
		t.Fatalf("stale cache lost EUR: ToUSD(10, EUR) = (%v, %v)", got, ok)
	}
	if got, ok := svc.ToUSD(5, "USD"); !ok || !approx(got, 5) {
		t.Fatalf("USD must still convert: ToUSD(5, USD) = (%v, %v)", got, ok)
	}
}
