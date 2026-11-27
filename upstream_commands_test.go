package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// commandsServer is a stand-in CashPilot that records the command URL of every
// heartbeat and lists the command feature in its answer only while modern is set,
// as web v3.0.0 does and web v2.10.x does not.
type commandsServer struct {
	*httptest.Server
	modern atomic.Bool
	// plan, when set, decides per heartbeat (1, 2, ...) whether the answer lists
	// the feature, in place of modern.
	plan func(n int) bool
	mu   sync.Mutex
	urls []string
}

func newCommandsServer(t *testing.T) *commandsServer {
	t.Helper()
	s := &commandsServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("heartbeat body: %v", err)
		}
		s.mu.Lock()
		s.urls = append(s.urls, body.URL)
		n := len(s.urls)
		s.mu.Unlock()
		reply := map[string]any{"status": "ok", "worker_id": 1, "fleet_exits": []string{}}
		modern := s.modern.Load()
		if s.plan != nil {
			modern = s.plan(n)
		}
		if modern {
			reply["features"] = []string{"something_else", upstream.CommandsFeature}
		}
		writeJSON(w, http.StatusOK, reply)
	}))
	t.Cleanup(s.Close)
	return s
}

// take returns the command URLs heartbeats sent since the last call.
func (s *commandsServer) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.urls
	s.urls = nil
	return out
}

func TestCommandURLGoesOnlyToAServerThatListsTheFeature(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	oldNeeded := firewallRuleNeeded
	t.Cleanup(func() { firewallRuleNeeded = oldNeeded })
	firewallRuleNeeded = false
	server := newCommandsServer(t)
	app := newAppUnderTest(t)
	t.Cleanup(app.stopUpstream)
	publishTestPairing(t, app.App, server.URL, testWorkerKey)
	app.syncWorkerAPI()
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	const commandURL = "http://192.168.1.30:8086"
	if got := app.workerAPIURL(server.URL); got != commandURL {
		t.Fatalf("worker API URL %q, want %q", got, commandURL)
	}
	client := &upstream.Client{HTTP: server.Client()}
	turn := func() []string {
		t.Helper()
		app.sendUpstreamLoop(t.Context(), client, server.URL)
		return server.take()
	}

	// An old server (web 2.10.x): no URL, ever, and the card says why.
	for i := range 2 {
		if got := turn(); !slices.Equal(got, []string{""}) {
			t.Fatalf("old server, turn %d: heartbeats sent URLs %q, want one without a URL", i, got)
		}
	}
	if state := app.serverCommandsState(); !state.ServerTooOld {
		t.Errorf("old server: card does not say the server is too old: %+v", state)
	}

	// The server is upgraded: the answer that first lists the feature brings a
	// second heartbeat at once, with the URL, and every later one carries it.
	server.modern.Store(true)
	if got := turn(); !slices.Equal(got, []string{"", commandURL}) {
		t.Fatalf("upgraded server, first turn: URLs %q, want none then %q", got, commandURL)
	}
	if got := turn(); !slices.Equal(got, []string{commandURL}) {
		t.Fatalf("upgraded server, next turn: URLs %q, want %q once", got, commandURL)
	}
	if state := app.serverCommandsState(); state.ServerTooOld {
		t.Errorf("upgraded server: card still says the server is too old: %+v", state)
	}

	// The server is rolled back: the heartbeat that learns it still carries the URL
	// (the server's last answer listed the feature), so a second heartbeat follows
	// at once without it, and none after it carries the URL.
	server.modern.Store(false)
	if got := turn(); !slices.Equal(got, []string{commandURL, ""}) {
		t.Fatalf("rolled-back server, first turn: URLs %q, want %q then none", got, commandURL)
	}
	for i := range 2 {
		if got := turn(); !slices.Equal(got, []string{""}) {
			t.Fatalf("rolled-back server, turn %d: URLs %q, want one without a URL", i, got)
		}
	}
	if state := app.serverCommandsState(); !state.ServerTooOld {
		t.Errorf("rolled-back server: card does not say the server is too old: %+v", state)
	}
}

func TestCommandFeatureIsForgottenWhenTheLoopRestarts(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	oldNeeded := firewallRuleNeeded
	t.Cleanup(func() { firewallRuleNeeded = oldNeeded })
	firewallRuleNeeded = false
	server := newCommandsServer(t)
	server.modern.Store(true)
	app := newAppUnderTest(t)
	t.Cleanup(app.stopUpstream)
	publishTestPairing(t, app.App, server.URL, testWorkerKey)
	app.syncWorkerAPI()
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	client := &upstream.Client{HTTP: server.Client()}
	app.sendUpstreamLoop(t.Context(), client, server.URL)
	if got := server.take(); len(got) != 2 || got[1] == "" {
		t.Fatalf("modern server: URLs %q, want none then the URL", got)
	}

	// A stop (a restart, a leave, another server) drops what the old answer said:
	// the next heartbeat asks again before it reports the URL.
	app.stopUpstream()
	if state := app.serverCommandsState(); state.ServerTooOld {
		t.Errorf("after a restart nothing is known, yet the card says too old: %+v", state)
	}
	server.modern.Store(false)
	app.sendUpstreamLoop(t.Context(), client, server.URL)
	if got := server.take(); !slices.Equal(got, []string{""}) {
		t.Fatalf("after a restart toward an old server: URLs %q, want one without a URL", got)
	}
}

// A server that lists the feature in one answer and withdraws it in the next
// (a rollback during the follow-up, or a mixed pool behind one address) must not
// keep the command URL until the next interval: the turn sends once more,
// without it. A server that flips on every answer gets a bounded number of
// heartbeats per turn.
func TestFollowUpThatLosesTheFeatureSendsAgainWithoutTheURL(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	oldNeeded := firewallRuleNeeded
	t.Cleanup(func() { firewallRuleNeeded = oldNeeded })
	firewallRuleNeeded = false
	server := newCommandsServer(t)
	app := newAppUnderTest(t)
	t.Cleanup(app.stopUpstream)
	publishTestPairing(t, app.App, server.URL, testWorkerKey)
	app.syncWorkerAPI()
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	const commandURL = "http://192.168.1.30:8086"
	client := &upstream.Client{HTTP: server.Client()}

	server.plan = func(n int) bool { return n == 1 }
	app.sendUpstreamLoop(t.Context(), client, server.URL)
	if got := server.take(); !slices.Equal(got, []string{"", commandURL, ""}) {
		t.Fatalf("feature listed then withdrawn: URLs %q, want none, the URL, then none", got)
	}

	// Listed, withdrawn, listed, withdrawn: the bound ends the turn on a
	// withdrawal, so one more heartbeat clears the URL the last one carried.
	server.plan = func(n int) bool { return n%2 == 1 }
	app.sendUpstreamLoop(t.Context(), client, server.URL)
	got := server.take()
	if len(got) != 2+maxHeartbeatFollowUps {
		t.Fatalf("feature flips on every answer: %d heartbeats in one turn, want %d", len(got), 2+maxHeartbeatFollowUps)
	}
	if !slices.Equal(got, []string{"", commandURL, "", commandURL, ""}) {
		t.Fatalf("feature flips on every answer: URLs %q, want the last one without the URL", got)
	}

	// Withdrawn, listed, withdrawn, listed: the bound ends on a listing, and the
	// URL the server now lists may stay, so the turn stops at the bound.
	server.plan = func(n int) bool { return n%2 == 0 }
	app.sendUpstreamLoop(t.Context(), client, server.URL)
	if got := server.take(); !slices.Equal(got, []string{commandURL, "", commandURL, ""}) {
		t.Fatalf("flips that end on a listing: URLs %q, want four heartbeats", got)
	}
}
