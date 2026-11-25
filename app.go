package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	neturl "net/url"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/bgservice"
	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/compose"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/exchange"
	"github.com/GeiserX/CashPilot-Desktop/internal/health"
	"github.com/GeiserX/CashPilot-Desktop/internal/preflight"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx context.Context
	// startErr holds the error that stopped startCore, so ready() can report it
	// instead of errStillStarting, which the window would wait on.
	startErr   atomic.Pointer[error]
	cfg        *config.Manager
	catalog    *catalog.Catalog
	store      *store.Store
	runtime    runtime.Provider
	fence      fenceController // LAN isolation of the Docker runtime (internal/runtime/lanfence.go)
	native     runtime.Provider
	services   *services.Manager
	collectors collectorRegistry
	exchange   *exchange.Service
	trayIcon   []byte
	fleetAPI   *fleetAPIServer
	// host caches the last reading of this Windows PC (host_check.go).
	host     *hostFacts
	hostOnce sync.Once
	// resumeSetup is set when the app was started with --resume-setup, or a second
	// start passed it (docker_guide.go).
	resumeSetup atomic.Bool
	// runOnce writes and removes the HKCU RunOnce value that reopens the
	// checklist after a restart (docker_guide.go). Nil means the real key; tests
	// set a fake so they never arm the real RunOnce key.
	runOnce runOnceWriter
	// saveDialog asks the user where to write a file and returns the chosen path,
	// or "" when they dismissed the dialog. Zero value means the real Wails dialog;
	// it is a field so the export path can be driven in a test, which has no window
	// for a native dialog to open in.
	saveDialog func(wailsruntime.SaveDialogOptions) (string, error)
	// openDialog is saveDialog's twin for choosing a file to read (a backup to
	// restore). Zero value means the real Wails dialog.
	openDialog func(wailsruntime.OpenDialogOptions) (string, error)
	// restoreFiles are the backup files the restore preview checked, by token, until
	// one is restored or restoreFileTTL passes (restore_binding.go). Guarded by
	// restoreMu.
	restoreMu    sync.Mutex
	restoreFiles map[string]restoreFile
	// credentialsMu serializes read-merge-write operations on saved credentials.
	credentialsMu sync.Mutex
	// fleetKey is the fleet bearer token held in memory for the per-request auth
	// check. It is loaded once at Startup by ensureFleetAPIKey from the OS keychain
	// (0600 file fallback) so the token is never persisted in config.json and the
	// keychain is not hit on every heartbeat.
	fleetKey string

	// upstream is the optional pairing loop that reports this Desktop to a
	// CashPilot server as a worker. Zero value means not paired, which is the
	// default -- Desktop is standalone unless a UpstreamURL is configured.
	upstream upstreamState

	// Background collection scheduler. collecting is a single-flight guard so
	// overlapping collectAll runs (the ticker, a post-deploy kick, a future manual
	// refresh) never stack. schedCancel/schedDone are the running loop's stop
	// handle, guarded by schedMu because Startup, SaveSettings and Shutdown touch
	// them from different goroutines.
	// addedDevices is, per slug, the device id the last deploy added to the user's
	// account through the provider's API, so the deploy output can say so instead of
	// asking the user to do it. Guarded by addedMu.
	addedMu      sync.Mutex
	addedDevices map[string]string

	// customPending is true while the custom services could not be loaded into the
	// catalog; see loadCustom.
	customPending atomic.Bool

	// Keep a service's deploy and its credential write in the same order.
	deployCredentialsLocks sync.Map // service slug -> *deployCredentialsLock

	collecting  atomic.Bool
	schedMu     sync.Mutex
	schedCancel context.CancelFunc
	schedDone   chan struct{}
	// fleetMu serializes the per-worker-key state machine. handleWorkerHeartbeat's
	// read(FleetDeviceKeyState) -> classify -> upsert -> set/confirm sequence is not
	// a single DB transaction (each call is its own statement), so without this lock
	// two concurrent/retried heartbeats for one device could both enroll and race
	// the key write (last-writer-wins -> a client holds a key the DB discarded), and
	// a reap/delete landing mid-sequence could blank a confirmed device's key. Reap
	// and manual delete take the same lock so they cannot interleave with a heartbeat.
	fleetMu sync.Mutex
	// workerDeployMu serializes live replacement checks and deploys, including
	// shared identity checks across workers.
	workerDeployMu sync.Mutex
	// fleetLimiter throttles the heartbeat/enrollment endpoint per client IP as
	// defense-in-depth against floods, mass enrollment (DB growth), and racing the
	// enrollment window. Initialised in startFleetAPI.
	fleetLimiter *fleetRateLimiter
	// walletReveals limits RevealWalletSeed per service (wallet_app.go). Made on
	// first use, under walletRevealMu.
	walletRevealMu sync.Mutex
	walletReveals  *fleetRateLimiter
	// usageStop stops the usage sampler (usage_sampler.go) and waits for it.
	usageStop func()
	// stalls is the stall alerts by container name (stall_alerts.go), guarded by stallMu.
	stallMu sync.Mutex
	stalls  map[string]StallAlert
	// proxyStop stops the proxied-copies pass (app_proxies.go) and waits for it.
	proxyStop func()
	// deviceLinkStop stops the device-registration retry pass (app_device_links.go).
	deviceLinkStop func()
	// activity is the Activity sampler (activity_sampler.go): off until the owner switches it on.
	activity activityState
	// workerAPI is the API a joined CashPilot server commands this computer through
	// (worker_api.go).
	workerAPI workerAPIState
	// pairingMu orders explicit Join and Leave requests in the window.
	pairingMu          sync.Mutex
	window             bool
	stopPairingUpgrade func()
}

// fenceController is the slice of the Docker provider that keeps earners off the LAN.
// An interface so the app tests can fake it without a daemon.
type fenceController interface {
	FenceStatus() runtime.FenceStatus
	ReconcileFence(ctx context.Context)
	EnableFence(ctx context.Context) (runtime.FenceStatus, error)
	// GuardFenceGated starts a fence-gated earner that is down, behind the fence.
	GuardFenceGated(ctx context.Context)
	// EgressIP is this machine's public IPv4 as the fence reads it, "" when unknown.
	EgressIP(ctx context.Context) string
}

// collectorRegistry is the slice of *collectors.Registry the app depends on: run a
// single service's collector (persisting its earnings record) and report whether a
// slug has a native collector at all. It is an interface so the scheduler tests can
// inject a fake collector without a live store or network.
type collectorRegistry interface {
	Collect(ctx context.Context, slug string, credentials map[string]string) (store.EarningsRecord, error)
	Supports(slug string) bool
}

// deviceRegistry is the part of the collector registry that knows whether a deployed
// device is on the user's account (collectors/devices.go). Optional, so test fakes of
// collectorRegistry need not implement it.
type deviceRegistry interface {
	Unlinked(slug string) []string
	RegisterDevice(ctx context.Context, slug string, credentials map[string]string) collectors.Registration
}

func NewApp() *App {
	return &App{}
}

// Startup is the Wails OnStartup hook. It brings the app's core engine up via
// startCore — the shared init that both the GUI and the headless --daemon role use.
// The engine init has nothing UI-specific in it (the window/tray/webview setup lives in
// DomReady), so the two roles share one definition of "bring the app up". A fatal core
// failure (config/store/catalog) was already surfaced as an app:error event inside
// startCore and leaves a.ready() failing for every binding, so — exactly as before —
// there is nothing further for the GUI hook to do with the returned error.
func (a *App) Startup(ctx context.Context) {
	a.window = true
	if a.startCore(ctx) == nil {
		upgradeCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		a.stopPairingUpgrade = func() { cancel(); <-done }
		go func() {
			defer close(done)
			a.enrolSavedPairing(upgradeCtx)
		}()
	}
}

// startCore initialises everything the app needs in BOTH roles: config, the local
// store, the embedded catalog, the Docker and native runtime providers (registering the
// native-process supervisor), the collectors, the periodic exchange-rate refresh, the
// loopback fleet API, and the background collection scheduler. It is deliberately free
// of any Wails/UI dependency — the window, tray and webview are set up separately in
// DomReady — so the --daemon path can call it to run the same engine headless.
//
// It returns an error only on a fatal init failure (config/store/catalog); a fleet
// key/listener failure is non-fatal and merely surfaced, matching the original Startup.
// The Wails path ignores the return (the failure is already an app:error event and
// a.ready() reports it); the daemon path logs it and exits.
//
// ctx is stored as a.ctx and drives the exchange refresh, the scheduler and event
// emission: the Wails app context under the GUI, a signal-cancelled context under the
// daemon. Under the daemon that context carries no "events" value, so emitEvent /
// emitError safely no-op (no window/tray code is ever reached).
func (a *App) startCore(ctx context.Context) error {
	a.ctx = ctx

	cfg, err := config.NewManager()
	if err != nil {
		a.failStart("config", err)
		return err
	}
	a.cfg = cfg

	st, err := store.Open(cfg.DataDir())
	if err != nil {
		a.failStart("store", err)
		return err
	}
	a.store = st

	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		a.failStart("catalog", err)
		return err
	}
	a.catalog = cat

	docker := runtime.NewDockerProvider()
	docker.SetIsolationSetting(func() string { return a.cfg.Config().Isolation })
	docker.SetFenceGated(func(slug string) bool {
		svc, ok := cat.Get(slug)
		return ok && svc.Docker.FenceGated
	})
	// A failed write is retried on every check, so it is reported once per failure
	// rather than every five minutes. Docker's own evidence still counts meanwhile.
	var memoryFailing atomic.Bool
	docker.SetEgressLookup(runtime.LookupPublicIP)
	// The owner's other lines: the fleet's exits, from the server and from the workers
	// that report here (fenceOtherExits).
	docker.SetOtherExits(a.fenceOtherExits)
	// Which fence-gated earners the user stopped, so CashPilot does not start them
	// again. A file, read on every check, so the window and the daemon share it.
	docker.SetGatedStoppedFile(filepath.Join(cfg.DataDir(), "fence_gated_stopped"))
	docker.SetIsolationMemory(
		func() bool { return a.cfg.Config().IsolationOn },
		func(on bool) {
			if err := a.cfg.Update(func(c *config.AppConfig) { c.IsolationOn = on }); err != nil {
				if !memoryFailing.Swap(true) {
					a.emitError("isolation", fmt.Errorf("could not save that LAN isolation is on: %w", err))
				}
				return
			}
			memoryFailing.Store(false)
		},
	)
	a.wireHostFacts(ctx, docker)
	a.runtime = docker
	a.fence = docker
	native := runtime.NewNativeProcessProvider(cfg.AppDir())
	// Feed the native supervisor's autonomous crashes/respawns into the same runtime_events
	// stream HealthScores aggregates, so native earners get the crash/restart accounting the
	// Docker path already has. Set on the concrete provider before it is stored behind the
	// Provider interface, and before any Deploy (no supervise goroutine exists yet).
	native.SetEventRecorder(a.store.RecordEvent)
	a.native = native
	a.services = services.NewManager(a.runtime, a.catalog, a.store)
	a.services.Register(runtime.NativeRuntimeKind, a.native)
	a.loadCustom()
	a.collectors = collectors.NewRegistry(a.store, collectors.WithNodeReader(a.readNodes))

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

	// A fleet-token problem must NOT disable core earnings collection: surface it and
	// continue so the scheduler and retention purge below still start. The fleet
	// heartbeat handler fails closed while fleetKey is empty (it returns 503 and
	// persists nothing), so continuing here never leaves the worker API open.
	if err := a.ensureFleetAPIKey(); err != nil {
		a.emitError("fleet-api", err)
	}
	if err := a.startFleetAPI(); err != nil {
		a.emitError("fleet-api", err)
	}

	// Start background earnings collection so the app is genuinely "passive":
	// balances refresh on a timer without the user clicking Collect. This never
	// blocks Startup — startScheduler only launches a goroutine.
	a.startScheduler(ctx)
	// CPU, memory and traffic per container for the usage graphs.
	a.startUsageSampler(ctx)
	// Proxied copies: traffic per proxy, the cap, and earners whose tunnel came back.
	a.startProxyTicker(ctx)
	// Device registrations the provider asked to retry later.
	a.startDeviceLinkRetry(ctx)
	// What each earner talks to, when the owner switched Activity on.
	a.startActivitySampler(ctx)
	// Resolve an interrupted Windows rule change before opening the worker API.
	if a.window {
		a.recoverServerCommandsFirewall()
	}
	// Report to the server when this Desktop is paired.
	a.startUpstream(ctx)
	// While joined, the server deploys and stops services here (worker_api.go).
	a.startWorkerAPI(ctx)
	// The window and the background helper each run the steps above from their
	// own copy of the config: follow a join or a switch the other one saved.
	a.startConfigSync(ctx)
	// The LAN fence: put the table back at startup and every few minutes, so a
	// reboot or a firewall reload leaves the earners offline only briefly, never on
	// the LAN.
	go func() {
		// First: on a CASHPILOT_PROXIED_ONLY machine every second a direct earner
		// runs after a reboot is traffic from its own IP.
		a.services.HoldDirect(ctx)
		// A deploy the app did not finish may have left an earner set aside.
		docker.RecoverSetAside(ctx)
		a.runFence(ctx)
	}()
	return nil
}

func (a *App) runFence(ctx context.Context) {
	ticker := time.NewTicker(runtime.FenceRecheck)
	defer ticker.Stop()
	// Fence-gated earners have no Docker restart policy, so this is what brings one
	// back after a crash (internal/runtime/fence_gated_start.go).
	guard := time.NewTicker(runtime.FenceGuard)
	defer guard.Stop()
	a.fence.ReconcileFence(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.fence.ReconcileFence(ctx)
		case <-guard.C:
			a.services.HoldDirect(ctx)
			a.fence.GuardFenceGated(ctx)
			// Docker tries an earner once at its own start and never again. Its own
			// goroutine: a Start can wait on a deploy's lock, and the fence must not.
			go a.services.StartFailedStarts(ctx)
		}
	}
}

func (a *App) DomReady(ctx context.Context) {
	// a.ctx is set once in Startup with the same Wails app context; deliberately do
	// NOT reassign it here. Binding goroutines (GetAppState -> runtime.Status,
	// computeEarningsSummary -> EnsureFresh, CollectService) read a.ctx
	// concurrently, so a second write would race with those reads under -race.
	// DomReady uses its local ctx parameter directly for the window/tray setup.
	// A screen smaller than the default size gets a window that fits its work
	// area, so the title bar stays on screen and the taskbar stays clear.
	fitMainWindow(ctx)
	wailsruntime.WindowShow(ctx)
	PositionMainWindowOnPrimaryScreen()
	InstallTrayIcon(a.trayIcon)
	// A helper registered by another copy of the app follows this one (helper_follow.go).
	go a.keepHelperOnThisCopy()
}

func (a *App) Shutdown(_ context.Context) {
	if a.stopPairingUpgrade != nil {
		a.stopPairingUpgrade()
	}
	// Stop the collection loop before closing the store so no in-flight collect
	// writes to a database that is about to close.
	a.stopScheduler()
	a.stopUsageSampler()
	a.stopProxyTicker()
	a.stopDeviceLinkRetry()
	a.stopActivitySampler()
	a.stopUpstream()
	a.stopWorkerAPI()
	if a.fleetAPI != nil {
		_ = a.fleetAPI.Close()
	}
	if a.store != nil {
		_ = a.store.Close()
	}
}

type AppState struct {
	Config      config.AppConfig   `json:"config"`
	Runtime     runtime.Status     `json:"runtime"`
	Services    []catalog.Service  `json:"services"`
	Deployments []store.Deployment `json:"deployments"`
	// OutdatedServices lists the slugs of deployed services whose running image no
	// longer matches the catalog's current image (the provider changed or re-pinned
	// it): deployed and often still "running", but likely earning nothing until
	// re-deployed. The frontend badges these; notifications() also alerts on them.
	OutdatedServices []string                     `json:"outdatedServices"`
	Earnings         []store.EarningsRecord       `json:"earnings"`
	Guides           []runtime.InstallGuide       `json:"guides"`
	Notifications    []Notification               `json:"notifications"`
	Currencies       []string                     `json:"currencies"`
	Summary          EarningsSummary              `json:"summary"`
	Health           map[string]store.HealthScore `json:"health"`
	// ProducerStates is the separate "is it actually earning?" verdict per deployed
	// slug (see internal/health). Health above is the container's reputation, which is
	// computed from starts and crashes and so scores a service that has earned nothing
	// for a month at full marks. A service missing from this map has no verdict at all.
	ProducerStates map[string]health.Report `json:"producerStates"`
	// ServiceDetails carries each collector's optional per-service JSON detail blob
	// keyed by slug (e.g. the MystNodes per-node earnings breakdown). The frontend
	// parses the raw JSON per service; the backend stores and forwards it opaquely.
	ServiceDetails map[string]string `json:"serviceDetails"`
	// Fleet is the server's account-level view of the platforms this machine
	// runs, present ONLY while paired and only once the server has reported.
	// Nil means show the local numbers alone -- which is standalone, and is also
	// the state a machine returns to after unlinking.
	Fleet *FleetView `json:"fleet"`
	// Pairing is the link to the CashPilot server while this computer is joined
	// (last heartbeat, last error in plain words); nil when it runs everything here.
	Pairing *PairingStatus `json:"pairing"`
	// ServerCommands is whether that server can start and stop services here, and the
	// last command it sent (worker_api.go); nil when this computer runs everything here.
	ServerCommands *ServerCommandsState `json:"serverCommands"`
	// Hostname is this machine's name, so a deploy form can render a {hostname}-defaulted
	// field with the real value the deploy path will substitute (instead of the literal
	// "cashpilot-{hostname}" the raw catalog default would otherwise show and submit).
	Hostname string `json:"hostname"`
	// CollectorFields carries, per slug, the credential inputs the earnings collector
	// needs that the container's docker.env does not already ask for (a browser cookie,
	// or an account password where the container authenticates with an API key). The
	// wizard form is docker.env plus these, and the backend owns the list because the
	// frontend's own copy of it went stale the moment a web-catalog rename moved a
	// service's env keys away from what its collector reads.
	CollectorFields map[string][]collectors.Field `json:"collectorFields"`
	// Scaling is, per slug, how many copies of the service this machine may run and
	// why not more: the deploy form's Instances box.
	Scaling map[string]services.ScalingLimit `json:"scaling"`
	// Copies is, per slug, copies 2..N of a service that runs more than once. Copy 1
	// is the service's entry in Deployments.
	Copies map[string][]runtime.ContainerInfo `json:"copies"`
	// AddedDevices is, per slug, the device id the last deploy added to the user's
	// account itself (ProxyRack with its API key saved), so no manual step is needed.
	AddedDevices map[string]string `json:"addedDevices"`
	// RetryingDevices is, per slug, the device id the provider did not know yet at
	// deploy time and the app keeps trying to add to the account for a day.
	RetryingDevices map[string]string `json:"retryingDevices"`
	// StateBackups lists the deployed services whose catalog entry marks state as
	// irreplaceable, and whether the user has a backup of it.
	StateBackups []statebackup.Row `json:"stateBackups"`
	// Drift is, per deployed slug, how its running container differs from what a
	// redeploy would build from the current catalog, in plain words (a capability,
	// device, port, mount or env key the catalog added since, or an older pin of our
	// own image). Empty when warnings are off or the user ignored that service at
	// the current catalog.
	Drift map[string][]string `json:"drift"`
	// Isolation is the LAN fence's status: whether earners are fenced off the LAN,
	// this machine, a tailnet and cloud metadata, and which ones are not yet.
	Isolation *runtime.FenceStatus `json:"isolation"`
	// Route is the deploy form's Route box (internal/services/routes.go): the proxies
	// a service can run through from its first start, why each service cannot, and
	// which services are routed.
	Route RouteState `json:"route"`
	// Reminders is the Money waiting panel: what to claim, withdraw or keep active
	// before a provider writes off the money (reminders_app.go).
	Reminders RemindersView `json:"reminders"`
	// Stalls are the running copies that carry almost no traffic while the other
	// copies of their service here are busy (stall_alerts.go).
	Stalls []StallAlert `json:"stalls"`
	// StallsServer is the CashPilot server that raises the stall alerts of this
	// computer's own copies while joined to it, "" when this computer raises them
	// (stallServer). The proxied copies' alerts stay here.
	StallsServer string `json:"stallsServer"`
	// WorkerTargets are the Docker workers the deploy form's Where box offers
	// (lead_workers.go); nil when this computer sends workers no commands.
	WorkerTargets []WorkerTarget `json:"workerTargets"`
}

// RouteState is what the deploy form needs to offer a route.
type RouteState struct {
	// Proxies are the owner's proxies, without their secrets.
	Proxies []RouteProxy `json:"proxies"`
	// Refusals is, per visible service, why it cannot run through a proxy ("" when it can).
	Refusals map[string]string `json:"refusals"`
	// Recorded is slug -> proxy id of every service routed on this machine, running or
	// not: a deploy from this machine's own IP asks the owner first.
	Recorded map[string]int64 `json:"recorded"`
	// Running is slug -> proxy id of the routed services that run as their copy now.
	Running map[string]int64 `json:"running"`
	// ProxiedOnly is CASHPILOT_PROXIED_ONLY: no service runs from this machine's own IP.
	ProxiedOnly bool `json:"proxiedOnly"`
}

// RouteProxy is one proxy in the Route box.
type RouteProxy struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	ExitIP  string `json:"exitIp"`
	Country string `json:"country"`
}

// IsolationResult is what turning isolation on did: the status after, the services
// moved onto the fenced network, and the ones whose redeploy failed with why.
type IsolationResult struct {
	Status runtime.FenceStatus `json:"status"`
	Moved  []string            `json:"moved"`
	Failed map[string]string   `json:"failed"`
}

type Notification struct {
	Level   string `json:"level"`
	Title   string `json:"title"`
	Message string `json:"message"`
	Runtime bool   `json:"runtime,omitempty"`
}

// EarningsSummary is the dashboard's converted, aggregated view of earnings.
// Balances stored per service are CUMULATIVE lifetime totals; the summary turns
// them into a single display-currency total plus per-day accrual figures.
type EarningsSummary struct {
	DisplayCurrency string  `json:"displayCurrency"`
	Total           float64 `json:"total"`
	// TotalKnown is false when Total is 0 only because nothing could be priced.
	// Absent is not zero: the UI must render an em dash, never a fabricated 0.
	TotalKnown   bool             `json:"totalKnown"`
	Today        float64          `json:"today"`
	Month        float64          `json:"month"`
	TodayChange  float64          `json:"todayChange"`
	MonthChange  float64          `json:"monthChange"`
	Breakdown    []ServiceEarning `json:"breakdown"`
	Points       []PointsBalance  `json:"points"`
	Daily        []DailyPoint     `json:"daily"`
	RatesStale   bool             `json:"ratesStale"`
	RatesUpdated string           `json:"ratesUpdated"`
	// PaidOut is every recorded cash-out in the display currency, each valued at
	// the USD rate it landed at; EarnedAllTime is Total plus PaidOut. Total drops
	// on every cash-out, so these say what the services made. Nil when unknown
	// (the ledger could not be read, no display rate, or Total unknown), never 0.
	// UnpricedPayouts counts cash-outs with no rate, left out of PaidOut.
	PaidOut         *float64 `json:"paidOut"`
	EarnedAllTime   *float64 `json:"earnedAllTime"`
	UnpricedPayouts int      `json:"unpricedPayouts"`
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
	Reach         FleetReach          `json:"reach"`
	APIKey        string              `json:"apiKey"`
	APIListening  bool                `json:"apiListening"`
	WorkerSnippet string              `json:"workerSnippet"`
	MobileSnippet string              `json:"mobileSnippet"`
	// WorkerCommands is, per worker id, its copies and why it takes no commands
	// now (lead_workers.go).
	WorkerCommands map[string]WorkerCommandsView `json:"workerCommands"`
}

// Fleet device lifecycle tunables, matching production CashPilot. A worker/mobile that
// stops sending heartbeats is flipped offline once it has been silent longer than
// fleetOfflineAfter (the ~3-missed-heartbeat grace), and a device offline longer than
// fleetReapAfter is removed entirely. collectAll's background sweep applies both to the
// DB; GetFleetState additionally uses fleetOfflineAfter to show a silent device as
// offline immediately, without waiting for the next tick.
const (
	fleetOfflineAfter = 180 * time.Second
	// A phone reports every 15 minutes (Android's WorkManager minimum), so it
	// gets three missed periods, the same rule as a 60 s worker's 180 s.
	fleetOfflineAfterMobile = 45 * time.Minute
	fleetReapAfter          = 1 * time.Hour

	// collectConcurrency bounds how many services collectAll collects at once. Each
	// Collect is a real network round trip (up to ~90s worst case on a stalled
	// provider), so running the batch through a small worker pool turns the cycle's
	// cost from the SUM of per-service latencies into roughly the MAX, without
	// unbounded fan-out against provider APIs or the (SetMaxOpenConns(1)) store.
	collectConcurrency = 6
)

func (a *App) GetAppState() (AppState, error) {
	if err := a.ready(); err != nil {
		return AppState{}, err
	}
	a.retryCustom()
	a.refreshHostSetup()
	// Fetch these once and thread them into the helpers below instead of letting
	// each helper re-run its own full-table scan (they were each queried up to 3x
	// per GetAppState call before this).
	deployments := a.store.ListDeployments()
	earnings := a.store.ListLatestEarnings()
	visible := a.catalog.ListVisible()
	// Docker's status with the native runtime's part added (host_check.go), so the
	// frontend can proceed, and say honestly what runs, on Docker OR native.
	runtimeStatus := a.runtimeStatus(visible)
	return AppState{
		Config:           a.cfg.Config(),
		Runtime:          runtimeStatus,
		Services:         visible,
		Deployments:      deployments,
		OutdatedServices: a.outdatedServices(deployments),
		Earnings:         earnings,
		Guides:           runtime.InstallGuides(),
		Notifications:    a.notifications(runtimeStatus, earnings, deployments),
		Currencies:       supportedCurrencies(),
		Summary:          a.computeEarningsSummary(earnings),
		Health:           a.store.HealthScores(7),
		ProducerStates:   a.producerStates(deployments),
		ServiceDetails:   a.store.ListServiceDetails(),
		Fleet:            a.fleetView(),
		Pairing:          a.pairingStatus(),
		ServerCommands:   a.serverCommandsView(),
		Hostname:         runtime.DeviceHostname(),
		CollectorFields:  collectors.AllFields(),
		Scaling:          a.services.ScalingLimits(visible),
		Copies:           a.services.Copies(),
		AddedDevices:     a.addedDeviceIDs(),
		RetryingDevices:  a.retryingDeviceIDs(),
		StateBackups:     a.stateBackups(deployments),
		Drift:            a.containerDrift(),
		Isolation:        a.isolationStatus(),
		Route:            a.routeState(visible),
		Reminders:        a.reminders(),
		Stalls:           a.currentStalls(),
		StallsServer:     a.stallServer(time.Now()),
		WorkerTargets:    a.workerTargets(),
	}, nil
}

func (a *App) routeState(visible []catalog.Service) RouteState {
	out := RouteState{
		Proxies:     []RouteProxy{},
		Refusals:    map[string]string{},
		Recorded:    map[string]int64{},
		Running:     a.services.Routes(),
		ProxiedOnly: services.ProxiedOnly(),
	}
	if views, err := a.services.ListProxies(); err == nil {
		for _, v := range views {
			out.Proxies = append(out.Proxies, RouteProxy{ID: v.ID, Name: v.Name, ExitIP: v.LastExitIP, Country: v.LastCountry})
		}
	}
	if recorded, err := a.store.ListRoutes(); err == nil {
		out.Recorded = recorded
	}
	for _, svc := range visible {
		out.Refusals[svc.Slug] = a.services.ProxyRefusal(svc.Slug)
	}
	return out
}

func (a *App) isolationStatus() *runtime.FenceStatus {
	if a.fence == nil {
		return nil
	}
	status := a.fence.FenceStatus()
	return &status
}

// EnableIsolation turns LAN isolation on for an upgraded install: it applies the
// fence, then redeploys each running earner onto the fenced network with the settings
// and data it already has. A redeploy is per service, so a service with a running
// copy is redeployed whole, its stopped copies included, and they come back running.
// A service with no running copy is left alone and moves on its next deploy. One
// failed redeploy does not stop the rest; each failure is named.
func (a *App) EnableIsolation() (IsolationResult, error) {
	if err := a.ready(); err != nil {
		return IsolationResult{}, err
	}
	if a.fence == nil {
		return IsolationResult{}, errors.New("this runtime has no LAN fence")
	}
	status, err := a.fence.EnableFence(a.ctx)
	if err != nil {
		a.emitError("isolation", err)
		return IsolationResult{Status: status}, err
	}
	result := IsolationResult{Failed: map[string]string{}}
	for _, slug := range status.Outside {
		if _, known := a.catalog.Get(slug); !known {
			result.Failed[slug] = "not a service CashPilot knows, so it was left as it is"
			continue
		}
		if _, err := a.DeployService(slug, nil, nil, nil); err != nil {
			result.Failed[slug] = err.Error()
			continue
		}
		result.Moved = append(result.Moved, slug)
	}
	a.fence.ReconcileFence(a.ctx)
	result.Status = a.fence.FenceStatus()
	return result, nil
}

// driftTimeout bounds the docker inspects behind the drift check, so a daemon that
// hangs cannot hold the whole dashboard.
const driftTimeout = 5 * time.Second

// containerDrift is the "needs fixing" list the dashboard shows: every deployed
// container that differs from what a redeploy would build, minus the services the
// user ignored at the current catalog, and nothing at all with warnings off.
func (a *App) containerDrift() map[string][]string {
	cfg := a.cfg.Config()
	if cfg.DriftWarningsOff || a.services == nil || a.catalog == nil {
		return nil
	}
	base := a.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, driftTimeout)
	defer cancel()
	return filterIgnoredDrift(a.services.ContainerDrift(ctx), cfg.DriftIgnored, a.catalog)
}

// filterIgnoredDrift drops the services muted at the catalog's current fingerprint.
// A mute taken at an older fingerprint no longer counts: the catalog has changed a
// judged field since, and that change is shown.
func filterIgnoredDrift(found map[string][]string, ignored map[string]string, cat *catalog.Catalog) map[string][]string {
	for slug := range found {
		svc, ok := cat.Get(slug)
		if mark, muted := ignored[slug]; ok && muted && mark == runtime.ShapeFingerprint(svc) {
			delete(found, slug)
		}
	}
	return found
}

// IgnoreDrift mutes the "needs fixing" warning of slug until the catalog changes a
// field the check judges. For a container the user changed on purpose.
func (a *App) IgnoreDrift(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return fmt.Errorf("unknown service: %s", slug)
	}
	// One read-modify-write under the config lock: two Ignores at once must not drop
	// one, and a field another writer changes meanwhile (a session consent) must not
	// be put back. The map is copied, because Config() hands out the stored one.
	fingerprint := runtime.ShapeFingerprint(svc)
	return a.cfg.Update(func(cfg *config.AppConfig) {
		ignored := make(map[string]string, len(cfg.DriftIgnored)+1)
		for k, v := range cfg.DriftIgnored {
			ignored[k] = v
		}
		ignored[slug] = fingerprint
		cfg.DriftIgnored = ignored
	})
}

func (a *App) addedDeviceIDs() map[string]string {
	a.addedMu.Lock()
	defer a.addedMu.Unlock()
	out := make(map[string]string, len(a.addedDevices))
	for slug, id := range a.addedDevices {
		out[slug] = id
	}
	return out
}

// registerDevice adds a just-deployed device to the user's account where the
// provider's API can (ProxyRack, with its API key among the credentials). It never
// fails the deploy, which has already happened: anything short of "on the account"
// leaves the manual step in the deploy output, except a device the provider does
// not know yet, which is stored and retried (app_device_links.go).
func (a *App) registerDevice(slug string, creds map[string]string) {
	devicesReg, ok := a.collectors.(deviceRegistry)
	svc, inCatalog := a.catalog.Get(slug)
	if !ok || !inCatalog || svc.DeviceLink == nil {
		return
	}
	id := strings.TrimSpace(creds[svc.DeviceLink.IDEnv])
	a.dropOtherDeviceLinks(slug, id)
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	result := devicesReg.RegisterDevice(ctx, slug, creds)
	if result == collectors.RegisterPending && a.store != nil {
		now := time.Now()
		if err := a.store.SavePendingDeviceLink(slug, id, now, now.Add(deviceLinkFirstRetry)); err != nil {
			log.Printf("device links: store %s: %v", slug, err)
		}
	}
	if result == collectors.RegisterAdded && a.store != nil {
		_ = a.store.DeletePendingDeviceLink(slug, id)
	}
	if result != collectors.RegisterAdded {
		a.addedMu.Lock()
		delete(a.addedDevices, slug)
		a.addedMu.Unlock()
		return
	}
	a.setAddedDevice(slug, id)
}

func (a *App) setAddedDevice(slug, id string) {
	a.addedMu.Lock()
	defer a.addedMu.Unlock()
	if a.addedDevices == nil {
		a.addedDevices = map[string]string{}
	}
	a.addedDevices[slug] = id
}

// GetEarningsSummary returns the converted, aggregated earnings summary on its
// own so the frontend can refresh it without pulling the whole app state.
func (a *App) GetEarningsSummary() (EarningsSummary, error) {
	if err := a.ready(); err != nil {
		return EarningsSummary{}, err
	}
	return a.computeEarningsSummary(a.store.ListLatestEarnings()), nil
}

// computeEarningsSummary converts the per-service CUMULATIVE daily balances into
// a single display-currency total plus per-day accrual figures. Convertible
// currencies (USD, known fiat, priced crypto) are summed; non-convertible reward
// points (e.g. GRASS) are surfaced separately and never summed. It is
// stale-graceful: missing rates simply drop a service from the total and set
// RatesStale, rather than erroring.
func (a *App) computeEarningsSummary(earnings []store.EarningsRecord) EarningsSummary {
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
	// priced / unpriced separate "nothing to add up" from "nothing could be
	// converted" -- see the TotalKnown assignment below.
	priced, unpriced := 0, 0
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
			priced++
			continue
		}
		unpriced++
		summary.RatesStale = true
	}
	summary.Total = total
	// A total of 0 means two completely different things and the UI cannot tell
	// them apart from the number alone: a user who has genuinely earned nothing,
	// and a user whose balances could not be priced at all. The second is an
	// UNKNOWN total, not a zero one -- and it is not exotic, because ToDisplay
	// routes through USD, so one missing DISPLAY-currency rate makes every
	// platform unpriceable at once. Reporting 0 there tells a user with real
	// money that they have none.
	//
	// Known when something was actually priced, or when there was nothing to
	// price in the first place (a new install really is at zero). A partial sum
	// stays known and is flagged stale, because an understated real figure is
	// still a measurement.
	summary.TotalKnown = priced > 0 || unpriced == 0
	a.addLifetime(&summary)

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
	for _, rec := range earnings {
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
		{Key: "CASHPILOT_API_KEY", Label: "Fleet API Key", Value: a.fleetKey, Source: "Config", Secret: true, ReadOnly: true, Help: "Bearer token used by external workers and mobile clients."},
		{Key: "CASHPILOT_UI_URL", Label: "Desktop API URL", Value: a.fleetUIURL(), Source: "Runtime", ReadOnly: true, Help: "URL that external workers should use for CASHPILOT_UI_URL."},
		{Key: "CASHPILOT_FLEET_SERVER", Label: "Accept workers (hub mode)", Value: strconv.FormatBool(cfg.FleetServerEnabled), Source: "Config", Help: "OFF by default, and that is the intended shape: CashPilot Desktop is a SPOKE. The CashPilot server is the hub. Turning this on lets other workers and phones report to THIS machine, which makes it a second source of truth for a fleet -- only do that if you are deliberately running Desktop as a hub instead of a CashPilot server."},
		{Key: "CASHPILOT_FLEET_BIND", Label: "Fleet Bind Address", Value: cfg.FleetBindAddress, Source: "Config", Help: "Default 127.0.0.1 (this machine only). Set to 0.0.0.0 only to accept worker/mobile connections from your LAN — this exposes the API to your network, and (if metrics are enabled) also serves the UNAUTHENTICATED /metrics endpoint — your earnings and health totals — to anyone on your LAN."},
		{Key: "CASHPILOT_FLEET_PORT", Label: "Fleet API Port", Value: strconv.Itoa(cfg.FleetPort), Source: "Config", Help: "Port used for external worker heartbeats."},
		{Key: "CASHPILOT_DRIFT_WARNINGS", Label: "Needs-fixing warnings", Value: strconv.FormatBool(!cfg.DriftWarningsOff), Source: "Config", Help: "On by default. Warns when a running container no longer matches what a redeploy would build from the current catalog (a capability, device, port, mount or setting added since). Set to false to turn the warnings off."},
		{Key: "CASHPILOT_ISOLATION", Label: "LAN isolation", Value: runtime.FenceSetting(cfg.Isolation), Source: "Config", Help: isolationHelp(a.catalog.ListVisible())},
		{Key: "TZ", Label: "System Timezone", Value: cfg.Timezone, Source: "Config", Help: "Timezone passed to future managed workers and mobile sync events."},
		{Key: "CASHPILOT_UPSTREAM_URL", Label: "Pair with CashPilot server", Value: cfg.UpstreamURL, Source: "Config", Help: "Leave EMPTY to run everything here (standalone), the default. To join a CashPilot, use How this computer runs above: it tests the connection before it saves anything. While joined, this computer reports what it runs and stops collecting earnings itself; your CashPilot collects them and shows them. Use https, or plain http only to this machine, a LAN or Tailscale address, or a .local/.lan/.home.arpa/.internal/.ts.net name, so the pairing key never crosses the internet in clear text."},
		{Key: "CASHPILOT_UPSTREAM_KEY", Label: "Pairing key", Value: a.upstreamEnrolmentKey(), Source: "Config", Secret: true, Help: "The CASHPILOT_API_KEY of the server above, used once to enrol. The server then issues this machine its own key, which is what authenticates every later heartbeat."},
		{Key: "CASHPILOT_DATA_DIR", Label: "Data Directory", Value: a.cfg.DataDir(), Source: "Read-only", ReadOnly: true, Help: "Directory containing the local SQLite database."},
		{Key: "CASHPILOT_RUNTIME_PROVIDER", Label: "Runtime provider (chosen)", Value: cfg.RuntimeProvider, Source: "Read-only", ReadOnly: true, Help: "The kind of runtime CashPilot is set to use. existing-docker means a Docker-compatible engine you install, such as Docker Desktop. It does not mean one is installed or running: the Runtime card above says that."},
	}
	collectors := make([]CollectorSetting, 0)
	for _, svc := range a.catalog.ListVisible() {
		if svc.Collector.Type == "" && !svc.ManualOnly {
			continue
		}
		creds, err := a.store.GetCredentials(svc.Slug)
		if err != nil {
			// A credential blob that fails to decrypt/parse must not be silently
			// dropped (which would wrongly show the service as "not configured");
			// surface it while keeping the rest of the settings list usable.
			a.emitError("credentials", err)
		}
		collectors = append(collectors, CollectorSetting{
			Slug:       svc.Slug,
			Name:       svc.Name,
			Configured: a.credsConfigured(svc.Slug, creds),
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
	previousInterval := cfg.CollectIntervalMinutes
	previousFleetPort := cfg.FleetPort
	previousFleetBind := cfg.FleetBindAddress
	previousIsolation := cfg.Isolation
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
	if value, present := values["fleetServerEnabled"]; present {
		// Presence-keyed, not non-emptiness: "false" is a meaningful value and
		// turning the hub back OFF must be possible.
		cfg.FleetServerEnabled = strings.EqualFold(strings.TrimSpace(value), "true")
	}
	if value, present := values["driftWarnings"]; present {
		// Presence-keyed: "false" is meaningful. The spellings the web accepts.
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "off", "false", "0", "no":
			cfg.DriftWarningsOff = true
		default:
			cfg.DriftWarningsOff = false
		}
	}
	if value, present := values["isolation"]; present {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "auto":
			cfg.Isolation = ""
		case "on", "off":
			cfg.Isolation = strings.ToLower(strings.TrimSpace(value))
		default:
			return SettingsState{}, fmt.Errorf("LAN isolation must be auto, on or off, not %q", value)
		}
		// Off forgets that isolation was on in this same save, so it holds even while
		// Docker is unreachable and the reconcile below cannot run.
		if cfg.Isolation == "off" {
			cfg.IsolationOn = false
		}
	}
	if value := strings.TrimSpace(values["fleetBindAddress"]); value != "" {
		// Only a parseable IP or "localhost" is a valid bind host (an empty value
		// keeps the loopback default via config.Normalize). A bad value stored verbatim
		// would silently brick the fleet listener on the next launch, so reject it here.
		if value != "localhost" && net.ParseIP(value) == nil {
			return SettingsState{}, fmt.Errorf("invalid fleet bind address %q", value)
		}
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
	address, hasAddress := values["upstreamUrl"]
	key, hasKey := values["upstreamKey"]
	left := false
	if hasAddress || hasKey {
		a.pairingMu.Lock()
		defer a.pairingMu.Unlock()
		var err error
		left, err = a.saveSettingsPairing(strings.TrimRight(strings.TrimSpace(address), "/"), hasAddress, strings.TrimSpace(key), hasKey)
		if err != nil {
			return SettingsState{}, err
		}
	}
	pairing := a.cfg.Config()
	joined := pairing.Mode == config.ModeFollower
	err := a.cfg.Update(func(cur *config.AppConfig) {
		cur.DisplayCurrency = cfg.DisplayCurrency
		cur.HostnamePrefix = cfg.HostnamePrefix
		cur.Timezone = cfg.Timezone
		if _, set := values["fleetServerEnabled"]; set || (hasAddress && joined && cfg.UpstreamURL != pairing.UpstreamURL) {
			cur.FleetServerEnabled = cfg.FleetServerEnabled && !joined
		}
		cur.DriftWarningsOff = cfg.DriftWarningsOff
		cur.Isolation = cfg.Isolation
		if cfg.Isolation == "off" {
			cur.IsolationOn = false
		}
		cur.FleetBindAddress = cfg.FleetBindAddress
		cur.CollectIntervalMinutes = cfg.CollectIntervalMinutes
		cur.FleetPort = cfg.FleetPort
	})
	if err != nil {
		return SettingsState{}, err
	}
	if left {
		if err := a.removeServerCommandsRule(); err != nil {
			a.emitNotice("server-commands", err.Error())
		}
	}
	// If the collection cadence changed, restart the ticker so the new interval
	// takes effect immediately instead of waiting for the next app launch.
	if (left || cfg.CollectIntervalMinutes != previousInterval) && a.ctx != nil {
		a.runScheduler(a.ctx, a.collectInterval())
	}
	// The fleet listener binds its port/address once at Startup; changing them here
	// does not rebind the already-running server (a live restart is deferred). Surface
	// a notice so the UI does not keep advertising a now-dead port to the user until
	// the app is restarted.
	if cfg.FleetPort != previousFleetPort || cfg.FleetBindAddress != previousFleetBind {
		a.emitNotice("fleet-api", "Fleet API port/bind change takes effect after restarting the app.")
	}
	// A changed isolation setting is checked at once rather than at the next five-minute
	// pass, so turning it off drops the filtering now and the dashboard says so.
	if cfg.Isolation != previousIsolation && a.fence != nil && a.ctx != nil {
		go a.fence.ReconcileFence(a.ctx)
	}
	// Pairing DOES take effect immediately, unlike the fleet listener above --
	// startUpstream stops any previous loop first, so this cannot leave two loops
	// reporting under one identity. It is also the no-op path when the URL is
	// empty, which is how "unpair" takes effect without a restart.
	if a.ctx != nil {
		a.startUpstream(a.ctx)
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
	for i := range devices {
		// Reflect the offline grace on the read path so opening the Fleet view is
		// accurate between scheduler ticks: a device silent longer than
		// fleetOfflineAfter shows offline immediately. This adjusts only the returned
		// value — the actual DB flip and reap happen in collectAll's background sweep.
		devices[i].Status = store.EffectiveFleetStatus(devices[i].Status, devices[i].LastSeen, fleetOfflineAfterFor(devices[i].Kind))
		if devices[i].Kind == "mobile" {
			mobiles++
		} else {
			workers++
		}
		if devices[i].Status == "online" {
			online++
		}
	}
	services := len(a.catalog.ListVisible())
	uiURL := a.fleetUIURL()
	localAPIURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.FleetPort)
	return a.withWorkerCommands(FleetState{
		Workers:       workers,
		Mobiles:       mobiles,
		Online:        online,
		Services:      services,
		Devices:       devices,
		UIURL:         uiURL,
		LocalAPIURL:   localAPIURL,
		Reach:         a.fleetReachNow(),
		APIKey:        a.fleetKey,
		APIListening:  a.fleetAPI != nil,
		WorkerSnippet: fmt.Sprintf("CASHPILOT_UI_URL=%s\nCASHPILOT_API_KEY=%s\nCASHPILOT_WORKER_NAME=%s-worker\nCASHPILOT_WORKER_URL=http://<worker-lan-ip>:8081", uiURL, a.fleetKey, cfg.HostnamePrefix),
		MobileSnippet: fmt.Sprintf("CASHPILOT_UI_URL=%s\nCASHPILOT_API_KEY=%s\nDevice type: mobile", uiURL, a.fleetKey),
	}), nil
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
	a.fleetMu.Lock()
	err := a.store.DeleteFleetDevice(id)
	a.fleetMu.Unlock()
	if err != nil {
		return FleetState{}, err
	}
	return a.GetFleetState()
}

func (a *App) CompleteOnboarding() error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.cfg.Update(func(cfg *config.AppConfig) { cfg.FirstRunComplete = true })
}

func (a *App) CheckRuntime() (runtime.Status, error) {
	if err := a.ready(); err != nil {
		return runtime.Status{}, err
	}
	// Cache-only: refreshState calls this after every service action, so it must
	// never wait for a host reading. "Check again" on first run calls HostCheck,
	// which reads the PC afresh. Native availability rides alongside Docker;
	// Available still reflects Docker.
	return a.runtimeStatus(a.catalog.ListVisible()), nil
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

// PreflightService reports what could stop a service earning on THIS machine,
// before the user deploys it: a provider that forbids containers, an image with no
// build for this CPU, a service already running on another machine in the fleet,
// and the requirements nobody can verify from here. The frontend checks the
// shared Docker status separately when it renders the report and Deploy button.
func (a *App) PreflightService(slug string) (preflight.Report, error) {
	if err := a.ready(); err != nil {
		return preflight.Report{}, err
	}
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return preflight.Report{}, fmt.Errorf("unknown service: %s", slug)
	}
	devices := a.store.ListFleetDevices()
	fleet := make([]preflight.Device, 0, len(devices))
	for _, device := range devices {
		fleet = append(fleet, preflight.Device{Name: device.Name, Services: device.Services})
	}
	return preflight.Assess(preflight.Input{
		Service:    svc,
		Fleet:      fleet,
		DaemonArch: preflight.DaemonArch(a.ctx),
	}), nil
}

// DeployService deploys slug with the submitted credentials. image is the Image box:
// nil (left alone) keeps the image the deployment already uses, "" goes back to the
// catalog's, anything else is the image to run instead. instances is the Instances
// box: nil keeps the number of copies the deployment already runs (1 for a new one).
func (a *App) DeployService(slug string, values map[string]string, image *string, instances *int) (store.Deployment, error) {
	deployment, _, err := a.deployService(slug, values, services.DeployOptions{Image: image, Instances: instances}, true)
	return deployment, err
}

type deployCredentialsLock struct {
	deploy        sync.Mutex
	explicitSaves uint64 // guarded by App.credentialsMu
}

func (a *App) deployCredentialsLock(slug string) *deployCredentialsLock {
	if _, ok := a.catalog.Get(slug); !ok {
		return &deployCredentialsLock{}
	}
	entry, _ := a.deployCredentialsLocks.LoadOrStore(slug, &deployCredentialsLock{})
	return entry.(*deployCredentialsLock)
}

func (l *deployCredentialsLock) startDeploy(credentialsMu *sync.Mutex) uint64 {
	// Let an in-progress credential write finish before taking the snapshot.
	// Release the mutex before queuing so the active deploy can finish its write
	// and an explicit save made while this deploy waits can still win.
	credentialsMu.Lock()
	startSaves := l.explicitSaves
	credentialsMu.Unlock()
	l.deploy.Lock()
	return startSaves
}

// register lets a local deploy add the device to the provider account. A deploy
// sent by the server leaves registration to the server.
func (a *App) deployService(slug string, values map[string]string, opts services.DeployOptions, register bool) (store.Deployment, *runtime.WalletResult, error) {
	if err := a.ready(); err != nil {
		return store.Deployment{}, nil, err
	}
	a.retryCustom()
	lock := a.deployCredentialsLock(slug)
	startSaves := lock.startDeploy(&a.credentialsMu)
	defer lock.deploy.Unlock()
	creds, err := a.prepareDeployCredentials(slug, values, opts.Image)
	if err != nil {
		return store.Deployment{}, nil, err
	}
	if err := a.refuseWithoutDocker(slug); err != nil {
		return store.Deployment{}, nil, err
	}
	deployment, walletResult, deployErr := a.services.DeployWallet(a.ctx, slug, creds, opts)
	if deployErr != nil && deployment.Slug == "" {
		a.emitError("deploy", deployErr)
		return store.Deployment{}, nil, deployErr
	}
	saved, saveErr := a.saveLocalDeployCredentials(slug, values, lock, startSaves)
	a.emitEvent("deployment:changed", deployment)
	if saveErr != nil {
		a.emitError("credentials", saveErr)
	}
	if deployErr != nil {
		if saveErr != nil {
			deployErr = errors.Join(deployErr, saveErr)
		} else if saved {
			for key, secret := range a.credentialFields(slug) {
				if secret && strings.TrimSpace(values[key]) != "" {
					deployErr = savedPartialDeployError{deployErr}
					break
				}
			}
		}
		a.emitError("deploy", deployErr)
		return deployment, nil, deployErr
	}
	if saveErr != nil {
		return deployment, walletResult, saveErr
	}
	if register {
		a.registerDevice(slug, creds)
	}
	// Collect this service's balance right away so the dashboard shows a figure
	// shortly after deploy instead of waiting for the next scheduled tick. Only
	// services with a native collector are worth kicking; the rest would merely
	// persist a "not ported yet" error row.
	if a.collectors != nil && a.collectors.Supports(slug) {
		go a.collectOne(a.ctx, slug)
	}
	return deployment, walletResult, nil
}

// DeployServiceRouted deploys slug through proxy proxyID from its first start, so it
// never runs from this machine's own IP (internal/services/routes.go).
func (a *App) DeployServiceRouted(slug string, values map[string]string, proxyID int64) (services.ProxiedResult, error) {
	if err := a.ready(); err != nil {
		return services.ProxiedResult{}, err
	}
	lock := a.deployCredentialsLock(slug)
	startSaves := lock.startDeploy(&a.credentialsMu)
	defer lock.deploy.Unlock()
	creds, err := a.prepareDeployCredentials(slug, values, nil)
	if err != nil {
		return services.ProxiedResult{}, err
	}
	if err := a.refuseWithoutDocker(slug); err != nil {
		return services.ProxiedResult{}, err
	}
	result, err := a.services.DeployRouted(a.ctx, slug, creds, proxyID)
	if err != nil {
		a.emitError("deploy", err)
		return services.ProxiedResult{}, err
	}
	_, saveErr := a.saveLocalDeployCredentials(slug, values, lock, startSaves)
	a.emitEvent("deployment:changed", slug)
	if saveErr != nil {
		a.emitError("credentials", saveErr)
		return result, saveErr
	}
	a.registerDevice(slug, creds)
	return result, nil
}

// DeployServiceLeavingRoute is DeployService for a service routed through a proxy, once
// the owner confirmed it runs from this machine's own IP from now on. Its routed copy
// is removed first.
func (a *App) DeployServiceLeavingRoute(slug string, values map[string]string, image *string, instances *int) (store.Deployment, error) {
	deployment, _, err := a.deployService(slug, values, services.DeployOptions{Image: image, Instances: instances, LeaveRoute: true}, true)
	return deployment, err
}

func (a *App) StopService(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.Stop(a.ctx, slug); err != nil {
		a.emitError("stop", err)
		return err
	}
	a.emitEvent("deployment:changed", slug)
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
	a.emitEvent("deployment:changed", slug)
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
	a.emitEvent("deployment:changed", slug)
	return nil
}

// PlanServiceRemoval reports what removing a service would delete, so the app can ask
// a question that names the data rather than one that hides it behind "and its
// volumes".
func (a *App) PlanServiceRemoval(slug string) (runtime.RemovalPlan, error) {
	if err := a.ready(); err != nil {
		return runtime.RemovalPlan{}, err
	}
	return a.services.PlanRemoval(a.ctx, slug)
}

// RemoveService removes the service's container. deleteData decides whether the data
// it stored is destroyed with it, and allowCritical is the separate yes required for
// data that cannot be recovered — a node identity, a keystore. Both default to false
// at the call site, so the ordinary remove never destroys anything irreversible.
func (a *App) RemoveService(slug string, deleteData, allowCritical bool) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.Remove(a.ctx, slug, deleteData, allowCritical); err != nil {
		a.emitError("remove", err)
		return err
	}
	// emitEvent, not EventsEmit: the raw call log.Fatalf's the whole process when the
	// context carries no Wails event manager, which is every test. The only
	// irreversible path in the app was untestable end to end because of it.
	a.emitEvent("deployment:changed", slug)
	return nil
}

// ListCustomServices returns the services the user added from an image reference,
// as the editor may see them: a secret variable's value is never sent.
func (a *App) ListCustomServices() ([]services.CustomServiceView, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.services.ListCustom()
}

// SaveCustomService creates a custom service when slug is "", or replaces the
// definition of an existing one. A secret left blank keeps its stored value. A
// running copy keeps what it was deployed with until it is redeployed.
func (a *App) SaveCustomService(slug string, definition services.CustomDefinition) (services.CustomServiceView, error) {
	if err := a.ready(); err != nil {
		return services.CustomServiceView{}, err
	}
	view, err := a.services.SaveCustom(slug, definition)
	if err != nil {
		return services.CustomServiceView{}, err
	}
	a.emitEvent("deployment:changed", view.Slug)
	return view, nil
}

// DeleteCustomService deletes a custom service's definition. It is refused while
// this computer still runs or records it. Its data volume, if it was kept on
// removal, stays; the slug is never given out again, so nothing else mounts it.
func (a *App) DeleteCustomService(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.DeleteCustom(a.ctx, slug); err != nil {
		return err
	}
	a.emitEvent("deployment:changed", slug)
	return nil
}

// StopServiceCopy, StartServiceCopy and GetCopyLogs act on copy n (2 and up) of a
// service that runs more than once. Copy 1 is the service: StopService and the rest.
func (a *App) StopServiceCopy(slug string, n int) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.StopCopy(a.ctx, slug, n); err != nil {
		a.emitError("stop", err)
		return err
	}
	a.emitEvent("deployment:changed", slug)
	return nil
}

func (a *App) StartServiceCopy(slug string, n int) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.StartCopy(a.ctx, slug, n); err != nil {
		a.emitError("start", err)
		return err
	}
	a.emitEvent("deployment:changed", slug)
	return nil
}

func (a *App) GetCopyLogs(slug string, n, lines int) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	return a.services.LogsCopy(a.ctx, slug, n, lines)
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
	a.refreshConfig()
	if a.joined() {
		return store.EarningsRecord{}, errJoinedNoCollect
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
	a.emitEvent("earnings:changed", record)
	return record, nil
}

// collectOne runs a single service's collector and emits earnings:changed. It is
// the fire-and-forget variant of CollectService used for the post-deploy balance
// fetch: a collector failure is already persisted as an error record (surfaced by
// notifications()), so only store/transport errors are reported here.
func (a *App) collectOne(ctx context.Context, slug string) {
	a.refreshConfig()
	if a.store == nil || a.collectors == nil || a.joined() {
		return
	}
	creds, err := a.store.GetCredentials(slug)
	if err != nil {
		a.emitError("collector", err)
		return
	}
	record, err := a.collectors.Collect(ctx, slug, creds)
	if err != nil {
		a.emitError("collector", err)
		return
	}
	a.emitEvent("earnings:changed", record)
}

// collectAll runs one full background collection cycle: it collects every supported
// service that is either deployed OR has saved credentials (deduped), loading each
// service's stored credentials. Imageless services (e.g. vast-ai, salad, grass,
// bytelixir) never create a deployment row, so unioning the credential set makes them
// participate in the scheduled cycle instead of only collecting on a manual click.
// A failing collector never aborts the batch — its error is already persisted as an
// EarningsRecord (surfaced by notifications()); a store/transport error is logged
// via emitError and the batch continues with the other services. Services are
// collected concurrently through a bounded worker pool (collectConcurrency) instead
// of one at a time, so the cycle's wall-clock cost is roughly the slowest single
// collector rather than the sum of all of them; a.emitError/emitEvent are safe to
// call from multiple goroutines (they only touch the set-once a.ctx and the
// thread-safe wailsruntime.EventsEmit) and the store serializes concurrent writes
// itself (SetMaxOpenConns(1)), so no extra locking is needed here. A single-flight
// guard (a.collecting) means overlapping triggers — the ticker, a post-deploy kick, a
// manual refresh — never stack; a run already in progress is skipped rather than
// queued. One earnings:changed event is emitted after the batch so the dashboard
// refreshes once per cycle.
func (a *App) collectAll(ctx context.Context) {
	if a.store == nil || a.collectors == nil {
		return
	}
	if !a.collecting.CompareAndSwap(false, true) {
		return
	}
	defer a.collecting.Store(false)

	// Collect every supported service that is either deployed OR has saved
	// credentials. Imageless services (e.g. vast-ai, salad, grass, bytelixir)
	// never create a deployment row, so without the credential set they would
	// only collect on a manual Collect click; unioning the two sets makes them
	// participate in the scheduled cycle. A service in both sets is collected once.
	slugs := make([]string, 0)
	seen := make(map[string]bool)
	add := func(slug string) {
		if seen[slug] || !a.collectors.Supports(slug) {
			return
		}
		seen[slug] = true
		slugs = append(slugs, slug)
	}
	// While joined, the CashPilot server logs in to the vendor accounts and this
	// computer does not, so the two never sign in to the same accounts. The health,
	// purge and fleet steps below still run. The file is read first: the join may
	// have been made in the other process (the window, while this is the helper).
	a.refreshConfig()
	if !a.joined() {
		for _, dep := range a.store.ListDeployments() {
			add(dep.Slug)
		}
		for _, slug := range a.store.ListCredentialSlugs() {
			add(slug)
		}
	}

	// Collect the batch through a bounded worker pool: sem caps how many Collect
	// calls run at once (collectConcurrency) so a large slug set can't fan out
	// unbounded HTTP requests, and wg is waited on below so every launched goroutine
	// has fully finished — success or error — before the post-loop steps (sampleHealth,
	// PurgeOldData, SweepStaleFleetDevices) run. collected is an atomic counter since
	// it is incremented from every worker goroutine.
	var collected atomic.Int32
	var wg sync.WaitGroup
	sem := make(chan struct{}, collectConcurrency)
	aborted := false
	for _, slug := range slugs {
		if ctx.Err() != nil {
			// Mirror the previous behavior: a cancelled ctx abandons the rest of the
			// batch (including the post-loop steps) rather than starting more work,
			// but goroutines already launched are still waited on below so none leak.
			aborted = true
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			defer func() { <-sem }()
			creds, err := a.store.GetCredentials(slug)
			if err != nil {
				a.emitError("collector", err)
				return
			}
			if _, err := a.collectors.Collect(ctx, slug, creds); err != nil {
				a.emitError("collector", err)
				return
			}
			collected.Add(1)
		}(slug)
	}
	wg.Wait()
	if aborted {
		return
	}
	if collected.Load() > 0 {
		a.emitEvent("earnings:changed", int(collected.Load()))
	}

	// Sample each deployed service's up/down state so the health score's uptime%
	// has data to work with. This piggybacks on the collection tick under the same
	// single-flight guard, recording one health_up/health_down runtime_event per
	// deployed service; PurgeOldData (below) prunes them with the rest.
	a.sampleHealth(ctx)

	// Bound local database growth: drop earnings/runtime_events rows older than the
	// retention window (keeping the latest earnings row per platform). a.store is
	// non-nil here (guarded at function entry). Runs every cycle — the DELETE is
	// cheap and idempotent — so no extra timer is needed. A purge failure must not
	// abort the cycle: log it and carry on, the next cycle simply retries.
	if _, err := a.store.PurgeOldData(a.retentionDays()); err != nil {
		a.emitError("store", err)
	}

	// Age silent fleet devices out of the online set and reap long-dead ones so
	// GetFleetState stops reporting a device online forever: a worker/mobile that has
	// missed heartbeats past fleetOfflineAfter is flipped offline, and one offline past
	// fleetReapAfter is deleted. a.store is non-nil here (guarded at entry) but keep the
	// nil-guard defensively; a sweep failure must not abort the cycle — log it and
	// continue, the next cycle simply retries.
	if a.store != nil {
		a.fleetMu.Lock()
		_, _, err := a.store.SweepStaleFleetDevices(fleetOfflineAfter, fleetOfflineAfterMobile, fleetReapAfter)
		a.fleetMu.Unlock()
		if err != nil {
			a.emitError("fleet", err)
		}
	}
}

// sampleHealth records one uptime datapoint per deployed service: a health_up
// event when the service's managed container is currently running, health_down
// otherwise. These rows feed store.HealthScores' uptime% and are pruned by
// PurgeOldData with the other runtime_events. It runs inside collectAll (under the
// single-flight guard) rather than on its own timer, so it needs no extra ticker.
// It never aborts the cycle: if every runtime provider fails to list (all offline)
// the error is surfaced via emitError and sampling is skipped for this tick. The
// nil guards mirror collectAll's, so the scheduler tests — which inject no services
// manager — are a no-op here.
func (a *App) sampleHealth(ctx context.Context) {
	if a.store == nil || a.services == nil {
		return
	}
	deployments := a.store.ListDeployments()
	if len(deployments) == 0 {
		return
	}
	// List unions every registered runtime provider; with only Docker registered
	// this is Docker's managed-container list (or its error) exactly as before.
	containers, err := a.services.List(ctx)
	if err != nil {
		a.emitError("health", err)
		return
	}
	running := make(map[string]bool, len(containers))
	for _, c := range containers {
		// The first copy is the service's uptime; a running copy 2 does not make a
		// stopped copy 1 healthy.
		if c.Slug != "" && c.Status == "running" && c.Instance <= 1 {
			running[c.Slug] = true
		}
	}
	for _, dep := range deployments {
		if running[dep.Slug] {
			a.store.RecordEvent(dep.Slug, "health_up", "")
		} else {
			a.store.RecordEvent(dep.Slug, "health_down", "")
		}
	}
}

// retentionDays is the configured data-retention window in days, applying the
// 400-day default for a non-positive setting. collectAll purges earnings and
// runtime_events rows older than this each cycle so the local database does not
// grow without bound.
func (a *App) retentionDays() int {
	days := 400
	if a.cfg != nil {
		if d := a.cfg.Config().RetentionDays; d > 0 {
			days = d
		}
	}
	return days
}

// collectInterval is the configured collection cadence as a duration, applying the
// 60-minute default for a non-positive setting.
func (a *App) collectInterval() time.Duration {
	minutes := 60
	if a.cfg != nil {
		if m := a.cfg.Config().CollectIntervalMinutes; m > 0 {
			minutes = m
		}
	}
	return time.Duration(minutes) * time.Minute
}

// startScheduler launches the background collection loop from Startup. It never
// blocks: runScheduler starts a goroutine and returns immediately.
func (a *App) startScheduler(ctx context.Context) {
	a.runScheduler(ctx, a.collectInterval())
}

// runScheduler starts (or restarts) the periodic collection loop with the given
// interval. The interval is a parameter rather than read from config so tests can
// inject a short cadence; a non-positive value falls back to the 60-minute default.
// Any previously running loop is cancelled first. The loop runs one collectAll
// immediately, then every interval, and exits cleanly when ctx is cancelled or the
// loop is stopped/replaced — no goroutine leak.
func (a *App) runScheduler(ctx context.Context, interval time.Duration) {
	if ctx == nil {
		return
	}
	if interval <= 0 {
		interval = 60 * time.Minute
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	a.schedMu.Lock()
	if a.schedCancel != nil {
		a.schedCancel() // cancel the prior loop; it drains and exits on its own
	}
	a.schedCancel = cancel
	a.schedDone = done
	a.schedMu.Unlock()

	go func() {
		defer close(done)
		a.collectAll(loopCtx) // one run shortly after start
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				a.collectAll(loopCtx)
			}
		}
	}()
}

// stopScheduler cancels the background collection loop and waits for it to exit, so
// Shutdown never leaves the goroutine writing to a closing store. It is idempotent;
// cancelling the context also aborts any in-flight collector HTTP request, so the
// join returns promptly.
func (a *App) stopScheduler() {
	a.schedMu.Lock()
	cancel := a.schedCancel
	done := a.schedDone
	a.schedCancel = nil
	a.schedDone = nil
	a.schedMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (a *App) ManagedRuntimePlan() runtime.ManagedRuntimePlan {
	return runtime.ManagedRuntimeRoadmap()
}

func (a *App) notifications(status runtime.Status, earnings []store.EarningsRecord, deployments []store.Deployment) []Notification {
	var items []Notification
	if notice := runtimeNotification(status); notice != nil {
		items = append(items, *notice)
	}
	// A container whose image no longer matches the catalog often keeps running (so it
	// looks healthy) while the retired provider client earns nothing — surface it so the
	// user re-deploys instead of trusting the green status.
	for _, slug := range a.outdatedServices(deployments) {
		name := slug
		if svc, ok := a.catalog.Get(slug); ok {
			name = svc.Name
		}
		items = append(items, Notification{
			Level:   "warning",
			Title:   name + " update available",
			Message: "The provider changed this service's image. Re-deploy it from the catalog so it keeps earning.",
		})
	}
	// Both come from the last local collection. While joined there is none, so
	// they would stay on screen from before the join, naming errors this computer
	// will never retry: your CashPilot reports them instead.
	if !a.joined() {
		for _, record := range earnings {
			if record.Error != "" {
				items = append(items, Notification{Level: "error", Title: record.Platform + " collector", Message: record.Error})
			}
		}
		items = append(items, a.unlinkedDeviceNotifications(deployments)...)
	}
	items = append(items, a.deviceLinkGaveUpNotifications()...)
	items = append(items, a.proxyNotifications()...)
	items = append(items, a.activityNotifications()...)
	items = append(items, a.stallNotifications()...)
	if len(deployments) == 0 {
		items = append(items, Notification{Level: "info", Title: "No services deployed", Message: "Use the setup wizard or register a mobile device to start tracking earnings."})
	}
	return items
}

// unlinkedDeviceNotifications warns, per deployed service, about device ids the
// provider itself says are not on the user's account (collectors/devices.go): such a
// node runs and earns for nobody. Only that answer counts, never a quiet balance.
func (a *App) unlinkedDeviceNotifications(deployments []store.Deployment) []Notification {
	devicesReg, ok := a.collectors.(deviceRegistry)
	if !ok || a.catalog == nil {
		return nil
	}
	// A device with a stored registration is being retried, or has its own
	// give-up notice (app_device_links.go).
	pending := a.pendingDeviceLinkIDs()
	var items []Notification
	for _, dep := range deployments {
		ids := slices.DeleteFunc(devicesReg.Unlinked(dep.Slug), func(id string) bool { return pending[dep.Slug][id] })
		svc, inCatalog := a.catalog.Get(dep.Slug)
		if len(ids) == 0 || !inCatalog || svc.DeviceLink == nil {
			continue
		}
		var lines []string
		for _, id := range ids {
			line := id
			if url := svc.DeviceLink.URL; url != "" {
				line += " " + strings.ReplaceAll(url, "${"+svc.DeviceLink.IDEnv+"}", neturl.PathEscape(id))
			}
			lines = append(lines, line)
		}
		items = append(items, Notification{
			Level:   "warning",
			Title:   svc.Name + " device not on your account",
			Message: "It earns for nobody until you add it. " + strings.TrimSpace(svc.DeviceLink.Steps) + " " + strings.Join(lines, "; "),
		})
	}
	return items
}

// outdatedServices returns the slugs of deployed services whose running image no
// longer matches the catalog's current image for that service. Such a container
// usually still runs — and so looks healthy — while earning nothing, so the UI
// flags it for re-deploy. Services absent from the catalog, or with an empty image
// on either side, are conservatively not flagged (see catalog.ImageOutdated).
func (a *App) outdatedServices(deployments []store.Deployment) []string {
	if a.catalog == nil {
		return nil
	}
	var out []string
	for _, dep := range deployments {
		svc, ok := a.catalog.Get(dep.Slug)
		if !ok {
			continue
		}
		// Against the image it was deployed from: a chosen image is not drift.
		want := svc.Docker.Image
		if dep.CustomImage != "" {
			want = dep.CustomImage
		}
		if catalog.ImageOutdated(dep.Image, want) {
			out = append(out, dep.Slug)
		}
	}
	return out
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

// credsConfigured reports whether a service should show as "Configured" in Settings:
// it must have stored credentials AND those credentials must satisfy the catalog's
// current required fields. Keying off required fields (not merely len(creds) > 0) stops
// an orphaned pre-migration blob — non-empty but missing the current keys — from
// falsely reading as configured when a deploy would in fact fail until re-entry.
//
// A blob whose every value is blank is not configured, and neither is one that
// leaves out a login the earnings collector requires (collectors.Fields): the form
// sends every box, so a deploy with the boxes empty used to read as "Configured".
func (a *App) credsConfigured(slug string, creds map[string]string) bool {
	if !hasCredentialValue(creds) {
		return false
	}
	for _, field := range collectors.Fields(slug) {
		if field.Required && strings.TrimSpace(creds[field.Key]) == "" {
			return false
		}
	}
	if a.services == nil {
		return true
	}
	return a.services.RequiredCredentialsMet(slug, creds)
}

// ensureFleetAPIKey loads the fleet bearer token into memory at startup, keeping it
// out of config.json. It prefers the OS keychain (with a 0600 file fallback) via
// config.FleetKey/SetFleetKey. A legacy plaintext token still in config.json is
// migrated into that store so already-configured workers and mobile clients keep
// authenticating, then blanked from config.json. A brand-new install generates a
// fresh 32-byte token.
func (a *App) ensureFleetAPIKey() error {
	appDir := a.cfg.AppDir()
	key, err := config.FleetKey(appDir)
	if err != nil {
		return err
	}
	if key == "" {
		if legacy := a.cfg.Config().FleetAPIKey; legacy != "" {
			// Preserve the existing plaintext token — workers already hold this value.
			key = legacy
		} else {
			raw := make([]byte, 32)
			if _, err := rand.Read(raw); err != nil {
				return err
			}
			key = base64.RawURLEncoding.EncodeToString(raw)
		}
		if err := config.SetFleetKey(appDir, key); err != nil {
			return err
		}
	}
	// Strip any plaintext token still carried in config.json (the omitempty tag drops
	// the now-empty field on the next write).
	if a.cfg.Config().FleetAPIKey != "" {
		if err := a.cfg.Update(func(c *config.AppConfig) { c.FleetAPIKey = "" }); err != nil {
			return err
		}
	}
	a.fleetKey = key
	return nil
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

// loadCustom puts the user's custom services into the catalog's lookups. A
// definition that cannot be read is surfaced, not fatal: the catalog services still
// work without it.
// A failed load is remembered, and GetAppState and DeployService try it again until
// it succeeds, so the services reappear without a restart.
func (a *App) loadCustom() {
	if err := a.services.LoadCustom(); err != nil {
		a.customPending.Store(true)
		a.emitError("custom-services", err)
		return
	}
	a.customPending.Store(false)
}

// retryCustom loads the custom services again if the last load failed.
func (a *App) retryCustom() {
	if a.customPending.Load() {
		a.loadCustom()
	}
}

// errStillStarting is what the bound methods return until startCore has set up the
// config, catalog, store and runtimes. The window waits while it sees exactly this
// text (STILL_STARTING in frontend/src/render/boot.ts), so keep the two equal.
var errStillStarting = errors.New("app is still starting")

func (a *App) ready() error {
	if a.cfg == nil || a.catalog == nil || a.store == nil || a.runtime == nil || a.services == nil {
		if failed := a.startErr.Load(); failed != nil {
			return *failed
		}
		return errStillStarting
	}
	return nil
}

// failStart reports the error that stopped startCore and keeps it for ready(), so
// the window shows it at once instead of waiting for a start that will not come.
func (a *App) failStart(scope string, err error) {
	a.emitError(scope, err)
	failed := fmt.Errorf("%s: %w", scope, err)
	a.startErr.Store(&failed)
}

func (a *App) emitError(scope string, err error) {
	a.emitEvent("app:error", map[string]string{
		"scope": scope,
		"error": err.Error(),
	})
}

// emitNotice emits an informational "app:notice" event. Unlike emitError this is NOT a
// failure — the frontend renders it as a neutral advisory toast, so a successful action
// (e.g. a settings save that needs a restart to fully take effect) is not surfaced as
// an error.
func (a *App) emitNotice(scope, message string) {
	a.emitEvent("app:notice", map[string]string{
		"scope":   scope,
		"message": message,
	})
}

// emitEvent sends an event to the frontend, and is the only way the app may do so.
// Wails' EventsEmit fatally exits the process (log.Fatalf inside getEvents) when the
// context has no internal "events" value, so a binding that emitted directly would
// kill the process the moment it ran outside the GUI — under the headless daemon
// role, or in any test that drives that binding. Guarding on that value here makes
// the absence of a frontend simply nothing to emit to, and lets background
// collection and its event emission be exercised in tests while behaving normally
// at runtime, where the OnStartup context always carries "events".
func (a *App) emitEvent(name string, data ...interface{}) {
	if a.ctx == nil || a.ctx.Value("events") == nil {
		return
	}
	wailsruntime.EventsEmit(a.ctx, name, data...)
}

// --- Background helper (OS login-agent) registration -------------------------------
//
// These bind the internal/bgservice login-agent to the frontend so a later "Keep earning
// in the background" toggle (Phase B2) can register THIS executable with the current
// user's OS service manager to run its headless --daemon supervisor at login, restarting
// on crash and surviving the GUI being closed — Docker-parity native supervision with no
// admin (docs/NATIVE-SUPERVISION.md, Phase B). Installation is always explicit/
// user-initiated; nothing here runs on startup.

// InstallBackgroundHelper registers the running CashPilot binary as a per-user OS login
// agent that runs it with --daemon (macOS LaunchAgent / Linux systemd --user unit /
// Windows Scheduled Task).
func (a *App) InstallBackgroundHelper() error {
	agent, err := a.backgroundAgent()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	return agent.Install(exe, []string{"--daemon"})
}

// RemoveBackgroundHelper unregisters the login agent and removes its backing file.
func (a *App) RemoveBackgroundHelper() error {
	agent, err := a.backgroundAgent()
	if err != nil {
		return err
	}
	return agent.Uninstall()
}

// BackgroundHelperStatus reports whether the login agent is installed and running.
func (a *App) BackgroundHelperStatus() (bgservice.Status, error) {
	agent, err := a.backgroundAgent()
	if err != nil {
		return bgservice.Status{}, err
	}
	return agent.Status()
}

// backgroundAgent builds the per-OS login agent, pointing the helper's captured
// stdout/stderr (used by the macOS plist) at a "logs" dir under the app data dir.
func (a *App) backgroundAgent() (bgservice.Agent, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	logDir := filepath.Join(a.cfg.AppDir(), "logs")
	return bgservice.New(bgservice.DefaultLabel, logDir), nil
}

// ExportCompose writes a docker-compose.yml for the chosen services and asks the
// user where to save it. It returns the path written, or "" when the save dialog
// was dismissed.
//
// The point of the file is that the machine keeps earning without CashPilot: it can
// be run by Docker, Podman, Portainer or a box's own GitOps, and because it carries
// the same cashpilot.* labels the app writes, CashPilot still finds and watches
// those containers afterwards. It contains no credentials — every value the user
// owns is a ${VAR} placeholder Compose fills from a .env file at run time — so the
// file is safe to keep in a repository or send to another machine.
//
// arch is "amd64", "arm64" or "arm" and names the machine the file is FOR, which is
// not always this one. It matters for the entries whose ARM builds Docker cannot
// select from the manifest. Empty means this machine, and is resolved here to the
// family this copy of CashPilot was built for: the frontend offers "This machine",
// and leaving it to the image manifest would hand an Apple silicon Mac or a
// Raspberry Pi the x86-64 build of those entries, which cannot start.
func (a *App) ExportCompose(slugs []string, arch string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	if strings.TrimSpace(arch) == "" {
		arch = compose.HostFamily(stdruntime.GOARCH)
	}
	body, err := compose.Generate(a.catalog, slugs, compose.Options{
		Arch:     arch,
		Hostname: runtime.DeviceHostname(),
	})
	if err != nil {
		return "", err
	}
	path, err := a.askWhereToSave(wailsruntime.SaveDialogOptions{
		Title:                "Save compose file",
		DefaultFilename:      "docker-compose.yml",
		CanCreateDirectories: true,
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Compose files (*.yml, *.yaml)", Pattern: "*.yml;*.yaml"},
		},
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("could not save the compose file: %w", err)
	}
	return path, nil
}

// askWhereToSave opens the save dialog, or the stand-in a test installed.
func (a *App) askWhereToSave(opts wailsruntime.SaveDialogOptions) (string, error) {
	if a.saveDialog != nil {
		return a.saveDialog(opts)
	}
	return wailsruntime.SaveFileDialog(a.ctx, opts)
}

// fleetOfflineAfterFor is how long a device of this kind may stay silent before
// it shows offline.
func fleetOfflineAfterFor(kind string) time.Duration {
	if kind == "mobile" {
		return fleetOfflineAfterMobile
	}
	return fleetOfflineAfter
}
