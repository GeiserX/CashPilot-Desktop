package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// handleMetrics serves the opt-in Prometheus /metrics endpoint. It is registered
// on the fleet mux only when MetricsEnabled is set (see fleetMux), so when the
// feature is off the route does not exist (404). It is deliberately
// UNAUTHENTICATED, per the Prometheus scraping convention, so it is restricted to
// LOOPBACK callers regardless of the fleet bind address: the rest of the fleet API
// is token-gated, but this endpoint is not, so a MetricsEnabled + FleetBindAddress
// 0.0.0.0 configuration must not let a LAN client read earnings/health/fleet data.
// It never panics — every data source is nil-guarded and the handler emits whatever
// it can.
func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !requestFromLoopback(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "metrics are available on loopback only"})
		return
	}
	// Prometheus text exposition format, version 0.0.4.
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = io.WriteString(w, a.renderMetrics())
}

// requestFromLoopback reports whether an HTTP request originated from the local
// machine (127.0.0.0/8 or ::1). It keeps the unauthenticated /metrics endpoint
// loopback-only even when the fleet API binds to a LAN address. A RemoteAddr that
// does not parse fails closed (treated as non-loopback).
func requestFromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// renderMetrics builds the full Prometheus text exposition. It is split from the
// HTTP handler so it can be unit-tested as a pure function. Every family is
// emitted as a contiguous block (# HELP, # TYPE, then its samples) so parsers
// never see interleaved or duplicated families. All metrics are gauges.
func (a *App) renderMetrics() string {
	mw := &metricsWriter{}

	// -- Liveness / build --
	mw.family("cashpilot_up", "Whether the CashPilot Desktop metrics endpoint is serving (always 1).")
	mw.sample("cashpilot_up", 1)

	// -- Collection cadence --
	mw.family("cashpilot_collect_interval_minutes", "Configured minutes between background earnings collection runs.")
	mw.sample("cashpilot_collect_interval_minutes", a.collectInterval().Minutes())

	// -- Earnings summary --
	// Reuse the SAME accrual + FX computation the dashboard/GetAppState uses so the
	// exposed figures match the app exactly and the complex per-platform cumulative
	// delta math is never duplicated. Values are in the configured display currency
	// (USD by default); computeEarningsSummary is internally nil-guarded and returns
	// an empty (all-zero) summary when its dependencies are not wired.
	var summary EarningsSummary
	if a.store != nil {
		summary = a.computeEarningsSummary(a.store.ListLatestEarnings())
	} else {
		summary = a.computeEarningsSummary(nil)
	}
	mw.family("cashpilot_earnings_usd_total", "Total earnings across all platforms, in the display currency (USD by default).")
	mw.sample("cashpilot_earnings_usd_total", summary.Total)
	mw.family("cashpilot_earnings_usd_today", "Earnings accrued today, in the display currency (USD by default).")
	mw.sample("cashpilot_earnings_usd_today", summary.Today)
	mw.family("cashpilot_earnings_usd_month", "Earnings accrued this month, in the display currency (USD by default).")
	mw.sample("cashpilot_earnings_usd_month", summary.Month)

	if a.store != nil {
		a.renderStoreMetrics(mw)
	}

	return mw.String()
}

// renderStoreMetrics emits the per-service and fleet gauges that read directly
// from the store. The caller guarantees a.store is non-nil. Store list methods are
// best-effort (they return nil on error), so a query failure simply yields an
// empty family rather than an error.
func (a *App) renderStoreMetrics(mw *metricsWriter) {
	// -- Per-platform latest balance / error --
	// ListLatestEarnings includes error rows. A platform whose latest run failed is
	// surfaced as an explicit error flag rather than a bogus zero balance.
	latest := a.store.ListLatestEarnings()
	mw.family("cashpilot_service_balance", "Latest known balance per platform, in the platform's native currency.")
	for _, rec := range latest {
		if rec.Error != "" {
			continue
		}
		mw.sample("cashpilot_service_balance", rec.Balance,
			label{"platform", rec.Platform}, label{"currency", rec.Currency})
	}
	mw.family("cashpilot_service_error", "Set to 1 for a platform whose latest collection attempt failed.")
	for _, rec := range latest {
		if rec.Error == "" {
			continue
		}
		mw.sample("cashpilot_service_error", 1, label{"platform", rec.Platform})
	}

	// -- Per-service health --
	// HealthScores returns a map; sort the slugs so the output is deterministic
	// (nice for scrape diffs and tests).
	scores := a.store.HealthScores(7)
	slugs := sortedKeys(scores)
	mw.family("cashpilot_service_health_score", "Rolling health score per service over the last 7 days (0-100).")
	for _, slug := range slugs {
		mw.sample("cashpilot_service_health_score", float64(scores[slug].Score), label{"slug", slug})
	}
	// Uptime% is only meaningful once there is at least one health sample; a service
	// with no samples yet reports 0, which would be a misleading "0% uptime". Mirror
	// the production "uptime is not None" guard and skip those.
	mw.family("cashpilot_service_uptime_percent", "Uptime percentage per service over the last 7 days.")
	for _, slug := range slugs {
		hs := scores[slug]
		if hs.Samples == 0 {
			continue
		}
		mw.sample("cashpilot_service_uptime_percent", hs.UptimePercent, label{"slug", slug})
	}

	// -- Fleet devices --
	// Count by LIVE status: EffectiveFleetStatus applies the offline grace on the
	// read path exactly like GetFleetState, so a device silent past the grace counts
	// as offline immediately (without waiting for the next scheduler sweep).
	devices := a.store.ListFleetDevices()
	online, offline := 0, 0
	for _, d := range devices {
		if store.EffectiveFleetStatus(d.Status, d.LastSeen, fleetOfflineAfter) == "online" {
			online++
		} else {
			offline++
		}
	}
	mw.family("cashpilot_fleet_devices", "Number of fleet worker/mobile devices by live status.")
	mw.sample("cashpilot_fleet_devices", float64(online), label{"status", "online"})
	mw.sample("cashpilot_fleet_devices", float64(offline), label{"status", "offline"})

	// Per-device heartbeat staleness. Devices whose last_seen is a non-timestamp
	// placeholder (e.g. AddFleetDevice's "not connected yet") do not parse as
	// RFC3339 and are skipped.
	mw.family("cashpilot_fleet_device_last_seen_seconds", "Seconds since a fleet device last checked in.")
	for _, d := range devices {
		seen, err := time.Parse(time.RFC3339, d.LastSeen)
		if err != nil {
			continue
		}
		mw.sample("cashpilot_fleet_device_last_seen_seconds", time.Since(seen).Seconds(), label{"device", d.Name})
	}
}

// label is one ordered key/value pair for a metric sample. Label names are always
// hardcoded (and therefore valid); label values are escaped at write time.
type label struct {
	name  string
	value string
}

// metricsWriter accumulates a Prometheus text exposition into a strings.Builder.
type metricsWriter struct {
	b strings.Builder
}

// family writes the "# HELP" and "# TYPE ... gauge" header lines for one metric
// family. help text is hardcoded ASCII (no backslashes or newlines) so it needs
// no escaping.
func (w *metricsWriter) family(name, help string) {
	fmt.Fprintf(&w.b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(&w.b, "# TYPE %s gauge\n", name)
}

// sample writes one gauge sample line: name{label="value",...} value. Label
// values are escaped per the exposition format.
func (w *metricsWriter) sample(name string, value float64, labels ...label) {
	w.b.WriteString(name)
	if len(labels) > 0 {
		w.b.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				w.b.WriteByte(',')
			}
			w.b.WriteString(l.name)
			w.b.WriteString(`="`)
			w.b.WriteString(escapeLabelValue(l.value))
			w.b.WriteByte('"')
		}
		w.b.WriteByte('}')
	}
	w.b.WriteByte(' ')
	w.b.WriteString(formatFloat(value))
	w.b.WriteByte('\n')
}

func (w *metricsWriter) String() string {
	return w.b.String()
}

// labelEscaper escapes the three characters the Prometheus text format requires
// to be escaped inside a label value: backslash, double-quote and newline.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabelValue(v string) string {
	return labelEscaper.Replace(v)
}

// formatFloat renders a metric value. 'g' with -1 precision emits integers
// cleanly (1 -> "1", 100 -> "100") and keeps full precision for fractions, and it
// produces the valid Prometheus tokens NaN/+Inf/-Inf for those special values.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// sortedKeys returns a map's string keys in sorted order, for deterministic output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
