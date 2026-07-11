package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

type fleetAPIServer struct {
	server *http.Server
	addr   string
}

type workerHeartbeat struct {
	Name       string            `json:"name"`
	URL        string            `json:"url"`
	ClientID   string            `json:"client_id"`
	Containers []workerContainer `json:"containers"`
	Apps       []string          `json:"apps"`
	SystemInfo workerSystemInfo  `json:"system_info"`
}

type workerContainer struct {
	Slug   string `json:"slug"`
	Status string `json:"status"`
}

type workerSystemInfo struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Hostname string `json:"hostname"`
}

func (a *App) startFleetAPI() error {
	cfg := a.cfg.Config()
	mux := a.fleetMux(cfg.MetricsEnabled)

	addr := fmt.Sprintf("%s:%d", cfg.FleetBindAddress, cfg.FleetPort)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	a.fleetAPI = &fleetAPIServer{server: server, addr: listener.Addr().String()}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			a.emitError("fleet-api", err)
		}
	}()
	return nil
}

// fleetMux builds the fleet HTTP router. The Prometheus /metrics endpoint is
// OPT-IN: it is registered only when metricsEnabled is true, so with the default
// (disabled) the route does not exist and a scrape gets a 404 rather than the
// endpoint exposing earnings/health/fleet data. Extracted from startFleetAPI so
// the registration gate can be exercised in tests without binding a socket.
func (a *App) fleetMux(metricsEnabled bool) *http.ServeMux {
	mux := http.NewServeMux()
	// SECURITY-REVIEW: This desktop-local HTTP API accepts LAN worker/mobile input and is protected by a generated bearer token.
	mux.HandleFunc("/api/health", a.handleFleetHealth)
	mux.HandleFunc("/api/workers/heartbeat", a.handleWorkerHeartbeat)
	if metricsEnabled {
		mux.HandleFunc("/metrics", a.handleMetrics)
	}
	return mux
}

func (s *fleetAPIServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func (a *App) handleFleetHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "app": "CashPilot Desktop"})
}

func (a *App) handleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if a.fleetKey == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "fleet key not configured"})
		return
	}
	var body workerHeartbeat
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid heartbeat"})
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		body.Name = body.ClientID
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "worker name or client_id is required"})
		return
	}
	kind := heartbeatKind(body)

	// Per-worker fleet keys: classify this heartbeat from the device's stored key
	// state and the presented token BEFORE recording it (the classification needs
	// the device identity from the body).
	storedHash, confirmed, err := a.store.FleetDeviceKeyState(kind, name)
	if err != nil {
		a.emitError("fleet-heartbeat", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record heartbeat"})
		return
	}
	action := classifyFleetAuth(storedHash, confirmed, bearerToken(r), a.fleetKey)
	if action == fleetAuthReject {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid API key"})
		return
	}

	device, err := a.store.UpsertFleetHeartbeat(store.FleetDevice{
		Name:     name,
		Kind:     kind,
		Endpoint: strings.TrimSpace(body.URL),
		OS:       body.SystemInfo.OS,
		Arch:     body.SystemInfo.Arch,
		Status:   "online",
		Services: heartbeatServices(body),
		LastSeen: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		a.emitError("fleet-heartbeat", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record heartbeat"})
		return
	}

	resp := map[string]any{"status": "ok", "worker_id": device.ID}
	switch action {
	case fleetAuthIssue:
		// Enroll (or reissue a fresh key for an unconfirmed device that came back
		// on the shared key): mint a key, store its hash, and hand it back once.
		key, err := newFleetKey()
		if err == nil {
			err = a.store.SetFleetDeviceKey(kind, name, store.HashFleetKey(key))
		}
		if err != nil {
			a.emitError("fleet-heartbeat", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not issue worker key"})
			return
		}
		resp["worker_key"] = key
	case fleetAuthOK:
		// The device authenticated with its own key — finalize the cutover so the
		// shared bootstrap key is refused for it from now on.
		if err := a.store.ConfirmFleetDeviceKey(kind, name); err != nil {
			a.emitError("fleet-heartbeat", err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// fleetAuthAction is how a heartbeat should be treated under the per-worker-key protocol.
type fleetAuthAction int

const (
	fleetAuthReject fleetAuthAction = iota
	fleetAuthIssue                  // enroll, or reissue a fresh key to an unconfirmed device
	fleetAuthOK                     // authenticated with its own key -> confirm
)

// classifyFleetAuth decides how to treat a heartbeat from the device's stored
// per-worker key hash + confirmed flag and the presented bearer token:
//   - the token hashes to the stored key -> OK (confirm)
//   - no key yet + the shared bootstrap key -> Issue (enroll)
//   - an UNCONFIRMED key + the shared key -> Issue (reissue a fresh key)
//   - otherwise (a confirmed device on the shared key, or any wrong token) -> Reject
func classifyFleetAuth(storedHash string, confirmed bool, presentedToken, sharedKey string) fleetAuthAction {
	if presentedToken != "" && storedHash != "" &&
		subtle.ConstantTimeCompare([]byte(store.HashFleetKey(presentedToken)), []byte(storedHash)) == 1 {
		return fleetAuthOK
	}
	sharedValid := presentedToken != "" && sharedKey != "" &&
		subtle.ConstantTimeCompare([]byte(presentedToken), []byte(sharedKey)) == 1
	if storedHash == "" {
		if sharedValid {
			return fleetAuthIssue
		}
		return fleetAuthReject
	}
	if !confirmed && sharedValid {
		return fleetAuthIssue
	}
	return fleetAuthReject
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// newFleetKey returns a fresh high-entropy per-worker fleet key.
func newFleetKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func heartbeatKind(body workerHeartbeat) string {
	osValue := strings.ToLower(body.SystemInfo.OS)
	nameValue := strings.ToLower(body.Name)
	if strings.Contains(osValue, "android") || strings.Contains(osValue, "ios") || strings.Contains(nameValue, "android") || strings.Contains(nameValue, "iphone") {
		return "mobile"
	}
	return "worker"
}

func heartbeatServices(body workerHeartbeat) []string {
	seen := map[string]bool{}
	var out []string
	for _, container := range body.Containers {
		slug := strings.TrimSpace(container.Slug)
		if slug != "" && !seen[slug] {
			seen[slug] = true
			out = append(out, slug)
		}
	}
	for _, app := range body.Apps {
		slug := strings.TrimSpace(app)
		if slug != "" && !seen[slug] {
			seen[slug] = true
			out = append(out, slug)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (a *App) fleetUIURL() string {
	cfg := a.cfg.Config()
	host := firstLANAddress()
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, cfg.FleetPort)
}

func firstLANAddress() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil {
			return addr.IP.String()
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip != nil {
				return ip.String()
			}
		}
	}
	return ""
}
