package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// A heartbeat shaped like the Android client's is a "mobile" device, and a
// mobile device silent for 20 minutes (one missed 15-minute report) still shows
// online. A worker silent for 5 minutes shows offline, so the grace is per kind
// and not simply longer for everyone.
func TestMobileDeviceShowsOnlineBetweenReports(t *testing.T) {
	var body workerHeartbeat
	raw := `{"name":"Pixel 8","client_id":"c1","system_info":{"os":"Android","device_type":"android"}}`
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	kind := heartbeatKind(body)
	if kind != "mobile" {
		t.Fatalf("kind = %q, want mobile", kind)
	}
	ago := func(d time.Duration) string { return time.Now().UTC().Add(-d).Format(time.RFC3339) }

	if got := store.EffectiveFleetStatus("online", ago(20*time.Minute), fleetOfflineAfterFor(kind)); got != "online" {
		t.Errorf("phone silent 20 min: %q, want online", got)
	}
	if got := store.EffectiveFleetStatus("online", ago(50*time.Minute), fleetOfflineAfterFor(kind)); got != "offline" {
		t.Errorf("phone silent 50 min: %q, want offline", got)
	}
	if got := store.EffectiveFleetStatus("online", ago(5*time.Minute), fleetOfflineAfterFor("worker")); got != "offline" {
		t.Errorf("worker silent 5 min: %q, want offline", got)
	}
}
