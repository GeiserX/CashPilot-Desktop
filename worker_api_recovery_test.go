package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

func reviewWindow(t *testing.T, helper *appUnderTest, url string) *App {
	t.Helper()
	if url != helper.cfg.Config().UpstreamURL {
		enrol, worker := storedKeys(t, helper.App)
		p, err := helper.cfg.Pairing()
		if err != nil {
			t.Fatal(err)
		}
		if err := helper.cfg.PublishPairing(p.Revision, url, false, config.PairingCredential{Enrolment: enrol, Worker: worker}); err != nil {
			t.Fatal(err)
		}
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

func TestRound7SameServerRejoinAllRoutes(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		name := "readable config"
		if unreadable {
			name = "unreadable config"
		}
		t.Run(name, func(t *testing.T) {
			useAddrs(t)
			helper := joinedApp(t)
			const newKey = "round-seven-current-key"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, map[string]any{"status": "ok", "worker_id": 2, "worker_key": newKey})
			}))
			defer server.Close()
			window := reviewWindow(t, helper, server.URL)
			status := func(key string) int { return call(t, helper, "GET", "/api/status", key, nil).Code }
			if old, next := status(testWorkerKey), status(newKey); old != 200 || next != 401 {
				t.Fatalf("before rejoin: previous/current %d/%d, want 200/401", old, next)
			}
			reviewRejoin(t, window, server.URL)
			if unreadable {
				path := filepath.Join(helper.cfg.AppDir(), "config.json")
				if err := os.Rename(path, path+".bak"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, route := range []struct {
				method, path string
				body         any
			}{
				{"GET", "/api/status", nil},
				{"POST", "/api/containers/honeygain/deploy", honeygainSpec(t, helper)},
				{"GET", "/api/containers", nil},
				{"POST", "/api/containers/honeygain/stop", nil},
				{"POST", "/api/containers/honeygain/start", nil},
				{"POST", "/api/containers/honeygain/restart", nil},
				{"GET", "/api/containers/honeygain/logs", nil},
				{"DELETE", "/api/containers/honeygain", nil},
			} {
				previous := call(t, helper, route.method, route.path, testWorkerKey, route.body)
				if previous.Code != 401 {
					t.Errorf("%s %s previous key: %d, want 401", route.method, route.path, previous.Code)
				}
				current := call(t, helper, route.method, route.path, newKey, route.body)
				if current.Code != 200 {
					t.Errorf("%s %s current key: %d, want 200: %s", route.method, route.path, current.Code, current.Body.String())
				}
			}
			if unreadable {
				testKeyring.fail(errors.New("test keychain inaccessible"))
				t.Cleanup(testKeyring.reset)
				if old, next := status(testWorkerKey), status(newKey); old != 503 || next != 503 {
					t.Errorf("with key store also unavailable: previous/current %d/%d, want 503/503", old, next)
				}
			}
		})
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
	oldNeeded, oldRun, oldProgram, oldRead := firewallRuleNeeded, runFirewallScript, thisProgram, readServerCommandsFirewall
	t.Cleanup(func() {
		firewallRuleNeeded, runFirewallScript, thisProgram, readServerCommandsFirewall = oldNeeded, oldRun, oldProgram, oldRead
	})
	firewallRuleNeeded = true
	thisProgram = func() (string, error) { return `C:\Moved\CashPilot.exe`, nil }
	const previousProgram = `C:\Old\CashPilot.exe`
	if err := setDesiredFirewall(t, app.App, previousProgram, false); err != nil {
		t.Fatal(err)
	}
	var installed atomic.Bool
	installed.Store(true)
	readServerCommandsFirewall = func() (runtime.NamedFirewallRule, error) {
		if installed.Load() {
			return testFirewallRule(previousProgram), nil
		}
		return runtime.NamedFirewallRule{}, nil
	}
	atPrompt, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	done := make(chan error, 1)
	runFirewallScript = func(script string, _ string, _ config.Pairing) error {
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
	pending, err := restarted.cfg.Pairing()
	if err != nil || pending.FirewallProgram != `C:\Moved\CashPilot.exe` || pending.RemoveFirewall {
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
func TestRound7ExitAtReplacementPromptThenLeave(t *testing.T) {
	const crashFlag = "CASHPILOT_TEST_EXIT_AT_FIREWALL_PROMPT"
	if os.Getenv(crashFlag) == "1" {
		cfg, err := config.NewManager()
		if err != nil {
			t.Fatal(err)
		}
		app := newAppUnderTest(t)
		app.cfg, app.ctx = cfg, nil
		installed := round8OldProgram
		setupRound8Firewall(t, &installed)
		runFirewallScript = func(string, string, config.Pairing) error {
			os.Exit(73)
			return nil
		}
		_, err = app.AllowServerCommandsThroughFirewall()
		t.Fatalf("Allow returned before the simulated exit: %v", err)
	}

	useAddrs(t)
	app := joinedApp(t)
	// A separate test process cannot see the mock keychain.
	testKeyring.fail(errors.New("credential store unavailable"))
	t.Cleanup(testKeyring.reset)
	saveTestWorkerKey(t, app.App, testWorkerKey)
	if err := setDesiredFirewall(t, app.App, round8OldProgram, false); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestRound7ExitAtReplacementPromptThenLeave$")
	child.Env = append(os.Environ(), crashFlag+"=1")
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("replacement child: %v\n%s", err, output)
	}

	installed := round8OldProgram
	setupRound8Firewall(t, &installed)
	window := reviewWindow(t, app, app.cfg.Config().UpstreamURL)
	window.recoverServerCommandsFirewall()
	out, err := window.SwitchToLead()
	if err != nil || out.FirewallError != "" || installed != "" {
		t.Fatalf("Leave after exit at replacement prompt: %+v, %v, Windows %q", out, err, installed)
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
	oldCredential, err := testKeyring.Get("CashPilot Desktop", "upstream-pairing")
	if err != nil {
		t.Fatal(err)
	}
	testKeyring.fail(errors.New("credential store temporarily locked"))
	t.Cleanup(testKeyring.reset)
	reviewRejoin(t, window, server.URL)
	status := func(key string) int { return call(t, helper, "GET", "/api/status", key, nil).Code }
	if old, next := status(testWorkerKey), status(newKey); old != 401 || next != 200 {
		t.Fatalf("while using fallback: previous/current %d/%d, want 401/200", old, next)
	}
	// The mock resets its map when unlocked, so put back the unchanged credential.
	testKeyring.reset()
	if err := testKeyring.Set("CashPilot Desktop", "upstream-pairing", oldCredential); err != nil {
		t.Fatal(err)
	}
	if old, next := status(testWorkerKey), status(newKey); old != 401 || next != 200 {
		t.Errorf("after keychain unlock: previous/current %d/%d, want 401/200", old, next)
	}
	if _, _, err := window.cfg.ReadPairingCredential(true); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{".upstream-pairing", ".upstream-pairing-alt"} {
		if _, err := os.Stat(filepath.Join(helper.cfg.AppDir(), file)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the fallback file still exists after the window recovered the keychain: %v", err)
		}
	}
	if old, next := status(testWorkerKey), status(newKey); old != 401 || next != 200 {
		t.Errorf("after removing the fallback: previous/current %d/%d, want 401/200", old, next)
	}
}

func TestStartupReconcilesInterruptedFirewallChanges(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remove     bool
		actual     string
		wantStatus string
	}{
		{"replacement already installed", false, round8NewProgram, "ready"},
		{"removal interrupted before Windows changed", true, round8OldProgram, "removal-pending"},
		{"removal already complete", true, "", "absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := joinedApp(t)
			installed := tc.actual
			setupRound8Firewall(t, &installed)
			program := round8NewProgram
			if tc.remove {
				program = ""
			}
			if err := setDesiredFirewall(t, app.App, program, tc.remove); err != nil {
				t.Fatal(err)
			}
			restarted := reviewWindow(t, app, app.cfg.Config().UpstreamURL)
			restarted.recoverServerCommandsFirewall()
			if pending, err := restarted.cfg.Pairing(); err != nil || pending.RemoveFirewall != (tc.remove && tc.actual != "") {
				t.Errorf("after recovery: %+v, %v", pending, err)
			}
			if got := restarted.serverCommandsFirewallStatus(); got != tc.wantStatus {
				t.Errorf("status: %q, want %q", got, tc.wantStatus)
			}
		})
	}
}
