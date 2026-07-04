package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	c.FleetAPIKey = apiKey
	c.FleetBindAddress = "127.0.0.1"
	if err := cfg.Save(c); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open error: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &App{cfg: cfg, store: st}
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
// fleet API key rejects every heartbeat: validFleetBearer returns false when the
// key is empty, so even a "Bearer " header gets a 401.
func TestHandleWorkerHeartbeatEmptyKeyRejects(t *testing.T) {
	app := newFleetTestApp(t, "")
	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", strings.NewReader(`{"name":"worker-7"}`))
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()

	app.handleWorkerHeartbeat(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when no API key is configured, got %d", w.Code)
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

func TestValidFleetBearer(t *testing.T) {
	app := newFleetTestApp(t, "abc123")

	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", nil)
	req.Header.Set("Authorization", "Bearer abc123")
	if !app.validFleetBearer(req) {
		t.Fatal("expected the valid bearer token to be accepted")
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", nil)
	req2.Header.Set("Authorization", "Bearer nope")
	if app.validFleetBearer(req2) {
		t.Fatal("expected the wrong bearer token to be rejected")
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
