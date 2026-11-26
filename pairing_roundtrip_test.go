package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// A user on 2.0 joined to server A downgrades to 1.x, changes the pairing
// there, and upgrades again. 1.x knows nothing of pairing.json, writes its own
// keys and config.json, and drops every config.json field it does not know.
// The 1.x choice must win at the next 2.0 start, and a 2.0 start with no 1.x
// in between must change nothing.

const (
	roundTripAKey      = "worker-key-a-from-2x-join"
	roundTripBKey      = "worker-key-b-from-1x-join"
	roundTripARejoined = "worker-key-a-from-1x-rejoin"
	keychainService    = "CashPilot Desktop"
)

// roundTripServer counts requests and accepts the keys in accept. A request
// with the shared key gets issued back as the worker key, as a join does.
func roundTripServer(t *testing.T, issued string, accept ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth := r.Header.Get("Authorization")
		if auth == "Bearer "+testSharedKey {
			writeJSON(w, http.StatusOK, upstream.Response{Status: "ok", WorkerID: 3, WorkerKey: issued})
			return
		}
		for _, key := range accept {
			if auth == "Bearer "+key {
				writeJSON(w, http.StatusOK, upstream.Response{Status: "ok", WorkerID: 3})
				return
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

// joinedOn2x is a 2.0 window that joined server A itself, with the server
// commands switch at its default (on). config.json never held A before.
func joinedOn2x(t *testing.T) (*appUnderTest, *httptest.Server, *atomic.Int32) {
	t.Helper()
	useAddrs(t, "192.168.1.30")
	noFirewallRule(t)
	app := newAppUnderTest(t)
	app.ctx = nil
	app.window = true
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	serverA, hitsA := roundTripServer(t, roundTripAKey, roundTripAKey, roundTripARejoined)
	if out, err := app.JoinCashPilot(JoinRequest{URL: serverA.URL, Key: testSharedKey}); err != nil || !out.OK {
		t.Fatalf("2.0 join to A: %+v, %v", out, err)
	}
	app.syncWorkerAPI()
	if n := listening(app); n != 1 {
		t.Fatalf("%d listeners after the 2.0 join, want 1", n)
	}
	return app, serverA, hitsA
}

// saveAs1x rewrites config.json as 1.x saves it: the fields 1.x does not know
// are gone, and change sets what the 1.x user chose.
func saveAs1x(t *testing.T, app *appUnderTest, change func(cfg map[string]any)) {
	t.Helper()
	path := filepath.Join(app.cfg.AppDir(), "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"pairingRevision", "serverCommandsOff", "serverCommandsChosen"} {
		delete(cfg, field)
	}
	change(cfg)
	if raw, err = json.MarshalIndent(cfg, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// joinOn1x is a 1.x join: its two keys in the keychain, then the address.
func joinOn1x(t *testing.T, app *appUnderTest, url, worker string) {
	t.Helper()
	setLegacyKeys(t, testSharedKey, worker)
	saveAs1x(t, app, func(cfg map[string]any) { cfg["upstreamUrl"], cfg["mode"] = url, config.ModeFollower })
}

func setLegacyKeys(t *testing.T, enrol, worker string) {
	t.Helper()
	if err := testKeyring.Set(keychainService, "upstream-enrolment-key", enrol); err != nil {
		t.Fatal(err)
	}
	if err := testKeyring.Set(keychainService, "upstream-worker-key", worker); err != nil {
		t.Fatal(err)
	}
}

// slotOf names the keychain entry and fallback file of a revision's credential.
func slotOf(p config.Pairing) string {
	if p.Alternate {
		return "upstream-pairing-alt"
	}
	return "upstream-pairing"
}

// assertSlotCleared fails when the revision's credential is still stored.
func assertSlotCleared(t *testing.T, app *appUnderTest, p config.Pairing) {
	t.Helper()
	if _, err := testKeyring.Get(keychainService, slotOf(p)); err == nil {
		t.Errorf("the credential of revision %s is still in the keychain", p.Revision)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.AppDir(), "."+slotOf(p))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the credential of revision %s still has a fallback file: %v", p.Revision, err)
	}
}

func assertLegacyKeysRetired(t *testing.T) {
	t.Helper()
	for _, user := range []string{"upstream-enrolment-key", "upstream-worker-key"} {
		if value, err := testKeyring.Get(keychainService, user); err == nil {
			t.Errorf("the 1.x key %s is still in the keychain: %q", user, value)
		}
	}
}

func pairingOf(t *testing.T, app *appUnderTest) config.Pairing {
	t.Helper()
	p, err := app.cfg.Pairing()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// helperBefore is the background helper started on the same files before the
// window: its own Manager, no window role.
func helperBefore(t *testing.T, app *appUnderTest) *appUnderTest {
	t.Helper()
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	helper := &appUnderTest{App: &App{cfg: cfg, store: app.store, catalog: app.catalog, runtime: app.runtime, services: app.services}}
	t.Cleanup(func() { helper.closeWorkerListeners(nil) })
	return helper
}

// assertNoAuthorityFor fails when this process reports to A with A's key or
// takes commands with it.
func assertNoAuthorityFor(t *testing.T, app *appUnderTest, serverA *httptest.Server, hitsA *atomic.Int32) {
	t.Helper()
	before := hitsA.Load()
	app.upstream.mu.Lock()
	app.upstream.lastFailure = upstream.Failure{}
	app.upstream.mu.Unlock()
	app.sendUpstream(t.Context(), &upstream.Client{HTTP: serverA.Client()}, serverA.URL, "")
	if hitsA.Load() != before {
		t.Error("a heartbeat reached server A")
	}
	// The wait for the window is not a key problem: no keychain error shows.
	app.upstream.mu.Lock()
	failure := app.upstream.lastFailure
	app.upstream.mu.Unlock()
	if failure.Code == "keychain" {
		t.Errorf("the heartbeat reported a keychain failure: %+v", failure)
	}
	if key := app.currentWorkerKey(); key != "" {
		t.Errorf("server A's key %q still authorizes commands", key)
	}
	app.syncWorkerAPI()
	if n := listening(app); n != 0 {
		t.Errorf("%d listeners for server A, want 0", n)
	}
}

// 1.x joined server B. The next 2.0 start carries B over as a first upgrade
// does: a new revision for B, server commands off, A's credential and the 1.x
// keys retired. The helper started before the window gives A nothing.
func TestOneXJoinAfterDowngradeReplacesThe2xPairing(t *testing.T) {
	app, serverA, hitsA := joinedOn2x(t)
	pairingA := pairingOf(t, app)
	serverB, hitsB := roundTripServer(t, "", roundTripBKey)

	joinOn1x(t, app, serverB.URL, roundTripBKey)
	restart(t, app)
	assertNoAuthorityFor(t, helperBefore(t, app), serverA, hitsA)

	app.enrolSavedPairing(t.Context())
	p := pairingOf(t, app)
	if p.Revision == pairingA.Revision || p.ServerURL != serverB.URL {
		t.Fatalf("after the start: %+v, want a new revision for B %s", p, serverB.URL)
	}
	if hitsB.Load() == 0 {
		t.Error("the carry-over never asked server B")
	}
	if cfg := app.cfg.Config(); !cfg.ServerCommandsOff || cfg.ServerCommandsChosen || cfg.UpstreamURL != serverB.URL {
		t.Errorf("config after the carry-over: off=%t chosen=%t server=%q", cfg.ServerCommandsOff, cfg.ServerCommandsChosen, cfg.UpstreamURL)
	}
	if _, worker := storedKeys(t, app.App); worker != roundTripBKey {
		t.Errorf("worker key %q, want B's 1.x key", worker)
	}
	assertSlotCleared(t, app, pairingA)
	assertLegacyKeysRetired(t)
	if w := call(t, app, http.MethodGet, "/api/status", roundTripAKey, nil); w.Code == http.StatusOK {
		t.Error("server A's key still opens the server commands")
	}
	app.syncWorkerAPI()
	if n := listening(app); n != 0 {
		t.Errorf("%d listeners after the carry-over, want 0 (commands off)", n)
	}

	// The next start is a plain 2.0 restart: nothing moves again.
	restart(t, app)
	app.enrolSavedPairing(t.Context())
	if again := pairingOf(t, app); again != p {
		t.Errorf("a second start changed the pairing: %+v, want %+v", again, p)
	}
}

// 1.x left the server. The next 2.0 start publishes a Leave: A's credential is
// gone, and nothing reports to A or listens for it.
func TestOneXLeaveAfterDowngradeLeavesThe2xPairing(t *testing.T) {
	app, serverA, hitsA := joinedOn2x(t)
	pairingA := pairingOf(t, app)

	setLegacyKeys(t, "", "")
	saveAs1x(t, app, func(cfg map[string]any) {
		delete(cfg, "upstreamUrl")
		cfg["mode"] = config.ModeLead
	})
	restart(t, app)
	assertNoAuthorityFor(t, helperBefore(t, app), serverA, hitsA)

	app.enrolSavedPairing(t.Context())
	p := pairingOf(t, app)
	if p.Revision == pairingA.Revision || p.ServerURL != "" {
		t.Fatalf("after the start: %+v, want a new revision with no server", p)
	}
	if mode := app.cfg.Config().Mode; mode != config.ModeLead {
		t.Errorf("mode %q after the 1.x Leave, want lead", mode)
	}
	assertSlotCleared(t, app, pairingA)
	assertLegacyKeysRetired(t)
	assertNoAuthorityFor(t, app, serverA, hitsA)
}

// 1.x joined server A again and got a new key. The next 2.0 start carries that
// pairing over: a new revision with the 1.x key, server commands off, and the
// key 2.0 held for A retired.
func TestOneXRejoinOfTheSameServerIsCarriedOver(t *testing.T) {
	app, serverA, _ := joinedOn2x(t)
	pairingA := pairingOf(t, app)

	joinOn1x(t, app, serverA.URL, roundTripARejoined)
	restart(t, app)
	app.enrolSavedPairing(t.Context())

	p := pairingOf(t, app)
	if p.Revision == pairingA.Revision || p.ServerURL != serverA.URL {
		t.Fatalf("after the start: %+v, want a new revision for A", p)
	}
	if cfg := app.cfg.Config(); !cfg.ServerCommandsOff || cfg.ServerCommandsChosen {
		t.Errorf("server commands off=%t chosen=%t, want off and not chosen", cfg.ServerCommandsOff, cfg.ServerCommandsChosen)
	}
	if _, worker := storedKeys(t, app.App); worker != roundTripARejoined {
		t.Errorf("worker key %q, want the key 1.x got", worker)
	}
	assertSlotCleared(t, app, pairingA)
	assertLegacyKeysRetired(t)
}

// 1.x opened and saved its settings, and left the pairing alone. The next 2.0
// start keeps the revision, and server commands start off because 1.x dropped
// the switch.
func TestOneXSaveThatKeptTheServerKeepsThe2xPairing(t *testing.T) {
	app, serverA, hitsA := joinedOn2x(t)
	pairingA := pairingOf(t, app)

	saveAs1x(t, app, func(cfg map[string]any) { cfg["displayCurrency"] = "EUR" })
	restart(t, app)
	app.enrolSavedPairing(t.Context())

	if p := pairingOf(t, app); p != pairingA {
		t.Fatalf("the pairing changed: %+v, want %+v", p, pairingA)
	}
	if cfg := app.cfg.Config(); !cfg.ServerCommandsOff || cfg.UpstreamURL != serverA.URL {
		t.Errorf("after the start: off=%t server=%q", cfg.ServerCommandsOff, cfg.UpstreamURL)
	}
	before := hitsA.Load()
	app.sendUpstream(t.Context(), &upstream.Client{HTTP: serverA.Client()}, serverA.URL, "")
	if hitsA.Load() == before {
		t.Error("the kept pairing does not report to A")
	}
}

// 1.x pointed at server B with no key, which 1.x could not use either. The next
// 2.0 start shows B as 1.x left it and keeps A's revision unused: nothing
// reports to A or takes its commands until the user joins or leaves here.
func TestOneXAddressWithNoKeyLeavesAUnused(t *testing.T) {
	app, serverA, hitsA := joinedOn2x(t)
	pairingA := pairingOf(t, app)
	serverB, hitsB := roundTripServer(t, "", roundTripBKey)

	saveAs1x(t, app, func(cfg map[string]any) { cfg["upstreamUrl"] = serverB.URL })
	restart(t, app)
	app.enrolSavedPairing(t.Context())
	if p := pairingOf(t, app); p != pairingA {
		t.Fatalf("the pairing changed with no 1.x key: %+v, want %+v", p, pairingA)
	}
	if url := app.cfg.Config().UpstreamURL; url != serverB.URL {
		t.Errorf("the window shows %q, want B as 1.x left it", url)
	}
	assertNoAuthorityFor(t, app, serverA, hitsA)
	if hitsB.Load() != 0 {
		t.Error("something reached server B with no key")
	}
}

// A plain 2.0 restart with no 1.x in between touches nothing: not the pairing,
// not the server commands switch (at its default, or turned on by the user),
// not config.json.
func TestPlain2xRestartKeepsThePairing(t *testing.T) {
	for _, chosen := range []bool{false, true} {
		t.Run(map[bool]string{false: "default switch", true: "switch turned on"}[chosen], func(t *testing.T) {
			app, _, _ := joinedOn2x(t)
			if chosen {
				if _, err := app.SetServerCommands(true); err != nil {
					t.Fatal(err)
				}
			}
			pairingA := pairingOf(t, app)
			path := filepath.Join(app.cfg.AppDir(), "config.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			restart(t, app)
			app.enrolSavedPairing(t.Context())
			assertStillOnA(t, app, pairingA, chosen)
			if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
				t.Errorf("a plain restart rewrote config.json:\n%s\nwant\n%s", after, before)
			}
		})
	}
}

// 2.0's own config saves, including a whole-object Save from a caller that
// never saw the marker, are not mistaken for a 1.x save: the switch keeps its
// default (on), and the pairing stays.
func Test2xConfigSavesAreNotA1xChange(t *testing.T) {
	app, _, _ := joinedOn2x(t)
	pairingA := pairingOf(t, app)

	raw, err := json.Marshal(app.cfg.Config())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "pairingRevision")
	fields["displayCurrency"] = "EUR"
	raw, _ = json.Marshal(fields)
	var cfg config.AppConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := app.cfg.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveSettings(map[string]string{"timezone": "Europe/Madrid"}); err != nil {
		t.Fatal(err)
	}
	if err := app.cfg.Update(func(c *config.AppConfig) { c.UpstreamHistoryPushedTo = c.UpstreamURL }); err != nil {
		t.Fatal(err)
	}
	restart(t, app)
	app.enrolSavedPairing(t.Context())
	assertStillOnA(t, app, pairingA, false)
}

func assertStillOnA(t *testing.T, app *appUnderTest, pairingA config.Pairing, chosen bool) {
	t.Helper()
	if p := pairingOf(t, app); p != pairingA {
		t.Fatalf("the pairing changed: %+v, want %+v", p, pairingA)
	}
	if cfg := app.cfg.Config(); cfg.ServerCommandsOff || cfg.ServerCommandsChosen != chosen {
		t.Errorf("server commands off=%t chosen=%t, want on and chosen=%t", cfg.ServerCommandsOff, cfg.ServerCommandsChosen, chosen)
	}
	if w := call(t, app, http.MethodGet, "/api/status", roundTripAKey, nil); w.Code != http.StatusOK {
		t.Errorf("server A's key gets %d, want 200", w.Code)
	}
	app.syncWorkerAPI()
	if n := listening(app); n != 1 {
		t.Errorf("%d listeners, want 1", n)
	}
}

// A locked keychain at the start is not "1.x left no key": the pairing waits,
// A gets nothing meanwhile, and the next start after the unlock carries the 1.x
// pairing over. The rejoin of A is the case that catches a locked read taken
// as "no key": that read would keep A's old revision and lose the 1.x key.
// Only macOS and Windows tell a locked keychain from a missing key
// (refuseKeyRegen); elsewhere a locked store reads as no key by design.
func TestLockedKeychainDelaysThe1xCarryOver(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("a locked keychain reads as a missing key on " + runtime.GOOS)
	}
	for _, sameServer := range []bool{false, true} {
		t.Run(map[bool]string{false: "1.x joined B", true: "1.x rejoined A"}[sameServer], func(t *testing.T) {
			app, serverA, hitsA := joinedOn2x(t)
			pairingA := pairingOf(t, app)
			url, worker := serverA.URL, roundTripARejoined
			if !sameServer {
				serverB, _ := roundTripServer(t, "", roundTripBKey)
				url, worker = serverB.URL, roundTripBKey
			}

			joinOn1x(t, app, url, worker)
			restart(t, app)
			testKeyring.fail(errors.New("test keychain locked"))
			t.Cleanup(testKeyring.reset)
			app.enrolSavedPairing(t.Context())
			if p := pairingOf(t, app); p != pairingA {
				t.Fatalf("a locked keychain changed the pairing: %+v, want %+v", p, pairingA)
			}
			assertNoAuthorityFor(t, app, serverA, hitsA)

			// The mock forgets every entry when it unlocks; 1.x's keys are put back.
			testKeyring.reset()
			setLegacyKeys(t, testSharedKey, worker)
			restart(t, app)
			app.enrolSavedPairing(t.Context())
			if p := pairingOf(t, app); p.ServerURL != url || p.Revision == pairingA.Revision {
				t.Fatalf("after the unlock: %+v, want a new revision for %s", p, url)
			}
			if _, got := storedKeys(t, app.App); got != worker {
				t.Errorf("worker key %q after the unlock, want the 1.x key %q", got, worker)
			}
			if !app.cfg.Config().ServerCommandsOff {
				t.Error("server commands are on after the carry-over")
			}
		})
	}
}

// The Leave that ends a 1.x round trip cannot write pairing.json. A's revision
// stays superseded: nothing reports to A or takes its commands, and the next
// start publishes the Leave.
func TestFailedPairingWriteKeepsThe1xLeaveWaiting(t *testing.T) {
	app, serverA, hitsA := joinedOn2x(t)
	pairingA := pairingOf(t, app)

	setLegacyKeys(t, "", "")
	saveAs1x(t, app, func(cfg map[string]any) {
		delete(cfg, "upstreamUrl")
		cfg["mode"] = config.ModeLead
	})
	restart(t, app)
	restore := config.FailPairingWritesForTest(errors.New("test disk full"))
	t.Cleanup(restore)
	app.enrolSavedPairing(t.Context())
	restore()
	if p := pairingOf(t, app); p != pairingA {
		t.Fatalf("the failed Leave changed the pairing: %+v, want %+v", p, pairingA)
	}
	assertNoAuthorityFor(t, app, serverA, hitsA)
	restart(t, app)
	assertNoAuthorityFor(t, app, serverA, hitsA)

	app.enrolSavedPairing(t.Context())
	if p := pairingOf(t, app); p.Revision == pairingA.Revision || p.ServerURL != "" {
		t.Fatalf("the next start: %+v, want a new revision with no server", p)
	}
	assertSlotCleared(t, app, pairingA)
	assertNoAuthorityFor(t, app, serverA, hitsA)
}

// The background helper loaded config.json before the window's first join, so
// its copy has no marker. The join is still the window's own: the helper
// reports to the server and listens at once.
func TestHelperStartedBeforeA2xJoinUsesItAtOnce(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	noFirewallRule(t)
	app := newAppUnderTest(t)
	app.ctx = nil
	app.window = true
	helper := helperBefore(t, app)
	serverA, hitsA := roundTripServer(t, roundTripAKey, roundTripAKey)
	if out, err := app.JoinCashPilot(JoinRequest{URL: serverA.URL, Key: testSharedKey}); err != nil || !out.OK {
		t.Fatalf("2.0 join to A: %+v, %v", out, err)
	}

	if _, _, err := helper.cfg.ReadPairingCredential(false); err != nil {
		t.Errorf("the helper cannot read the new pairing: %v", err)
	}
	before := hitsA.Load()
	helper.sendUpstream(t.Context(), &upstream.Client{HTTP: serverA.Client()}, serverA.URL, "")
	if hitsA.Load() == before {
		t.Error("the helper did not report to the server the window joined")
	}
	helper.syncWorkerAPI()
	if n := listening(helper); n != 1 {
		t.Errorf("%d helper listeners after the join, want 1", n)
	}
}
