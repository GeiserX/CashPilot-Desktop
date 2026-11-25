package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
)

// The server-commands API (worker_api.go): a joined CashPilot server deploys, stops,
// starts, restarts and removes services on this computer through the web worker's
// routes, with the key it issued to this computer at the join.

const (
	testWorkerKey = "per-worker-key-issued-at-join"
	testSharedKey = "shared-fleet-key"
	testLANPeer   = "192.168.1.20:51234"
)

// joinedApp is the App under test, joined to a server with its own key saved.
func joinedApp(t *testing.T) *appUnderTest {
	t.Helper()
	app := newAppUnderTest(t)
	// Heartbeats go to a local stand-in, never to a real address, and every loop the
	// test starts ends with it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "worker_id": 1})
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app.ctx = ctx
	t.Cleanup(app.stopScheduler)
	t.Cleanup(app.stopUpstream)
	publishTestPairing(t, app.App, server.URL, testWorkerKey)
	return app
}

// call sends one request to the API as a LAN peer with the given key.
func call(t *testing.T, app *appUnderTest, method, path, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = testLANPeer
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	app.workerAPIMux().ServeHTTP(w, req)
	// Let the guard finish any asynchronous resync before a test changes the
	// switch or restores the listener and firewall fakes.
	app.workerAPI.guardResync.Wait()
	return w
}

func honeygainSpec(t *testing.T, app *appUnderTest) map[string]any {
	t.Helper()
	svc, ok := app.catalog.Get("honeygain")
	if !ok {
		t.Fatal("honeygain is not in the catalog")
	}
	return map[string]any{
		// The server sends its own catalog's tag; the repository is what must match.
		"image": imageRepository(svc.Docker.Image) + ":1.0.0",
		"env": map[string]string{
			"HONEYGAIN_EMAIL":    "someone@example.test",
			"HONEYGAIN_PASSWORD": "not-a-real-password",
		},
		"ports":        map[string]any{},
		"volumes":      map[string]any{},
		"cap_add":      []string{"SYS_ADMIN"},
		"network_mode": "host",
		"instance":     1,
	}
}

func detailText(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		return w.Body.String()
	}
	if s, ok := body["detail"].(string); ok {
		return s
	}
	raw, _ := json.Marshal(body["detail"])
	return string(raw)
}

func TestServerCommandsTakeOnlyThisComputersOwnKey(t *testing.T) {
	app := joinedApp(t)
	for _, tc := range []struct {
		name, key string
		want      int
	}{
		{"the key the server issued at the join", testWorkerKey, http.StatusOK},
		{"the shared fleet key", testSharedKey, http.StatusUnauthorized},
		{"a wrong key", "not-the-key", http.StatusUnauthorized},
		{"no key", "", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call(t, app, http.MethodGet, "/api/status", tc.key, nil)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if strings.Contains(w.Body.String(), testWorkerKey) {
				t.Fatal("the answer carries the key")
			}
		})
	}
}

func TestServerCommandsUseTheKeySavedByARejoin(t *testing.T) {
	useAddrs(t)
	helper := joinedApp(t)
	const newKey = "new-key-issued-at-rejoin"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "worker_id": 2, "worker_key": newKey})
	}))
	t.Cleanup(server.Close)
	window := reviewWindow(t, helper, server.URL)
	if w := call(t, helper, http.MethodGet, "/api/status", testWorkerKey, nil); w.Code != http.StatusOK {
		t.Fatalf("status before rejoin: got %d, want 200", w.Code)
	}
	if _, err := window.SwitchToLead(); err != nil {
		t.Fatal(err)
	}
	if result, err := window.JoinCashPilot(JoinRequest{URL: server.URL, Key: testSharedKey}); err != nil || !result.OK {
		t.Fatalf("rejoin: %+v, %v", result, err)
	}

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/status", nil},
		{http.MethodPost, "/api/containers/honeygain/deploy", honeygainSpec(t, helper)},
	} {
		if w := call(t, helper, tc.method, tc.path, testWorkerKey, tc.body); w.Code != http.StatusUnauthorized {
			t.Errorf("%s with the previous key: got %d, want 401", tc.path, w.Code)
		}
	}
	if w := call(t, helper, http.MethodGet, "/api/status", newKey, nil); w.Code != http.StatusOK {
		t.Errorf("status with the current key: got %d, want 200", w.Code)
	}
	if w := call(t, helper, http.MethodPost, "/api/containers/honeygain/deploy", newKey, honeygainSpec(t, helper)); w.Code != http.StatusOK {
		t.Errorf("deploy with the current key: got %d, want 200", w.Code)
	}
}

func TestServerCommandsRefuseAPublicSource(t *testing.T) {
	app := joinedApp(t)
	for _, peer := range []string{"203.0.113.5:4000", "[2001:db8::1]:4000", "[::ffff:8.8.8.8]:4000"} {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		req.RemoteAddr = peer
		req.Header.Set("Authorization", "Bearer "+testWorkerKey)
		w := httptest.NewRecorder()
		app.workerAPIMux().ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", peer, w.Code)
		}
	}
	for _, peer := range []string{"10.1.2.3:1", "172.20.0.9:1", "100.95.1.2:1", "[fd7a:115c:a1e0::1]:1"} {
		if !sourceAllowed(peer) {
			t.Errorf("%s should be allowed", peer)
		}
	}
}

func TestServerCommandsWithoutAnIssuedKeyFailClosed(t *testing.T) {
	app := joinedApp(t)
	saveTestWorkerKey(t, app.App, "")
	// The shared key is the only one left, and it still opens nothing.
	if w := call(t, app, http.MethodGet, "/api/status", testSharedKey, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", w.Code)
	}
}

func TestServerCommandsListenOnPrivateAddressesOnly(t *testing.T) {
	in := []ifaceAddr{
		{Iface: "lo0", Up: true, Loop: true, Addr: netip.MustParseAddr("127.0.0.1")},
		{Iface: "en0", Up: true, Addr: netip.MustParseAddr("192.168.1.30")},
		{Iface: "en0", Up: true, Addr: netip.MustParseAddr("fe80::1")},
		{Iface: "en0", Up: true, Addr: netip.MustParseAddr("2001:db8::30")},
		{Iface: "en1", Up: true, Addr: netip.MustParseAddr("203.0.113.30")},
		{Iface: "tailscale0", Up: true, Addr: netip.MustParseAddr("100.101.102.103")},
		{Iface: "tailscale0", Up: true, Addr: netip.MustParseAddr("fd7a:115c:a1e0::5")},
		{Iface: "docker0", Up: true, Addr: netip.MustParseAddr("172.17.0.1")},
		{Iface: "br-1234", Up: true, Addr: netip.MustParseAddr("172.18.0.1")},
		{Iface: "vEthernet (WSL)", Up: true, Addr: netip.MustParseAddr("172.29.0.1")},
		// The Secure VM's own switch, beside the fenced earner VM (internal/winvm/names.go).
		{Iface: "vEthernet (CashPilot-VM)", Up: true, Addr: netip.MustParseAddr("172.30.0.1")},
		{Iface: "vEthernet (cpd-svm-2)", Up: true, Addr: netip.MustParseAddr("172.31.0.1")},
		// An External switch's host vNIC carries this computer's own LAN address.
		{Iface: "vEthernet (LAN)", Up: true, Addr: netip.MustParseAddr("192.168.1.40")},
		{Iface: "vEthernet (nat)", Up: true, Addr: netip.MustParseAddr("172.24.0.1")},
		{Iface: "veth1a2b3c", Up: true, Addr: netip.MustParseAddr("172.19.0.1")},
		{Iface: "en5", Up: false, Addr: netip.MustParseAddr("10.0.0.5")},
		{Iface: "en6", Up: true, Addr: netip.MustParseAddr("0.0.0.0")},
	}
	got := listenAddrs(in)
	want := []netip.Addr{
		netip.MustParseAddr("100.101.102.103"),
		netip.MustParseAddr("192.168.1.30"),
		netip.MustParseAddr("192.168.1.40"),
		netip.MustParseAddr("fd7a:115c:a1e0::5"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("listens on %v, want %v", got, want)
	}
	if ip := pickURLAddr(got, netip.MustParseAddr("100.101.102.103")); ip.String() != "100.101.102.103" {
		t.Errorf("the reported address is %v, want the one the server is reached from", ip)
	}
	if ip := pickURLAddr(got, netip.MustParseAddr("203.0.113.30")); ip.String() != "192.168.1.30" {
		t.Errorf("the reported address is %v, want the LAN address", ip)
	}
}

// fakeListen records the addresses the API asks for and listens on loopback instead,
// so the lifecycle is tested without owning a LAN address.
type fakeListen struct {
	mu    sync.Mutex
	asked []string
}

func (f *fakeListen) listen(network, addr string) (net.Listener, error) {
	f.mu.Lock()
	f.asked = append(f.asked, addr)
	f.mu.Unlock()
	return net.Listen(network, "127.0.0.1:0")
}

func useAddrs(t *testing.T, addrs ...string) *fakeListen {
	t.Helper()
	oldAddrs, oldListen := machineAddrs, listenTCP
	t.Cleanup(func() { machineAddrs, listenTCP = oldAddrs, oldListen })
	machineAddrs = func() []ifaceAddr {
		var out []ifaceAddr
		for _, a := range addrs {
			out = append(out, ifaceAddr{Iface: "en0", Up: true, Addr: netip.MustParseAddr(a)})
		}
		return out
	}
	f := &fakeListen{}
	listenTCP = f.listen
	return f
}

func listening(app *appUnderTest) int {
	app.workerAPI.mu.Lock()
	defer app.workerAPI.mu.Unlock()
	return len(app.workerAPI.listeners)
}

func TestServerCommandsRunWhileJoinedAndStopOnLeave(t *testing.T) {
	f := useAddrs(t, "192.168.1.30", "8.8.8.8")
	app := joinedApp(t)
	installed := round8NewProgram
	setupRound8Firewall(t, &installed)
	if err := setDesiredFirewall(t, app.App, installed, false); err != nil {
		t.Fatal(err)
	}
	var scripts []string
	change := runFirewallScript
	runFirewallScript = func(script, path string, p config.Pairing) error {
		scripts = append(scripts, script)
		return change(script, path, p)
	}

	app.syncWorkerAPI()
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	if n := listening(app); n != 1 {
		t.Fatalf("%d listeners while joined, want 1", n)
	}
	if !slices.Equal(f.asked, []string{"192.168.1.30:8086"}) {
		t.Fatalf("asked to listen on %v, want only the private address", f.asked)
	}
	if got := app.workerAPIURL(app.cfg.Config().UpstreamURL); got != "http://192.168.1.30:8086" {
		t.Errorf("the heartbeat reports %q", got)
	}

	if _, err := app.SwitchToLead(); err != nil {
		t.Fatal(err)
	}
	if n := listening(app); n != 0 {
		t.Fatalf("%d listeners after leaving, want 0", n)
	}
	if got := app.workerAPIURL(""); got != "" {
		t.Errorf("the heartbeat still reports %q after leaving", got)
	}
	if len(scripts) != 1 || !strings.Contains(scripts[0], "Remove-NetFirewallRule") {
		t.Errorf("leaving did not remove the firewall rule: %v", scripts)
	}
	if p, err := app.cfg.Pairing(); err != nil || p.RemoveFirewall || installed != "" {
		t.Errorf("cleanup after Leave: %+v, Windows %q, %v", p, installed, err)
	}
}

func TestServerCommandsSwitchOffMeansNoListener(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	// The switch, not the firewall: on Windows nothing listens before the rule
	// exists (TestServerCommandsWaitForTheFirewallRuleOnWindows).
	oldNeeded := firewallRuleNeeded
	t.Cleanup(func() { firewallRuleNeeded = oldNeeded })
	firewallRuleNeeded = false
	app := joinedApp(t)
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	app.syncWorkerAPI()
	if n := listening(app); n != 1 {
		t.Fatalf("%d listeners with the switch on (the default), want 1", n)
	}
	state, err := app.SetServerCommands(false)
	if err != nil {
		t.Fatal(err)
	}
	if n := listening(app); n != 0 || state.Listening || state.On {
		t.Fatalf("switch off: %d listeners, state %+v", n, state)
	}
	// A request that reaches a process that has not caught up yet is refused too.
	if w := call(t, app, http.MethodGet, "/api/status", testWorkerKey, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d with the switch off, want 503", w.Code)
	}
	if _, err := app.SetServerCommands(true); err != nil {
		t.Fatal(err)
	}
	if n := listening(app); n != 1 {
		t.Fatalf("%d listeners after switching on again, want 1", n)
	}
}

func TestServerCommandsAreOffWhileNotJoined(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	app := newAppUnderTest(t)
	app.syncWorkerAPI()
	if n := listening(app); n != 0 {
		t.Fatalf("%d listeners on a computer that runs everything itself", n)
	}
	if w := call(t, app, http.MethodGet, "/api/status", testWorkerKey, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", w.Code)
	}
}

func TestServerCommandsDriveAServiceThroughEveryRoute(t *testing.T) {
	app := joinedApp(t)

	w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, honeygainSpec(t, app))
	if w.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	var deployed map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &deployed)
	if deployed["status"] != "deployed" || deployed["container_id"] == "" {
		t.Fatalf("deploy answered %v", deployed)
	}
	c, ok := app.Docker.Container("cashpilot-honeygain")
	if !ok || c.State != "running" {
		t.Fatalf("no running earner after deploy: %+v", c)
	}
	svc, _ := app.catalog.Get("honeygain")
	if c.Image != svc.Docker.Image {
		t.Errorf("the earner runs %q, want the catalog's pinned %q", c.Image, svc.Docker.Image)
	}

	w = call(t, app, http.MethodGet, "/api/containers", testWorkerKey, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"slug":"honeygain"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	w = call(t, app, http.MethodGet, "/api/status", testWorkerKey, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"container_count":1`) {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}

	for _, step := range []struct{ action, answer, state string }{
		{"stop", "stopped", "exited"},
		{"start", "started", "running"},
		{"restart", "restarted", "running"},
	} {
		w = call(t, app, http.MethodPost, "/api/containers/honeygain/"+step.action, testWorkerKey, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), step.answer) {
			t.Fatalf("%s: %d %s", step.action, w.Code, w.Body.String())
		}
		if c, _ := app.Docker.Container("cashpilot-honeygain"); c.State != step.state {
			t.Fatalf("after %s the earner is %q, want %q", step.action, c.State, step.state)
		}
	}
	if last := app.lastServerCommand(); last == nil || last.Action != "restart" || !last.OK {
		t.Errorf("the last command is %+v", last)
	}

	w = call(t, app, http.MethodGet, "/api/containers/honeygain/logs?lines=10", testWorkerKey, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"logs"`) {
		t.Fatalf("logs: %d %s", w.Code, w.Body.String())
	}

	w = call(t, app, http.MethodDelete, "/api/containers/honeygain", testWorkerKey, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "removed") {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if _, ok := app.Docker.Container("cashpilot-honeygain"); ok {
		t.Fatal("the earner is still there after remove")
	}
	w = call(t, app, http.MethodPost, "/api/containers/honeygain/stop", testWorkerKey, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("stop of a removed service: %d, want 404", w.Code)
	}
}

func TestServerCommandsKeepTheLocalDeployRules(t *testing.T) {
	app := joinedApp(t)

	missing := honeygainSpec(t, app)
	missing["env"] = map[string]string{"HONEYGAIN_EMAIL": "someone@example.test"}
	if w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, missing); w.Code != http.StatusBadRequest {
		t.Errorf("a deploy without a required field: %d %s, want 400", w.Code, w.Body.String())
	}

	other := honeygainSpec(t, app)
	other["image"] = "attacker/miner:1.0"
	if w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, other); w.Code != http.StatusForbidden {
		t.Errorf("a deploy of another image: %d %s, want 403", w.Code, w.Body.String())
	}

	if w := call(t, app, http.MethodPost, "/api/containers/not-a-service/deploy", testWorkerKey, honeygainSpec(t, app)); w.Code != http.StatusForbidden {
		t.Errorf("a deploy of a slug this catalog lacks: %d, want 403", w.Code)
	}

	second := honeygainSpec(t, app)
	second["instance"] = 2
	if w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, second); w.Code != http.StatusBadRequest {
		t.Errorf("a deploy of copy 2: %d, want 400", w.Code)
	}

	// A computer set to run services only through a proxy refuses a direct deploy,
	// from the server as from its own Deploy button.
	t.Setenv(services.ProxiedOnlyEnv, "true")
	w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, honeygainSpec(t, app))
	if w.Code != http.StatusConflict {
		t.Fatalf("a deploy the local rules refuse: %d %s, want 409", w.Code, w.Body.String())
	}
	if _, ok := app.Docker.Container("cashpilot-honeygain"); ok {
		t.Fatal("a refused deploy left a container")
	}
	if last := app.lastServerCommand(); last == nil || last.OK || last.Action != "deploy" {
		t.Errorf("the refused deploy is recorded as %+v", last)
	}
	if strings.Contains(w.Body.String(), "not-a-real-password") {
		t.Error("the refusal carries a credential")
	}
}

// An older CashPilot can still send a seed in env. A refused deploy must leave
// both the Docker volume and Desktop's local files untouched.
func TestServerCommandsRefuseSeedFileDeployWithoutWrites(t *testing.T) {
	app := joinedApp(t)
	svc, ok := app.catalog.Get(siaSlug)
	if !ok {
		t.Fatalf("%s is not in the catalog", siaSlug)
	}
	if _, ok := svc.SeedVar(); !ok {
		t.Fatalf("%s does not declare a seed file", siaSlug)
	}
	phrase := strings.Repeat("abandon ", 11) + "about" // Public BIP-39 test vector.
	env := map[string]string{
		siaSeedKey:           phrase,
		"HOSTD_API_PASSWORD": t.Name(),
		"SIA_STORAGE_DIR":    t.TempDir(),
	}
	spec := map[string]any{"image": imageRepository(svc.Docker.Image) + ":1.0.0", "env": env, "instance": 1}
	appDir := os.DirFS(app.cfg.AppDir())
	snapshot := func() map[string][]byte {
		files := make(map[string][]byte)
		err := fs.WalkDir(appDir, ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			files[path], err = fs.ReadFile(appDir, path)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	before := snapshot()
	w := call(t, app, http.MethodPost, "/api/containers/"+siaSlug+"/deploy", testWorkerKey, spec)
	if w.Code != http.StatusForbidden {
		t.Errorf("deploy returned HTTP %d, want 403", w.Code)
	}
	if msg := detailText(t, w); !strings.Contains(msg, "wallet seed phrase in a file") || !strings.Contains(msg, "Open CashPilot Desktop on that computer") {
		t.Error("the refusal does not explain where to deploy Sia")
	}
	if strings.Contains(w.Body.String(), phrase) {
		t.Error("the refusal carries the seed phrase")
	}
	if _, ok := app.Docker.VolumeFile(siaVolume, siaSeedFile); ok {
		t.Error("the request wrote the wallet file into the Docker volume")
	}
	if len(app.Docker.Created()) != 0 {
		t.Error("the request created a container")
	}
	if !maps.EqualFunc(before, snapshot(), bytes.Equal) {
		t.Error("the request changed files in the app directory")
	}
}

func TestServerCommandsNeverDeleteIrreplaceableData(t *testing.T) {
	app := joinedApp(t)
	if w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, honeygainSpec(t, app)); w.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	w := call(t, app, http.MethodDelete, "/api/containers/honeygain?delete_volumes=true&allow_delete_critical=true", testWorkerKey, nil)
	if w.Code != http.StatusForbidden || !strings.Contains(detailText(t, w), "step_up_rejected") {
		t.Fatalf("a critical delete: %d %s, want 403 step_up_rejected", w.Code, w.Body.String())
	}
	if _, ok := app.Docker.Container("cashpilot-honeygain"); !ok {
		t.Fatal("the refused delete removed the earner")
	}
}

func TestServerCommandsFirewallRuleIsScoped(t *testing.T) {
	script := addRuleScript(serverCommandsRuleName(), `C:\Program Files\CashPilot\it's.exe`)
	for _, want := range []string{
		"-Direction Inbound", "-Protocol TCP", "-LocalPort 8086", "-Profile Private",
		"'10.0.0.0/8'", "'172.16.0.0/12'", "'192.168.0.0/16'", "'100.64.0.0/10'", "'fc00::/7'",
		`-Program 'C:\Program Files\CashPilot\it''s.exe'`,
		// The Block rules Windows made for this program (a Cancel on its own prompt)
		// beat an Allow rule, so they go, and only inbound Block rules of this program.
		`Get-NetFirewallApplicationFilter -Program 'C:\Program Files\CashPilot\it''s.exe'`,
		"$_.Direction -eq 'Inbound' -and $_.Action -eq 'Block'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the rule script lacks %q:\n%s", want, script)
		}
	}
	var rule string
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "New-NetFirewallRule") {
			rule = line
		}
	}
	if rule == "" || strings.Contains(rule, "Any") || strings.Contains(rule, "Public") || strings.Contains(rule, "Domain") {
		t.Errorf("the rule is wider than the private profile:\n%s", rule)
	}
}

func TestServerCommandsFirewallRuleNeedsTheUsersYes(t *testing.T) {
	app := joinedApp(t)
	installed := ""
	setupRound8Firewall(t, &installed)
	allow := runFirewallScript
	runFirewallScript = func(_ string, _ string, _ config.Pairing) error { return errors.New("exit status 1223") }
	if state, err := app.AllowServerCommandsThroughFirewall(); err == nil || state.FirewallRule {
		t.Fatalf("a refused admin prompt reported an installed rule: %+v, %v", state, err)
	}
	runFirewallScript = allow
	state, err := app.AllowServerCommandsThroughFirewall()
	if err != nil || !state.FirewallRule {
		t.Fatalf("allow: %+v, %v", state, err)
	}
}

func TestServerCommandsWaitForTheFirewallRuleOnWindows(t *testing.T) {
	f := useAddrs(t, "192.168.1.30")
	app := joinedApp(t)
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	installed := ""
	setupRound8Firewall(t, &installed)
	var scripts []string
	change := runFirewallScript
	runFirewallScript = func(script, path string, p config.Pairing) error {
		scripts = append(scripts, script)
		return change(script, path, p)
	}

	// Joined, the switch on, no rule yet: nothing listens, so Windows never shows its
	// own prompt, and the heartbeat reports no URL the server would deploy to.
	app.syncWorkerAPI()
	if n := listening(app); n != 0 || len(f.asked) != 0 {
		t.Fatalf("listened before the firewall rule exists: %d listeners, asked %v", n, f.asked)
	}
	if got := app.workerAPIURL(app.cfg.Config().UpstreamURL); got != "" {
		t.Fatalf("the heartbeat reports %q before the firewall rule exists", got)
	}
	state := app.serverCommandsState()
	if state.Listening || !strings.Contains(state.Problem, "Waiting for the Windows Firewall rule") {
		t.Fatalf("the card does not say it waits for the rule: %+v", state)
	}

	// The user's yes makes the rule first; only then does it listen.
	state, err := app.AllowServerCommandsThroughFirewall()
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 1 || !strings.Contains(scripts[0], "New-NetFirewallRule") {
		t.Fatalf("the rule script did not run once: %v", scripts)
	}
	if n := listening(app); n != 1 || !state.Listening {
		t.Fatalf("after the rule: %d listeners, state %+v", n, state)
	}
	if got := app.workerAPIURL(app.cfg.Config().UpstreamURL); got != "http://192.168.1.30:8086" {
		t.Errorf("the heartbeat reports %q after the rule", got)
	}
}

// A rule made for another path allows nothing to this program: the portable exe
// moved or was replaced. Nothing listens, the button shows again, and pressing it
// replaces the rule for the new path.
func TestServerCommandsNeedTheRuleForThisProgram(t *testing.T) {
	f := useAddrs(t, "192.168.1.30")
	app := joinedApp(t)
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	installed, program := "", `C:\Users\someone\Downloads\CashPilot.exe`
	setupRound8Firewall(t, &installed)
	var scripts []string
	change := runFirewallScript
	runFirewallScript = func(script, path string, p config.Pairing) error {
		scripts = append(scripts, script)
		return change(script, path, p)
	}
	thisProgram = func() (string, error) { return program, nil }

	if _, err := app.AllowServerCommandsThroughFirewall(); err != nil {
		t.Fatal(err)
	}
	if n := listening(app); n != 1 {
		t.Fatalf("%d listeners with the rule for this program, want 1", n)
	}

	previousProgram := program
	program = `C:\Users\someone\Desktop\CashPilot.exe`
	app.syncWorkerAPI()
	if n := listening(app); n != 0 {
		t.Fatalf("%d listeners with a rule for another path, want 0", n)
	}
	if state := app.serverCommandsState(); state.FirewallRule || state.Listening {
		t.Fatalf("the card counts a rule for another path: %+v", state)
	}

	add := runFirewallScript
	runFirewallScript = func(_ string, _ string, _ config.Pairing) error {
		return errors.New("administrator declined replacement")
	}
	if _, err := app.AllowServerCommandsThroughFirewall(); err == nil {
		t.Fatal("Allow succeeded after Windows declined the replacement")
	}
	runFirewallScript = add
	if installed != previousProgram {
		t.Errorf("declined replacement changed the Windows rule: %q", installed)
	}

	asked := len(f.asked)
	state, err := app.AllowServerCommandsThroughFirewall()
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 2 || !strings.Contains(scripts[1], `-Program 'C:\Users\someone\Desktop\CashPilot.exe'`) {
		t.Fatalf("the rule was not made again for the new path: %v", scripts)
	}
	if n := listening(app); n != 1 || !state.FirewallRule || len(f.asked) == asked {
		t.Fatalf("after the new rule: %d listeners, state %+v", n, state)
	}
}

func TestServerCommandsAnswerActionsAsAWorkerDoes(t *testing.T) {
	app := joinedApp(t)
	if w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, honeygainSpec(t, app)); w.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}

	if w := call(t, app, http.MethodPost, "/api/containers/honeygain/stop?instance=3", testWorkerKey, nil); w.Code != http.StatusNotFound {
		t.Errorf("stop of a copy that is not here: %d %s, want 404", w.Code, w.Body.String())
	}

	app.Docker.SetDown(true)
	w := call(t, app, http.MethodPost, "/api/containers/honeygain/stop", testWorkerKey, nil)
	app.Docker.SetDown(false)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("stop with Docker unreachable: %d %s, want 503", w.Code, w.Body.String())
	}

	// The route rule: a computer set to run services only through a proxy refuses to
	// start one on its own IP, with the code the web worker sends.
	t.Setenv(services.ProxiedOnlyEnv, "true")
	w = call(t, app, http.MethodPost, "/api/containers/honeygain/restart", testWorkerKey, nil)
	if w.Code != http.StatusForbidden || !strings.Contains(detailText(t, w), `"error":"proxied_only"`) {
		t.Errorf("a restart the route rule refuses: %d %s, want 403 proxied_only", w.Code, w.Body.String())
	}
}

func TestServerCommandsRefuseToDeleteACriticalVolume(t *testing.T) {
	app := joinedApp(t)
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/bandwidth/keeper.yml": {Data: []byte(
			"name: Identity Keeper\nslug: keeper\ncategory: bandwidth\nstatus: active\n" +
				"docker:\n  image: keeper/image:1.0.0\n  volumes:\n    - \"keeper-data:/var/lib/keeper\"\n" +
				"  critical_volumes:\n    - target: \"/var/lib/keeper\"\n      holds: \"Node identity keystore.\"\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.catalog = cat
	app.services = services.NewManager(app.runtime, cat, app.store)
	spec := map[string]any{"image": "keeper/image:1.0.0", "env": map[string]string{}}
	if w := call(t, app, http.MethodPost, "/api/containers/keeper/deploy", testWorkerKey, spec); w.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	w := call(t, app, http.MethodDelete, "/api/containers/keeper?delete_volumes=true", testWorkerKey, nil)
	if w.Code != http.StatusConflict || !strings.Contains(detailText(t, w), `"error":"critical_volume"`) {
		t.Fatalf("a delete of a critical volume: %d %s, want 409 critical_volume", w.Code, w.Body.String())
	}
	if _, ok := app.Docker.Container("cashpilot-keeper"); !ok {
		t.Fatal("the refused delete removed the container")
	}
}

// Clearing the server's address in Settings leaves the server too, so it cleans up as
// Leave does: the API closes and the firewall rule CashPilot made is removed.
func TestClearingTheAddressInSettingsClosesServerCommands(t *testing.T) {
	useAddrs(t, "192.168.1.30")
	app := joinedApp(t)
	t.Cleanup(func() { app.closeWorkerListeners(nil) })
	installed := round8NewProgram
	setupRound8Firewall(t, &installed)
	if err := setDesiredFirewall(t, app.App, installed, false); err != nil {
		t.Fatal(err)
	}
	var scripts []string
	change := runFirewallScript
	runFirewallScript = func(script, path string, p config.Pairing) error {
		scripts = append(scripts, script)
		return change(script, path, p)
	}

	app.syncWorkerAPI()
	if n := listening(app); n != 1 {
		t.Fatalf("%d listeners while joined, want 1", n)
	}

	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": ""}); err != nil {
		t.Fatal(err)
	}
	if app.joined() {
		t.Fatal("still joined after clearing the address")
	}
	if n := listening(app); n != 0 {
		t.Errorf("%d listeners after clearing the address, want 0", n)
	}
	if len(scripts) != 1 || !strings.Contains(scripts[0], "Remove-NetFirewallRule") || strings.Contains(scripts[0], "New-NetFirewallRule") {
		t.Errorf("clearing the address did not remove the firewall rule: %v", scripts)
	}
	if p, err := app.cfg.Pairing(); err != nil || p.RemoveFirewall || installed != "" {
		t.Errorf("cleanup after clearing the address: %+v, Windows %q, %v", p, installed, err)
	}

	// A save that does not leave runs no firewall script.
	if _, err := app.SaveSettings(map[string]string{"timezone": "Europe/Madrid"}); err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 1 {
		t.Errorf("a save that did not leave ran a firewall script: %v", scripts)
	}
}

func TestPairingChangesCancelAnInFlightFirewallAllow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		save    map[string]string
		cancel  bool
		replace bool
	}{
		{"Leave button", nil, true, false},
		{"clear address", map[string]string{"upstreamUrl": ""}, true, false},
		{"replace address", map[string]string{"upstreamUrl": "http://127.0.0.1:2"}, true, false},
		{"replace key", map[string]string{"upstreamKey": "other-test-key"}, true, false},
		{"remove key", map[string]string{"upstreamKey": ""}, true, false},
		{"save the same key", map[string]string{"upstreamKey": testSharedKey}, false, false},
		{"save unrelated settings", map[string]string{"timezone": "Europe/Madrid"}, false, false},
		{"replace address while replacing the rule", map[string]string{"upstreamUrl": "http://127.0.0.1:2"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useAddrs(t)
			app := joinedApp(t)
			app.ctx = nil
			// Use independent Managers to exercise the shared lock.
			window := reviewWindow(t, app, app.cfg.Config().UpstreamURL)
			var installed atomic.Bool
			installed.Store(tc.replace)
			program := ""
			if tc.replace {
				program = round8OldProgram
				if err := setDesiredFirewall(t, app.App, program, false); err != nil {
					t.Fatal(err)
				}
			}
			setupRound8Firewall(t, &program)
			readServerCommandsFirewall = func() (runtime.NamedFirewallRule, error) {
				if installed.Load() {
					if tc.replace {
						return testFirewallRule(round8OldProgram), nil
					}
					return testFirewallRule(round8NewProgram), nil
				}
				return runtime.NamedFirewallRule{}, nil
			}
			adding, resume := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(resume) })
			defer release()
			runFirewallScript = func(script string, _ string, p config.Pairing) error {
				if script == removeRuleScript() {
					installed.Store(false)
					return nil
				}
				close(adding)
				<-resume
				// Simulate the elevated script taking the shared lock after UAC.
				return app.cfg.UpdatePairing(p.Revision, func(current *config.Pairing) error {
					if current.RemoveFirewall || current.FirewallProgram != p.FirewallProgram {
						return config.ErrPairingChanged
					}
					installed.Store(true)
					return nil
				})
			}
			allowDone := make(chan error, 1)
			go func() { _, err := app.AllowServerCommandsThroughFirewall(); allowDone <- err }()
			t.Cleanup(func() { release(); <-allowDone })
			select {
			case <-adding:
			case <-time.After(8 * time.Second):
				t.Fatal("Allow did not reach the firewall prompt")
			}

			if tc.save == nil {
				if _, err := window.SwitchToLead(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := window.SaveSettings(tc.save); err != nil {
				t.Fatal(err)
			}
			release()
			if err := <-allowDone; (err != nil) != tc.cancel {
				t.Errorf("late Allow returned %v, want cancellation %v", err, tc.cancel)
			}
			close(allowDone)
			if tc.replace {
				if err := window.RetryServerCommandsFirewallRemoval(); err != nil {
					t.Fatal(err)
				}
				thisProgram = func() (string, error) { return round8OldProgram, nil }
				if window.serverCommandsFirewallStatus() == "ready" {
					t.Error("the old executable is firewall-ready after Windows removed the rule")
				}
			}
			if installed.Load() == tc.cancel {
				t.Errorf("rule installed: %v, want %v", installed.Load(), !tc.cancel)
			}
		})
	}
}

func TestFirewallAllowDoesNotDependOnAPostElevationConfigSave(t *testing.T) {
	useAddrs(t)
	app := joinedApp(t)
	app.ctx = nil
	installed := round8OldProgram
	setupRound8Firewall(t, &installed)
	if err := setDesiredFirewall(t, app.App, installed, false); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(app.cfg.AppDir(), "config.json")
	savedPath := configPath + ".saved"
	change := runFirewallScript
	removals := 0
	runFirewallScript = func(script, path string, p config.Pairing) error {
		if strings.Contains(script, "New-NetFirewallRule") {
			if err := os.Rename(configPath, savedPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(configPath, 0o700); err != nil {
				t.Fatal(err)
			}
		} else {
			removals++
			if removals == 1 {
				return errors.New("administrator declined removal")
			}
		}
		return change(script, path, p)
	}
	state, err := app.AllowServerCommandsThroughFirewall()
	if err != nil || removals != 0 || installed != round8NewProgram || !state.FirewallRule {
		t.Fatalf("Allow depended on a post-elevation save: %+v, %v, removals %d, Windows %q", state, err, removals, installed)
	}
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(savedPath, configPath); err != nil {
		t.Fatal(err)
	}
	left, err := app.SwitchToLead()
	if err != nil || left.FirewallError == "" || installed == "" || removals != 1 {
		t.Fatalf("denied removal: %+v, %v, Windows %q, attempts %d", left, err, installed, removals)
	}
	if err := app.RetryServerCommandsFirewallRemoval(); err != nil || installed != "" || removals != 2 {
		t.Errorf("retry removal: %v, Windows %q, attempts %d", err, installed, removals)
	}
}

// The Allow button removes only the Block rules for this program that can beat the new
// rule: inbound, TCP or any protocol, port 8086 or any port. Every other Block rule
// stays, such as one an admin made for the hub's port.
func TestAllowRemovesOnlyBlockRulesThatCoverThePort(t *testing.T) {
	script := addRuleScript(serverCommandsRuleName(), `C:\CashPilot.exe`)
	var removal string
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "$_.Action -eq 'Block'") {
			removal = line
		}
	}
	if !strings.Contains(removal, "Test-CoversPort ($_ | Get-NetFirewallPortFilter) 8086") {
		t.Fatalf("the Block-rule removal is not limited to rules that cover TCP 8086:\n%s", removal)
	}
	if !strings.Contains(script, "function Test-CoversPort") {
		t.Fatalf("the script does not define Test-CoversPort:\n%s", script)
	}

	// The predicate itself, where PowerShell is installed (the Windows runners have it).
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh is not installed; the predicate runs on the Windows runners")
	}
	out, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", testCoversPortScript).CombinedOutput()
	if err != nil {
		t.Fatalf("pwsh: %v\n%s", err, out)
	}
	if got := strings.Join(strings.Fields(string(out)), " "); got != testCoversPortWant {
		t.Fatalf("Test-CoversPort answers %q, want %q", got, testCoversPortWant)
	}
}

// testCoversPortScript asks Test-CoversPort about the port filters Windows makes and
// some it does not: a Cancel on Windows' prompt makes TCP and UDP Block rules on any
// port.
var testCoversPortScript = blocksPortFilter + `
$cases = @(
  [pscustomobject]@{ Protocol = 'TCP'; LocalPort = @('Any') },
  [pscustomobject]@{ Protocol = 'Any'; LocalPort = @('Any') },
  [pscustomobject]@{ Protocol = 'TCP'; LocalPort = @('8086') },
  [pscustomobject]@{ Protocol = '6'; LocalPort = @('80', '8000-9000') },
  [pscustomobject]@{ Protocol = 'UDP'; LocalPort = @('Any') },
  [pscustomobject]@{ Protocol = 'TCP'; LocalPort = @('8085') },
  [pscustomobject]@{ Protocol = 'TCP'; LocalPort = @('8087-9000') },
  [pscustomobject]@{ Protocol = 'TCP'; LocalPort = @('RPC') },
  $null
)
foreach ($c in $cases) { if (Test-CoversPort $c 8086) { 'yes' } else { 'no' } }
`

const testCoversPortWant = "yes yes yes yes no no no no no"

func TestAllowRemovesOnlyMatchingFirewallRules(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh is not installed; the firewall script runs on the Windows runners")
	}
	name, program := serverCommandsRuleName(), `C:\CashPilot.exe`
	script := "$displayName = " + psLiteral(name) + "\n$program = " + psLiteral(program) + "\n" +
		mockFirewallRules + "\n" + addRuleScript(name, program)
	out, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		t.Fatalf("pwsh: %v\n%s", err, out)
	}
	removed := strings.Fields(string(out))
	slices.Sort(removed)
	if want := []string{"own-tcp-block", "own-wide-block"}; !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want only %v", removed, want)
	}
}

const mockFirewallRules = `
function Test-Rule($name, $path, $direction, $action, $protocol, $port, $label = $displayName) {
  [pscustomobject]@{ Name = $name; DisplayName = $label; Program = $path; Direction = $direction;
    Action = $action; Protocol = $protocol; LocalPort = @($port) }
}
$rules = @(
  Test-Rule other-outbound 'C:\Other.exe' Outbound Block TCP 8086
  Test-Rule other-inbound 'C:\Other.exe' Inbound Block TCP 8086
  Test-Rule own-outbound $program Outbound Block TCP 8086
  Test-Rule own-udp $program Inbound Block UDP 8086
  Test-Rule own-other-port $program Inbound Block TCP 8085
  Test-Rule own-allowed $program Inbound Allow TCP 8086
  Test-Rule own-tcp-block $program Inbound Block TCP 8086
  Test-Rule own-wide-block $program Inbound Block Any Any 'Windows prompt'
)
function Get-NetFirewallRule {
  [CmdletBinding()]
  param([string]$DisplayName, [string]$Name, [Parameter(ValueFromPipeline)]$Filter)
  process {
    if ($DisplayName) { $rules | Where-Object DisplayName -eq $DisplayName }
    elseif ($Name) { $rules | Where-Object Name -eq $Name }
    else { $Filter }
  }
}
function Get-NetFirewallApplicationFilter {
  [CmdletBinding()]
  param([string]$Program)
  $rules | Where-Object Program -eq $Program
}
function Get-NetFirewallPortFilter {
  param([Parameter(ValueFromPipeline)]$Rule)
  process { $Rule }
}
function Remove-NetFirewallRule {
  param([Parameter(ValueFromPipeline)]$Rule)
  process { $Rule.Name }
}
function New-NetFirewallRule { }
function Set-NetFirewallRule { }
`

// PowerShell ends a single-quoted string at ' and at the curly quotes U+2018 to U+201B,
// so each is doubled. A Windows user folder can hold a curly apostrophe.
func TestPsLiteralEscapesEverySingleQuote(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Users\it's\CashPilot.exe`:    `'C:\Users\it''s\CashPilot.exe'`,
		"C:\\Users\\O\u2019Brien\\a.exe": "'C:\\Users\\O\u2019\u2019Brien\\a.exe'",
		"\u2018\u201A\u201B":             "'\u2018\u2018\u201A\u201A\u201B\u201B'",
		"plain":                          "'plain'",
	} {
		if got := psLiteral(in); got != want {
			t.Errorf("psLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

// A deploy the server sent does not register the device on the provider account: the
// server registers it itself. The Deploy button on this computer still does.
func TestServerDeployLeavesDeviceRegistrationToTheServer(t *testing.T) {
	app := joinedApp(t)
	devices := &fakeDevices{fakeCollector: newFakeCollector(nil, nil), register: collectors.RegisterAdded}
	app.collectors = devices
	svc, ok := app.catalog.Get("proxyrack")
	if !ok || svc.DeviceLink == nil {
		t.Fatal("proxyrack with a device link is not in the catalog")
	}
	env := map[string]string{"UUID": strings.Repeat("ab", 32), "API_KEY": "not-a-real-key"}
	spec := map[string]any{"image": imageRepository(svc.Docker.Image) + ":1.0.0", "env": env, "instance": 1}
	if w := call(t, app, http.MethodPost, "/api/containers/proxyrack/deploy", testWorkerKey, spec); w.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	if len(devices.asked) != 0 {
		t.Fatalf("a deploy from the server registered the device here too: %v", devices.asked)
	}

	if _, err := app.DeployService("proxyrack", env, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(devices.asked, []string{"proxyrack"}) {
		t.Fatalf("the Deploy button did not register the device: %v", devices.asked)
	}
}
