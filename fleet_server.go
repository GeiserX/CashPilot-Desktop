package main

import (
	"context"
	"crypto/subtle"
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
	if !a.validFleetBearer(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid API key"})
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
	if strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "worker name or client_id is required"})
		return
	}
	device, err := a.store.UpsertFleetHeartbeat(store.FleetDevice{
		Name:     strings.TrimSpace(body.Name),
		Kind:     heartbeatKind(body),
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
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "worker_id": device.ID})
}

func (a *App) validFleetBearer(r *http.Request) bool {
	key := a.fleetKey
	if key == "" {
		return false
	}
	expected := "Bearer " + key
	got := r.Header.Get("Authorization")
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
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
