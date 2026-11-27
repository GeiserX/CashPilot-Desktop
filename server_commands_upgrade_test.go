package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

const upgradeIssuedKey = "issued-on-upgrade"

// upgradedApp is a 1.x follower as the upgrade finds it: mode and server address
// in config.json, the shared key in its 1.x file, and no pairing.json. The server
// issues upgradeIssuedKey while up is true.
func upgradedApp(t *testing.T, up bool) (*appUnderTest, *atomic.Bool) {
	t.Helper()
	app := newAppUnderTest(t)
	app.ctx = nil
	var serverUp atomic.Bool
	serverUp.Store(up)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !serverUp.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testSharedKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, upstream.Response{Status: "ok", WorkerKey: upgradeIssuedKey})
	}))
	t.Cleanup(server.Close)
	if err := app.cfg.Update(func(c *config.AppConfig) { c.Mode, c.UpstreamURL = config.ModeFollower, server.URL }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.cfg.AppDir(), ".upstream_enrolment_key"), []byte(testSharedKey), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	return app, &serverUp
}

func noFirewallRule(t *testing.T) {
	t.Helper()
	old := firewallRuleNeeded
	t.Cleanup(func() { firewallRuleNeeded = old })
	firewallRuleNeeded = false
}

// restart gives the app a new config.Manager on the same files, as a new start does.
func restart(t *testing.T, app *appUnderTest) {
	t.Helper()
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	app.cfg = cfg
	app.closeWorkerListeners(nil)
}

// A pairing carried over from 1.x starts with server commands off: the upgrade
// is not the user's yes. The switch turns them on, and that choice survives a
// restart.
func TestUpgradedPairingStartsWithServerCommandsOff(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	noFirewallRule(t)
	app, _ := upgradedApp(t, true)

	app.enrolSavedPairing(t.Context())
	if p, err := app.cfg.Pairing(); err != nil || p.Revision == "" {
		t.Fatalf("the 1.x pairing did not migrate: %+v, %v", p, err)
	}
	if !app.cfg.Config().ServerCommandsOff {
		t.Fatal("server commands are on after an upgrade from 1.x")
	}
	app.syncWorkerAPI()
	if n := listening(app); n != 0 {
		t.Fatalf("%d listeners after the upgrade, want 0", n)
	}
	if state := app.serverCommandsState(); state.On || state.Listening {
		t.Fatalf("the card after the upgrade: %+v", state)
	}
	if w := call(t, app, http.MethodGet, "/api/status", upgradeIssuedKey, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d after the upgrade, want 503", w.Code)
	}

	if _, err := app.SetServerCommands(true); err != nil {
		t.Fatal(err)
	}
	if n := listening(app); n != 1 {
		t.Fatalf("%d listeners after the user turned them on, want 1", n)
	}

	restart(t, app)
	app.enrolSavedPairing(t.Context())
	app.syncWorkerAPI()
	if app.cfg.Config().ServerCommandsOff {
		t.Fatal("a restart turned server commands off again")
	}
	if n := listening(app); n != 1 {
		t.Fatalf("%d listeners after a restart, want 1", n)
	}
	if w := call(t, app, http.MethodGet, "/api/status", upgradeIssuedKey, nil); w.Code != http.StatusOK {
		t.Fatalf("status %d after a restart, want 200", w.Code)
	}
}

// The upgrade retries on every window start until the server answers. A switch
// the user moved before that keeps what the user chose.
func TestUpgradeKeepsAServerCommandsChoiceMadeBeforeItFinished(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	noFirewallRule(t)
	app, serverUp := upgradedApp(t, false)

	app.enrolSavedPairing(t.Context())
	if p, _ := app.cfg.Pairing(); p.Revision != "" {
		t.Fatal("the pairing migrated while the server was down")
	}
	if state := app.serverCommandsState(); state.On || state.Listening {
		t.Fatalf("the card before the carry-over: %+v, want the switch off", state)
	}
	state, err := app.SetServerCommands(true)
	if err != nil {
		t.Fatal(err)
	}
	if !state.On {
		t.Fatalf("the card after the user turned the switch on: %+v", state)
	}

	serverUp.Store(true)
	restart(t, app)
	app.enrolSavedPairing(t.Context())
	if p, err := app.cfg.Pairing(); err != nil || p.Revision == "" {
		t.Fatalf("the 1.x pairing did not migrate: %+v, %v", p, err)
	}
	if app.cfg.Config().ServerCommandsOff {
		t.Fatal("the upgrade overrode the user's switch")
	}
	app.syncWorkerAPI()
	if n := listening(app); n != 1 {
		t.Fatalf("%d listeners, want 1", n)
	}
}

// A join made in 2.0 is the user's own act, so server commands start on, as
// before.
func TestFreshJoinStartsWithServerCommandsOn(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	noFirewallRule(t)
	app := newAppUnderTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app.ctx = ctx
	t.Cleanup(app.stopScheduler)
	t.Cleanup(app.stopUpstream)
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	stub := newJoinStub(t, http.StatusOK, `{"status":"ok","worker_id":7,"worker_key":"issued-own-key"}`)

	res, err := app.JoinCashPilot(JoinRequest{URL: stub.URL, Key: "fleet-key"})
	if err != nil || !res.OK {
		t.Fatalf("join: %+v, %v", res, err)
	}
	if app.cfg.Config().ServerCommandsOff {
		t.Fatal("a fresh join left server commands off")
	}
	if !res.ServerCommandsOn {
		t.Fatal("the join answer says server commands are off; its text would tell the user the server cannot deploy here")
	}
	if n := listening(app); n != 1 {
		t.Fatalf("%d listeners after a fresh join, want 1", n)
	}
}

// On Windows the upgrade adds no firewall rule and nothing listens. Turning the
// switch on still waits for the firewall button.
func TestUpgradedPairingOnWindowsStillWaitsForTheFirewallButton(t *testing.T) {
	f := useAddrs(t, "192.168.1.30")
	app, _ := upgradedApp(t, true)
	installed := ""
	setupRound8Firewall(t, &installed)
	var scripts []string
	change := runFirewallScript
	runFirewallScript = func(script, path string, p config.Pairing) error {
		scripts = append(scripts, script)
		return change(script, path, p)
	}

	app.enrolSavedPairing(t.Context())
	app.syncWorkerAPI()
	if !app.cfg.Config().ServerCommandsOff {
		t.Fatal("server commands are on after an upgrade from 1.x")
	}
	if n := listening(app); n != 0 || len(f.asked) != 0 || len(scripts) != 0 {
		t.Fatalf("after the upgrade: %d listeners, asked %v, firewall scripts %v", n, f.asked, scripts)
	}

	state, err := app.SetServerCommands(true)
	if err != nil {
		t.Fatal(err)
	}
	if n := listening(app); n != 0 || state.Listening || !strings.Contains(state.Problem, "Waiting for the Windows Firewall rule") {
		t.Fatalf("switch on with no rule: %d listeners, state %+v", n, state)
	}
	if state, err = app.AllowServerCommandsThroughFirewall(); err != nil {
		t.Fatal(err)
	}
	if n := listening(app); n != 1 || !state.Listening || len(scripts) != 1 {
		t.Fatalf("after the rule: %d listeners, state %+v, scripts %v", n, state, scripts)
	}
}

// The helper holds its own config.Manager. If it read config.json before the
// window turned the switch off, and pairing.json after the window published the
// carried-over pairing, it must still not listen.
func TestHelperWithAStaleSwitchDoesNotListenAfterTheCarryOver(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	noFirewallRule(t)
	app, _ := upgradedApp(t, true)
	helperCfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}

	app.enrolSavedPairing(t.Context())
	if p, err := app.cfg.Pairing(); err != nil || p.Revision == "" {
		t.Fatalf("the 1.x pairing did not migrate: %+v, %v", p, err)
	}

	app.cfg = helperCfg
	app.closeWorkerListeners(nil)
	app.syncWorkerAPI()
	if n := listening(app); n != 0 {
		t.Fatalf("the helper opened %d listeners with a switch read before the carry-over, want 0", n)
	}
}
