package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/zalando/go-keyring"
)

// TestMain mocks the keyring so store.Open (via config.MasterKey) stays fully
// in-memory and never touches the real OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

// newFleetTestApp builds a minimal App wired to a temp config + store, with the
// given fleet API key. Only the fields the fleet handlers touch are set. The
// fleet API binds to loopback so the start/stop test never triggers a firewall
// prompt.
func newFleetTestApp(t *testing.T, apiKey string) *App {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager error: %v", err)
	}
	c := cfg.Config()
	c.FleetBindAddress = "127.0.0.1"
	if err := cfg.Save(c); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open error: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// The fleet bearer token now lives in memory (loaded by ensureFleetAPIKey from the
	// keychain/file store), not in config.json, so inject it directly for the handlers.
	return &App{cfg: cfg, store: st, fleetKey: apiKey}
}

func TestHandleFleetHealth(t *testing.T) {
	app := newFleetTestApp(t, "unused")
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	app.handleFleetHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status ok, got %q", body["status"])
	}
}

// TestHandleFleetHealthRejectsNonGet pins the GET-only health endpoint.
func TestHandleFleetHealthRejectsNonGet(t *testing.T) {
	app := newFleetTestApp(t, "unused")
	req := httptest.NewRequest(http.MethodPost, "/api/health", nil)
	w := httptest.NewRecorder()

	app.handleFleetHealth(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST /api/health, got %d", w.Code)
	}
}

func TestHandleWorkerHeartbeatSuccess(t *testing.T) {
	app := newFleetTestApp(t, "secret-token")
	payload := `{"name":"worker-7","url":"http://192.168.1.5:8081","system_info":{"os":"linux","arch":"amd64"},"containers":[{"slug":"storj","status":"running"}],"apps":["mysterium"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()

	app.handleWorkerHeartbeat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body %s)", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", body["status"])
	}

	devices := app.store.ListFleetDevices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 persisted device, got %d", len(devices))
	}
	d := devices[0]
	if d.Name != "worker-7" || d.Kind != "worker" || d.Status != "online" {
		t.Fatalf("unexpected persisted device: %+v", d)
	}
	if d.OS != "linux" || d.Arch != "amd64" || d.Endpoint != "http://192.168.1.5:8081" {
		t.Fatalf("unexpected device system info: %+v", d)
	}
	found := map[string]bool{}
	for _, svc := range d.Services {
		found[svc] = true
	}
	if !found["storj"] || !found["mysterium"] {
		t.Fatalf("expected services storj and mysterium, got %v", d.Services)
	}
}

func TestHandleWorkerHeartbeatRejectsBadToken(t *testing.T) {
	app := newFleetTestApp(t, "right-token")
	payload := `{"name":"worker-7"}`

	cases := []struct {
		name   string
		header string
	}{
		{"wrong token", "Bearer wrong-token"},
		{"missing header", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", strings.NewReader(payload))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			app.handleWorkerHeartbeat(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", w.Code)
			}
		})
	}
	if devices := app.store.ListFleetDevices(); len(devices) != 0 {
		t.Fatalf("expected no devices persisted for unauthorized requests, got %d", len(devices))
	}
}

func TestHandleWorkerHeartbeatRejectsNonPost(t *testing.T) {
	app := newFleetTestApp(t, "tok")
	req := httptest.NewRequest(http.MethodGet, "/api/workers/heartbeat", nil)
	w := httptest.NewRecorder()

	app.handleWorkerHeartbeat(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestHandleWorkerHeartbeatRejectsOversizedBody(t *testing.T) {
	app := newFleetTestApp(t, "tok")
	// A body larger than the 1 MiB cap must be rejected by MaxBytesReader,
	// which surfaces as a decode error -> 400.
	huge := `{"name":"` + strings.Repeat("a", (1<<20)+1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", strings.NewReader(huge))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()

	app.handleWorkerHeartbeat(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an oversized body, got %d", w.Code)
	}
	if devices := app.store.ListFleetDevices(); len(devices) != 0 {
		t.Fatalf("expected no device persisted for a rejected body, got %d", len(devices))
	}
}

// TestHandleWorkerHeartbeatEmptyKeyRejects pins that an App with no configured
// fleet API key rejects every heartbeat with 503 (the server is not configured to
// authenticate anyone), persisting nothing.
func TestHandleWorkerHeartbeatEmptyKeyRejects(t *testing.T) {
	app := newFleetTestApp(t, "")
	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", strings.NewReader(`{"name":"worker-7"}`))
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()

	app.handleWorkerHeartbeat(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no API key is configured, got %d", w.Code)
	}
	if devices := app.store.ListFleetDevices(); len(devices) != 0 {
		t.Fatalf("expected no device persisted for an unauthorized request, got %d", len(devices))
	}
}

// TestHandleWorkerHeartbeatNameFallsBackToClientID pins the name fallback: an
// empty name uses client_id as the device name.
func TestHandleWorkerHeartbeatNameFallsBackToClientID(t *testing.T) {
	app := newFleetTestApp(t, "tok")
	payload := `{"client_id":"abc","system_info":{"os":"linux","arch":"amd64"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()

	app.handleWorkerHeartbeat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body %s)", w.Code, w.Body.String())
	}
	devices := app.store.ListFleetDevices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 persisted device, got %d", len(devices))
	}
	if devices[0].Name != "abc" {
		t.Fatalf("expected the device name to fall back to client_id \"abc\", got %q", devices[0].Name)
	}
}

// TestHandleWorkerHeartbeatRequiresNameOrClientID pins the 400 when both name and
// client_id are empty, with nothing persisted.
func TestHandleWorkerHeartbeatRequiresNameOrClientID(t *testing.T) {
	app := newFleetTestApp(t, "tok")
	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()

	app.handleWorkerHeartbeat(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when name and client_id are both empty, got %d", w.Code)
	}
	if devices := app.store.ListFleetDevices(); len(devices) != 0 {
		t.Fatalf("expected no device persisted for a nameless heartbeat, got %d", len(devices))
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct{ header, want string }{
		{"Bearer abc", "abc"},
		{"Bearer ", ""},
		{"bearer abc", ""}, // prefix is case-sensitive
		{"Basic xyz", ""},
		{"", ""},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		if got := bearerToken(req); got != tc.want {
			t.Errorf("bearerToken(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestClassifyFleetAuth(t *testing.T) {
	const shared = "shared-key"
	ownHash := store.HashFleetKey("own-key")
	cases := []struct {
		name       string
		storedHash string
		confirmed  bool
		token      string
		want       fleetAuthAction
	}{
		{"unenrolled + shared -> enroll", "", false, shared, fleetAuthIssue},
		{"unenrolled + wrong -> reject", "", false, "nope", fleetAuthReject},
		{"unenrolled + empty -> reject", "", false, "", fleetAuthReject},
		{"own key -> ok", ownHash, false, "own-key", fleetAuthOK},
		{"own key while confirmed -> ok", ownHash, true, "own-key", fleetAuthOK},
		{"unconfirmed + shared -> reissue", ownHash, false, shared, fleetAuthIssue},
		{"confirmed + shared -> reject", ownHash, true, shared, fleetAuthReject},
		{"enrolled + wrong token -> reject", ownHash, false, "wrong", fleetAuthReject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyFleetAuth(tc.storedHash, tc.confirmed, tc.token, shared); got != tc.want {
				t.Errorf("classifyFleetAuth = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHandleWorkerHeartbeatEnrollConfirmReject exercises the full cutover through
// the HTTP handler: a device enrolls on the shared key, adopts its own key, and is
// then rejected when it falls back to the shared key.
func TestHandleWorkerHeartbeatEnrollConfirmReject(t *testing.T) {
	app := newFleetTestApp(t, "shared")
	post := func(token string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat",
			strings.NewReader(`{"name":"phone","system_info":{"os":"android"}}`))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		app.handleWorkerHeartbeat(w, req)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}

	// 1) Enroll on the shared key -> issued a worker_key.
	code, body := post("shared")
	if code != http.StatusOK {
		t.Fatalf("enroll: got %d (%v)", code, body)
	}
	key, _ := body["worker_key"].(string)
	if key == "" {
		t.Fatal("enroll must return a worker_key")
	}

	// 2) Authenticate with the OWN key -> ok, confirmed, no new key issued.
	code, body = post(key)
	if code != http.StatusOK {
		t.Fatalf("own-key heartbeat: got %d", code)
	}
	if _, ok := body["worker_key"]; ok {
		t.Fatalf("confirmed heartbeat must not re-issue a key, got %v", body["worker_key"])
	}

	// 3) The shared key is now rejected for the confirmed device.
	code, _ = post("shared")
	if code != http.StatusUnauthorized {
		t.Fatalf("confirmed device on shared key must be 401, got %d", code)
	}
}

// TestEnsureFleetAPIKeyMigratesLegacyPlaintext pins the storage-hardening migration:
// a legacy plaintext fleetApiKey found in config.json is preserved (moved into the
// keychain/file store so already-configured workers keep authenticating), stripped
// from config.json on disk, and reused on the next startup instead of regenerated.
// TestMain mocks the keyring, so keyring.MockInit resets it to a clean in-memory store.
func TestEnsureFleetAPIKeyMigratesLegacyPlaintext(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager error: %v", err)
	}
	c := cfg.Config()
	c.FleetAPIKey = "legacy-worker-token"
	if err := cfg.Save(c); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}

	app := &App{cfg: cfg}
	if err := app.ensureFleetAPIKey(); err != nil {
		t.Fatalf("ensureFleetAPIKey error: %v", err)
	}

	// The existing token is preserved in memory for the per-request bearer check.
	if app.fleetKey != "legacy-worker-token" {
		t.Fatalf("expected the legacy token to be preserved, got %q", app.fleetKey)
	}
	// It is no longer carried in the in-memory config...
	if got := cfg.Config().FleetAPIKey; got != "" {
		t.Fatalf("expected FleetAPIKey blanked in the in-memory config, got %q", got)
	}
	// ...nor on disk in config.json (omitempty drops the blanked field entirely).
	raw, err := os.ReadFile(filepath.Join(cfg.AppDir(), "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if strings.Contains(string(raw), "legacy-worker-token") || strings.Contains(string(raw), "fleetApiKey") {
		t.Fatalf("expected config.json to no longer carry the fleet token, got %s", raw)
	}
	// The migrated token is retrievable from the hardened store.
	stored, err := config.FleetKey(cfg.AppDir())
	if err != nil {
		t.Fatalf("config.FleetKey error: %v", err)
	}
	if stored != "legacy-worker-token" {
		t.Fatalf("expected the store to hold the migrated token, got %q", stored)
	}

	// A second startup reuses the stored token instead of regenerating it.
	app2 := &App{cfg: cfg}
	if err := app2.ensureFleetAPIKey(); err != nil {
		t.Fatalf("ensureFleetAPIKey (reload) error: %v", err)
	}
	if app2.fleetKey != "legacy-worker-token" {
		t.Fatalf("expected the stored token to be reused on the next startup, got %q", app2.fleetKey)
	}
}

// TestEnsureFleetAPIKeyGeneratesWhenAbsent pins that a fresh install with no legacy
// token gets a generated bearer token stored in the hardened store, and that the
// generated token never touches config.json.
func TestEnsureFleetAPIKeyGeneratesWhenAbsent(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager error: %v", err)
	}

	app := &App{cfg: cfg}
	if err := app.ensureFleetAPIKey(); err != nil {
		t.Fatalf("ensureFleetAPIKey error: %v", err)
	}
	if app.fleetKey == "" {
		t.Fatal("expected a generated fleet token, got empty")
	}
	if got := cfg.Config().FleetAPIKey; got != "" {
		t.Fatalf("expected the generated token to never touch config.json, got %q", got)
	}
	stored, err := config.FleetKey(cfg.AppDir())
	if err != nil {
		t.Fatalf("config.FleetKey error: %v", err)
	}
	if stored != app.fleetKey {
		t.Fatalf("expected the store to hold the generated token %q, got %q", app.fleetKey, stored)
	}
}

func TestHeartbeatKind(t *testing.T) {
	cases := []struct {
		name string
		body workerHeartbeat
		want string
	}{
		{"linux worker", workerHeartbeat{Name: "worker-1", SystemInfo: workerSystemInfo{OS: "linux"}}, "worker"},
		{"android os", workerHeartbeat{Name: "device", SystemInfo: workerSystemInfo{OS: "Android 13"}}, "mobile"},
		{"ios os", workerHeartbeat{Name: "device", SystemInfo: workerSystemInfo{OS: "iOS 17"}}, "mobile"},
		{"iphone name", workerHeartbeat{Name: "My iPhone", SystemInfo: workerSystemInfo{OS: ""}}, "mobile"},
		{"android name", workerHeartbeat{Name: "android-tablet", SystemInfo: workerSystemInfo{OS: ""}}, "mobile"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := heartbeatKind(tc.body); got != tc.want {
				t.Fatalf("heartbeatKind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHeartbeatServicesDeduplicates(t *testing.T) {
	body := workerHeartbeat{
		Containers: []workerContainer{{Slug: "a"}, {Slug: ""}, {Slug: "a"}, {Slug: "b"}},
		Apps:       []string{"b", "c", ""},
	}
	got := heartbeatServices(body)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestFleetUIURLIncludesPort(t *testing.T) {
	app := newFleetTestApp(t, "tok")
	url := app.fleetUIURL()
	if !strings.HasPrefix(url, "http://") {
		t.Fatalf("expected an http URL, got %q", url)
	}
	if !strings.Contains(url, ":8085") {
		t.Fatalf("expected the URL to include the fleet port 8085, got %q", url)
	}
}

// freeTCPPort reserves an ephemeral loopback port and releases it, returning the
// port number. applyDefaults coerces FleetPort<=0 back to 8085, so a literal
// port 0 can never reach net.Listen through the config; picking a known-free
// port keeps the bind conflict-proof in CI where 8085 may be in use.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func TestStartFleetAPIBindsAndCloses(t *testing.T) {
	app := newFleetTestApp(t, "tok")

	cfg := app.cfg.Config()
	cfg.FleetPort = freeTCPPort(t)
	if err := app.cfg.Save(cfg); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}

	if err := app.startFleetAPI(); err != nil {
		t.Fatalf("startFleetAPI error: %v", err)
	}
	if app.fleetAPI == nil {
		t.Fatal("expected fleetAPI to be set after a successful start")
	}
	addr := app.fleetAPI.addr
	if addr == "" {
		t.Fatal("expected the bound listener address to be recorded")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health error: %v", err)
	}
	statusCode := resp.StatusCode
	_ = resp.Body.Close()
	if statusCode != http.StatusOK {
		t.Fatalf("expected 200 from a live /api/health, got %d", statusCode)
	}

	if err := app.fleetAPI.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// After shutdown the listener is closed; a follow-up request must fail.
	if resp, err := client.Get("http://" + addr + "/api/health"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected the follow-up request to fail after Close")
	}
}

// TestHandleWorkerHeartbeatReissuesUntilConfirmed pins the reissue transition
// through the real handler+store: an unconfirmed device that comes back on the
// shared key is rotated to a fresh key, the old key goes stale, and confirming with
// the new key finalizes — all on a single device row.
func TestHandleWorkerHeartbeatReissuesUntilConfirmed(t *testing.T) {
	app := newFleetTestApp(t, "shared")
	post := func(token string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat",
			strings.NewReader(`{"name":"phone","system_info":{"os":"android"}}`))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		app.handleWorkerHeartbeat(w, req)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}

	code, body := post("shared")
	if code != http.StatusOK {
		t.Fatalf("enroll: got %d", code)
	}
	key1, _ := body["worker_key"].(string)
	if key1 == "" {
		t.Fatal("enroll must return a worker_key")
	}

	// Unconfirmed device returns on the shared key -> a FRESH, different key.
	code, body = post("shared")
	if code != http.StatusOK {
		t.Fatalf("reissue: got %d", code)
	}
	key2, _ := body["worker_key"].(string)
	if key2 == "" || key2 == key1 {
		t.Fatalf("reissue must rotate to a fresh key (key1=%q key2=%q)", key1, key2)
	}

	// The rotated-away key1 is now stale.
	if code, _ := post(key1); code != http.StatusUnauthorized {
		t.Fatalf("stale key1 must be 401, got %d", code)
	}
	// key2 works and confirms.
	if code, _ := post(key2); code != http.StatusOK {
		t.Fatalf("key2 must be accepted, got %d", code)
	}
	if devs := app.store.ListFleetDevices(); len(devs) != 1 {
		t.Fatalf("expected exactly 1 device row, got %d", len(devs))
	}
}

// TestHandleWorkerHeartbeatConcurrentEnroll drives concurrent first-contact
// heartbeats for one identity under -race; the fleetMu-serialized sequence must
// leave exactly one row in a consistent unconfirmed-with-key state (no duplicate
// rows, no corruption).
func TestHandleWorkerHeartbeatConcurrentEnroll(t *testing.T) {
	app := newFleetTestApp(t, "shared")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat",
				strings.NewReader(`{"name":"phone","system_info":{"os":"android"}}`))
			req.Header.Set("Authorization", "Bearer shared")
			w := httptest.NewRecorder()
			app.handleWorkerHeartbeat(w, req)
		}()
	}
	wg.Wait()

	if devs := app.store.ListFleetDevices(); len(devs) != 1 {
		t.Fatalf("concurrent enroll must not duplicate rows, got %d", len(devs))
	}
	hash, confirmed, err := app.store.FleetDeviceKeyState("mobile", "phone")
	if err != nil || hash == "" || confirmed {
		t.Fatalf("want a stored unconfirmed key, got hash=%q confirmed=%v (err %v)", hash, confirmed, err)
	}
}

func TestFleetRateLimiter(t *testing.T) {
	l := newFleetRateLimiter(2, time.Minute)
	now := time.Now()
	if !l.allow("1.2.3.4", now) || !l.allow("1.2.3.4", now) {
		t.Fatal("first two hits must be allowed")
	}
	if l.allow("1.2.3.4", now) {
		t.Fatal("third hit within the window must be blocked")
	}
	if !l.allow("5.6.7.8", now) {
		t.Fatal("a different IP has its own budget")
	}
	if !l.allow("1.2.3.4", now.Add(2*time.Minute)) {
		t.Fatal("hits must be allowed again after the window elapses")
	}
}

func TestHandleWorkerHeartbeatRateLimited(t *testing.T) {
	app := newFleetTestApp(t, "shared")
	app.fleetLimiter = newFleetRateLimiter(1, time.Minute)
	post := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat",
			strings.NewReader(`{"name":"phone","system_info":{"os":"android"}}`))
		req.Header.Set("Authorization", "Bearer shared")
		req.RemoteAddr = "10.0.0.1:5000"
		w := httptest.NewRecorder()
		app.handleWorkerHeartbeat(w, req)
		return w.Code
	}
	if code := post(); code != http.StatusOK {
		t.Fatalf("first heartbeat: got %d", code)
	}
	if code := post(); code != http.StatusTooManyRequests {
		t.Fatalf("second heartbeat over the limit must be 429, got %d", code)
	}
}

func TestFleetRateLimiterSweepsExpiredIPs(t *testing.T) {
	l := newFleetRateLimiter(5, time.Minute)
	old := time.Now().Add(-2 * time.Minute)
	// >1024 distinct, already-expired IPs trigger the whole-map sweep.
	for i := 0; i < 1100; i++ {
		l.allow(fmt.Sprintf("10.%d.%d.1", i/256, i%256), old)
	}
	if !l.allow("fresh-ip", time.Now()) {
		t.Fatal("a fresh IP must be allowed after the expired-entry sweep")
	}
}
