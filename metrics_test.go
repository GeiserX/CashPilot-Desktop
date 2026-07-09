package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// TestHandleMetrics exercises the Prometheus exposition end to end against a
// seeded in-memory store: a normal earnings row, an error row, a health-scored
// service, and an online fleet device with a label value that must be escaped.
func TestHandleMetrics(t *testing.T) {
	// Exchange pointed at httptest servers (MYST=0.25), pre-refreshed so the summary
	// computation stays fully in-memory and never kicks a background fetch.
	app, st := newEarningsTestApp(t,
		`{"mysterium":{"usd":0.25}}`,
		`{"amount":1,"base":"USD","rates":{"EUR":0.9}}`,
		map[string]string{"MYST": "mysterium"}, true)

	now := time.Now().UTC()
	atUTCStr := func(daysAgo, hour int) string {
		d := now.AddDate(0, 0, -daysAgo)
		return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, time.UTC).Format(time.RFC3339)
	}
	// honeygain is a normal USD balance; brokencollector's latest run failed.
	seedEarnings(t, st,
		store.EarningsRecord{Platform: "honeygain", Balance: 2.0, Currency: "USD", CreatedAt: atUTCStr(0, 10)},
		store.EarningsRecord{Platform: "brokencollector", Balance: 0, Currency: "USD", Error: "login failed", CreatedAt: atUTCStr(0, 11)},
	)

	// Health samples for honeygain so both a score and an uptime% are emitted.
	for i := 0; i < 3; i++ {
		st.RecordEvent("honeygain", "health_up", "")
	}

	// An online worker whose name contains characters the exposition format must
	// escape (a backslash and double-quotes). last_seen is a real RFC3339 timestamp
	// so the staleness gauge is emitted.
	deviceName := "rig \"A\"\\B" // literally: rig "A"\B
	if _, err := st.UpsertFleetDevice(store.FleetDevice{
		Name:     deviceName,
		Kind:     "worker",
		Status:   "online",
		LastSeen: now.Add(-30 * time.Second).Format(time.RFC3339),
		Services: []string{"storj"},
	}); err != nil {
		t.Fatalf("UpsertFleetDevice error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	app.handleMetrics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("unexpected content-type: %q", ct)
	}

	body := w.Body.String()

	// Format sanity: HELP/TYPE header and the trivial liveness gauge.
	mustContain(t, body, "# HELP cashpilot_up ")
	mustContain(t, body, "# TYPE cashpilot_up gauge")
	mustContain(t, body, "cashpilot_up 1")

	// Collection cadence (default 60 minutes).
	mustContain(t, body, "cashpilot_collect_interval_minutes 60")

	// Normal earnings row -> a balance sample with platform + currency labels.
	mustContain(t, body, `cashpilot_service_balance{platform="honeygain",currency="USD"} 2`)

	// The error-row platform is represented as an error metric, NOT a bogus balance.
	mustContain(t, body, `cashpilot_service_error{platform="brokencollector"} 1`)
	if strings.Contains(body, `cashpilot_service_balance{platform="brokencollector"`) {
		t.Fatalf("an error-row platform must not emit a balance sample:\n%s", body)
	}

	// Health score + uptime for the sampled service.
	mustContain(t, body, `cashpilot_service_health_score{slug="honeygain"} 100`)
	mustContain(t, body, `cashpilot_service_uptime_percent{slug="honeygain"} 100`)

	// Fleet counts by live status, and the escaped device label on the staleness gauge.
	mustContain(t, body, `cashpilot_fleet_devices{status="online"} 1`)
	mustContain(t, body, `cashpilot_fleet_devices{status="offline"} 0`)
	mustContain(t, body, `cashpilot_fleet_device_last_seen_seconds{device="rig \"A\"\\B"}`)

	// Earnings USD gauges are present (summary reused; total = 2.00 from honeygain).
	mustContain(t, body, `cashpilot_earnings_usd_total 2`)
}

// TestHandleMetricsRejectsNonGet pins the GET-only method guard.
func TestHandleMetricsRejectsNonGet(t *testing.T) {
	app, _ := newEarningsTestApp(t, `{}`,
		`{"amount":1,"base":"USD","rates":{"EUR":0.9}}`, map[string]string{}, true)

	req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	w := httptest.NewRecorder()
	app.handleMetrics(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST /metrics, got %d", w.Code)
	}
}

// TestHandleMetricsNilStore proves the handler never panics and still emits the
// store-independent gauges when the store is not wired.
func TestHandleMetricsNilStore(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	app.handleMetrics(w, req) // must not panic

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with a nil store, got %d", w.Code)
	}
	mustContain(t, w.Body.String(), "cashpilot_up 1")
}

// TestHandleMetricsRejectsNonLoopback pins that the unauthenticated /metrics
// endpoint is loopback-only: a LAN client (non-loopback RemoteAddr) gets 403 even
// when the fleet API is bound to a non-loopback address, so earnings/health/fleet
// data is never exposed to the network.
func TestHandleMetricsRejectsNonLoopback(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "192.168.1.50:54321"
	w := httptest.NewRecorder()
	app.handleMetrics(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-loopback /metrics scrape, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "cashpilot_up") {
		t.Fatalf("metrics must not be served to a non-loopback client:\n%s", w.Body.String())
	}
}

// TestFleetMuxGatesMetricsEndpoint pins the opt-in registration: the /metrics
// route exists only when metrics are enabled (404 otherwise), while the health
// route is always available.
func TestFleetMuxGatesMetricsEndpoint(t *testing.T) {
	app, _ := newEarningsTestApp(t, `{}`,
		`{"amount":1,"base":"USD","rates":{"EUR":0.9}}`, map[string]string{}, true)

	// Disabled: /metrics is not registered, so a scrape gets a 404.
	muxOff := app.fleetMux(false)
	recOff := httptest.NewRecorder()
	muxOff.ServeHTTP(recOff, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recOff.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for /metrics when metrics are disabled, got %d", recOff.Code)
	}

	// Enabled: the route exists and serves the exposition format.
	muxOn := app.fleetMux(true)
	recOn := httptest.NewRecorder()
	reqOn := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reqOn.RemoteAddr = "127.0.0.1:12345"
	muxOn.ServeHTTP(recOn, reqOn)
	if recOn.Code != http.StatusOK {
		t.Fatalf("expected 200 for /metrics when metrics are enabled, got %d", recOn.Code)
	}
	if ct := recOn.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("unexpected content-type: %q", ct)
	}

	// The health route is unaffected by the metrics flag.
	recHealth := httptest.NewRecorder()
	muxOff.ServeHTTP(recHealth, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if recHealth.Code != http.StatusOK {
		t.Fatalf("expected /api/health to remain 200 with metrics disabled, got %d", recHealth.Code)
	}
}

func mustContain(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("expected metrics body to contain %q, got:\n%s", want, body)
	}
}
