package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

const round8OldProgram = `C:\Old\CashPilot.exe`
const round8NewProgram = `C:\Moved\CashPilot.exe`

func setupRound8Firewall(t *testing.T, installed *string) {
	t.Helper()
	oldNeeded, oldRun, oldProgram, oldRead := firewallRuleNeeded, runFirewallScript, thisProgram, readServerCommandsFirewall
	t.Cleanup(func() {
		firewallRuleNeeded, runFirewallScript, thisProgram, readServerCommandsFirewall = oldNeeded, oldRun, oldProgram, oldRead
	})
	firewallRuleNeeded = true
	thisProgram = func() (string, error) { return round8NewProgram, nil }
	readServerCommandsFirewall = func() (runtime.NamedFirewallRule, error) { return testFirewallRule(*installed), nil }
	runFirewallScript = func(_ string, _ string, change config.Pairing) error {
		*installed = change.FirewallProgram
		return nil
	}
}

func TestRound8ServerRevokedKeyStopsOpeningWorkerAPI(t *testing.T) {
	useAddrs(t)
	app := joinedApp(t)
	var refused atomic.Int32
	requests := make(chan struct{}, 2)
	respond := make(chan struct{})
	release := sync.OnceFunc(func() { close(respond) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+testWorkerKey {
			refused.Add(1)
			requests <- struct{}{}
			<-respond
			http.Error(w, "Invalid API key", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") == "Bearer "+testSharedKey {
			writeJSON(w, 200, map[string]any{"status": "ok", "worker_id": 2, "worker_key": "current-key"})
			return
		}
		t.Errorf("unexpected server credential")
		http.Error(w, "Invalid API key", http.StatusUnauthorized)
	}))
	defer server.Close()
	defer release()
	window := reviewWindow(t, app, server.URL)
	// Use telemetry-only senders so the traffic-counter lock does not serialize
	// the two requests before they reach the server.
	sender := &App{cfg: app.cfg, store: app.store}
	var beats sync.WaitGroup
	for range 2 {
		beats.Go(func() {
			sender.sendUpstream(t.Context(), &upstream.Client{HTTP: server.Client()}, server.URL, testSharedKey)
		})
	}
	for range 2 {
		select {
		case <-requests:
		case <-time.After(8 * time.Second):
			t.Fatal("both requests must be in flight before CashPilot refuses them")
		}
	}
	release()
	beats.Wait()
	if refused.Load() != 2 {
		t.Errorf("server refusal count = %d, want 2", refused.Load())
	}
	if got := call(t, app, "GET", "/api/status", testWorkerKey, nil).Code; got == 200 {
		t.Errorf("upstream explicitly rejected the worker key twice, but Desktop still accepts it with %d", got)
	}
	restarted, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	other := &appUnderTest{App: &App{cfg: restarted, store: app.store, catalog: app.catalog, runtime: app.runtime, services: app.services}}
	if got := call(t, other, "GET", "/api/status", testWorkerKey, nil).Code; got == 200 {
		t.Fatal("the rejected key survived restart")
	}
	if out, err := window.JoinCashPilot(JoinRequest{URL: server.URL, Key: testSharedKey}); err != nil || !out.OK {
		t.Fatalf("explicit rejoin after suspension: %+v, %v", out, err)
	}
	if got := call(t, other, "GET", "/api/status", "current-key", nil).Code; got != 200 {
		t.Errorf("new key after explicit rejoin returned %d", got)
	}
}

func TestRound8AdvancedSettingsEnrolls(t *testing.T) {
	useAddrs(t)
	app := joinedApp(t)
	app.ctx = nil
	if _, err := app.SwitchToLead(); err != nil {
		t.Fatal(err)
	}
	const issued = "current-from-settings-enrolment"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+testSharedKey {
			writeJSON(w, 200, map[string]any{"status": "ok", "worker_id": 2, "worker_key": issued})
			return
		}
		if r.Header.Get("Authorization") == "Bearer "+issued {
			writeJSON(w, 200, map[string]any{"status": "ok", "worker_id": 2})
			return
		}
		http.Error(w, "Invalid API key", 401)
	}))
	defer server.Close()
	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": server.URL, "upstreamKey": testSharedKey}); err != nil {
		t.Fatal(err)
	}
	app.sendUpstream(t.Context(), &upstream.Client{HTTP: server.Client()}, server.URL, testSharedKey)
	if got := call(t, app, "GET", "/api/status", issued, nil).Code; got != 200 {
		t.Errorf("documented settings enrolment got the server key but Desktop returned %d", got)
	}
}

func TestRound8StartupContinuesInterruptedLeave(t *testing.T) {
	for _, stage := range []string{"before querying Windows", "during removal prompt", "Windows refuses read"} {
		t.Run(stage, func(t *testing.T) {
			useAddrs(t)
			app := joinedApp(t)
			app.ctx = nil
			installed := round8OldProgram
			setupRound8Firewall(t, &installed)
			remove, read := runFirewallScript, readServerCommandsFirewall
			denied := errors.New("Windows Firewall query denied")
			switch stage {
			case "before querying Windows", "Windows refuses read":
				readServerCommandsFirewall = func() (runtime.NamedFirewallRule, error) { return runtime.NamedFirewallRule{}, denied }
			case "during removal prompt":
				runFirewallScript = func(string, string, config.Pairing) error { return errors.New("administrator cancelled") }
			}
			result, err := app.SwitchToLead()
			if err != nil || result.FirewallError == "" {
				t.Fatalf("interrupted Leave: %+v, %v", result, err)
			}
			if stage != "Windows refuses read" {
				readServerCommandsFirewall = read
			}
			runFirewallScript = remove
			cfg, err := config.NewManager()
			if err != nil {
				t.Fatal(err)
			}
			next := &App{cfg: cfg, store: app.store, catalog: app.catalog, runtime: app.runtime, services: app.services}
			next.recoverServerCommandsFirewall()
			pending, err := cfg.Pairing()
			if err != nil || !pending.RemoveFirewall || installed == "" {
				t.Fatalf("restart lost the pending removal: %+v, %v, installed %q", pending, err, installed)
			}
			mode, err := next.GetModeState()
			if err != nil || mode.Mode != config.ModeLead || mode.FirewallCleanup == "" {
				t.Fatalf("cleanup is missing from the Lead screen: %+v, %v", mode, err)
			}
			if stage == "Windows refuses read" {
				if next.serverCommandsFirewallStatus() != "unknown" {
					t.Fatal("a denied read did not report unknown")
				}
				if err := next.RetryServerCommandsFirewallRemoval(); !errors.Is(err, denied) {
					t.Fatalf("retry with a denied read: %v", err)
				}
				readServerCommandsFirewall = read
			}
			if err := next.RetryServerCommandsFirewallRemoval(); err != nil {
				t.Fatal(err)
			}
			if pending, err := cfg.Pairing(); err != nil || pending.RemoveFirewall || installed != "" {
				t.Errorf("cleanup was not confirmed: %+v, %v, installed %q", pending, err, installed)
			}
		})
	}
}

func TestRound8LeaveQueriesWindowsWithNoSavedRule(t *testing.T) {
	useAddrs(t)
	app := joinedApp(t)
	app.ctx = nil
	installed := round8NewProgram
	setupRound8Firewall(t, &installed)
	reads, removals := 0, 0
	read, remove := readServerCommandsFirewall, runFirewallScript
	readServerCommandsFirewall = func() (runtime.NamedFirewallRule, error) {
		reads++
		return read()
	}
	runFirewallScript = func(script, path string, p config.Pairing) error {
		removals++
		return remove(script, path, p)
	}
	result, err := app.SwitchToLead()
	if err != nil || result.FirewallError != "" {
		t.Fatalf("Leave: %+v %v", result, err)
	}
	if installed != "" || reads < 2 {
		t.Errorf("Leave did not confirm absence in Windows: program %q, reads %d", installed, reads)
	}
	if err := app.RetryServerCommandsFirewallRemoval(); err != nil || removals != 1 {
		t.Errorf("retry after confirmed removal: %v, Windows removal attempts %d", err, removals)
	}
}

func setDesiredFirewall(t *testing.T, app *App, program string, remove bool) error {
	t.Helper()
	p, err := app.cfg.Pairing()
	if err != nil {
		return err
	}
	return app.cfg.UpdatePairing(p.Revision, func(next *config.Pairing) error {
		next.FirewallProgram, next.RemoveFirewall = program, remove
		return nil
	})
}

func testFirewallRule(program string) runtime.NamedFirewallRule {
	return runtime.NamedFirewallRule{Found: program != "", Program: program}
}

func TestLateHeartbeatRefusalCannotSuspendRejoin(t *testing.T) {
	useAddrs(t)
	app := joinedApp(t)
	atServer, respond := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(respond) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+testWorkerKey {
			close(atServer)
			<-respond
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, upstream.Response{Status: "ok", WorkerKey: "replacement-worker-key"})
	}))
	t.Cleanup(server.Close)
	window := reviewWindow(t, app, server.URL)
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.sendUpstream(t.Context(), &upstream.Client{HTTP: server.Client()}, server.URL, testSharedKey)
	}()
	t.Cleanup(func() { release(); <-done })
	select {
	case <-atServer:
	case <-time.After(8 * time.Second):
		t.Fatal("the heartbeat did not reach CashPilot")
	}
	reviewRejoin(t, window, server.URL)
	release()
	<-done
	if got := call(t, app, "GET", "/api/status", "replacement-worker-key", nil).Code; got != 200 {
		t.Errorf("late refusal blocked the new key, status %d", got)
	}
}

func TestEarningsImportRefusalSuspendsPairing(t *testing.T) {
	useAddrs(t)
	app := joinedApp(t)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 12.5, Currency: "GRASS"})
	var imported atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workers/earnings-import" {
			imported.Store(true)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, upstream.Response{Status: "ok"})
	}))
	defer server.Close()
	reviewWindow(t, app, server.URL)
	app.sendUpstream(t.Context(), &upstream.Client{HTTP: server.Client()}, server.URL, testSharedKey)
	if p, err := app.cfg.Pairing(); !imported.Load() || err != nil || !p.Suspended || app.currentWorkerKey() != "" {
		t.Fatalf("after earnings import 401: import requested %t, pairing %+v, %v", imported.Load(), p, err)
	}
}

func TestSavedEnrolmentKeyUpgradesThroughTheWindow(t *testing.T) {
	useAddrs(t)
	app := newAppUnderTest(t)
	app.ctx = nil
	const issued = "issued-on-upgrade"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSharedKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, upstream.Response{Status: "ok", WorkerKey: issued})
	}))
	defer server.Close()
	if err := app.cfg.Update(func(c *config.AppConfig) { c.Mode, c.UpstreamURL = config.ModeFollower, server.URL }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.cfg.AppDir(), ".upstream_enrolment_key"), []byte(testSharedKey), 0o600); err != nil {
		t.Fatal(err)
	}
	app.enrolSavedPairing(t.Context())
	if got := call(t, app, "GET", "/api/status", issued, nil).Code; got != 200 {
		t.Errorf("guarded route after upgrade returned %d", got)
	}
}

func TestWindowsDriftStopsFirewallReadiness(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	app := joinedApp(t)
	installed := round8NewProgram
	setupRound8Firewall(t, &installed)
	if err := setDesiredFirewall(t, app.App, installed, false); err != nil {
		t.Fatal(err)
	}
	app.syncWorkerAPI()
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	if n := listening(app); n != 1 {
		t.Fatalf("listeners with the scoped rule: %d", n)
	}
	installed = ""
	app.syncWorkerAPI()
	if state := app.serverCommandsState(); state.FirewallStatus != "absent" || state.FirewallRule || state.Listening || listening(app) != 0 {
		t.Errorf("Desktop still trusts the saved rule after Windows removed it: %+v", state)
	}
}
