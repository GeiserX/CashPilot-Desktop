package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	stdruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/exchange"
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
	exchange   *exchange.Service
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

	// Exchange rates power the earnings summary. Kick a best-effort initial fetch
	// and a periodic refresh; a failed fetch must never fail Startup (the summary
	// is stale-graceful and flags stale rates rather than blanking balances).
	a.exchange = exchange.NewService()
	go func() { _ = a.exchange.Refresh(ctx) }()
	go func() {
		ticker := time.NewTicker(exchange.CacheTTL)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = a.exchange.Refresh(ctx)
			}
		}
	}()

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
	Summary       EarningsSummary        `json:"summary"`
}

type Notification struct {
	Level   string `json:"level"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

// EarningsSummary is the dashboard's converted, aggregated view of earnings.
// Balances stored per service are CUMULATIVE lifetime totals; the summary turns
// them into a single display-currency total plus per-day accrual figures.
type EarningsSummary struct {
	DisplayCurrency string           `json:"displayCurrency"`
	Total           float64          `json:"total"`
	Today           float64          `json:"today"`
	Month           float64          `json:"month"`
	TodayChange     float64          `json:"todayChange"`
	MonthChange     float64          `json:"monthChange"`
	Breakdown       []ServiceEarning `json:"breakdown"`
	Points          []PointsBalance  `json:"points"`
	Daily           []DailyPoint     `json:"daily"`
	RatesStale      bool             `json:"ratesStale"`
	RatesUpdated    string           `json:"ratesUpdated"`
}

// ServiceEarning is one service's latest balance, both native and converted to
// the display currency, with its payout/cashout progress. Error rows are kept so
// the UI can surface a "needs attention" chip.
type ServiceEarning struct {
	Platform       string          `json:"platform"`
	Name           string          `json:"name"`
	Balance        float64         `json:"balance"`
	Currency       string          `json:"currency"`
	BalanceDisplay float64         `json:"balanceDisplay"`
	Convertible    bool            `json:"convertible"`
	Delta          float64         `json:"delta"`
	Error          string          `json:"error"`
	Cashout        CashoutProgress `json:"cashout"`
}

// CashoutProgress describes how close a service is to its minimum payout. It is
// only meaningful (Comparable) when the balance currency matches the cashout
// currency; otherwise the UI hides the progress bar.
type CashoutProgress struct {
	MinAmount    float64 `json:"minAmount"`
	Currency     string  `json:"currency"`
	Percent      float64 `json:"percent"`
	Eligible     bool    `json:"eligible"`
	Comparable   bool    `json:"comparable"`
	Method       string  `json:"method"`
	DashboardURL string  `json:"dashboardUrl"`
	Notes        string  `json:"notes"`
}

// PointsBalance is a non-convertible reward balance (e.g. GRASS points) shown in
// native units and deliberately excluded from fiat totals.
type PointsBalance struct {
	Platform string  `json:"platform"`
	Name     string  `json:"name"`
	Balance  float64 `json:"balance"`
	Currency string  `json:"currency"`
}

// DailyPoint is one day's earnings (accrual) in the display currency, for the
// dashboard chart.
type DailyPoint struct {
	Day    string  `json:"day"`
	Amount float64 `json:"amount"`
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
		Summary:       a.computeEarningsSummary(),
	}, nil
}

// GetEarningsSummary returns the converted, aggregated earnings summary on its
// own so the frontend can refresh it without pulling the whole app state.
func (a *App) GetEarningsSummary() (EarningsSummary, error) {
	if err := a.ready(); err != nil {
		return EarningsSummary{}, err
	}
	return a.computeEarningsSummary(), nil
}

// computeEarningsSummary converts the per-service CUMULATIVE daily balances into
// a single display-currency total plus per-day accrual figures. Convertible
// currencies (USD, known fiat, priced crypto) are summed; non-convertible reward
// points (e.g. GRASS) are surfaced separately and never summed. It is
// stale-graceful: missing rates simply drop a service from the total and set
// RatesStale, rather than erroring.
func (a *App) computeEarningsSummary() EarningsSummary {
	disp := "USD"
	if a.cfg != nil {
		if c := a.cfg.Config().DisplayCurrency; c != "" {
			disp = c
		}
	}
	summary := EarningsSummary{
		DisplayCurrency: disp,
		Breakdown:       []ServiceEarning{},
		Points:          []PointsBalance{},
		Daily:           []DailyPoint{},
	}
	if a.exchange == nil || a.store == nil || a.catalog == nil {
		return summary
	}
	a.exchange.EnsureFresh(a.ctx)

	now := time.Now().UTC()
	dayStr := func(offset int) string { return now.AddDate(0, 0, offset).Format("2006-01-02") }
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	beforeMonthStart := monthStart.AddDate(0, 0, -1).Format("2006-01-02")
	lastMonthStart := monthStart.AddDate(0, -1, 0)
	beforeLastMonthStartTime := lastMonthStart.AddDate(0, 0, -1)
	beforeLastMonthStart := beforeLastMonthStartTime.Format("2006-01-02")

	// Fetch a window wide enough to reach the day BEFORE last month started (two
	// months plus a margin) so the month and last-month baselines are actually in
	// the map. A hardcoded window would leave those baselines unreachable; combined
	// with per-platform accrual below, an unreachable baseline just degrades to
	// "contributes 0", never a garbage cumulative number.
	daysBack := int(now.Sub(beforeLastMonthStartTime).Hours()/24) + 7

	// Build per-platform day -> cumulative balance maps, the platform's currency,
	// and a sorted list of the days it was actually collected.
	perPlat := map[string]map[string]float64{}
	perCur := map[string]string{}
	daysByPlat := map[string][]string{}
	for _, b := range a.store.ListDailyBalances(daysBack) {
		if perPlat[b.Platform] == nil {
			perPlat[b.Platform] = map[string]float64{}
		}
		if _, seen := perPlat[b.Platform][b.Day]; !seen {
			daysByPlat[b.Platform] = append(daysByPlat[b.Platform], b.Day)
		}
		perPlat[b.Platform][b.Day] = b.Balance
		perCur[b.Platform] = b.Currency
	}
	for plat := range daysByPlat {
		sort.Strings(daysByPlat[plat])
	}

	// asOf carries the cumulative balance forward: the balance on the latest
	// collected day on or before `day` (ok=false when nothing was collected on or
	// before that day, i.e. there is no established baseline yet).
	asOf := func(plat, day string) (float64, bool) {
		var val float64
		found := false
		for _, d := range daysByPlat[plat] {
			if d <= day {
				val = perPlat[plat][d]
				found = true
				continue
			}
			break
		}
		return val, found
	}

	// platformDelta is a SINGLE platform's display-currency accrual between two
	// days, from its own cumulative balances (not the whole-portfolio total). It
	// books 0 unless BOTH endpoints have an established, convertible baseline: a
	// platform's first-ever observation (or a baseline that predates the fetch
	// window) has no `fromDay` balance, so its whole lifetime cumulative is never
	// counted as a single day's earning. Per-platform clamping at 0 keeps one
	// platform's dip from cancelling another's gain.
	platformDelta := func(plat, fromDay, toDay string) float64 {
		fromBal, fromOK := asOf(plat, fromDay)
		if !fromOK {
			return 0
		}
		toBal, toOK := asOf(plat, toDay)
		if !toOK {
			return 0
		}
		cur := perCur[plat]
		fromDisp, ok := a.exchange.ToDisplay(fromBal, cur, disp)
		if !ok {
			return 0
		}
		toDisp, ok := a.exchange.ToDisplay(toBal, cur, disp)
		if !ok {
			return 0
		}
		return max(0, toDisp-fromDisp)
	}
	// sumDelta accrues every platform's per-platform delta over one period.
	sumDelta := func(fromDay, toDay string) float64 {
		var sum float64
		for plat := range daysByPlat {
			sum += platformDelta(plat, fromDay, toDay)
		}
		return sum
	}

	// Total / Points from each platform's LATEST cumulative balance, classified by
	// INTENT: a declared reward point (PointsCurrencies) is surfaced natively and
	// never summed; any other currency is added to the fiat Total when it can be
	// priced right now; a non-points currency that cannot currently be priced (a
	// rate outage or a zero rate) is dropped from BOTH and flags the rates stale,
	// so it is never mislabeled as a reward point.
	var total float64
	latestDay := ""
	for plat, days := range daysByPlat {
		if len(days) == 0 {
			continue
		}
		last := days[len(days)-1]
		if last > latestDay {
			latestDay = last
		}
		cur := perCur[plat]
		bal := perPlat[plat][last]
		if a.exchange.IsPoints(cur) {
			summary.Points = append(summary.Points, PointsBalance{
				Platform: plat,
				Name:     a.serviceName(plat),
				Balance:  bal,
				Currency: cur,
			})
			continue
		}
		if conv, ok := a.exchange.ToDisplay(bal, cur, disp); ok {
			total += conv
			continue
		}
		summary.RatesStale = true
	}
	summary.Total = total

	today := dayStr(0)
	if latestDay == "" {
		latestDay = today
	}

	// Today / yesterday accrual = per-platform deltas across the two day pairs.
	todayEarned := sumDelta(dayStr(-1), today)
	yesterdayEarned := sumDelta(dayStr(-2), dayStr(-1))
	summary.Today = todayEarned
	if yesterdayEarned > 0 {
		summary.TodayChange = (todayEarned - yesterdayEarned) / yesterdayEarned * 100
	}

	// Month = per-platform accrual since the day before this month started; a
	// platform whose baseline is unknown (before first collection or outside the
	// window) contributes 0 rather than its whole cumulative balance.
	summary.Month = sumDelta(beforeMonthStart, latestDay)
	prevMonthEarned := sumDelta(beforeLastMonthStart, beforeMonthStart)
	if prevMonthEarned > 0 {
		summary.MonthChange = (summary.Month - prevMonthEarned) / prevMonthEarned * 100
	}

	// Daily = last 30 days of per-day accrual in the display currency.
	for i := 29; i >= 0; i-- {
		d := now.AddDate(0, 0, -i)
		curDay := d.Format("2006-01-02")
		prevDay := d.AddDate(0, 0, -1).Format("2006-01-02")
		summary.Daily = append(summary.Daily, DailyPoint{
			Day:    d.Format("Jan 02"),
			Amount: sumDelta(prevDay, curDay),
		})
	}

	// Breakdown = every service's latest record, INCLUDING error rows so the UI
	// keeps a "needs attention" chip.
	for _, rec := range a.store.ListLatestEarnings() {
		se := ServiceEarning{
			Platform: rec.Platform,
			Name:     rec.Platform,
			Balance:  rec.Balance,
			Currency: rec.Currency,
			Error:    rec.Error,
		}
		var cash catalog.Cashout
		if svc, ok := a.catalog.Get(rec.Platform); ok {
			se.Name = svc.Name
			cash = svc.Cashout
		}
		if a.exchange.Convertible(rec.Currency) {
			se.Convertible = true
			if conv, ok := a.exchange.ToDisplay(rec.Balance, rec.Currency, disp); ok {
				se.BalanceDisplay = conv
			}
		}
		// Native day-over-day change from the last two collected days.
		if days := daysByPlat[rec.Platform]; len(days) >= 2 {
			se.Delta = perPlat[rec.Platform][days[len(days)-1]] - perPlat[rec.Platform][days[len(days)-2]]
		}
		cp := CashoutProgress{
			MinAmount:    cash.MinAmount,
			Currency:     cash.Currency,
			Method:       cash.Method,
			DashboardURL: cash.DashboardURL,
			Notes:        cash.Notes,
			Comparable:   rec.Currency == cash.Currency,
		}
		if cp.Comparable && cash.MinAmount > 0 {
			cp.Percent = max(0, min(100, rec.Balance/cash.MinAmount*100))
		}
		cp.Eligible = cp.Comparable && cash.MinAmount > 0 && rec.Balance >= cash.MinAmount
		se.Cashout = cp
		summary.Breakdown = append(summary.Breakdown, se)
	}

	// Preserve any stale flag already raised above (a non-points currency that
	// could not be priced) and OR in the exchange's own staleness.
	summary.RatesStale = summary.RatesStale || a.exchange.Stale()
	summary.RatesUpdated = a.exchange.Snapshot().LastUpdated
	return summary
}

// serviceName resolves a catalog display name for a slug, falling back to the
// slug itself when the service is not in the catalog.
func (a *App) serviceName(slug string) string {
	if a.catalog != nil {
		if svc, ok := a.catalog.Get(slug); ok {
			return svc.Name
		}
	}
	return slug
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
		{Key: "CASHPILOT_FLEET_BIND", Label: "Fleet Bind Address", Value: cfg.FleetBindAddress, Source: "Config", Help: "Default 127.0.0.1 (this machine only). Set to 0.0.0.0 only to accept worker/mobile connections from your LAN — this exposes the API to your network."},
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
		upper := strings.ToUpper(value)
		if !isSupportedCurrency(upper) {
			return SettingsState{}, fmt.Errorf("unsupported display currency: %s", value)
		}
		cfg.DisplayCurrency = upper
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

func isSupportedCurrency(code string) bool {
	for _, c := range supportedCurrencies() {
		if c == code {
			return true
		}
	}
	return false
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
