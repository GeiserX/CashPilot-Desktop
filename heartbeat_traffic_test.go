package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/stall"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// noHeartbeatReads stands in for the runtime in tests: nothing can be read, so a
// heartbeat keeps the stored copies, as when Docker is down.
func noHeartbeatReads(*App) (func(context.Context) ([]runtime.ContainerInfo, error), func(context.Context) ([]runtime.ProxiedCopy, error)) {
	fail := errors.New("no runtime in tests")
	return func(context.Context) ([]runtime.ContainerInfo, error) { return nil, fail },
		func(context.Context) ([]runtime.ProxiedCopy, error) { return nil, fail }
}

func u64(v uint64) *uint64 { return &v }

// fullID is a 64-character Docker id whose short id is prefix (12 characters).
func fullID(prefix string) string { return prefix + strings.Repeat("0", 64-len(prefix)) }

// heartbeatJSON is the payload as the server reads it: plain JSON objects.
func heartbeatJSON(t *testing.T, p upstream.Payload) map[string]any {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func liveListing() []runtime.ContainerInfo {
	return []runtime.ContainerInfo{
		{Slug: "honeygain", Name: "cashpilot-honeygain", ContainerID: fullID("aaaaaaaaaaaa"), Status: "running", Instance: 1,
			CPUPercent: 1.5, CPUKnown: true, MemoryMB: 42, MemoryKnown: true, NetRxBytes: u64(9_000_000), NetTxBytes: u64(123_456_789)},
		{Slug: "honeygain", Name: "cashpilot-honeygain-2", ContainerID: fullID("bbbbbbbbbbbb"), Status: "running", Instance: 2,
			CPUKnown: false, NetRxBytes: u64(1_000), NetTxBytes: u64(2_000)},
		// Stopped: Docker has no stats for it, so no counters.
		{Slug: "honeygain", Name: "cashpilot-honeygain-3", ContainerID: fullID("cccccccccccc"), Status: "exited", Instance: 3},
	}
}

func liveProxied() []runtime.ProxiedCopy {
	return []runtime.ProxiedCopy{{
		Slug: "honeygain", ProxyID: 4, Name: "cashpilot-honeygain-p4", Status: "running", ContainerID: fullID("dddddddddddd"),
		TunnelStatus: "running", TunnelContainerID: fullID("eeeeeeeeeeee"), TunnelStartedAt: "2026-11-25T10:00:00Z",
		NetRxBytes: u64(5_000_000), NetTxBytes: u64(77_000_000), ExitIP: "203.0.113.9", ExitCountry: "DE",
	}}
}

// The heartbeat carries every copy, with the web worker's per-container counters
// (net_rx_bytes and net_tx_bytes, cumulative bytes, keyed by the 12-character
// container_id), a stopped copy as "exited" with null counters, and the proxied
// copies apart under system_info.proxied with their tunnel's counters.
func TestHeartbeatCarriesTheWebWorkersTrafficCounters(t *testing.T) {
	app := newPayloadTestApp(t, []store.Deployment{{Slug: "honeygain", Name: "cashpilot-honeygain", Status: "running"}})
	p := app.upstreamPayload()
	release := app.withLiveUnits(t.Context(), &p,
		func(context.Context) ([]runtime.ContainerInfo, error) { return liveListing(), nil },
		func(context.Context) ([]runtime.ProxiedCopy, error) { return liveProxied(), nil })
	release()

	body := heartbeatJSON(t, p)
	containers, _ := body["containers"].([]any)
	if len(containers) != 3 {
		t.Fatalf("containers = %v, want all three copies", body["containers"])
	}
	first := containers[0].(map[string]any)
	if first["container_id"] != "aaaaaaaaaaaa" || first["name"] != "cashpilot-honeygain" || first["slug"] != "honeygain" ||
		first["status"] != "running" || first["instance"] != float64(1) {
		t.Fatalf("copy 1 = %v", first)
	}
	if first["net_tx_bytes"] != float64(123_456_789) || first["net_rx_bytes"] != float64(9_000_000) {
		t.Fatalf("copy 1 counters = %v / %v, want the cumulative bytes", first["net_rx_bytes"], first["net_tx_bytes"])
	}
	if first["cpu_percent"] != 1.5 || first["memory_mb"] != float64(42) {
		t.Fatalf("copy 1 cpu/memory = %v / %v", first["cpu_percent"], first["memory_mb"])
	}
	second := containers[1].(map[string]any)
	if second["instance"] != float64(2) || second["net_tx_bytes"] != float64(2_000) {
		t.Fatalf("copy 2 = %v, want instance 2 with its counters", second)
	}
	if v, present := second["cpu_percent"]; !present || v != nil {
		t.Fatalf("copy 2 cpu_percent = %v (present %v), want null for a figure not read", v, present)
	}
	stopped := containers[2].(map[string]any)
	if stopped["status"] != "exited" {
		t.Fatalf("stopped copy status = %v, want exited", stopped["status"])
	}
	for _, k := range []string{"net_rx_bytes", "net_tx_bytes"} {
		if v, present := stopped[k]; !present || v != nil {
			t.Fatalf("stopped copy %s = %v (present %v), want null as the web worker sends it", k, v, present)
		}
	}
	for _, c := range containers {
		if c.(map[string]any)["name"] == "cashpilot-honeygain-p4" {
			t.Fatal("the proxied copy is in containers; the web worker keeps it out")
		}
	}

	info := body["system_info"].(map[string]any)
	proxied, _ := info["proxied"].([]any)
	if len(proxied) != 1 {
		t.Fatalf("system_info.proxied = %v, want the proxied copy", info["proxied"])
	}
	pc := proxied[0].(map[string]any)
	// This computer's proxy ids are its own: a server reads proxy_id as one of its
	// proxies, so it would count, cap and stop this copy as that proxy's.
	if v, present := pc["proxy_id"]; present {
		t.Fatalf("proxied copy proxy_id = %v, want none: the server reads it as its own proxy's id", v)
	}
	if pc["slug"] != "honeygain" || pc["name"] != "cashpilot-honeygain-p4" ||
		pc["tunnel_container_id"] != "eeeeeeeeeeee" || pc["container_id"] != "dddddddddddd" ||
		pc["net_tx_bytes"] != float64(77_000_000) || pc["net_rx_bytes"] != float64(5_000_000) || pc["exit_ip"] != "203.0.113.9" {
		t.Fatalf("proxied copy = %v, want the tunnel's counters in the web worker's fields", pc)
	}
}

// A list that fails keeps the stored copies, without counters; proxied copies that
// cannot be read go out as null, never as an empty list that reads as none.
func TestHeartbeatKeepsWhatItHadWhenTheReadFails(t *testing.T) {
	app := newPayloadTestApp(t, []store.Deployment{{Slug: "honeygain", Name: "cashpilot-honeygain", Status: "running"}})
	p := app.upstreamPayload()
	app.withLiveUnits(t.Context(), &p,
		func(context.Context) ([]runtime.ContainerInfo, error) { return nil, errors.New("docker is down") },
		func(context.Context) ([]runtime.ProxiedCopy, error) { return nil, errors.New("docker is down") })()
	body := heartbeatJSON(t, p)
	containers, _ := body["containers"].([]any)
	if len(containers) != 1 || containers[0].(map[string]any)["net_tx_bytes"] != nil {
		t.Fatalf("containers = %v, want the stored copy without counters", body["containers"])
	}
	info := body["system_info"].(map[string]any)
	if v, present := info["proxied"]; !present || v != nil {
		t.Fatalf("proxied = %v (present %v), want null when it could not be read", v, present)
	}
	if v, present := info["containers_listed"]; !present || v != false {
		t.Fatalf("containers_listed = %v (present %v), want false when only the stored copies went out", v, present)
	}

	// No proxied copy at all is an empty list.
	p = app.upstreamPayload()
	app.withLiveUnits(t.Context(), &p,
		func(context.Context) ([]runtime.ContainerInfo, error) { return nil, nil },
		func(context.Context) ([]runtime.ProxiedCopy, error) { return nil, nil })()
	if got, _ := heartbeatJSON(t, p)["system_info"].(map[string]any)["proxied"].([]any); got == nil || len(got) != 0 {
		t.Fatalf("proxied = %v, want an empty list", got)
	}
}

// While the other process (the window or the helper) holds its turn, this one
// reads nothing and sends no counters, so the server never gets an older reading
// after a newer one and counts a whole counter again.
func TestHeartbeatSendsNoCountersOutOfTurn(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	old := heartbeatLockWait
	heartbeatLockWait = 200 * time.Millisecond
	t.Cleanup(func() { heartbeatLockWait = old })

	hold, ok := upstream.SendLock(t.Context(), app.heartbeatLockPath(), time.Second)
	if !ok {
		t.Fatal("could not take the lock to hold it")
	}
	read := false
	p := app.upstreamPayload()
	app.withLiveUnits(t.Context(), &p,
		func(context.Context) ([]runtime.ContainerInfo, error) { read = true; return liveListing(), nil },
		func(context.Context) ([]runtime.ProxiedCopy, error) { read = true; return liveProxied(), nil })()
	if read {
		t.Fatal("read the copies while the other process held its turn")
	}
	if len(p.Containers) != 0 || p.SystemInfo.Proxied != nil {
		t.Fatalf("payload = %+v, want no reading", p)
	}
	if p.SystemInfo.ContainersListed == nil || *p.SystemInfo.ContainersListed {
		t.Fatal("containers_listed is not false for a heartbeat that read nothing")
	}

	hold()
	app.withLiveUnits(t.Context(), &p,
		func(context.Context) ([]runtime.ContainerInfo, error) { read = true; return liveListing(), nil },
		func(context.Context) ([]runtime.ProxiedCopy, error) { return liveProxied(), nil })()
	if !read || len(p.Containers) != 3 {
		t.Fatalf("after the turn came back: read %v, containers %d; want the reading", read, len(p.Containers))
	}
	if p.SystemInfo.ContainersListed == nil || !*p.SystemInfo.ContainersListed {
		t.Fatal("containers_listed is not true for a whole live list")
	}
}

// The server's answer says whether it judges this computer's copies for stalls: a
// v2.10.0 server answers with fleet_exits (a list or null), an older one without.
func TestTheServersAnswerSaysWhetherItJudgesStalls(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		want        bool
	}{
		{"v2.10.0 with a list", `{"status":"ok","worker_id":2,"fleet_exits":["198.51.100.7"]}`, true},
		{"v2.10.0 that could not read its fleet", `{"status":"ok","worker_id":2,"fleet_exits":null}`, true},
		{"older server", `{"status":"ok","worker_id":2}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.reply))
			}))
			defer srv.Close()
			app := newPayloadTestApp(t, nil)
			app.services = nil // no runtime read in this test
			if err := app.cfg.Update(func(c *config.AppConfig) { c.Mode = config.ModeFollower; c.UpstreamURL = srv.URL }); err != nil {
				t.Fatal(err)
			}
			saveTestWorkerKey(t, app, "own-key")
			app.sendUpstream(t.Context(), &upstream.Client{HTTP: srv.Client()}, srv.URL, "shared")
			if got := app.stallServer(time.Now()) != ""; got != tc.want {
				t.Fatalf("server judges stalls = %v, want %v", got, tc.want)
			}
		})
	}
}

// The proxied copies become units for the usage rows: their counters are the
// tunnel's, keyed by the tunnel's id, so two readings give the tunnel's traffic.
func TestProxiedCopiesGetUsageRowsFromTheirTunnel(t *testing.T) {
	a := newStallApp(t)
	units := proxiedUnits(liveProxied())
	if len(units) != 1 || units[0].Name != "cashpilot-honeygain-p4" || units[0].ContainerID != fullID("eeeeeeeeeeee") || units[0].ProxyID != 4 {
		t.Fatalf("units = %+v", units)
	}
	reading := func(tx uint64) func(context.Context) ([]runtime.ContainerInfo, error) {
		return func(context.Context) ([]runtime.ContainerInfo, error) {
			cp := liveProxied()
			cp[0].NetTxBytes = u64(tx)
			return proxiedUnits(cp), nil
		}
	}
	a.sampleUsage(t.Context(), reading(77_000_000))
	a.sampleUsage(t.Context(), reading(80_000_000))
	rows, err := a.store.UsageHours(0)
	if err != nil {
		t.Fatal(err)
	}
	var tx int64
	found := false
	for _, r := range rows {
		if r.Container == "cashpilot-honeygain-p4" && r.TxBytes != nil {
			tx += *r.TxBytes
			found = true
		}
	}
	if !found || tx != 3_000_000 {
		t.Fatalf("rows = %+v, want 3 MB sent by the proxied copy", rows)
	}
}

// sendUpstream puts the live reading in the heartbeat the server receives, and
// gives up its turn once the server answered, so the next heartbeat (or the other
// process's) can take one.
func TestSendUpstreamSendsTheCountersToTheServer(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"status":"ok","worker_id":2,"fleet_exits":[]}`))
	}))
	defer srv.Close()
	old := heartbeatReads
	heartbeatReads = func(*App) (func(context.Context) ([]runtime.ContainerInfo, error), func(context.Context) ([]runtime.ProxiedCopy, error)) {
		return func(context.Context) ([]runtime.ContainerInfo, error) { return liveListing(), nil },
			func(context.Context) ([]runtime.ProxiedCopy, error) { return liveProxied(), nil }
	}
	t.Cleanup(func() { heartbeatReads = old })
	oldWait := heartbeatLockWait
	heartbeatLockWait = 200 * time.Millisecond
	t.Cleanup(func() { heartbeatLockWait = oldWait })

	// Count the turns given back: a file left unreleased can still be closed by the
	// garbage collector, so the lock alone would catch a missed release only by luck.
	oldLock := heartbeatSendLock
	taken, given := 0, 0
	heartbeatSendLock = func(ctx context.Context, path string, wait time.Duration) (func(), bool) {
		release, ok := oldLock(ctx, path, wait)
		if ok {
			taken++
		}
		return func() { given++; release() }, ok
	}
	t.Cleanup(func() { heartbeatSendLock = oldLock })

	app := newPayloadTestApp(t, nil)
	publishTestPairing(t, app, srv.URL, "own-key")
	client := &upstream.Client{HTTP: srv.Client()}
	app.sendUpstream(t.Context(), client, srv.URL, "shared")
	if taken != 1 || given != 1 {
		t.Fatalf("turns taken %d, given back %d; want the turn given back once the server answered", taken, given)
	}
	// The turn is free the moment the send returned: the other process can take it.
	free, ok := upstream.SendLock(t.Context(), app.heartbeatLockPath(), 0)
	if !ok {
		t.Fatal("sendUpstream kept its turn after the server answered")
	}
	free()
	app.sendUpstream(t.Context(), client, srv.URL, "shared")
	if len(bodies) != 2 {
		t.Fatalf("server got %d heartbeats, want 2", len(bodies))
	}
	for i, body := range bodies {
		containers, _ := body["containers"].([]any)
		if len(containers) != 3 || containers[0].(map[string]any)["net_tx_bytes"] != float64(123_456_789) {
			t.Fatalf("heartbeat %d containers = %v, want every copy with its counters", i+1, body["containers"])
		}
		if proxied, _ := body["system_info"].(map[string]any)["proxied"].([]any); len(proxied) != 1 {
			t.Fatalf("heartbeat %d proxied = %v, want the proxied copy", i+1, proxied)
		}
	}
}

// The usage sampler's reading has the proxied copies beside the service's own
// copies, and a proxied read that fails fails the whole reading, so a quiet proxied
// copy's alert is never cleared as gone while it may still run. Through the
// sampler, twelve quiet hours of a proxied copy raise its alert.
func TestTheUsageReadingHasTheProxiedCopies(t *testing.T) {
	ok := func(context.Context) ([]runtime.ContainerInfo, error) { return listing("honeygain", 1, nil), nil }
	units, err := withProxiedUnits(ok, func(context.Context) ([]runtime.ProxiedCopy, error) { return liveProxied(), nil })(t.Context())
	if err != nil || len(units) != 2 || units[1].ProxyID != 4 || units[1].Name != "cashpilot-honeygain-p4" {
		t.Fatalf("units = %+v, %v; want copy 1 and the proxied copy", units, err)
	}
	if _, err := withProxiedUnits(ok, func(context.Context) ([]runtime.ProxiedCopy, error) {
		return nil, errors.New("docker is down")
	})(t.Context()); err == nil {
		t.Fatal("a failed proxied read gave a reading")
	}

	a := newStallApp(t)
	now := time.Now()
	sent(t, a, "honeygain", 1, now, 12, 30_000_000)
	last := stall.LastWholeHour(now.Unix())
	var rows []store.UsageHour
	for i := 0; i < 12; i++ {
		tx := int64(0)
		rows = append(rows, store.UsageHour{Container: "cashpilot-honeygain-p4", Slug: "honeygain", Hour: last - 3600*int64(i), Beats: 12, TxBytes: &tx, NetSeconds: 3600})
	}
	if err := a.store.RecordUsage(func(map[string]store.UsageBaseline) ([]store.UsageHour, map[string]store.UsageBaseline) {
		return rows, nil
	}); err != nil {
		t.Fatal(err)
	}
	a.sampleUsage(t.Context(), withProxiedUnits(ok, func(context.Context) ([]runtime.ProxiedCopy, error) { return liveProxied(), nil }))
	if got := a.currentStalls(); len(got) != 1 || got[0].ProxyID != 4 {
		t.Fatalf("stalls = %+v, want the quiet proxied copy", got)
	}
}
