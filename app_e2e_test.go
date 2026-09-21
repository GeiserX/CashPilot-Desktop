package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness"
	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/exchange"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// This file drives the App itself — the object the user interface calls — against
// the fakes in e2e/harness. The tests in e2e/ cover the engine underneath; these
// cover the layer on top of it, which is where the app decides what to save, what
// to collect and what to show.
//
// It lives at the repository root because package main cannot be imported. The
// keyring is already mocked for this package by TestMain in fleet_server_test.go,
// so nothing here touches the real OS keychain.

// appUnderTest is the App plus the fakes it was wired to.
type appUnderTest struct {
	*App
	Docker    *harness.Docker
	Providers *harness.Providers
}

// newAppUnderTest builds the App exactly as startCore does — same config, store,
// catalog, runtime provider, manager, collectors and rate service — but pointed at
// the fakes, and without the background loops startCore also launches (the
// scheduler and the periodic rate refresh), which would otherwise run on their own
// timers in the middle of the assertions.
func newAppUnderTest(t *testing.T) *appUnderTest {
	t.Helper()

	docker := harness.NewDocker()
	t.Cleanup(docker.Close)
	providers := harness.NewProviders()
	t.Cleanup(providers.Close)

	t.Setenv("DOCKER_HOST", docker.Host())
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())

	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded: %v", err)
	}

	dockerProvider := runtime.NewDockerProvider()
	app := &App{
		ctx:      context.Background(),
		cfg:      cfg,
		store:    st,
		catalog:  cat,
		runtime:  dockerProvider,
		services: services.NewManager(dockerProvider, cat, st),
		collectors: collectors.NewRegistry(st,
			collectors.WithHTTPClient(providers.Client())),
		exchange: exchange.NewService(
			exchange.WithBaseURLs(providers.URL(), providers.URL()),
			exchange.WithHTTPClient(providers.Client()),
		),
	}
	return &appUnderTest{App: app, Docker: docker, Providers: providers}
}

// TestTheAppRunsAnEarnerEndToEnd is the path a user takes on the first day: pick a
// service, give it credentials, deploy it, see a balance, then stop and remove it.
// Every step goes through the binding the interface actually calls.
func TestTheAppRunsAnEarnerEndToEnd(t *testing.T) {
	app := newAppUnderTest(t)
	app.Providers.SetBalance("honeygain", 12.50)

	services, err := app.ListServices()
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(services) == 0 {
		t.Fatal("the service list is empty")
	}
	for _, svc := range services {
		if catalog.IsRetired(svc.Status) {
			t.Errorf("%s is retired but still offered to the user", svc.Slug)
		}
	}

	creds := map[string]string{
		"HONEYGAIN_EMAIL":    "someone@example.test",
		"HONEYGAIN_PASSWORD": "not-a-real-password",
	}
	deployment, err := app.DeployService("honeygain", creds)
	if err != nil {
		t.Fatalf("DeployService: %v", err)
	}
	if deployment.Status != "running" {
		t.Errorf("the deployment came back as %q", deployment.Status)
	}
	if container, ok := app.Docker.Container("cashpilot-honeygain"); !ok || container.State != "running" {
		t.Fatalf("the runtime has no running earner: %+v (ok=%v)", container, ok)
	}

	// Deploying saves the credentials, so the user is not asked again.
	saved, err := app.GetCredentials("honeygain")
	if err != nil || saved["HONEYGAIN_EMAIL"] != creds["HONEYGAIN_EMAIL"] {
		t.Errorf("the credentials were not kept: %v (err=%v)", saved, err)
	}

	// Deploying also asks the platform for a balance straight away, so the card
	// does not sit empty until the next scheduled collection.
	waitFor(t, "the balance from the post-deploy collection", func() bool {
		for _, row := range app.store.ListLatestEarnings() {
			if row.Platform == "honeygain" && row.Balance > 0 {
				return true
			}
		}
		return false
	})

	record, err := app.CollectService("honeygain")
	if err != nil {
		t.Fatalf("CollectService: %v", err)
	}
	if record.Balance != 12.50 || record.Currency != "USD" {
		t.Errorf("collected %v %s, want 12.50 USD", record.Balance, record.Currency)
	}

	summary, err := app.GetEarningsSummary()
	if err != nil {
		t.Fatalf("GetEarningsSummary: %v", err)
	}
	if summary.DisplayCurrency != "USD" {
		t.Errorf("the summary is in %q, want USD", summary.DisplayCurrency)
	}
	found := false
	for _, service := range summary.Breakdown {
		if service.Platform == "honeygain" {
			found = true
			if service.Balance != 12.50 || service.BalanceDisplay != 12.50 {
				t.Errorf("the summary shows %v (%v converted) for honeygain, want 12.50", service.Balance, service.BalanceDisplay)
			}
			if service.Error != "" {
				t.Errorf("the summary reports an error for a healthy collector: %q", service.Error)
			}
		}
	}
	if !found {
		t.Errorf("honeygain is missing from the summary: %+v", summary.Breakdown)
	}

	state, err := app.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState: %v", err)
	}
	if !state.Runtime.Available {
		t.Errorf("the app reports no runtime although the fake daemon is up: %+v", state.Runtime)
	}
	if len(state.Deployments) != 1 || state.Deployments[0].Slug != "honeygain" {
		t.Errorf("the dashboard does not show the earner: %+v", state.Deployments)
	}

	if err := app.StopService("honeygain"); err != nil {
		t.Fatalf("StopService: %v", err)
	}
	if container, _ := app.Docker.Container("cashpilot-honeygain"); container.State != "exited" {
		t.Errorf("the container is %q after Stop", container.State)
	}
	if err := app.StartService("honeygain"); err != nil {
		t.Fatalf("StartService: %v", err)
	}
	if container, _ := app.Docker.Container("cashpilot-honeygain"); container.State != "running" {
		t.Errorf("the container is %q after Start", container.State)
	}
	if row, ok, err := app.store.GetDeployment("honeygain"); err != nil || !ok || row.Status != "running" {
		t.Errorf("the dashboard row says %+v after Start (ok=%v err=%v)", row, ok, err)
	}
	if err := app.RestartService("honeygain"); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	if container, _ := app.Docker.Container("cashpilot-honeygain"); container.State != "running" {
		t.Errorf("the container is %q after Restart", container.State)
	}
	if err := app.RemoveService("honeygain"); err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	if _, ok := app.Docker.Container("cashpilot-honeygain"); ok {
		t.Error("the container survived Remove")
	}
	if names := app.Docker.Names(); len(names) != 0 {
		t.Errorf("the runtime still holds %v", names)
	}
}

// TestTheAppRefusesAServiceItCannotRun covers the two ways a deploy is wrong
// before it starts. Both must be refused without touching the runtime and without
// leaving credentials behind that could never work.
func TestTheAppRefusesAServiceItCannotRun(t *testing.T) {
	app := newAppUnderTest(t)

	if _, err := app.DeployService("honeygain", map[string]string{"HONEYGAIN_EMAIL": "someone@example.test"}); err == nil {
		t.Error("a deploy with no password was accepted")
	}
	if _, err := app.DeployService("no-such-service", map[string]string{"X": "y"}); err == nil {
		t.Error("a deploy of an unknown service was accepted")
	}
	if calls := app.Docker.Calls(); len(calls) > 0 {
		t.Errorf("a refused deploy still talked to the runtime: %v", calls)
	}
	if saved, _ := app.GetCredentials("honeygain"); len(saved) > 0 {
		t.Errorf("a refused deploy left credentials behind: %v", saved)
	}
}

// TestAWorkerEnrollsOverTheNetwork drives the fleet API the way a phone or a
// Docker worker on the LAN does: over a real TCP socket, with a real HTTP client,
// through the whole enrolment handshake. The unit tests call the handler directly;
// this one proves the server the user switches on in Settings is actually
// listening and that the device then shows up on their Fleet page.
func TestAWorkerEnrollsOverTheNetwork(t *testing.T) {
	app := newAppUnderTest(t)

	const sharedKey = "shared-bootstrap-key"
	app.fleetKey = sharedKey
	cfg := app.cfg.Config()
	cfg.FleetServerEnabled = true
	cfg.FleetBindAddress = "127.0.0.1"
	cfg.FleetPort = freeTCPPort(t)
	if err := app.cfg.Save(cfg); err != nil {
		t.Fatalf("saving the fleet config: %v", err)
	}
	if err := app.startFleetAPI(); err != nil {
		t.Fatalf("startFleetAPI: %v", err)
	}
	t.Cleanup(func() { _ = app.fleetAPI.Close() })

	base := "http://" + app.fleetAPI.addr
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("the fleet API is not listening: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/health answered %d", resp.StatusCode)
	}

	heartbeat := func(token string) (int, map[string]any) {
		t.Helper()
		payload := `{"name":"kitchen-pi","url":"http://192.168.1.50:8081",` +
			`"system_info":{"os":"linux","arch":"arm64","hostname":"kitchen-pi"},` +
			`"containers":[{"slug":"honeygain","status":"running"}],"apps":["earnapp"]}`
		req, err := http.NewRequest(http.MethodPost, base+"/api/workers/heartbeat", strings.NewReader(payload))
		if err != nil {
			t.Fatalf("building the heartbeat: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("sending the heartbeat: %v", err)
		}
		defer res.Body.Close()
		var body map[string]any
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(res.Body)
		_ = json.Unmarshal(buf.Bytes(), &body)
		return res.StatusCode, body
	}

	// First contact on the shared key: the worker is enrolled and handed its own
	// key, which is the only time that key is ever sent.
	code, body := heartbeat(sharedKey)
	if code != http.StatusOK {
		t.Fatalf("enrolment answered %d (%v)", code, body)
	}
	workerKey, _ := body["worker_key"].(string)
	if workerKey == "" {
		t.Fatal("enrolment did not issue a worker key")
	}

	// From then on the worker uses its own key, and is not handed another.
	code, body = heartbeat(workerKey)
	if code != http.StatusOK {
		t.Fatalf("the worker's own key was refused: %d (%v)", code, body)
	}
	if _, reissued := body["worker_key"]; reissued {
		t.Error("a confirmed worker was handed a second key")
	}

	// And the shared bootstrap key no longer works for that device, so a copy of
	// the setup snippet cannot take over an enrolled worker's identity.
	if code, _ := heartbeat(sharedKey); code != http.StatusUnauthorized {
		t.Errorf("the shared key still works for an enrolled worker: %d", code)
	}

	// The device is on the user's Fleet page with what it reported.
	fleet, err := app.GetFleetState()
	if err != nil {
		t.Fatalf("GetFleetState: %v", err)
	}
	var device *store.FleetDevice
	for i := range fleet.Devices {
		if fleet.Devices[i].Name == "kitchen-pi" {
			device = &fleet.Devices[i]
		}
	}
	if device == nil {
		t.Fatalf("the enrolled worker is not on the fleet page: %+v", fleet.Devices)
	}
	if device.Status != "online" {
		t.Errorf("the worker is %q right after a heartbeat", device.Status)
	}
	if device.OS != "linux" || device.Arch != "arm64" {
		t.Errorf("the worker's details are wrong: %+v", device)
	}
	if !containsString(device.Services, "honeygain") || !containsString(device.Services, "earnapp") {
		t.Errorf("the worker's services are missing: %v", device.Services)
	}
	if !fleet.APIListening {
		t.Error("the app says its fleet API is not listening while it is answering requests")
	}
}

// TestAWorkerWithoutAKeyIsRefused is the other half: an unauthenticated device on
// the same network gets nothing and leaves no trace.
func TestAWorkerWithoutAKeyIsRefused(t *testing.T) {
	app := newAppUnderTest(t)
	app.fleetKey = "shared-bootstrap-key"
	cfg := app.cfg.Config()
	cfg.FleetServerEnabled = true
	cfg.FleetBindAddress = "127.0.0.1"
	cfg.FleetPort = freeTCPPort(t)
	if err := app.cfg.Save(cfg); err != nil {
		t.Fatalf("saving the fleet config: %v", err)
	}
	if err := app.startFleetAPI(); err != nil {
		t.Fatalf("startFleetAPI: %v", err)
	}
	t.Cleanup(func() { _ = app.fleetAPI.Close() })

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, "http://"+app.fleetAPI.addr+"/api/workers/heartbeat",
		strings.NewReader(`{"name":"stranger","system_info":{"os":"linux"}}`))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer not-the-key")
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("sending the request: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unknown key got %d, want 401", res.StatusCode)
	}
	if devices := app.store.ListFleetDevices(); len(devices) != 0 {
		t.Errorf("a refused device was still recorded: %+v", devices)
	}
}

// waitFor polls until the condition holds, so a background step (the collection
// kicked off by a deploy) is awaited rather than slept on.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
