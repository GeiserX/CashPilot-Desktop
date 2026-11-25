package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// The server-commands API: while this computer is joined to a CashPilot server, that
// server deploys, stops, starts, restarts and removes services here, as it does on a
// CashPilot Docker worker. The routes, request bodies, answers and status codes are
// the web worker's (app/worker_api.py), so the server's own proxy code drives this
// computer unchanged.
//
// What the server sends is a request, not an order this computer obeys blindly:
//   - A deploy names a service of THIS computer's catalog. The image, the
//     capabilities, the devices, the ports and the fence come from that catalog
//     entry and its digest pin, never from the request; only the variables do.
//   - The deploy runs through the same path as the app's own Deploy button
//     (services.Manager), so every local rule holds: retired and manual services,
//     required fields, routes through a proxy, a restore in progress.
//   - Deleting data that cannot be recovered is refused: that yes is given on this
//     computer.
//
// Only the key the server issued to this computer at the join opens it. The shared
// key every box enrols with does not. It listens on private LAN and Tailscale
// addresses only (worker_api_net.go) and refuses a request from any other source.
// It runs only while joined and the dashboard switch is on, which it is by default.

// workerAPIRescan is how often the API re-reads the config and the machine's
// addresses: a join or a leave saved by the other process, a new DHCP lease, a
// tailnet that came up after the app.
var workerAPIRescan = 30 * time.Second

// listenTCP opens a listener. A var so tests can watch which addresses the API asks
// for without owning them.
var listenTCP = net.Listen

// workerAPIBodyLimit caps a request body. A deploy spec is a few kilobytes.
const workerAPIBodyLimit = 1 << 20

// workerAPIState is the API's listeners and what the dashboard shows about them.
type workerAPIState struct {
	mu sync.Mutex
	// listeners are the servers this process runs, by address.
	listeners map[netip.Addr]*http.Server
	// elsewhere are the addresses the other CashPilot process (the window or the
	// background helper) serves the API on, read from its health answer.
	elsewhere map[netip.Addr]bool
	// problem says why the API is not reachable, in plain words; "" when it is.
	problem string
	cancel  context.CancelFunc
	done    chan struct{}
	limiter *fleetRateLimiter
	// keyReadAt limits how often a key that does not match makes the API read the
	// saved key again, so a stream of wrong keys cannot hammer the keychain.
	keyReadAt time.Time
}

// ServerCommand is the last command the server sent, for the dashboard.
type ServerCommand struct {
	At     string `json:"at"`
	Action string `json:"action"`
	Slug   string `json:"slug"`
	OK     bool   `json:"ok"`
	// Detail is the answer in plain words: what was done, or why not.
	Detail string `json:"detail"`
}

// ServerCommandsState is what the dashboard shows while this computer is joined.
type ServerCommandsState struct {
	// Server is the server's host, as the join screen names it.
	Server string `json:"server"`
	// On is the switch.
	On bool `json:"on"`
	// Listening is true when this computer serves the API, here or in the other
	// CashPilot process.
	Listening bool `json:"listening"`
	// URL is the address the heartbeat reports to the server.
	URL     string `json:"url"`
	Problem string `json:"problem"`
	// FirewallNeeded is true on Windows, where the API needs a firewall rule;
	// FirewallRule is true once CashPilot made it. Opens says what the rule opens.
	FirewallNeeded bool           `json:"firewallNeeded"`
	FirewallRule   bool           `json:"firewallRule"`
	Opens          string         `json:"opens"`
	Last           *ServerCommand `json:"last"`
}

// startWorkerAPI starts the loop that keeps the API in step with the config and the
// machine's addresses. Idempotent.
func (a *App) startWorkerAPI(ctx context.Context) {
	a.stopWorkerAPI()
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	a.workerAPI.mu.Lock()
	a.workerAPI.cancel, a.workerAPI.done = cancel, done
	a.workerAPI.mu.Unlock()
	a.syncWorkerAPI()
	go func() {
		defer close(done)
		ticker := time.NewTicker(workerAPIRescan)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				a.refreshConfig()
				a.syncWorkerAPI()
			}
		}
	}()
}

// stopWorkerAPI stops the loop and every listener.
func (a *App) stopWorkerAPI() {
	a.workerAPI.mu.Lock()
	cancel, done := a.workerAPI.cancel, a.workerAPI.done
	a.workerAPI.cancel, a.workerAPI.done = nil, nil
	a.workerAPI.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	a.closeWorkerListeners(nil)
}

// workerAPIWanted reports whether the API should run: joined, and the switch on.
func (a *App) workerAPIWanted() bool {
	return a.cfg != nil && a.joined() && !a.cfg.Config().ServerCommandsOff
}

// syncWorkerAPI opens a listener on each private address that has none and closes
// the ones the API should no longer have.
func (a *App) syncWorkerAPI() {
	var want []netip.Addr
	if a.workerAPIWanted() {
		want = listenAddrs(machineAddrs())
	}
	a.closeWorkerListeners(want)
	if len(want) == 0 {
		a.workerAPI.mu.Lock()
		a.workerAPI.elsewhere, a.workerAPI.problem = nil, ""
		if a.workerAPIWanted() {
			a.workerAPI.problem = "This computer has no private network or Tailscale address, so your CashPilot cannot reach it."
		}
		a.workerAPI.mu.Unlock()
		return
	}
	elsewhere := map[netip.Addr]bool{}
	var failures []string
	for _, ip := range want {
		a.workerAPI.mu.Lock()
		_, have := a.workerAPI.listeners[ip]
		a.workerAPI.mu.Unlock()
		if have {
			continue
		}
		err := a.listenWorkerAPI(ip)
		if err == nil {
			continue
		}
		if a.otherProcessServes(ip) {
			elsewhere[ip] = true
			continue
		}
		failures = append(failures, err.Error())
	}
	a.workerAPI.mu.Lock()
	a.workerAPI.elsewhere = elsewhere
	a.workerAPI.problem = ""
	if len(a.workerAPI.listeners) == 0 && len(elsewhere) == 0 && len(failures) > 0 {
		a.workerAPI.problem = fmt.Sprintf("CashPilot could not listen on port %d: %s", workerAPIPort, failures[0])
	}
	a.workerAPI.mu.Unlock()
}

// closeWorkerListeners closes every listener whose address is not in keep.
func (a *App) closeWorkerListeners(keep []netip.Addr) {
	a.workerAPI.mu.Lock()
	var closing []*http.Server
	for ip, srv := range a.workerAPI.listeners {
		if !slices.Contains(keep, ip) {
			closing = append(closing, srv)
			delete(a.workerAPI.listeners, ip)
		}
	}
	a.workerAPI.mu.Unlock()
	for _, srv := range closing {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
}

// listenWorkerAPI opens one listener on ip.
func (a *App) listenWorkerAPI(ip netip.Addr) error {
	addr := net.JoinHostPort(ip.String(), strconv.Itoa(workerAPIPort))
	ln, err := listenTCP("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           a.workerAPIMux(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No write timeout: a deploy pulls an image and waits for the old container
		// to stop, and the server waits for it as long as the catalog says.
		IdleTimeout: 60 * time.Second,
	}
	a.workerAPI.mu.Lock()
	if a.workerAPI.listeners == nil {
		a.workerAPI.listeners = map[netip.Addr]*http.Server{}
	}
	a.workerAPI.listeners[ip] = srv
	a.workerAPI.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server commands: %s stopped: %v", addr, err)
		}
	}()
	return nil
}

// workerAPIIdentity is what the unauthenticated health answer says about who serves
// it: a digest of this computer's own key, so the window and the background helper,
// which hold the same key, recognise each other, and nobody else learns anything.
func workerAPIIdentity(workerKey string) string {
	if strings.TrimSpace(workerKey) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("cashpilot-desktop server commands\x00" + strings.TrimSpace(workerKey)))
	return hex.EncodeToString(sum[:16])
}

// otherProcessServes reports whether the port on ip is held by the other CashPilot
// process of this computer. Only then does this process report that address: a port
// another program holds must never receive the server's commands and its key.
func (a *App) otherProcessServes(ip netip.Addr) bool {
	want := workerAPIIdentity(a.currentWorkerKey())
	if want == "" {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(ip.String(), strconv.Itoa(workerAPIPort)) + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(body.ID), []byte(want)) == 1
}

// currentWorkerKey is the key the server issued to this computer, "" when none.
func (a *App) currentWorkerKey() string {
	a.upstream.mu.Lock()
	key := a.upstream.workerKey
	a.upstream.mu.Unlock()
	if strings.TrimSpace(key) != "" || a.cfg == nil {
		return strings.TrimSpace(key)
	}
	saved, err := config.UpstreamWorkerKey(a.cfg.AppDir())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(saved)
}

// workerAPIURL is the address the heartbeat reports, "" when the API is not reachable.
func (a *App) workerAPIURL(serverURL string) string {
	a.workerAPI.mu.Lock()
	var serving []netip.Addr
	for ip := range a.workerAPI.listeners {
		serving = append(serving, ip)
	}
	for ip := range a.workerAPI.elsewhere {
		serving = append(serving, ip)
	}
	a.workerAPI.mu.Unlock()
	if len(serving) == 0 {
		return ""
	}
	slices.SortFunc(serving, func(x, y netip.Addr) int { return x.Compare(y) })
	host := upstream.Host(serverURL)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := pickURLAddr(serving, routeAddr(host))
	if !ip.IsValid() {
		return ""
	}
	return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(workerAPIPort))
}

// --- HTTP ---------------------------------------------------------------------

// workerAPIMux is the router. Every route but /api/health checks the source and the key.
func (a *App) workerAPIMux() *http.ServeMux {
	mux := http.NewServeMux()
	// SECURITY-REVIEW: LAN input. Private sources only, and the per-worker key the
	// server issued at the join, compared in constant time.
	mux.HandleFunc("GET /api/health", a.handleWorkerHealth)
	mux.HandleFunc("GET /api/status", a.guard(a.handleWorkerStatus))
	mux.HandleFunc("GET /api/containers", a.guard(a.handleWorkerContainers))
	mux.HandleFunc("POST /api/containers/{slug}/deploy", a.guard(a.handleWorkerDeploy))
	mux.HandleFunc("POST /api/containers/{slug}/{action}", a.guard(a.handleWorkerAction))
	mux.HandleFunc("DELETE /api/containers/{slug}", a.guard(a.handleWorkerRemove))
	mux.HandleFunc("GET /api/containers/{slug}/logs", a.guard(a.handleWorkerLogs))
	return mux
}

// detail writes an error the way FastAPI does, so the server reads it as it reads
// the web worker's: {"detail": text} or {"detail": {...}}.
func detail(w http.ResponseWriter, status int, d any) {
	writeJSON(w, status, map[string]any{"detail": d})
}

// sourceAllowed reports whether a request comes from a private LAN or Tailscale
// address.
func sourceAllowed(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return isPrivateAddr(ip)
}

// keyMatches compares the bearer token with key in constant time.
func keyMatches(header, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte("Bearer "+key)) == 1
}

// guard refuses a request from a public source, while not joined or switched off,
// and without this computer's own key.
func (a *App) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sourceAllowed(r.RemoteAddr) {
			detail(w, http.StatusForbidden, "Requests are accepted from private networks and Tailscale only")
			return
		}
		a.workerAPI.mu.Lock()
		if a.workerAPI.limiter == nil {
			a.workerAPI.limiter = newFleetRateLimiter(120, time.Minute)
		}
		limiter := a.workerAPI.limiter
		a.workerAPI.mu.Unlock()
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && !limiter.allow(host, time.Now()) {
			detail(w, http.StatusTooManyRequests, "Too many requests")
			return
		}
		// The other process may have left the server or turned the switch off.
		a.refreshConfig()
		if !a.workerAPIWanted() {
			go a.syncWorkerAPI()
			detail(w, http.StatusServiceUnavailable, "This computer does not take commands from a CashPilot right now")
			return
		}
		key := a.currentWorkerKey()
		if key == "" {
			detail(w, http.StatusServiceUnavailable, "This computer has no key from your CashPilot yet")
			return
		}
		auth := r.Header.Get("Authorization")
		if !keyMatches(auth, key) && !a.keyMatchesSaved(auth) {
			detail(w, http.StatusUnauthorized, "Invalid API key")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, workerAPIBodyLimit)
		next(w, r)
	}
}

// keyMatchesSaved reads the saved key again, at most every 10 seconds, for the case
// where the other process got a new key from the server since this one read it.
func (a *App) keyMatchesSaved(auth string) bool {
	a.workerAPI.mu.Lock()
	if time.Since(a.workerAPI.keyReadAt) < 10*time.Second {
		a.workerAPI.mu.Unlock()
		return false
	}
	a.workerAPI.keyReadAt = time.Now()
	a.workerAPI.mu.Unlock()
	saved, err := config.UpstreamWorkerKey(a.cfg.AppDir())
	if err != nil || !keyMatches(auth, saved) {
		return false
	}
	a.upstream.mu.Lock()
	a.upstream.workerKey = strings.TrimSpace(saved)
	a.upstream.mu.Unlock()
	return true
}

func (a *App) handleWorkerHealth(w http.ResponseWriter, r *http.Request) {
	if !sourceAllowed(r.RemoteAddr) {
		detail(w, http.StatusForbidden, "Requests are accepted from private networks and Tailscale only")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "id": workerAPIIdentity(a.currentWorkerKey())})
}

// workerContainer is one container in the web worker's /api/containers shape.
type workerAPIContainer struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	ContainerID string `json:"container_id"`
	Image       string `json:"image"`
	Instance    int    `json:"instance"`
}

func (a *App) listForServer(ctx context.Context) ([]workerAPIContainer, error) {
	list, err := a.services.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]workerAPIContainer, 0, len(list))
	for _, c := range list {
		if strings.TrimSpace(c.Slug) == "" {
			continue
		}
		id := c.ContainerID
		if len(id) > 12 {
			id = id[:12]
		}
		out = append(out, workerAPIContainer{Slug: c.Slug, Name: c.Name, Status: c.Status, ContainerID: id, Image: c.Image, Instance: max(c.Instance, 1)})
	}
	return out, nil
}

func (a *App) handleWorkerStatus(w http.ResponseWriter, r *http.Request) {
	list, _ := a.listForServer(r.Context())
	running := 0
	for _, c := range list {
		if c.Status == "running" {
			running++
		}
	}
	payload := a.upstreamPayload()
	writeJSON(w, http.StatusOK, map[string]any{
		"name":             payload.Name,
		"client_id":        payload.ClientID,
		"docker_available": a.runtime.Status(r.Context()).Available,
		"ui_connected":     a.pairingConnected(),
		"proxied_only":     services.ProxiedOnly(),
		"container_count":  len(list),
		"running_count":    running,
	})
}

// pairingConnected is whether the server accepted the last heartbeat.
func (a *App) pairingConnected() bool {
	p := a.pairingStatus()
	return p != nil && p.LastHeartbeat != "" && p.LastError == ""
}

func (a *App) handleWorkerContainers(w http.ResponseWriter, r *http.Request) {
	list, err := a.listForServer(r.Context())
	if err != nil {
		detail(w, http.StatusServiceUnavailable, oneLine(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// workerDeploySpec is the part of the web worker's DeploySpec this computer reads.
// Everything else (ports, volumes, capabilities, devices, the command) comes from
// this computer's own catalog entry.
type workerDeploySpec struct {
	Image    string            `json:"image"`
	Env      map[string]string `json:"env"`
	Instance int               `json:"instance"`
}

// imageRepository is an image reference without its tag and digest, and without the
// Docker Hub prefixes that name the same repository.
func imageRepository(ref string) string {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if c := strings.LastIndex(ref, ":"); c > strings.LastIndex(ref, "/") {
		ref = ref[:c]
	}
	for _, prefix := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		ref = strings.TrimPrefix(ref, prefix)
	}
	return strings.TrimPrefix(ref, "library/")
}

// catalogImage reports whether image names a repository the catalog entry runs.
func catalogImage(svc catalog.Service, image string) bool {
	want := imageRepository(image)
	if want == "" {
		return false
	}
	if imageRepository(svc.Docker.Image) == want {
		return true
	}
	for _, ref := range svc.Docker.ImageByArch {
		if imageRepository(ref) == want {
			return true
		}
	}
	return false
}

func (a *App) handleWorkerDeploy(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	var spec workerDeploySpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		detail(w, http.StatusUnprocessableEntity, "The deploy request is not valid JSON in the expected shape")
		return
	}
	status, answer, ok := a.deployForServer(slug, spec)
	a.recordServerCommand("deploy", slug, ok, answer)
	if !ok {
		detail(w, status, answer)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deployed", "container_id": answer})
}

// deployForServer runs one deploy the server asked for. It answers the HTTP status,
// and the container id on success or the refusal in plain words.
func (a *App) deployForServer(slug string, spec workerDeploySpec) (int, string, bool) {
	instance := spec.Instance
	if instance == 0 {
		instance = 1
	}
	if instance != 1 {
		return http.StatusBadRequest, "CashPilot Desktop takes one copy per deploy from your CashPilot; add more copies on this computer", false
	}
	svc, known := a.catalog.Get(slug)
	if !known || catalog.IsCustomSlug(slug) || svc.Custom {
		return http.StatusForbidden, fmt.Sprintf("'%s' is not in this computer's catalog, so it cannot be deployed here", slug), false
	}
	if svc.ManualOnly {
		return http.StatusForbidden, fmt.Sprintf("%s has no container, so it cannot be deployed from your CashPilot", svc.Name), false
	}
	if !catalogImage(svc, spec.Image) {
		return http.StatusForbidden, fmt.Sprintf("'%s' runs only the image this computer's catalog pins", slug), false
	}
	if err := a.services.ValidateDeploy(slug, spec.Env); err != nil {
		return http.StatusBadRequest, oneLine(err.Error()), false
	}
	if err := a.store.SaveCredentials(slug, spec.Env); err != nil {
		return http.StatusInternalServerError, "Container deployment failed", false
	}
	// "" is the catalog's image, with this computer's digest pin.
	catalogOnly := ""
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	dep, err := a.services.DeployWith(ctx, slug, spec.Env, services.DeployOptions{Image: &catalogOnly})
	if err != nil {
		return http.StatusConflict, oneLine(err.Error()), false
	}
	a.emitEvent("deployment:changed", dep)
	id := dep.ContainerID
	if len(id) > 12 {
		id = id[:12]
	}
	return http.StatusOK, id, true
}

// instanceParam reads ?instance=, 1 when absent, as the web worker validates it.
func instanceParam(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("instance"))
	if raw == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > services.MaxInstances {
		return 0, fmt.Errorf("instance must be between 1 and %d", services.MaxInstances)
	}
	return n, nil
}

// deployedHere reports whether slug runs here, as a container or a routed copy.
func (a *App) deployedHere(slug string) bool {
	if _, ok, err := a.store.GetDeployment(slug); err == nil && ok {
		return true
	}
	_, routed := a.services.Route(slug)
	return routed
}

func (a *App) handleWorkerAction(w http.ResponseWriter, r *http.Request) {
	slug, action := r.PathValue("slug"), r.PathValue("action")
	done := map[string]string{"stop": "stopped", "start": "started", "restart": "restarted"}[action]
	if done == "" {
		detail(w, http.StatusNotFound, "Not Found")
		return
	}
	n, err := instanceParam(r)
	if err != nil {
		detail(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.deployedHere(slug) {
		msg := fmt.Sprintf("%s is not deployed on this computer", slug)
		a.recordServerCommand(action, slug, false, msg)
		detail(w, http.StatusNotFound, msg)
		return
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	switch {
	case n == 1 && action == "stop":
		err = a.services.Stop(ctx, slug)
	case n == 1 && action == "start":
		err = a.services.Start(ctx, slug)
	case n == 1:
		err = a.services.Restart(ctx, slug)
	case action == "stop":
		err = a.services.StopCopy(ctx, slug, n)
	case action == "start":
		err = a.services.StartCopy(ctx, slug, n)
	default:
		if err = a.services.StopCopy(ctx, slug, n); err == nil {
			err = a.services.StartCopy(ctx, slug, n)
		}
	}
	if err != nil {
		a.recordServerCommand(action, slug, false, oneLine(err.Error()))
		detail(w, http.StatusConflict, oneLine(err.Error()))
		return
	}
	a.emitEvent("deployment:changed", slug)
	a.recordServerCommand(action, slug, true, done)
	writeJSON(w, http.StatusOK, map[string]string{"status": done})
}

func (a *App) handleWorkerRemove(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	q := r.URL.Query()
	deleteData := q.Get("delete_volumes") == "true"
	if q.Get("allow_delete_critical") == "true" {
		msg := "Deleting data that cannot be recovered is done on this computer, not from your CashPilot. Nothing was removed."
		a.recordServerCommand("remove", slug, false, msg)
		detail(w, http.StatusForbidden, map[string]string{"error": "step_up_rejected", "message": msg})
		return
	}
	if q.Get("only_if_restore_unfinished") == "true" {
		detail(w, http.StatusConflict, map[string]string{"error": "restore_cleared", "message": "No restore that did not finish is on this computer. Nothing was removed."})
		return
	}
	if !a.deployedHere(slug) {
		msg := fmt.Sprintf("%s is not deployed on this computer", slug)
		a.recordServerCommand("remove", slug, false, msg)
		detail(w, http.StatusNotFound, msg)
		return
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if deleteData {
		plan, err := a.services.PlanRemoval(ctx, slug)
		if err != nil {
			a.recordServerCommand("remove", slug, false, oneLine(err.Error()))
			detail(w, http.StatusServiceUnavailable, oneLine(err.Error()))
			return
		}
		var blocked []map[string]string
		for _, v := range plan.Volumes {
			if v.Critical || !plan.CatalogKnown {
				blocked = append(blocked, map[string]string{"volume": v.Source, "target": v.Target, "holds": v.Holds})
			}
		}
		if len(blocked) > 0 {
			msg := fmt.Sprintf("%s keeps data that cannot be recovered, so nothing was removed", slug)
			a.recordServerCommand("remove", slug, false, msg)
			detail(w, http.StatusConflict, map[string]any{
				"error":        "critical_volume",
				"message":      msg,
				"blocked":      blocked,
				"critical_set": "",
				"hint":         "Nothing was removed. Remove it without its data, or delete the data on this computer.",
			})
			return
		}
	}
	if err := a.services.Remove(ctx, slug, deleteData, false); err != nil {
		a.recordServerCommand("remove", slug, false, oneLine(err.Error()))
		detail(w, http.StatusConflict, oneLine(err.Error()))
		return
	}
	a.emitEvent("deployment:changed", slug)
	a.recordServerCommand("remove", slug, true, "removed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (a *App) handleWorkerLogs(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	n, err := instanceParam(r)
	if err != nil {
		detail(w, http.StatusBadRequest, err.Error())
		return
	}
	lines := 50
	if raw := r.URL.Query().Get("lines"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			lines = min(v, 1000)
		}
	}
	if !a.deployedHere(slug) {
		detail(w, http.StatusNotFound, fmt.Sprintf("%s is not deployed on this computer", slug))
		return
	}
	var logs string
	if n == 1 {
		logs, err = a.services.Logs(r.Context(), slug, lines)
	} else {
		logs, err = a.services.LogsCopy(r.Context(), slug, n, lines)
	}
	if err != nil {
		detail(w, http.StatusServiceUnavailable, oneLine(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

// oneLine is an error on one line, capped, as the web worker sends its refusals.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:299] + "…"
	}
	return s
}

// --- The last command, for the dashboard ---------------------------------------

// lastCommandFile is where the last command is kept: a file, so the window shows
// a command the background helper served.
func (a *App) lastCommandFile() string {
	return filepath.Join(a.cfg.DataDir(), "server_commands_last.json")
}

func (a *App) recordServerCommand(action, slug string, ok bool, answer string) {
	if ok && action == "deploy" {
		// A deploy's answer is its container id; the dashboard says "deployed".
		answer = "deployed"
	}
	entry := ServerCommand{At: time.Now().UTC().Format(time.RFC3339), Action: action, Slug: slug, OK: ok, Detail: answer}
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	path := a.lastCommandFile()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("server commands: could not record the last command: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("server commands: could not record the last command: %v", err)
	}
	log.Printf("server commands: %s %s: ok=%v", action, slug, ok)
}

func (a *App) lastServerCommand() *ServerCommand {
	raw, err := os.ReadFile(a.lastCommandFile())
	if err != nil {
		return nil
	}
	var entry ServerCommand
	if json.Unmarshal(raw, &entry) != nil || entry.Action == "" {
		return nil
	}
	return &entry
}

// --- What the dashboard shows, and the switch ----------------------------------

// serverCommandsState is the dashboard's view; call only while joined.
func (a *App) serverCommandsState() ServerCommandsState {
	serverURL := strings.TrimRight(strings.TrimSpace(a.cfg.Config().UpstreamURL), "/")
	cfg := a.cfg.Config()
	a.workerAPI.mu.Lock()
	listening := len(a.workerAPI.listeners) > 0 || len(a.workerAPI.elsewhere) > 0
	problem := a.workerAPI.problem
	a.workerAPI.mu.Unlock()
	out := ServerCommandsState{
		Server:         upstream.Host(serverURL),
		On:             !cfg.ServerCommandsOff,
		Listening:      listening,
		Problem:        problem,
		FirewallNeeded: firewallRuleNeeded,
		FirewallRule:   strings.TrimSpace(cfg.ServerCommandsFirewallRule) != "",
		Opens:          fmt.Sprintf("incoming TCP port %d to CashPilot only, on networks Windows calls private, from %s", workerAPIPort, strings.Join(privateSubnets(), ", ")),
		Last:           a.lastServerCommand(),
	}
	if listening {
		out.URL = a.workerAPIURL(serverURL)
	}
	return out
}

// serverCommandsView is serverCommandsState for AppState: nil unless joined.
func (a *App) serverCommandsView() *ServerCommandsState {
	if !a.joined() {
		return nil
	}
	s := a.serverCommandsState()
	return &s
}

// SetServerCommands is the dashboard switch. Off closes the API at once; on opens it.
// The next heartbeat, sent now, tells the server.
func (a *App) SetServerCommands(on bool) (ServerCommandsState, error) {
	if err := a.ready(); err != nil {
		return ServerCommandsState{}, err
	}
	if !a.joined() {
		return ServerCommandsState{}, errors.New("this computer is not joined to a CashPilot")
	}
	if err := a.cfg.Update(func(c *config.AppConfig) { c.ServerCommandsOff = !on }); err != nil {
		return ServerCommandsState{}, err
	}
	a.syncWorkerAPI()
	if a.ctx != nil {
		a.startUpstream(a.ctx)
	}
	return a.serverCommandsState(), nil
}
