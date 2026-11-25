package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// The owner's other internet lines (web GeiserX/CashPilot#810): every box's fence
// drops the public IPs of all the others, so an earner on one line cannot reach the
// port forwards of another. Desktop takes part in both directions: as a worker of a
// CashPilot server, and as a hub that workers report to.

// heartbeatAs posts one worker heartbeat to the hub and returns the decoded reply.
func heartbeatAs(t *testing.T, app *App, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	app.handleWorkerHeartbeat(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat %s: %d %s", body, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("reply: %v", err)
	}
	return out
}

func exitsOf(t *testing.T, reply map[string]any) []string {
	t.Helper()
	raw, ok := reply["fleet_exits"]
	if !ok {
		t.Fatalf("the reply carries no fleet_exits: %v", reply)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("fleet_exits is %T (%v), want a list", raw, raw)
	}
	var out []string
	for _, v := range list {
		out = append(out, v.(string))
	}
	return out
}

// THE RULE: a worker reporting to this Desktop gets back the public IPv4 of every
// box that reports here and of this Desktop itself. A phone neither gets the list
// nor adds its carrier's address to it, and a heartbeat without an address keeps
// the last one, since the line is the same line.
func TestTheHubSendsEveryBoxsExitBack(t *testing.T) {
	app := newFleetTestApp(t, "secret-token")
	app.fence = &fakeFence{egress: "203.0.113.1"}

	reply := heartbeatAs(t, app, `{"name":"a","system_info":{"os":"linux","egress_ip":"198.51.100.10"}}`)
	if got := exitsOf(t, reply); !slices.Equal(got, []string{"198.51.100.10", "203.0.113.1"}) {
		t.Errorf("first worker got %v", got)
	}

	// A phone on mobile data: its address is its carrier's.
	phone := heartbeatAs(t, app, `{"name":"pixel","system_info":{"os":"Android","egress_ip":"192.0.0.99"}}`)
	if _, ok := phone["fleet_exits"]; ok {
		t.Errorf("a phone got fleet_exits: %v", phone)
	}

	// A private, CGNAT or IPv6 address is never handed on.
	heartbeatAs(t, app, `{"name":"b","system_info":{"os":"linux","egress_ip":"100.64.0.7"}}`)
	heartbeatAs(t, app, `{"name":"c","system_info":{"os":"linux","egress_ip":"2001:db8::1"}}`)
	reply = heartbeatAs(t, app, `{"name":"d","system_info":{"os":"linux","egress_ip":"192.0.2.44"}}`)
	if got := exitsOf(t, reply); !slices.Equal(got, []string{"192.0.2.44", "198.51.100.10", "203.0.113.1"}) {
		t.Errorf("fleet_exits %v", got)
	}
	reply = heartbeatAs(t, app, `{"name":"a","system_info":{"os":"linux","egress_ip":123}}`)
	if got := exitsOf(t, reply); !slices.Contains(got, "198.51.100.10") {
		t.Errorf("an invalid address cleared the last known one: %v", got)
	}

	// Worker a's lookup failed this time: its line stays dropped.
	reply = heartbeatAs(t, app, `{"name":"a","system_info":{"os":"linux"}}`)
	if got := exitsOf(t, reply); !slices.Contains(got, "198.51.100.10") {
		t.Errorf("a heartbeat without an address lost the last one: %v", got)
	}
	// And a new address replaces the old one.
	reply = heartbeatAs(t, app, `{"name":"a","system_info":{"os":"linux","egress_ip":"198.51.100.11"}}`)
	if got := exitsOf(t, reply); slices.Contains(got, "198.51.100.10") || !slices.Contains(got, "198.51.100.11") {
		t.Errorf("after a's new address: %v", got)
	}
}

// With no exit known anywhere the list is empty, never null: null tells a web worker
// that the hub could not read its fleet, and it would keep stale drops for good.
func TestTheHubSendsAnEmptyListNotNull(t *testing.T) {
	app := newFleetTestApp(t, "secret-token")
	reply := heartbeatAs(t, app, `{"name":"a","system_info":{"os":"linux"}}`)
	if got := exitsOf(t, reply); len(got) != 0 {
		t.Errorf("fleet_exits %v, want []", got)
	}
}

func TestHubFencesAWorkerWithAPhoneLikeName(t *testing.T) {
	app := newFleetTestApp(t, "secret-token")
	heartbeatAs(t, app, `{"name":"android-builder","system_info":{"os":"Linux","egress_ip":"198.51.100.40"}}`)
	phone := heartbeatAs(t, app, `{"name":"pixel","system_info":{"device_type":"android","egress_ip":"192.0.2.44"}}`)
	if _, ok := phone["fleet_exits"]; ok {
		t.Error("the phone got a fleet list")
	}
	reply := heartbeatAs(t, app, `{"name":"second-worker","system_info":{"os":"Linux"}}`)
	if got := exitsOf(t, reply); !slices.Equal(got, []string{"198.51.100.40"}) {
		t.Errorf("fleet exits = %v, want the Linux worker's address", got)
	}
}

type egressLookupFence struct {
	fakeFence
	lookup func() string
}

func (f *egressLookupFence) EgressIP(context.Context) string { return f.lookup() }

func TestHubOnlyLooksUpItsExitForAuthenticatedWorkers(t *testing.T) {
	app := newFleetTestApp(t, "secret-token")
	app.fence = &egressLookupFence{lookup: func() string {
		t.Error("a rejected heartbeat triggered a hub egress lookup")
		return ""
	}}
	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", strings.NewReader(`{"name":"worker"}`))
	w := httptest.NewRecorder()
	app.handleWorkerHeartbeat(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("heartbeat status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// THE RULE: as a worker, Desktop reports its own exit, takes the fleet's exits from
// the reply and has the fence applied again when they change. A null list keeps what
// it holds; so does a reply without the key.
func TestPairedDesktopReportsItsExitAndTakesTheFleets(t *testing.T) {
	var mu sync.Mutex
	reply := `{"status":"ok","worker_id":2,"fleet_exits":["198.51.100.10","203.0.113.1","10.0.0.4"]}`
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		mu.Lock()
		sent = append(sent, got)
		out := reply
		mu.Unlock()
		_, _ = w.Write([]byte(out))
	}))
	defer srv.Close()

	app := newPayloadTestApp(t, nil)
	fence := &fakeFence{egress: "203.0.113.1"}
	app.fence = fence
	// The pairing loop is running: until the server answers, the list is unknown.
	app.upstream.cancel = func() {}
	setFleetUpstreamURL(t, app, srv.URL)
	if _, complete := app.fenceOtherExits(); complete {
		t.Fatal("before the first reply the fleet's exits read as complete")
	}
	client := &upstream.Client{HTTP: srv.Client()}

	app.sendUpstream(context.Background(), client, srv.URL, "shared")
	info, _ := sent[0]["system_info"].(map[string]any)
	if info["egress_ip"] != "203.0.113.1" {
		t.Errorf("the heartbeat did not carry this machine's exit: %v", info)
	}
	ips, complete := app.fenceOtherExits()
	if !complete || !slices.Equal(ips, []string{"198.51.100.10", "203.0.113.1"}) {
		t.Errorf("other exits %v complete %v", ips, complete)
	}
	if fence.reconciled != 1 {
		t.Errorf("the fence was applied %d times after the list arrived, want 1", fence.reconciled)
	}

	// The same list again: nothing to apply.
	app.sendUpstream(context.Background(), client, srv.URL, "shared")
	if fence.reconciled != 1 {
		t.Errorf("an unchanged list applied the fence again (%d)", fence.reconciled)
	}

	// null: the server could not read its fleet. The list stays.
	mu.Lock()
	reply = `{"status":"ok","worker_id":2,"fleet_exits":null}`
	mu.Unlock()
	app.sendUpstream(context.Background(), client, srv.URL, "shared")
	if ips, complete := app.fenceOtherExits(); !complete || len(ips) != 2 || fence.reconciled != 1 {
		t.Errorf("after null: %v complete %v, applied %d", ips, complete, fence.reconciled)
	}

	// A line went away.
	mu.Lock()
	reply = `{"status":"ok","worker_id":2,"fleet_exits":["203.0.113.1"]}`
	mu.Unlock()
	app.sendUpstream(context.Background(), client, srv.URL, "shared")
	if ips, _ := app.fenceOtherExits(); !slices.Equal(ips, []string{"203.0.113.1"}) || fence.reconciled != 2 {
		t.Errorf("after a line left: %v, applied %d", ips, fence.reconciled)
	}

	// Unpairing forgets the server's list.
	setFleetUpstreamURL(t, app, "")
	app.stopUpstream()
	if ips, complete := app.fenceOtherExits(); len(ips) != 0 || !complete {
		t.Errorf("after unpairing: %v complete %v", ips, complete)
	}
	// Pairing again, maybe with another server, starts from unknown: the old
	// server's list is not this one's.
	setFleetUpstreamURL(t, app, srv.URL)
	app.upstream.cancel = func() {}
	if ips, complete := app.fenceOtherExits(); len(ips) != 0 || complete {
		t.Errorf("after pairing again: %v complete %v", ips, complete)
	}
}

// THE RULE (web worker_api._apply_fleet_exits_reply): a list is taken, null keeps what
// is held, and a reply with no fleet_exits key means no other exits only while nothing
// is held. Once a list is held, a missing key keeps it: only a UI rolled back to before
// the list sends that, and clearing would let every earner reach the owner's other lines.
func TestAReplyWithoutFleetExitsKeepsAHeldList(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	app.upstream.cancel = func() {}
	setFleetUpstreamURL(t, app, "https://hub.example")

	// Unknown, then no key: a server from before the list. No other exits, and the
	// list is complete, so a fence does not keep drops for good waiting on it.
	if !app.applyFleetExits(nil) {
		t.Error("the first reply without the key did not settle the list")
	}
	if ips, complete := app.fenceOtherExits(); len(ips) != 0 || !complete {
		t.Errorf("unknown then no key: other exits %v complete %v", ips, complete)
	}

	// A list is taken.
	if !app.applyFleetExits(json.RawMessage(`["198.51.100.4"]`)) {
		t.Error("a list did not replace the empty one")
	}
	if ips, complete := app.fenceOtherExits(); !slices.Equal(ips, []string{"198.51.100.4"}) || !complete {
		t.Errorf("after a list: other exits %v complete %v", ips, complete)
	}

	// A list, then no key: kept, and nothing to apply again.
	if app.applyFleetExits(nil) {
		t.Error("a reply without the key changed a held list")
	}
	if ips, complete := app.fenceOtherExits(); !slices.Equal(ips, []string{"198.51.100.4"}) || !complete {
		t.Errorf("after a reply without the key: other exits %v complete %v", ips, complete)
	}

	// A list, then null: kept.
	if app.applyFleetExits(json.RawMessage("null")) {
		t.Error("null changed a held list")
	}
	if ips, complete := app.fenceOtherExits(); !slices.Equal(ips, []string{"198.51.100.4"}) || !complete {
		t.Errorf("after null: other exits %v complete %v", ips, complete)
	}

	// Unpairing resets to unknown, so the next server's missing key settles an
	// empty list again rather than keeping the old server's.
	app.stopUpstream()
	app.upstream.cancel = func() {}
	if ips, complete := app.fenceOtherExits(); len(ips) != 0 || complete {
		t.Errorf("after unpairing: other exits %v complete %v", ips, complete)
	}
	if !app.applyFleetExits(nil) {
		t.Error("after unpairing, a reply without the key did not settle the list")
	}
	if ips, complete := app.fenceOtherExits(); len(ips) != 0 || !complete {
		t.Errorf("after unpairing then no key: other exits %v complete %v", ips, complete)
	}

	// null before any list: still unknown.
	fresh := newPayloadTestApp(t, nil)
	fresh.upstream.cancel = func() {}
	setFleetUpstreamURL(t, fresh, "https://hub.example")
	if fresh.applyFleetExits(json.RawMessage("null")) {
		t.Error("null settled the list")
	}
	if _, complete := fresh.fenceOtherExits(); complete {
		t.Error("null made the list complete")
	}
}

func setFleetUpstreamURL(t *testing.T, app *App, url string) {
	t.Helper()
	if err := app.cfg.Update(func(c *config.AppConfig) { c.UpstreamURL = url }); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredUpstreamKeepsFenceExitsWhileHeartbeatIsStopped(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	setFleetUpstreamURL(t, app, "https://hub.example")
	// A locked keychain prevents the loop from starting, but the configured fleet
	// still exists and may already be in the firewall table.
	if _, complete := app.fenceOtherExits(); complete {
		t.Fatal("a configured pairing with no heartbeat loop reported a complete list")
	}
}

func TestHeartbeatAppliesFleetExitsWhenTheCredentialStoreLocks(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	fence := &fakeFence{}
	app.fence = fence
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The signed-in account locked its credential store while the report
		// was in flight. The key generation is still readable.
		testKeyring.fail(errors.New("test keychain locked"))
		_, _ = io.WriteString(w, `{"status":"ok","worker_key":"issued","fleet_exits":["198.51.100.4"]}`)
	}))
	defer srv.Close()
	setFleetUpstreamURL(t, app, srv.URL)
	t.Cleanup(testKeyring.reset)

	app.sendUpstream(context.Background(), &upstream.Client{HTTP: srv.Client()}, srv.URL, "shared")
	if ips, complete := app.fenceOtherExits(); !slices.Equal(ips, []string{"198.51.100.4"}) || !complete || fence.reconciled != 1 {
		t.Errorf("exits %v complete %v, fence applies %d", ips, complete, fence.reconciled)
	}
}

// As a hub, this Desktop's own fence drops the exits of the workers that report here.
func TestTheHubsOwnFenceDropsItsWorkersExits(t *testing.T) {
	app := newFleetTestApp(t, "secret-token")
	heartbeatAs(t, app, `{"name":"a","system_info":{"os":"linux","egress_ip":"198.51.100.10"}}`)
	heartbeatAs(t, app, `{"name":"pixel","system_info":{"os":"Android","egress_ip":"192.0.0.99"}}`)
	ips, complete := app.fenceOtherExits()
	if !complete || !slices.Equal(ips, []string{"198.51.100.10"}) {
		t.Errorf("other exits %v complete %v", ips, complete)
	}
	// The phone's carrier address is not even kept.
	for _, d := range app.store.ListFleetDevices() {
		if d.Kind == "mobile" && d.EgressIP != "" {
			t.Errorf("a phone's address was stored: %q", d.EgressIP)
		}
	}
}
