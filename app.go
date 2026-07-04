package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	stdruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx        context.Context
	cfg        *config.Manager
	catalog    *catalog.Catalog
	store      *store.Store
	runtime    runtime.Provider
	services   *services.Manager
	collectors *collectors.Registry
	trayIcon   []byte
	fleetAPI   *fleetAPIServer
}

func NewApp() *App {
	return &App{}
}

func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx

	cfg, err := config.NewManager()
	if err != nil {
		a.emitError("config", err)
		return
	}
	a.cfg = cfg

	st, err := store.Open(cfg.DataDir())
	if err != nil {
		a.emitError("store", err)
		return
	}
	a.store = st

	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		a.emitError("catalog", err)
		return
	}
	a.catalog = cat

	a.runtime = runtime.NewDockerProvider()
	a.services = services.NewManager(a.runtime, a.catalog, a.store)
	a.collectors = collectors.NewRegistry(a.store)
	if err := a.ensureFleetAPIKey(); err != nil {
		a.emitError("fleet-api", err)
		return
	}
	if err := a.startFleetAPI(); err != nil {
		a.emitError("fleet-api", err)
	}
}

func (a *App) DomReady(ctx context.Context) {
	a.ctx = ctx
	wailsruntime.WindowShow(ctx)
	PositionMainWindowOnPrimaryScreen()
	InstallTrayIcon(a.trayIcon)
}

func (a *App) Shutdown(_ context.Context) {
	if a.fleetAPI != nil {
		_ = a.fleetAPI.Close()
	}
	if a.store != nil {
		_ = a.store.Close()
	}
}

type AppState struct {
	Config        config.AppConfig       `json:"config"`
	Runtime       runtime.Status         `json:"runtime"`
	Services      []catalog.Service      `json:"services"`
	Deployments   []store.Deployment     `json:"deployments"`
	Earnings      []store.EarningsRecord `json:"earnings"`
	History       []store.EarningsRecord `json:"history"`
	Guides        []runtime.InstallGuide `json:"guides"`
	Notifications []Notification         `json:"notifications"`
	Currencies    []string               `json:"currencies"`
}

type Notification struct {
	Level   string `json:"level"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

type EnvSetting struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	Source   string `json:"source"`
	Secret   bool   `json:"secret"`
	ReadOnly bool   `json:"readOnly"`
	Help     string `json:"help"`
}

type CollectorSetting struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	Collector  string `json:"collector"`
}

type SettingsState struct {
	Environment []EnvSetting       `json:"environment"`
	Collectors  []CollectorSetting `json:"collectors"`
	Config      config.AppConfig   `json:"config"`
}

type FleetState struct {
	Workers       int                 `json:"workers"`
	Mobiles       int                 `json:"mobiles"`
	Online        int                 `json:"online"`
	Services      int                 `json:"services"`
	Devices       []store.FleetDevice `json:"devices"`
	UIURL         string              `json:"uiUrl"`
	LocalAPIURL   string              `json:"localApiUrl"`
	APIKey        string              `json:"apiKey"`
	APIListening  bool                `json:"apiListening"`
	WorkerSnippet string              `json:"workerSnippet"`
	MobileSnippet string              `json:"mobileSnippet"`
}

func (a *App) GetAppState() (AppState, error) {
	if err := a.ready(); err != nil {
		return AppState{}, err
	}
	runtimeStatus := a.runtime.Status(a.ctx)
	return AppState{
		Config:        a.cfg.Config(),
		Runtime:       runtimeStatus,
		Services:      a.catalog.ListVisible(),
		Deployments:   a.store.ListDeployments(),
		Earnings:      a.store.ListLatestEarnings(),
		History:       a.store.ListEarningsHistory(200),
		Guides:        runtime.InstallGuides(),
		Notifications: a.notifications(runtimeStatus),
		Currencies:    supportedCurrencies(),
	}, nil
}

func (a *App) GetSettingsState() (SettingsState, error) {
	if err := a.ready(); err != nil {
		return SettingsState{}, err
	}
	cfg := a.cfg.Config()
	env := []EnvSetting{
		{Key: "CASHPILOT_HOSTNAME_PREFIX", Label: "Hostname Prefix", Value: cfg.HostnamePrefix, Source: "Config", Help: "Containers are named <prefix>-<service> where supported."},
		{Key: "CASHPILOT_COLLECT_INTERVAL", Label: "Collect Interval (min)", Value: strconv.Itoa(cfg.CollectIntervalMinutes), Source: "Config", Help: "Minutes between future automatic earnings collection runs."},
		{Key: "CASHPILOT_DISPLAY_CURRENCY", Label: "Display Currency", Value: cfg.DisplayCurrency, Source: "Config", Help: "Currency used in the topbar and dashboard summaries."},
		{Key: "CASHPILOT_API_KEY", Label: "Fleet API Key", Value: cfg.FleetAPIKey, Source: "Config", Secret: true, Help: "Bearer token used by external workers and mobile clients."},
		{Key: "CASHPILOT_UI_URL", Label: "Desktop API URL", Value: a.fleetUIURL(), Source: "Runtime", ReadOnly: true, Help: "URL that external workers should use for CASHPILOT_UI_URL."},
		{Key: "CASHPILOT_FLEET_BIND", Label: "Fleet Bind Address", Value: cfg.FleetBindAddress, Source: "Config", Help: "Address the desktop worker API listens on. Use 0.0.0.0 for LAN workers."},
		{Key: "CASHPILOT_FLEET_PORT", Label: "Fleet API Port", Value: strconv.Itoa(cfg.FleetPort), Source: "Config", Help: "Port used for external worker heartbeats."},
		{Key: "TZ", Label: "System Timezone", Value: cfg.Timezone, Source: "Config", Help: "Timezone passed to future managed workers and mobile sync events."},
		{Key: "CASHPILOT_DATA_DIR", Label: "Data Directory", Value: a.cfg.DataDir(), Source: "Read-only", ReadOnly: true, Help: "Directory containing the local SQLite database."},
		{Key: "CASHPILOT_RUNTIME_PROVIDER", Label: "Runtime Provider", Value: cfg.RuntimeProvider, Source: "Read-only", ReadOnly: true, Help: "Current Docker-compatible runtime integration."},
	}
	collectors := make([]CollectorSetting, 0)
	for _, svc := range a.catalog.ListVisible() {
		if svc.Collector.Type == "" && !svc.ManualOnly {
			continue
		}
		creds, _ := a.store.GetCredentials(svc.Slug)
		collectors = append(collectors, CollectorSetting{
			Slug:       svc.Slug,
			Name:       svc.Name,
			Configured: len(creds) > 0,
			Collector:  svc.Collector.Type,
		})
	}
	return SettingsState{Environment: env, Collectors: collectors, Config: cfg}, nil
}

func (a *App) SaveSettings(values map[string]string) (SettingsState, error) {
	if err := a.ready(); err != nil {
		return SettingsState{}, err
	}
	cfg := a.cfg.Config()
	if value := strings.TrimSpace(values["displayCurrency"]); value != "" {
		cfg.DisplayCurrency = strings.ToUpper(value)
	}
	if value := strings.TrimSpace(values["hostnamePrefix"]); value != "" {
		cfg.HostnamePrefix = value
	}
	if value := strings.TrimSpace(values["timezone"]); value != "" {
		cfg.Timezone = value
	}
	if value := strings.TrimSpace(values["fleetBindAddress"]); value != "" {
		cfg.FleetBindAddress = value
	}
	if value := strings.TrimSpace(values["collectIntervalMinutes"]); value != "" {
		minutes, err := strconv.Atoi(value)
		if err != nil || minutes <= 0 {
			return SettingsState{}, fmt.Errorf("collect interval must be a positive number")
		}
		cfg.CollectIntervalMinutes = minutes
	}
	if value := strings.TrimSpace(values["fleetPort"]); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil || port <= 0 || port > 65535 {
			return SettingsState{}, fmt.Errorf("fleet API port must be between 1 and 65535")
		}
		cfg.FleetPort = port
	}
	if err := a.cfg.Save(cfg); err != nil {
		return SettingsState{}, err
	}
	return a.GetSettingsState()
}

func (a *App) GetFleetState() (FleetState, error) {
	if err := a.ready(); err != nil {
		return FleetState{}, err
	}
	cfg := a.cfg.Config()
	devices := a.store.ListFleetDevices()
	hostname, _ := os.Hostname()
	local := store.FleetDevice{
		ID:       0,
		Name:     hostnameOrDefault(hostname),
		Kind:     "desktop",
		Endpoint: "local",
		OS:       stdruntime.GOOS,
		Arch:     stdruntime.GOARCH,
		Status:   "online",
		Services: deploymentSlugs(a.store.ListDeployments()),
		LastSeen: time.Now().UTC().Format(time.RFC3339),
	}
	devices = append([]store.FleetDevice{local}, devices...)
	workers, mobiles, online := 0, 0, 0
	for _, device := range devices {
		if device.Kind == "mobile" {
			mobiles++
		} else {
			workers++
		}
		if device.Status == "online" {
			online++
		}
	}
	services := len(a.catalog.ListVisible())
	uiURL := a.fleetUIURL()
	localAPIURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.FleetPort)
	return FleetState{
		Workers:       workers,
		Mobiles:       mobiles,
		Online:        online,
		Services:      services,
		Devices:       devices,
		UIURL:         uiURL,
		LocalAPIURL:   localAPIURL,
		APIKey:        cfg.FleetAPIKey,
		APIListening:  a.fleetAPI != nil,
		WorkerSnippet: fmt.Sprintf("CASHPILOT_UI_URL=%s\nCASHPILOT_API_KEY=%s\nCASHPILOT_WORKER_NAME=%s-worker\nCASHPILOT_WORKER_URL=http://<worker-lan-ip>:8081", uiURL, cfg.FleetAPIKey, cfg.HostnamePrefix),
		MobileSnippet: fmt.Sprintf("CASHPILOT_UI_URL=%s\nCASHPILOT_API_KEY=%s\nDevice type: mobile", uiURL, cfg.FleetAPIKey),
	}, nil
}

func (a *App) AddFleetDevice(values map[string]string) (FleetState, error) {
	if err := a.ready(); err != nil {
		return FleetState{}, err
	}
	name := strings.TrimSpace(values["name"])
	if name == "" {
		return FleetState{}, fmt.Errorf("device name is required")
	}
	kind := strings.TrimSpace(values["kind"])
	if kind != "mobile" && kind != "worker" {
		kind = "worker"
	}
	services := splitList(values["services"])
	_, err := a.store.UpsertFleetDevice(store.FleetDevice{
		Name:     name,
		Kind:     kind,
		Endpoint: strings.TrimSpace(values["endpoint"]),
		OS:       strings.TrimSpace(values["os"]),
		Arch:     strings.TrimSpace(values["arch"]),
		Status:   "offline",
		Services: services,
		LastSeen: "not connected yet",
	})
	if err != nil {
		return FleetState{}, err
	}
	return a.GetFleetState()
}

func (a *App) RemoveFleetDevice(id int64) (FleetState, error) {
	if err := a.ready(); err != nil {
		return FleetState{}, err
	}
	if id <= 0 {
		return FleetState{}, fmt.Errorf("the local desktop device cannot be removed")
	}
	if err := a.store.DeleteFleetDevice(id); err != nil {
		return FleetState{}, err
	}
	return a.GetFleetState()
}

func (a *App) CompleteOnboarding() error {
	if err := a.ready(); err != nil {
		return err
	}
	cfg := a.cfg.Config()
	cfg.FirstRunComplete = true
	return a.cfg.Save(cfg)
}

func (a *App) CheckRuntime() (runtime.Status, error) {
	if err := a.ready(); err != nil {
		return runtime.Status{}, err
	}
	return a.runtime.Status(a.ctx), nil
}

func (a *App) GetRuntimeGuides() []runtime.InstallGuide {
	return runtime.InstallGuides()
}

func (a *App) ListServices() ([]catalog.Service, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.catalog.ListVisible(), nil
}

func (a *App) GetService(slug string) (catalog.Service, error) {
	if err := a.ready(); err != nil {
		return catalog.Service{}, err
	}
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return catalog.Service{}, fmt.Errorf("unknown service: %s", slug)
	}
	return svc, nil
}

func (a *App) SaveCredentials(slug string, values map[string]string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.store.SaveCredentials(slug, values)
}

func (a *App) GetCredentials(slug string) (map[string]string, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.store.GetCredentials(slug)
}

func (a *App) DeployService(slug string, values map[string]string) (store.Deployment, error) {
	if err := a.ready(); err != nil {
		return store.Deployment{}, err
	}
	if len(values) > 0 {
		if err := a.store.SaveCredentials(slug, values); err != nil {
			return store.Deployment{}, err
		}
	}
	creds, err := a.store.GetCredentials(slug)
	if err != nil {
		return store.Deployment{}, err
	}
	deployment, err := a.services.Deploy(a.ctx, slug, creds)
	if err != nil {
		a.emitError("deploy", err)
		return store.Deployment{}, err
	}
	wailsruntime.EventsEmit(a.ctx, "deployment:changed", deployment)
	return deployment, nil
}

func (a *App) StopService(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.Stop(a.ctx, slug); err != nil {
		a.emitError("stop", err)
		return err
	}
	wailsruntime.EventsEmit(a.ctx, "deployment:changed", slug)
	return nil
}

func (a *App) StartService(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.Start(a.ctx, slug); err != nil {
		a.emitError("start", err)
		return err
	}
	wailsruntime.EventsEmit(a.ctx, "deployment:changed", slug)
	return nil
}

func (a *App) RestartService(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.Restart(a.ctx, slug); err != nil {
		a.emitError("restart", err)
		return err
	}
	wailsruntime.EventsEmit(a.ctx, "deployment:changed", slug)
	return nil
}

func (a *App) RemoveService(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.Remove(a.ctx, slug); err != nil {
		a.emitError("remove", err)
		return err
	}
	wailsruntime.EventsEmit(a.ctx, "deployment:changed", slug)
	return nil
}

func (a *App) GetLogs(slug string, lines int) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	return a.services.Logs(a.ctx, slug, lines)
}

func (a *App) RefreshDeployments() ([]store.Deployment, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.services.Refresh(a.ctx)
}

func (a *App) CollectService(slug string) (store.EarningsRecord, error) {
	if err := a.ready(); err != nil {
		return store.EarningsRecord{}, err
	}
	creds, err := a.store.GetCredentials(slug)
	if err != nil {
		return store.EarningsRecord{}, err
	}
	record, err := a.collectors.Collect(a.ctx, slug, creds)
	if err != nil {
		a.emitError("collector", err)
		return store.EarningsRecord{}, err
	}
	wailsruntime.EventsEmit(a.ctx, "earnings:changed", record)
	return record, nil
}

func (a *App) ManagedRuntimePlan() runtime.ManagedRuntimePlan {
	return runtime.ManagedRuntimeRoadmap()
}

func (a *App) notifications(status runtime.Status) []Notification {
	var items []Notification
	if !status.Available {
		items = append(items, Notification{Level: "warning", Title: "Runtime offline", Message: status.Message})
	}
	for _, record := range a.store.ListLatestEarnings() {
		if record.Error != "" {
			items = append(items, Notification{Level: "error", Title: record.Platform + " collector", Message: record.Error})
		}
	}
	if len(a.store.ListDeployments()) == 0 {
		items = append(items, Notification{Level: "info", Title: "No services deployed", Message: "Use the setup wizard or register a mobile device to start tracking earnings."})
	}
	return items
}

func deploymentSlugs(deployments []store.Deployment) []string {
	out := make([]string, 0, len(deployments))
	for _, dep := range deployments {
		out = append(out, dep.Slug)
	}
	return out
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func hostnameOrDefault(value string) string {
	if strings.TrimSpace(value) == "" {
		return "This Mac"
	}
	return value
}

func (a *App) ensureFleetAPIKey() error {
	cfg := a.cfg.Config()
	if cfg.FleetAPIKey != "" {
		return nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	cfg.FleetAPIKey = base64.RawURLEncoding.EncodeToString(raw)
	return a.cfg.Save(cfg)
}

func supportedCurrencies() []string {
	return []string{
		"USD", "EUR", "GBP", "JPY", "AUD", "BGN", "BRL", "CAD", "CHF", "CNY", "CZK", "DKK",
		"HKD", "HUF", "IDR", "ILS", "INR", "ISK", "KRW", "MXN", "MYR", "NOK", "NZD", "PHP",
		"PLN", "RON", "SEK", "SGD", "THB", "TRY", "ZAR", "AED", "ARS", "CLP", "COP", "EGP",
		"MAD", "NGN", "PEN", "SAR", "TWD", "UAH", "VND", "MYST",
	}
}

func (a *App) ready() error {
	if a.cfg == nil || a.catalog == nil || a.store == nil || a.runtime == nil || a.services == nil {
		return fmt.Errorf("app is still starting")
	}
	return nil
}

func (a *App) emitError(scope string, err error) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "app:error", map[string]string{
			"scope": scope,
			"error": err.Error(),
		})
	}
}
