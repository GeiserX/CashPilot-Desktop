package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// joinStub is a CashPilot server for the join tests: it answers each heartbeat
// with status and body, and counts them and the tokens it saw.
type joinStub struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	body   string
	beats  int
	tokens []string
}

func newJoinStub(t *testing.T, status int, body string) *joinStub {
	t.Helper()
	s := &joinStub{status: status, body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path != "/api/workers/heartbeat" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.beats++
		s.tokens = append(s.tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *joinStub) set(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body = status, body
}

func (s *joinStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.beats
}

func storedKeys(t *testing.T, app *App) (enrol, worker string) {
	t.Helper()
	_, key, err := app.cfg.ReadPairingCredential(false)
	if err != nil {
		t.Fatal(err)
	}
	return key.Enrolment, key.Worker
}

// TestJoinSavesNothingUntilTheServerAccepts: a refused join leaves no address,
// no key and no mode behind, and the user stays on the screen with a plain
// reason. Only an accepted heartbeat saves the link.
func TestJoinSavesNothingUntilTheServerAccepts(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	stub := newJoinStub(t, http.StatusUnauthorized, `{"detail":"Invalid API key"}`)

	res, err := app.JoinCashPilot(JoinRequest{URL: stub.URL, Key: "wrong-key"})
	if err != nil {
		t.Fatalf("JoinCashPilot: %v", err)
	}
	if res.OK || res.Code != "key-refused" || !strings.Contains(res.Message, "refused the key") {
		t.Fatalf("a refused key reads %+v", res)
	}
	if strings.Contains(res.Message, "wrong-key") {
		t.Fatal("the message echoes the key")
	}
	cfg := app.cfg.Config()
	if cfg.UpstreamURL != "" || cfg.Mode == config.ModeFollower {
		t.Fatalf("a refused join was saved: url %q mode %q", cfg.UpstreamURL, cfg.Mode)
	}
	if enrol, worker := storedKeys(t, app); enrol != "" || worker != "" {
		t.Fatalf("a refused join stored keys: %q %q", enrol, worker)
	}
	if stub.count() != 1 {
		t.Fatalf("heartbeats = %d, want exactly one per try", stub.count())
	}

	stub.set(http.StatusOK, `{"status":"ok","worker_id":7,"worker_key":"issued-own-key"}`)
	block := "CASHPILOT_UI_URL=" + stub.URL + "/\nCASHPILOT_API_KEY=fleet-key\nCASHPILOT_WORKER_NAME=server-name"
	res, err = app.JoinCashPilot(JoinRequest{Block: block})
	if err != nil {
		t.Fatalf("JoinCashPilot: %v", err)
	}
	if !res.OK || res.WorkerID != 7 || res.ServerURL != stub.URL || res.Name == "" {
		t.Fatalf("an accepted join reads %+v", res)
	}
	cfg = app.cfg.Config()
	if cfg.UpstreamURL != stub.URL || cfg.Mode != config.ModeFollower {
		t.Fatalf("after joining: url %q mode %q", cfg.UpstreamURL, cfg.Mode)
	}
	if enrol, worker := storedKeys(t, app); enrol != "fleet-key" || worker != "issued-own-key" {
		t.Fatalf("keys after joining: enrol %q worker %q", enrol, worker)
	}
	if stub.tokens[len(stub.tokens)-1] != "fleet-key" {
		t.Fatalf("the join heartbeat did not use the fleet key: %v", stub.tokens)
	}
	if p := app.pairingStatus(); p == nil || p.LastHeartbeat == "" || p.LastError != "" {
		t.Fatalf("the pairing status after a join: %+v", p)
	}
}

// TestJoinRefusesBadInputWithoutSendingAnything: a paste that cannot be read and
// an address the key must not travel to never reach the network.
func TestJoinRefusesBadInputWithoutSendingAnything(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	stub := newJoinStub(t, http.StatusOK, `{"status":"ok","worker_id":1}`)
	for _, tc := range []struct {
		req  JoinRequest
		code string
	}{
		{JoinRequest{Block: "just some words"}, "input"},
		{JoinRequest{URL: stub.URL}, "input"},
		{JoinRequest{URL: "http://cashpilot.example.com", Key: "k"}, "address"},
		{JoinRequest{URL: "http://169.254.169.254", Key: "k"}, "address"},
	} {
		res, err := app.JoinCashPilot(tc.req)
		if err != nil {
			t.Fatalf("%+v: %v", tc.req, err)
		}
		if res.OK || res.Code != tc.code || res.Message == "" {
			t.Fatalf("%+v reads %+v, want code %q", tc.req, res, tc.code)
		}
	}
	if stub.count() != 0 {
		t.Fatalf("bad input reached the server %d time(s)", stub.count())
	}
	if app.cfg.Config().Mode == config.ModeFollower {
		t.Fatal("bad input changed the mode")
	}
}

// TestJoinRefusesAReplyThatIsNotAHeartbeat: a server that answers 200 with some
// other JSON is not a CashPilot.
func TestJoinRefusesAReplyThatIsNotAHeartbeat(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	stub := newJoinStub(t, http.StatusOK, `{"hello":"world"}`)
	res, err := app.JoinCashPilot(JoinRequest{URL: stub.URL, Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Code != "not-cashpilot" {
		t.Fatalf("got %+v", res)
	}
	if app.cfg.Config().UpstreamURL != "" {
		t.Fatal("saved a server that is not CashPilot")
	}
}

// TestJoiningTurnsTheHubOffAndListsItsDevices: a lead that accepted workers
// stops accepting them once it joins a CashPilot, and the result names the
// devices to point at the server instead.
func TestJoiningTurnsTheHubOffAndListsItsDevices(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	if err := app.cfg.Update(func(c *config.AppConfig) {
		c.Mode, c.FleetServerEnabled, c.FirstRunComplete = config.ModeLead, true, true
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nas-worker", "pixel-phone"} {
		if _, err := app.store.UpsertFleetDevice(store.FleetDevice{Name: name, Kind: "worker", Status: "online", LastSeen: time.Now().UTC().Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := app.GetModeState()
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode != config.ModeLead || !before.HubOn || len(before.HubDevices) != 2 {
		t.Fatalf("mode state before: %+v", before)
	}
	stub := newJoinStub(t, http.StatusOK, `{"status":"ok","worker_id":3,"worker_key":"own"}`)
	res, err := app.JoinCashPilot(JoinRequest{URL: stub.URL, Key: "k"})
	if err != nil || !res.OK {
		t.Fatalf("join: %+v %v", res, err)
	}
	if !res.HubWasOn || strings.Join(res.HubDevices, ",") != "nas-worker,pixel-phone" {
		t.Fatalf("hub devices to re-point: %+v", res)
	}
	if app.cfg.Config().FleetServerEnabled {
		t.Fatal("the hub is still on after joining")
	}
	if !app.cfg.Config().FirstRunComplete {
		t.Fatal("joining from Settings reset the first run")
	}
	if _, err := app.JoinCashPilot(JoinRequest{URL: stub.URL, Key: "k"}); err == nil {
		t.Fatal("a second join while joined was accepted")
	}
}

// TestCollectionStopsWhileJoined: a joined computer never logs in to the vendor
// accounts, on the timer, after a deploy or on a click. Switching back starts it.
func TestCollectionStopsWhileJoined(t *testing.T) {
	fake := newFakeCollector([]string{"svc-a", "svc-cred"}, nil)
	app, _, cancel := newSchedulerTestApp(t, fake, "svc-a")
	defer cancel()
	if err := app.store.SaveCredentials("svc-cred", map[string]string{"api_key": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := app.cfg.Update(func(c *config.AppConfig) { c.Mode = config.ModeFollower; c.UpstreamURL = "http://192.168.1.10:8080" }); err != nil {
		t.Fatal(err)
	}

	app.collectAll(context.Background())
	app.collectOne(context.Background(), "svc-a")
	if c := fake.counts(); len(c) != 0 {
		t.Fatalf("a joined computer collected: %v", c)
	}

	if err := app.cfg.Update(func(c *config.AppConfig) { c.Mode = config.ModeLead; c.UpstreamURL = "" }); err != nil {
		t.Fatal(err)
	}
	app.collectAll(context.Background())
	if c := fake.counts(); c["svc-a"] != 1 || c["svc-cred"] != 1 {
		t.Fatalf("a lead did not collect: %v", c)
	}
}

func TestManualCollectIsRefusedWhileJoined(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	fake := newFakeCollector([]string{"honeygain"}, nil)
	app.collectors = fake
	if err := app.cfg.Update(func(c *config.AppConfig) { c.Mode = config.ModeFollower; c.UpstreamURL = "http://192.168.1.10:8080" }); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CollectService("honeygain"); !errors.Is(err, errJoinedNoCollect) {
		t.Fatalf("CollectService while joined: %v", err)
	}
	if len(fake.counts()) != 0 {
		t.Fatal("a manual collect logged in while joined")
	}
}

// TestSwitchingToLeadClearsTheLinkAndCollectsAgain: leaving clears the address
// and both keys, names the Fleet row to remove, and collects at once.
func TestSwitchingToLeadClearsTheLinkAndCollectsAgain(t *testing.T) {
	app := newPayloadTestApp(t, []store.Deployment{{Slug: "svc-a", Status: "running"}})
	fake := newFakeCollector([]string{"svc-a"}, nil)
	app.collectors = fake
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.ctx = ctx
	t.Cleanup(app.stopScheduler)
	t.Cleanup(app.stopUpstream)

	stub := newJoinStub(t, http.StatusOK, `{"status":"ok","worker_id":9,"worker_key":"own"}`)
	if res, err := app.JoinCashPilot(JoinRequest{URL: stub.URL, Key: "fleet-key"}); err != nil || !res.OK {
		t.Fatalf("join: %+v %v", res, err)
	}

	leave, err := app.SwitchToLead()
	if err != nil {
		t.Fatalf("SwitchToLead: %v", err)
	}
	if leave.ServerURL != stub.URL || leave.Name == "" {
		t.Fatalf("leave result: %+v", leave)
	}
	cfg := app.cfg.Config()
	if cfg.Mode != config.ModeLead || cfg.UpstreamURL != "" {
		t.Fatalf("after leaving: mode %q url %q", cfg.Mode, cfg.UpstreamURL)
	}
	if enrol, worker := storedKeys(t, app); enrol != "" || worker != "" {
		t.Fatalf("keys survived leaving: %q %q", enrol, worker)
	}
	if app.pairingStatus() != nil {
		t.Fatal("a lead still shows a pairing status")
	}
	deadline := time.Now().Add(5 * time.Second)
	for fake.counts()["svc-a"] == 0 {
		if time.Now().After(deadline) {
			t.Fatal("local collection did not start again after leaving")
		}
		time.Sleep(5 * time.Millisecond)
	}
	beats := stub.count()
	time.Sleep(50 * time.Millisecond)
	if stub.count() != beats {
		t.Fatal("heartbeats continued after leaving")
	}
	if _, err := app.SwitchToLead(); err == nil {
		t.Fatal("leaving twice was accepted")
	}
}

// TestPairingStatusShowsTheLastHeartbeatAndError: a failing link shows in the
// app in plain words, not only in the log.
func TestPairingStatusShowsTheLastHeartbeatAndError(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	stub := newJoinStub(t, http.StatusOK, `{"status":"ok","worker_id":1}`)
	if err := app.cfg.Update(func(c *config.AppConfig) { c.Mode = config.ModeFollower; c.UpstreamURL = stub.URL }); err != nil {
		t.Fatal(err)
	}
	client := upstream.New()

	app.sendUpstream(context.Background(), client, stub.URL, "fleet-key")
	ok := app.pairingStatus()
	if ok == nil || ok.LastHeartbeat == "" || ok.LastError != "" || ok.ServerURL != stub.URL {
		t.Fatalf("after a good heartbeat: %+v", ok)
	}

	stub.set(http.StatusInternalServerError, `boom`)
	app.sendUpstream(context.Background(), client, stub.URL, "fleet-key")
	bad := app.pairingStatus()
	if bad.LastError == "" || bad.ErrorCode != "server-error" || strings.Contains(bad.LastError, "upstream:") {
		t.Fatalf("after a failed heartbeat: %+v", bad)
	}
	if bad.LastHeartbeat != ok.LastHeartbeat {
		t.Fatal("a failure moved the last good heartbeat")
	}
	state, err := app.GetModeState()
	if err != nil || state.Pairing == nil || state.Pairing.LastError != bad.LastError {
		t.Fatalf("GetModeState pairing: %+v %v", state.Pairing, err)
	}
}

func TestStartAsLeadOnlyDuringTheFirstRun(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	if got := app.cfg.Config().Mode; got != "" {
		t.Fatalf("a new install has mode %q", got)
	}
	if err := app.StartAsLead(); err != nil {
		t.Fatal(err)
	}
	if got := app.cfg.Config().Mode; got != config.ModeLead {
		t.Fatalf("mode = %q", got)
	}
	if err := app.CompleteOnboarding(); err != nil {
		t.Fatal(err)
	}
	if err := app.StartAsLead(); err == nil {
		t.Fatal("StartAsLead after the first run was accepted")
	}
}

// TestSettingsAddressKeepsTheModeInStep: the advanced address row still works,
// and the mode follows it, so the app never says joined without an address.
func TestSettingsAddressKeepsTheModeInStep(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	if err := app.cfg.Update(func(c *config.AppConfig) { c.Mode, c.FleetServerEnabled = config.ModeLead, true }); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": "https://cp.example", "fleetServerEnabled": "true"}); err != nil {
		t.Fatal(err)
	}
	if c := app.cfg.Config(); c.Mode != config.ModeFollower || c.FleetServerEnabled {
		t.Fatalf("after setting the address: mode %q hub %v", c.Mode, c.FleetServerEnabled)
	}
	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": "https://cp.example", "timezone": "Europe/Madrid"}); err != nil {
		t.Fatal(err)
	}
	if c := app.cfg.Config(); c.Mode != config.ModeFollower {
		t.Fatalf("an unrelated save changed the mode: %q", c.Mode)
	}
	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": ""}); err != nil {
		t.Fatal(err)
	}
	if c := app.cfg.Config(); c.Mode != config.ModeLead {
		t.Fatalf("after clearing the address: mode %q", c.Mode)
	}
}

// The JSON names are the frontend's contract (src/wails.d.ts).
func TestModeTypesKeepTheirJSONNames(t *testing.T) {
	raw, _ := json.Marshal(JoinResult{})
	for _, k := range []string{"ok", "code", "message", "serverUrl", "serverHost", "name", "workerId", "hubWasOn", "hubDevices"} {
		if !strings.Contains(string(raw), `"`+k+`"`) {
			t.Errorf("JoinResult lacks %q: %s", k, raw)
		}
	}
	raw, _ = json.Marshal(ModeState{})
	for _, k := range []string{"mode", "name", "pairing", "hubOn", "hubDevices"} {
		if !strings.Contains(string(raw), `"`+k+`"`) {
			t.Errorf("ModeState lacks %q: %s", k, raw)
		}
	}
}

// TestTheHelperFollowsAJoinAndALeaveMadeInTheWindow: the background helper
// (--daemon) runs the collection scheduler and the pairing loop from its own
// copy of the config. A join made in the window must stop the helper logging in
// to the provider accounts and start its heartbeats; a leave must stop its
// heartbeats, so the computer does not stay online on the server, or enrol
// again after the user removed it there, and start its collection again.
func TestTheHelperFollowsAJoinAndALeaveMadeInTheWindow(t *testing.T) {
	window := newPayloadTestApp(t, []store.Deployment{{Slug: "svc-a", Status: "running"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	window.ctx = ctx
	t.Cleanup(window.stopScheduler)
	t.Cleanup(window.stopUpstream)

	// A second process on the same config file: its own Manager, read now, while
	// this computer still runs everything itself.
	helperCfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	helperFake := newFakeCollector([]string{"svc-a"}, nil)
	helper := &App{cfg: helperCfg, store: window.store, catalog: window.catalog, collectors: helperFake, ctx: ctx}
	helper.startUpstream(ctx) // a lead: no loop
	t.Cleanup(helper.stopUpstream)

	stub := newJoinStub(t, http.StatusOK, `{"status":"ok","worker_id":9,"worker_key":"own"}`)
	if res, err := window.JoinCashPilot(JoinRequest{URL: stub.URL, Key: "fleet-key"}); err != nil || !res.OK {
		t.Fatalf("join: %+v %v", res, err)
	}
	window.stopUpstream() // only the helper's heartbeats are counted below

	helper.collectAll(ctx)
	if c := helperFake.counts(); len(c) != 0 {
		t.Fatalf("the helper logged in to the provider accounts after the window joined: %v", c)
	}

	before := stub.count()
	helper.syncConfig(ctx)
	waitFor(t, "the helper's first heartbeat after the join", func() bool { return stub.count() > before })
	stub.mu.Lock()
	token := stub.tokens[len(stub.tokens)-1]
	stub.mu.Unlock()
	if token != "own" {
		t.Fatalf("the helper heartbeat used %q, want the key the server issued at the join", token)
	}

	if _, err := window.SwitchToLead(); err != nil {
		t.Fatalf("SwitchToLead: %v", err)
	}
	if helper.linkStillSaved(stub.URL) {
		t.Fatal("the helper's loop still sees the server after the window left it")
	}
	// The user removes the computer on the server: its key is refused from now on.
	stub.set(http.StatusUnauthorized, `{"detail":"Invalid API key"}`)
	helper.syncConfig(ctx)
	beats := stub.count()
	time.Sleep(50 * time.Millisecond)
	if stub.count() != beats {
		t.Fatal("the helper kept sending heartbeats after the window left the server")
	}
	helper.upstream.mu.Lock()
	running := helper.upstream.cancel != nil
	helper.upstream.mu.Unlock()
	if running {
		t.Fatal("the helper's pairing loop still runs after the window left the server")
	}

	helper.collectAll(ctx)
	if c := helperFake.counts(); c["svc-a"] != 1 {
		t.Fatalf("the helper did not collect again after the window left: %v", c)
	}
}

// TestTheHelperLoopStopsAtItsNextTickWhenTheWindowLeaves: the pairing loop
// itself reads the config file at each tick, so a leave made in the other
// process stops its heartbeats at the next tick, not at the next config sync,
// which can be a minute away.
func TestTheHelperLoopStopsAtItsNextTickWhenTheWindowLeaves(t *testing.T) {
	const tick = 20 * time.Millisecond
	saved := heartbeatInterval
	heartbeatInterval = func(int) time.Duration { return tick }
	t.Cleanup(func() { heartbeatInterval = saved })

	window := newPayloadTestApp(t, nil)
	stub := newJoinStub(t, http.StatusOK, `{"status":"ok","worker_id":9,"worker_key":"own"}`)
	if res, err := window.JoinCashPilot(JoinRequest{URL: stub.URL, Key: "fleet-key"}); err != nil || !res.OK {
		t.Fatalf("join: %+v %v", res, err)
	}

	// The helper: its own Manager on the same file, read after the join. No
	// startConfigSync runs and syncConfig is never called below, so only the
	// loop's own check can stop it.
	helperCfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	helper := &App{cfg: helperCfg, store: window.store, catalog: window.catalog, collectors: newFakeCollector(nil, nil), ctx: ctx}
	helper.startUpstream(ctx)
	t.Cleanup(helper.stopUpstream)
	helper.upstream.mu.Lock()
	done := helper.upstream.done
	helper.upstream.mu.Unlock()
	if done == nil {
		t.Fatal("the helper did not start its pairing loop")
	}
	before := stub.count()
	waitFor(t, "two helper heartbeats", func() bool { return stub.count() >= before+2 })

	if _, err := window.SwitchToLead(); err != nil {
		t.Fatalf("SwitchToLead: %v", err)
	}
	left := stub.count()
	select {
	case <-done:
	case <-time.After(50 * tick):
		t.Fatal("the helper's pairing loop kept running after the window left the server")
	}
	if extra := stub.count() - left; extra > 1 {
		t.Fatalf("the helper sent %d heartbeats after the window left; at most one already in flight is allowed", extra)
	}
	stopped := stub.count()
	time.Sleep(5 * tick)
	if stub.count() != stopped {
		t.Fatal("heartbeats went on after the loop stopped")
	}
	helper.upstream.mu.Lock()
	link := helper.upstream.linkURL
	helper.upstream.mu.Unlock()
	if link != stub.URL {
		t.Fatalf("linkURL = %q: something other than the loop's own check stopped it", link)
	}
}

// TestCollectorErrorsAreHiddenWhileJoined: the collector errors and the
// "device not on your account" notices come from the last local collection.
// While joined there is none, so they would stay on screen for good.
func TestCollectorErrorsAreHiddenWhileJoined(t *testing.T) {
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	devices := &fakeDevices{
		fakeCollector: newFakeCollector(nil, nil),
		unlinked:      map[string][]string{"linked": {"dev-1"}},
	}
	app := &App{cfg: cfg, catalog: deviceLinkCatalog(t), collectors: devices}
	earnings := []store.EarningsRecord{{Platform: "honeygain", Error: "login failed"}}
	deps := []store.Deployment{{Slug: "linked"}}
	stale := func() (collector, device bool) {
		for _, it := range app.notifications(runtime.Status{Available: true}, earnings, deps) {
			collector = collector || strings.HasSuffix(it.Title, " collector")
			device = device || strings.Contains(it.Title, "not on your account")
		}
		return collector, device
	}
	if c, d := stale(); !c || !d {
		t.Fatalf("a lead should see both notices: collector %v device %v", c, d)
	}
	if err := cfg.Update(func(c *config.AppConfig) { c.Mode = config.ModeFollower; c.UpstreamURL = "http://192.168.1.10:8080" }); err != nil {
		t.Fatal(err)
	}
	if c, d := stale(); c || d {
		t.Fatalf("a joined computer still shows notices from before the join: collector %v device %v", c, d)
	}
}
