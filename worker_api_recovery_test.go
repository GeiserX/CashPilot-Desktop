package main

import (
	"context"
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
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
	"github.com/zalando/go-keyring"
)

func reviewWindow(t *testing.T, helper *appUnderTest, url string) *App {
	t.Helper()
	if err := helper.cfg.Update(func(c *config.AppConfig) { c.UpstreamURL = url }); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	return &App{cfg: cfg, store: helper.store, catalog: helper.catalog, runtime: helper.runtime, services: helper.services}
}

func reviewRejoin(t *testing.T, window *App, url string) {
	t.Helper()
	if _, err := window.SwitchToLead(); err != nil {
		t.Fatal(err)
	}
	if result, err := window.JoinCashPilot(JoinRequest{URL: url, Key: testSharedKey}); err != nil || !result.OK {
		t.Fatalf("rejoin: %+v, %v", result, err)
	}
}

func TestRound7DelayedHelperHeartbeatDoesNotReplaceRejoinKey(t *testing.T) {
	useAddrs(t)
	helper := joinedApp(t)
	// A legacy pairing can still be using its shared key for reports. Pause
	// the reply to one such report while the window leaves and joins again.
	saveTestWorkerKey(t, helper.App, "")
	const earlierKey, currentKey = "earlier-enrolment-key", "current-rejoin-key"
	received, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	var enrolments atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if enrolments.Add(1) == 1 {
			close(received)
			<-resume
			writeJSON(w, 200, map[string]any{"status": "ok", "worker_id": 1, "worker_key": earlierKey})
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ok", "worker_id": 2, "worker_key": currentKey})
	}))
	t.Cleanup(server.Close)
	t.Cleanup(release)
	window := reviewWindow(t, helper, server.URL)
	done := make(chan struct{})
	go func() {
		defer close(done)
		helper.sendUpstream(context.Background(), &upstream.Client{HTTP: server.Client()}, server.URL, testSharedKey)
	}()
	t.Cleanup(func() { release(); <-done })
	select {
	case <-received:
	case <-time.After(8 * time.Second):
		t.Fatal("helper did not reach enrolment")
	}
	reviewRejoin(t, window, server.URL)
	if got := call(t, helper, "GET", "/api/status", currentKey, nil).Code; got != 200 {
		t.Fatalf("current key right after rejoin: %d, want 200", got)
	}
	release()
	<-done
	for _, check := range []struct {
		label, key string
		want       int
	}{{"earlier enrolment", earlierKey, 401}, {"current rejoin", currentKey, 200}} {
		if got := call(t, helper, "GET", "/api/status", check.key, nil).Code; got != check.want {
			t.Errorf("%s key after delayed heartbeat: %d, want %d", check.label, got, check.want)
		}
	}
}

func TestRound7ReplacementKeepsCleanupDiscoverableAtAdminPrompt(t *testing.T) {
	useAddrs(t)
	app := joinedApp(t)
	app.ctx = nil
	oldNeeded, oldRun, oldProgram, oldRead := firewallRuleNeeded, runFirewallScript, thisProgram, readServerCommandsFirewallProgram
	t.Cleanup(func() {
		firewallRuleNeeded, runFirewallScript, thisProgram, readServerCommandsFirewallProgram = oldNeeded, oldRun, oldProgram, oldRead
	})
	firewallRuleNeeded = true
	thisProgram = func() (string, error) { return `C:\Moved\CashPilot.exe`, nil }
	const previousProgram = `C:\Old\CashPilot.exe`
	if err := app.cfg.Update(func(c *config.AppConfig) {
		c.ServerCommandsFirewallRule, c.ServerCommandsFirewallProgram = serverCommandsRuleName(), previousProgram
	}); err != nil {
		t.Fatal(err)
	}
	var installed atomic.Bool
	installed.Store(true)
	readServerCommandsFirewallProgram = func() (string, error) {
		if installed.Load() {
			return previousProgram, nil
		}
		return "", nil
	}
	atPrompt, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	done := make(chan error, 1)
	runFirewallScript = func(script string, _ string, _ config.FirewallChange) error {
		if script == removeRuleScript() {
			installed.Store(false)
			return nil
		}
		close(atPrompt)
		<-resume
		return errors.New("administrator cancelled")
	}
	go func() { _, err := app.AllowServerCommandsThroughFirewall(); done <- err }()
	t.Cleanup(func() { release(); <-done })
	select {
	case <-atPrompt:
	case <-time.After(8 * time.Second):
		t.Fatal("Allow did not reach the elevated call")
	}
	// A new process sees the state that would survive a crash at the admin prompt.
	restarted := reviewWindow(t, app, app.cfg.Config().UpstreamURL)
	pending, err := restarted.cfg.ReadFirewallChange()
	if err != nil || pending == nil || pending.Action != "allow" || pending.OldProgram != previousProgram || pending.NewProgram != `C:\Moved\CashPilot.exe` {
		t.Fatalf("pending Allow after restart: %+v, %v", pending, err)
	}
	result, err := restarted.SwitchToLead()
	if err != nil || result.FirewallError != "" {
		t.Fatalf("Leave: %+v, %v", result, err)
	}
	if installed.Load() {
		t.Error("Leave from a fresh process reported success while the old Windows rule was still installed")
	}
}
func TestRound7RecoveringKeychainKeepsRejoinKey(t *testing.T) {
	useAddrs(t)
	helper := joinedApp(t)
	const newKey = "current-key-written-to-fallback"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "worker_id": 2, "worker_key": newKey})
	}))
	defer server.Close()
	window := reviewWindow(t, helper, server.URL)
	oldCredential, err := keyring.Get("CashPilot Desktop", "upstream-worker-key")
	if err != nil {
		t.Fatal(err)
	}
	keyring.MockInitWithError(errors.New("credential store temporarily locked"))
	t.Cleanup(keyring.MockInit)
	reviewRejoin(t, window, server.URL)
	status := func(key string) int { return call(t, helper, "GET", "/api/status", key, nil).Code }
	if old, next := status(testWorkerKey), status(newKey); old != 401 || next != 200 {
		t.Fatalf("while using fallback: previous/current %d/%d, want 401/200", old, next)
	}
	// The mock resets its map when unlocked, so put back the unchanged credential.
	keyring.MockInit()
	if err := keyring.Set("CashPilot Desktop", "upstream-worker-key", oldCredential); err != nil {
		t.Fatal(err)
	}
	if old, next := status(testWorkerKey), status(newKey); old != 401 || next != 200 {
		t.Errorf("after keychain unlock: previous/current %d/%d, want 401/200", old, next)
	}
	if _, err := os.Stat(filepath.Join(helper.cfg.AppDir(), ".upstream_worker_key")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the fallback file still exists after the keychain recovered: %v", err)
	}
	if old, next := status(testWorkerKey), status(newKey); old != 401 || next != 200 {
		t.Errorf("after removing the fallback: previous/current %d/%d, want 401/200", old, next)
	}
}

func TestStartupReconcilesInterruptedFirewallChanges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  string
		program string
	}{
		{"replacement already installed", "allow", `C:\Moved\CashPilot.exe`},
		{"removal interrupted before Windows changed", "remove", `C:\Old\CashPilot.exe`},
		{"removal already complete", "remove", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := joinedApp(t)
			oldNeeded, oldRead := firewallRuleNeeded, readServerCommandsFirewallProgram
			t.Cleanup(func() { firewallRuleNeeded, readServerCommandsFirewallProgram = oldNeeded, oldRead })
			firewallRuleNeeded = true
			readServerCommandsFirewallProgram = func() (string, error) { return tc.program, nil }
			previous := serverCommandsRule{serverCommandsRuleName(), `C:\Old\CashPilot.exe`}
			if err := app.confirmServerCommandsRule(previous); err != nil {
				t.Fatal(err)
			}
			program := ""
			if tc.action == "allow" {
				program = tc.program
			}
			if _, err := app.beginFirewallChange(previous, program, tc.action); err != nil {
				t.Fatal(err)
			}

			restarted := reviewWindow(t, app, app.cfg.Config().UpstreamURL)
			restarted.recoverServerCommandsFirewall()
			if pending, err := restarted.cfg.ReadFirewallChange(); err != nil || pending != nil {
				t.Errorf("after recovery journal: %+v, %v", pending, err)
			}
			rule := restarted.savedServerCommandsRule()
			if rule.program != tc.program || (rule.name != "") != (tc.program != "") {
				t.Errorf("after recovery record: %+v, want Windows program %q", rule, tc.program)
			}
		})
	}
}
