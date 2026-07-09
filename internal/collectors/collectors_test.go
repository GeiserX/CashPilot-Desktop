package collectors

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/zalando/go-keyring"
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

// TestRound pins round's half-away-from-zero behavior for BOTH signs. The prior
// float64(int(value*scale+0.5)) implementation rounded negatives toward +inf (e.g.
// -2.5 -> -2, -0.5 -> 0) instead of away from zero; math.Round fixes that.
func TestRound(t *testing.T) {
	const eps = 1e-9
	cases := []struct {
		value  float64
		places int
		want   float64
	}{
		{1.23456, 4, 1.2346},
		{1.0, 4, 1.0},
		{0, 4, 0},
		{2.5, 0, 3},   // half away from zero
		{-2.5, 0, -3}, // negatives round away from zero, not toward +inf (old: -2)
		{-0.5, 0, -1}, // old code gave 0
		{-1.23456, 4, -1.2346},
		{123.456, 2, 123.46},
	}
	for _, tc := range cases {
		if got := round(tc.value, tc.places); math.Abs(got-tc.want) > eps {
			t.Errorf("round(%v, %d) = %v, want %v", tc.value, tc.places, got, tc.want)
		}
	}
}

// TestRedactURLSecrets pins fix 5's helper: a secret carried in a URL query must be
// scrubbed, in both the "<url> returned HTTP" and the net/url.Error quoted forms,
// while non-secret text and query keys (and path segments) are left intact.
func TestRedactURLSecrets(t *testing.T) {
	const key = "AIzaSyD-EXAMPLE-firebase-key-1234567890"
	cases := []string{
		"https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=" + key + " returned HTTP 400",
		`Post "https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=` + key + `": dial tcp: lookup host: no such host`,
	}
	for _, in := range cases {
		out := redactURLSecrets(in)
		if strings.Contains(out, key) {
			t.Errorf("key not removed from %q -> %q", in, out)
		}
		if !strings.Contains(out, "REDACTED") {
			t.Errorf("redactURLSecrets(%q) = %q, want a REDACTED marker", in, out)
		}
	}
	// Other sensitive params are scrubbed, and the surrounding non-secret query is kept.
	if got := redactURLSecrets("https://x/y?token=abc123&page=2"); strings.Contains(got, "abc123") || !strings.Contains(got, "page=2") {
		t.Errorf("token redaction mishandled: %q", got)
	}
	// A plain message and a path segment that merely contains 'password' are untouched.
	if got := redactURLSecrets("could not sign in"); got != "could not sign in" {
		t.Errorf("plain message mangled: %q", got)
	}
	if got := redactURLSecrets("POST /accounts:signInWithPassword failed"); got != "POST /accounts:signInWithPassword failed" {
		t.Errorf("non-query path touched: %q", got)
	}
}

// TestCollectRepocketRedactsFirebaseKey pins fix 5 end-to-end: the Repocket
// collector signs in via a Firebase URL that embeds the API key as ?key=<KEY>. A
// failed request must not leak that key into the persisted earnings.error string.
func TestCollectRepocketRedactsFirebaseKey(t *testing.T) {
	keyring.MockInit()
	const firebaseKey = "AIzaSyD-TEST-firebase-key-DO-NOT-LEAK"
	t.Setenv("CASHPILOT_REPOCKET_FIREBASE_KEY", firebaseKey)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	// The Firebase sign-in returns 400, so doJSON builds "<url> returned HTTP 400"
	// with the key still in the query string.
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"INVALID_PASSWORD"}}`)), Header: make(http.Header)}, nil
	})
	reg := &Registry{http: &http.Client{Transport: rt}, store: st}

	rec, err := reg.Collect(context.Background(), "repocket", map[string]string{"REPOCKET_EMAIL": "a@b.co", "REPOCKET_PASSWORD": "pw"})
	if err != nil {
		t.Fatalf("Collect(repocket): %v", err)
	}
	if rec.Error == "" {
		t.Fatal("expected a persisted collector error for a 400 sign-in")
	}
	if strings.Contains(rec.Error, firebaseKey) {
		t.Fatalf("Firebase key leaked into persisted error: %q", rec.Error)
	}
	if !strings.Contains(rec.Error, "REDACTED") {
		t.Fatalf("expected the leaked key to be replaced with REDACTED, got: %q", rec.Error)
	}
}

// TestCollectGrassNon2xxIsError pins the M1 fix: a non-2xx that collectGrass does
// not already special-case (401/403/429) must surface as an ERROR, never a silent
// Result{Balance:0} "success" that overwrites the real balance. doJSONStatus leaves
// the decode target zero-valued on a non-2xx, so without the guard a 404/400 would
// fall through to Balance:0 with no Error. Both Grass endpoints are covered.
func TestCollectGrassNon2xxIsError(t *testing.T) {
	t.Run("retrieveUser 404 errors", func(t *testing.T) {
		rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"message":"not found"}`)), Header: make(http.Header)}, nil
		})
		r := &Registry{http: &http.Client{Transport: rt}}
		res, err := r.collectGrass(context.Background(), map[string]string{"GRASS_ACCESS_TOKEN": "tok"})
		if err == nil {
			t.Fatalf("expected an error for a 404 from retrieveUser, got Result %+v (a silent Balance:0 would overwrite the real balance)", res)
		}
	})

	t.Run("activeDevices 400 errors after zero-point retrieveUser", func(t *testing.T) {
		rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			// retrieveUser: valid 200 but zero points -> the collector falls through
			// to the activeDevices estimate path.
			if strings.Contains(req.URL.Path, "retrieveUser") {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"result":{"data":{"totalPoints":0}}}`)), Header: make(http.Header)}, nil
			}
			return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		})
		r := &Registry{http: &http.Client{Transport: rt}}
		res, err := r.collectGrass(context.Background(), map[string]string{"GRASS_ACCESS_TOKEN": "tok"})
		if err == nil {
			t.Fatalf("expected an error for a 400 from activeDevices, got Result %+v", res)
		}
	})
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

// TestRegistrySupports pins the collectorDispatch map / Supports contract the
// scheduler relies on: every wired collector reports true, and unported or
// unknown slugs report false (so the scheduler skips them instead of writing a
// spurious "not ported yet" error row every cycle).
func TestRegistrySupports(t *testing.T) {
	r := &Registry{}
	supported := []string{
		"anyone-protocol", "bitping", "bytelixir", "earnapp", "honeygain", "earnfm",
		"grass", "iproyal", "mysterium", "packetstream", "proxyrack", "repocket",
		"salad", "storj", "traffmonetizer", "vast-ai",
	}
	for _, slug := range supported {
		if !r.Supports(slug) {
			t.Errorf("Supports(%q) = false, want true (it has a native collector)", slug)
		}
	}
	unsupported := []string{"gaganode", "presearch", "golem", "unknown-service", ""}
	for _, slug := range unsupported {
		if r.Supports(slug) {
			t.Errorf("Supports(%q) = true, want false (no collector wired)", slug)
		}
	}
}

// TestCollectDispatch covers Registry.Collect: an unsupported slug persists a
// "not ported yet" error record, and a supported slug dispatches through the
// collectorDispatch map to its collector.
func TestCollectDispatch(t *testing.T) {
	keyring.MockInit()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	// Unsupported slug -> not-ported error, still persisted with the slug as Platform.
	rec, err := (&Registry{store: st}).Collect(context.Background(), "gaganode", nil)
	if err != nil {
		t.Fatalf("Collect(gaganode) unexpected error: %v", err)
	}
	if rec.Platform != "gaganode" || rec.Error == "" {
		t.Fatalf("Collect(gaganode) = %+v, want Platform=gaganode with a not-ported Error", rec)
	}

	// Supported slug dispatches to its collector (mock HTTP; the {} body makes the
	// collector fail, which still exercises the dispatch + error persistence path).
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})
	rec2, err := (&Registry{http: &http.Client{Transport: rt}, store: st}).Collect(context.Background(), "honeygain", map[string]string{})
	if err != nil {
		t.Fatalf("Collect(honeygain) unexpected error: %v", err)
	}
	if rec2.Platform != "honeygain" {
		t.Fatalf("Collect(honeygain) Platform=%q, want honeygain (dispatch reached the collector)", rec2.Platform)
	}
}

// TestCollectVast covers the Vast.ai GPU-marketplace collector: it must sum the
// per-machine earnings into a USD balance, send the API key as a Bearer token to
// the machine-earnings endpoint, and treat a valid empty response (no GPU rented
// yet) as Balance 0 rather than an error.
func TestCollectVast(t *testing.T) {
	t.Run("sums per-machine earnings and sends Bearer auth", func(t *testing.T) {
		var authHeader, gotPath string
		rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			authHeader = req.Header.Get("Authorization")
			gotPath = req.URL.Path
			// Two machines: (5+1+0.25+0.25)=6.5 and (3+0.5+0.25+0.25)=4.0 => 10.5.
			// current.total (9.75) is deliberately different so the test pins that
			// the per-machine SUM is used, not the current-balance fallback.
			body := `{
				"current": {"balance": 9.75, "service_fee": 0.25, "total": 9.75, "credit": 0},
				"summary": {"total_gpu": 8.0, "total_stor": 1.5, "total_bwu": 0.5, "total_bwd": 0.5},
				"per_machine": [
					{"machine_id": 1001, "gpu_earn": 5.0, "sto_earn": 1.0, "bwu_earn": 0.25, "bwd_earn": 0.25},
					{"machine_id": 1002, "gpu_earn": 3.0, "sto_earn": 0.5, "bwu_earn": 0.25, "bwd_earn": 0.25}
				],
				"per_day": []
			}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
		r := &Registry{http: &http.Client{Transport: rt}}

		res, err := r.collectVast(context.Background(), map[string]string{"VAST_API_KEY": "vast-secret-123"})
		if err != nil {
			t.Fatalf("collectVast error: %v", err)
		}
		if res.Platform != "vast-ai" {
			t.Fatalf("Platform = %q, want vast-ai", res.Platform)
		}
		if res.Currency != "USD" {
			t.Fatalf("Currency = %q, want USD", res.Currency)
		}
		if res.Balance != 10.5 {
			t.Fatalf("Balance = %v, want 10.5 (sum of per-machine gpu+sto+bwu+bwd earnings)", res.Balance)
		}
		if authHeader != "Bearer vast-secret-123" {
			t.Fatalf("Authorization = %q, want %q", authHeader, "Bearer vast-secret-123")
		}
		if gotPath != "/api/v0/users/me/machine-earnings" {
			t.Fatalf("request path = %q, want /api/v0/users/me/machine-earnings", gotPath)
		}
	})

	t.Run("empty earnings is Balance 0, not an error", func(t *testing.T) {
		rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
			// A host with no GPU rented yet: valid response, no machine earnings.
			body := `{"current": {"balance": 0, "total": 0, "credit": 0}, "per_machine": [], "per_day": []}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
		r := &Registry{http: &http.Client{Transport: rt}}

		res, err := r.collectVast(context.Background(), map[string]string{"VAST_API_KEY": "k"})
		if err != nil {
			t.Fatalf("collectVast (empty) error: %v", err)
		}
		if res.Balance != 0 {
			t.Fatalf("Balance = %v, want 0 for an empty earnings response", res.Balance)
		}
		if res.Currency != "USD" {
			t.Fatalf("Currency = %q, want USD", res.Currency)
		}
	})

	t.Run("missing API key is an error", func(t *testing.T) {
		r := &Registry{http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("collector must not hit the network without an API key")
			return nil, nil
		})}}
		if _, err := r.collectVast(context.Background(), map[string]string{}); err == nil {
			t.Fatal("collectVast with no API key = nil error, want an error")
		}
	})
}

// TestCatalogCollectorParity is the Slice 1 / T2 guard: every service the catalog
// marks automatable (collector.type api/scrape/auto) and still live (status not
// dead/dropped/broken) MUST either have a native collector wired in
// collectorDispatch or be explicitly listed in knownUnported below. It fails when
// a new automatable service is added without a collector or an allowlist entry, or
// when a slug is renamed out from under its collector — either is a real gap that
// would otherwise silently stop earning data from being collected.
func TestCatalogCollectorParity(t *testing.T) {
	// Active api/scrape/auto catalog slugs that do NOT yet have a native collector.
	// Each is a deliberate, known gap. Remove a slug the moment its collector lands:
	// the checks below fail if an allowlisted slug is actually supported or is no
	// longer an active automatable service, so this list cannot silently rot.
	// vast-ai is intentionally absent — this slice ports it.
	knownUnported := map[string]bool{
		"dawn":      true,
		"golem":     true,
		"gradient":  true,
		"helium":    true,
		"nodepay":   true,
		"nosana":    true,
		"presearch": true,
		"proxylite": true,
		"teneo":     true,
		"titan":     true,
	}

	cat, err := catalog.LoadEmbedded(os.DirFS("../.."))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if len(cat.List()) == 0 {
		t.Fatal(`catalog loaded 0 services; check the LoadEmbedded(os.DirFS("../..")) path`)
	}

	reg := &Registry{}
	automatable := map[string]bool{"api": true, "scrape": true, "auto": true}
	inactive := map[string]bool{"dead": true, "dropped": true, "broken": true}

	var offenders []string
	allowlistUsed := map[string]bool{}
	for _, svc := range cat.List() {
		if !automatable[svc.Collector.Type] || inactive[svc.Status] {
			continue
		}
		switch {
		case reg.Supports(svc.Slug):
			// has a native collector — good
		case knownUnported[svc.Slug]:
			allowlistUsed[svc.Slug] = true
		default:
			offenders = append(offenders, fmt.Sprintf("%s (status=%s, collector.type=%s)", svc.Slug, svc.Status, svc.Collector.Type))
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("automatable catalog services with neither a collector nor a knownUnported entry:\n  %s\n"+
			"Wire a collector into collectorDispatch (preferred), or if it truly can't be automated yet add the slug to knownUnported in this test.",
			strings.Join(offenders, "\n  "))
	}

	// vast-ai is ported by this slice: it must be supported and must not sit in the
	// allowlist.
	if !reg.Supports("vast-ai") {
		t.Error("vast-ai must have a native collector wired in collectorDispatch")
	}
	if knownUnported["vast-ai"] {
		t.Error("vast-ai now has a collector; it must not be in knownUnported")
	}

	// Keep the allowlist honest: prune any entry that has since gained a collector
	// or is no longer an active api/scrape/auto catalog service.
	for slug := range knownUnported {
		switch {
		case reg.Supports(slug):
			t.Errorf("knownUnported lists %q but it now has a native collector; remove it from the allowlist", slug)
		case !allowlistUsed[slug]:
			t.Errorf("knownUnported lists %q but no active api/scrape/auto catalog service has that slug (renamed/removed/deactivated?); prune it", slug)
		}
	}
}

// TestVastEarningsUSD exercises every branch of the defensive Vast.ai earnings
// reducer + the per-machine earnings() fallback so the whole chain is covered.
func TestVastEarningsUSD(t *testing.T) {
	check := func(name string, e vastEarnings, want float64) {
		if got := vastEarningsUSD(e); got != want {
			t.Errorf("%s: vastEarningsUSD = %v, want %v", name, got, want)
		}
	}
	check("per_machine total field", vastEarnings{PerMachine: []vastMachineEarning{{Total: 3}, {Total: 4}}}, 7)
	check("per_machine total_earn field", vastEarnings{PerMachine: []vastMachineEarning{{TotalEarn: 2.5}}}, 2.5)
	check("per_machine component sum", vastEarnings{PerMachine: []vastMachineEarning{{GPUEarn: 1, StoEarn: 0.5, BWUEarn: 0.25, BWDEarn: 0.25}}}, 2.0)
	check("machine_earnings alt key", vastEarnings{MachineEarnings: []vastMachineEarning{{Total: 5}}}, 5)

	var e5 vastEarnings
	e5.Current.Total = 9
	check("current.total fallback", e5, 9)

	var e6 vastEarnings
	e6.Current.Balance = 11
	check("current.balance fallback", e6, 11)

	var e7 vastEarnings
	e7.Summary.TotalGPU, e7.Summary.TotalStor, e7.Summary.TotalBWU, e7.Summary.TotalBWD = 1, 2, 0.5, 0.5
	check("summary fallback", e7, 4.0)

	check("top-level total fallback", vastEarnings{Total: 13}, 13)
	check("empty -> 0", vastEarnings{}, 0)
}
