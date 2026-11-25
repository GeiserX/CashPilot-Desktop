package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/filelock"
	"github.com/zalando/go-keyring"
)

const (
	appID            = "com.cashpilot.desktop"
	keyringService   = "CashPilot Desktop"
	keyringUser      = "credential-master-key"
	keyringUserFleet = "fleet-api-key"
	// Two DISTINCT upstream secrets. The enrolment key is the shared token the
	// user pastes in once; the worker key is what the server mints for this
	// machine and is the only credential valid after enrolment. Storing them
	// under one name would make the shared key unrecoverable after the first
	// heartbeat overwrote it, and re-pairing would then be impossible.
	keyringUserUpstreamEnrol  = "upstream-enrolment-key"
	keyringUserUpstreamWorker = "upstream-worker-key"
)

type AppConfig struct {
	FirstRunComplete       bool   `json:"firstRunComplete"`
	DisplayCurrency        string `json:"displayCurrency"`
	RuntimeProvider        string `json:"runtimeProvider"`
	AutoUpdate             bool   `json:"autoUpdate"`
	HostnamePrefix         string `json:"hostnamePrefix"`
	CollectIntervalMinutes int    `json:"collectIntervalMinutes"`
	RetentionDays          int    `json:"retentionDays"`
	Timezone               string `json:"timezone"`
	// FleetAPIKey is the legacy plaintext location for the fleet bearer token. It is
	// no longer persisted here — the token lives in the OS keychain with a 0600 file
	// fallback via FleetKey/SetFleetKey. The field is retained with omitempty only so
	// an older config.json still unmarshals and ensureFleetAPIKey can migrate its
	// value into the keychain and then blank it from disk.
	FleetAPIKey      string `json:"fleetApiKey,omitempty"`
	FleetBindAddress string `json:"fleetBindAddress"`
	FleetPort        int    `json:"fleetPort"`
	// MetricsEnabled turns on the opt-in Prometheus /metrics endpoint on the fleet
	// server. It defaults to false (disabled) — the bool zero value, so applyDefaults
	// needs no change. Enabling it exposes earnings, health and fleet stats on the
	// fleet bind address (loopback by default) with no authentication, matching the
	// Prometheus scraping convention.
	MetricsEnabled bool `json:"metricsEnabled"`
	// FleetServerEnabled turns this Desktop into a HUB that accepts worker and
	// mobile heartbeats. It defaults to FALSE, which is the bool zero value, so
	// every existing install becomes a pure spoke on upgrade.
	//
	// CashPilot is hub-and-spoke: the CashPilot SERVER is the hub, and Desktop
	// and Android are spokes. Spokes do not collect from each other. Nothing
	// enforced that -- startFleetAPI ran unconditionally, so any worker or phone
	// could enrol into a Desktop and create a second, competing source of truth.
	//
	// Kept as a gate rather than deleted because the capability works and
	// someone may want it later; off is the right default until they ask.
	FleetServerEnabled bool `json:"fleetServerEnabled"`
	// UpstreamURL optionally pairs this Desktop with a CashPilot server, which it
	// then reports to as a worker. EMPTY IS THE DEFAULT AND MEANS STANDALONE:
	// Desktop keeps working exactly as before and nothing is sent anywhere.
	//
	// The credentials are deliberately NOT here — they live in the OS keychain
	// (0600 file fallback) via the pairing credential store, the same
	// way the fleet token does, so config.json never carries a bearer token.
	UpstreamURL string `json:"upstreamUrl,omitempty"`
	// UpstreamIntervalMinutes is how often to heartbeat when paired. Zero means
	// use the default; see DefaultUpstreamIntervalMinutes.
	UpstreamIntervalMinutes int `json:"upstreamIntervalMinutes,omitempty"`
	// UpstreamHistoryPushedTo is the server URL this Desktop has already handed
	// its pre-pairing earnings history to, or empty for none.
	//
	// The import itself is idempotent — the server keys a reading on (platform,
	// source, date) and updates rather than appends — so re-sending would be
	// harmless but wasteful: it is up to 400 days of readings on every
	// heartbeat, which is every minute by default.
	//
	// Storing the URL rather than a bare boolean is what makes re-pairing work:
	// pointing Desktop at a DIFFERENT server no longer matches, so that server
	// gets the history too. It is also why this is not a secret and belongs in
	// config.json — it records where data was sent, not how to authenticate.
	UpstreamHistoryPushedTo string `json:"upstreamHistoryPushedTo,omitempty"`
	// DriftWarningsOff turns off the "needs fixing" warning for a container that no
	// longer matches what a redeploy would build. Its zero value keeps warnings on
	// (CASHPILOT_DRIFT_WARNINGS on the web).
	DriftWarningsOff bool `json:"driftWarningsOff,omitempty"`
	// DriftIgnored maps a service slug to the catalog fingerprint
	// (runtime.ShapeFingerprint) its drift warning was muted at. The mute holds
	// until the catalog changes a field the check judges.
	DriftIgnored map[string]string `json:"driftIgnored,omitempty"`
	// Isolation is the LAN isolation setting: "" or "auto" (fence new installs, leave
	// an upgraded one as it is until the user switches), "on" or "off".
	// CASHPILOT_ISOLATION on the web worker; see internal/runtime/lanfence.go.
	Isolation string `json:"isolation,omitempty"`
	// IsolationOn is state, not a setting: true once the LAN fence has been on, so the
	// app stays fenced after Docker loses every trace of it (internal/runtime/lanfence.go).
	// Isolation set to off clears it. The web worker keeps it as /data/.isolation_on.
	IsolationOn bool `json:"isolationOn,omitempty"`
	// ActivityEnabled switches on the Activity page's sampling (internal/activity):
	// a short-lived helper per earner every 5 minutes reads which destinations it
	// talks to. Off by default, the bool zero value, so no install starts it on
	// upgrade.
	ActivityEnabled bool `json:"activityEnabled,omitempty"`
	// SessionConsent maps a service slug to the risk disclosure the user accepted
	// before installing that provider's desktop app in their own session, outside
	// any fence. The wizard shows the install steps only while the recorded text
	// version matches the disclosure it shows now. Revoking deletes the entry.
	SessionConsent map[string]SessionConsent `json:"sessionConsent,omitempty"`
	// Mode is how this computer runs: ModeLead ("Run everything here": it collects
	// the earnings, shows the dashboard and runs its earners) or ModeFollower ("Join
	// my CashPilot": it runs earners for a CashPilot server, which collects and shows
	// the earnings). Empty only on a new install whose first run has not asked yet.
	// An install from before this field gets it once, from its saved state
	// (inferMode), and never sees the first run again.
	Mode string `json:"mode,omitempty"`
	// SetupJournal is a guided setup a restart interrupts: where the app lands when
	// it opens again. It is written before the restart and cleared when the flow
	// finishes or the user cancels it. nil when no setup is under way.
	SetupJournal *SetupJournal `json:"setupJournal,omitempty"`
	// ServerCommandsOff is the dashboard switch "<server> can start and stop
	// services on this computer", turned off. The zero value is on, so joining a
	// server turns the server-commands API on; joining again keeps the user's choice.
	ServerCommandsOff bool `json:"serverCommandsOff,omitempty"`
}

// SetupJournal says where a guided setup stood before a restart. It only says
// where to land: every check runs again on resume, because the machine is the
// truth, not the journal (internal/setupresume).
type SetupJournal struct {
	// Flow is the guided setup: "docker" today.
	Flow string `json:"flow"`
	// Step is the checklist step that asked for the restart.
	Step string `json:"step"`
	// BootTimeBefore is the PC's last boot time when the journal was written,
	// RFC 3339; "" when it could not be read. A later boot time proves the restart.
	BootTimeBefore string `json:"bootTimeBefore"`
	// ExePath is the app that asked Windows to reopen it after the restart.
	ExePath string `json:"exePath"`
	// Attempts counts how many times this step asked for a restart.
	Attempts int `json:"attempts"`
	// Finish is what the step waits for: "" or "restart" for a restart, "signout"
	// for a sign-out and back in (a restart does it too).
	Finish string `json:"finish,omitempty"`
}

// The two values of AppConfig.Mode.
const (
	ModeLead     = "lead"
	ModeFollower = "follower"
)

// inferMode gives an install from before AppConfig.Mode its mode, from what it
// already has: a saved server address means it reports to a CashPilot, so it is
// a follower; an install that finished its first run without one is a lead. A
// new install that has not finished its first run keeps "" so the first run asks.
// It reports whether it set anything, so load can persist the answer once.
func inferMode(cfg *AppConfig) bool {
	if cfg.Mode != "" {
		return false
	}
	switch {
	case strings.TrimSpace(cfg.UpstreamURL) != "":
		cfg.Mode = ModeFollower
	case cfg.FirstRunComplete:
		cfg.Mode = ModeLead
	default:
		return false
	}
	return true
}

// SessionConsent is one accepted disclosure: which text the user read, and when.
type SessionConsent struct {
	TextVersion string    `json:"textVersion"`
	AcceptedAt  time.Time `json:"acceptedAt"`
}

// DefaultUpstreamIntervalMinutes matches what a CashPilot worker sends, so a
// paired Desktop does not look stale next to the Docker workers on the same
// fleet page — the server marks a worker unreachable on heartbeat age.
const DefaultUpstreamIntervalMinutes = 1

type Manager struct {
	appDir  string
	dataDir string
	path    string
	mu      sync.RWMutex
	cfg     AppConfig
	pairing Pairing
	// saved is the file as this Manager last read or wrote it, so Reload can tell
	// a change another process saved (the background helper and the window each
	// hold a Manager on the same file) from its own.
	saved []byte
}

// applyDefaults fills zero/invalid fields with safe defaults. The fleet bind
// address defaults to loopback (127.0.0.1) so the worker API is never exposed
// to the network unless the user explicitly changes it to a routable address.
func applyDefaults(cfg AppConfig) AppConfig {
	if cfg.DisplayCurrency == "" {
		cfg.DisplayCurrency = "USD"
	}
	if cfg.RuntimeProvider == "" {
		cfg.RuntimeProvider = "existing-docker"
	}
	if cfg.HostnamePrefix == "" {
		cfg.HostnamePrefix = "cashpilot"
	}
	if cfg.CollectIntervalMinutes <= 0 {
		cfg.CollectIntervalMinutes = 60
	}
	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = 400
	}
	if cfg.Timezone == "" {
		cfg.Timezone = "UTC"
	}
	if cfg.FleetBindAddress == "" {
		cfg.FleetBindAddress = "127.0.0.1"
	}
	if cfg.FleetPort <= 0 {
		cfg.FleetPort = 8085
	}
	return cfg
}

func NewManager() (*Manager, error) {
	appDir, err := appDataDir()
	if err != nil {
		return nil, err
	}
	dataDir := filepath.Join(appDir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}

	m := &Manager{
		appDir:  appDir,
		dataDir: dataDir,
		path:    filepath.Join(appDir, "config.json"),
		cfg:     applyDefaults(AppConfig{AutoUpdate: true}),
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	m.pairing, _ = m.Pairing()
	return m, nil
}

func (m *Manager) Config() AppConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg := m.cfg
	if m.pairing.Revision != "" {
		cfg.UpstreamURL = m.pairing.ServerURL
		cfg.Mode = ModeLead
		if cfg.UpstreamURL != "" {
			cfg.Mode = ModeFollower
		}
	}
	return cfg
}

func (m *Manager) AppDir() string {
	return m.appDir
}

func (m *Manager) DataDir() string {
	return m.dataDir
}

func (m *Manager) Save(cfg AppConfig) error {
	cfg = applyDefaults(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	release, err := m.lockConfig()
	if err != nil {
		return err
	}
	defer release()
	if m.pairing.Revision != "" {
		cfg.Mode, cfg.UpstreamURL = m.cfg.Mode, m.cfg.UpstreamURL
	}
	if err := m.persist(cfg); err != nil {
		return err
	}
	m.cfg = cfg
	return nil
}

// Reload reads the file again when another process changed it since this
// Manager last read or wrote it, and reports whether it did. The window and the
// background helper (--daemon) each hold a Manager on the same file, so a join
// or a switch made in the window reaches the helper only through here. A file
// that cannot be read or parsed (another process may be halfway through writing
// it) leaves the config in memory as it was and is read again next time.
func (m *Manager) Reload() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed, err := m.reloadLocked()
	if err != nil {
		return false, err
	}
	p, err := m.Pairing()
	if err != nil {
		return changed, err
	}
	pairingChanged := p != m.pairing
	m.pairing = p
	return changed || pairingChanged, nil
}

func (m *Manager) reloadLocked() (bool, error) {
	raw, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if bytes.Equal(raw, m.saved) {
		return false, nil
	}
	var cfg AppConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false, err
	}
	m.cfg, m.saved = applyDefaults(cfg), raw
	return true, nil
}

// Update applies mutate to the current config and persists the result as ONE
// read-modify-write.
//
// Save takes a whole AppConfig, so a caller that reads the config, does slow
// work, and then saves silently discards everything the user changed meanwhile.
// The pairing loop has exactly that shape — it reads the config, uploads up to
// 400 days of earnings over the network, then records where it sent them — and
// it runs while the settings screen is open. Holding the lock across the whole
// read-modify-write closes the window instead of narrowing it.
//
// Use this for any single field written by background work. Save stays the
// right call for the settings form, which legitimately writes the whole object.
func (m *Manager) Update(mutate func(cfg *AppConfig)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	release, err := m.lockConfig()
	if err != nil {
		return err
	}
	defer release()
	// Start from the file, not only from memory: another process may have saved
	// a change (a join made in the window, seen by the helper), and writing this
	// Manager's older copy back would undo it.
	_, _ = m.reloadLocked()
	next := m.cfg
	mutate(&next)
	next = applyDefaults(next)
	if err := m.persist(next); err != nil {
		return err
	}
	m.cfg = next
	return nil
}

// The window and helper have separate Managers. Hold the file lock across
// each write so neither can put back the other's previous settings.
func (m *Manager) lockConfig() (func(), error) {
	release, ok := filelock.Acquire(context.Background(), m.path+".lock", 5*time.Second)
	if !ok {
		return nil, errors.New("could not lock the config file")
	}
	return release, nil
}

// persist writes the config to disk and remembers what it wrote (saved). Takes
// no lock: call it with m.mu held, or before the Manager is shared (load).
func (m *Manager) persist(cfg AppConfig) error {
	if err := os.MkdirAll(m.appDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(m.path, raw); err != nil {
		return err
	}
	m.saved = raw
	return nil
}

// writeFileAtomic replaces path with raw so that a reader sees the old file or
// the new one, never an empty or half-written one. The window and the
// background helper (--daemon) each read and write config.json, and a plain
// os.WriteFile truncates the file before it writes. The new content goes to a
// temp file in the same directory (created 0600), is synced, and is renamed
// over path; on
// Windows os.Rename replaces the target (MoveFileEx with
// MOVEFILE_REPLACE_EXISTING).
func writeFileAtomic(path string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := renameReplacing(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// renameReplacing is os.Rename, retried for a moment on Windows: there a
// rename over a file the other process has open for reading fails with a
// sharing violation until that read ends, which takes milliseconds.
func renameReplacing(from, to string) error {
	err := os.Rename(from, to)
	for i := 0; err != nil && runtime.GOOS == "windows" && i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		err = os.Rename(from, to)
	}
	return err
}

// load runs once during NewManager (single-threaded, before the Manager is
// shared with the background fleet HTTP server), so it only takes the file lock.
func (m *Manager) load() error {
	release, err := m.lockConfig()
	if err != nil {
		return err
	}
	defer release()
	raw, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return m.persist(applyDefaults(m.cfg))
	}
	if err != nil {
		return err
	}
	var cfg AppConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	m.cfg, m.saved = applyDefaults(cfg), raw
	if inferMode(&m.cfg) {
		// Written once, so the answer holds even if the address is cleared later
		// by hand: from now on only the user's own switch changes the mode.
		if err := m.persist(m.cfg); err != nil {
			return err
		}
	}
	return nil
}

// runtimeGOOS is a seam over runtime.GOOS so the platform-gated key-regeneration
// guard below can be exercised from tests on any CI runner.
var runtimeGOOS = runtime.GOOS

// refuseKeyRegen reports whether MasterKey/FleetKey must NOT mint a replacement key.
// A non-"not found" keychain error on a platform whose keychain is always present
// (macOS, Windows) means the key likely exists but is locked or access was denied —
// minting would silently overwrite it. On such platforms we surface the error instead.
func refuseKeyRegen(err error, goos string) bool {
	return err != nil && !errors.Is(err, keyring.ErrNotFound) &&
		(goos == "darwin" || goos == "windows")
}

func MasterKey(appDir string) ([]byte, error) {
	encoded, err := credentials.Get(keyringService, keyringUser)
	if err == nil && encoded != "" {
		return base64.StdEncoding.DecodeString(encoded)
	}

	keyPath := filepath.Join(appDir, ".credential_key")
	if raw, readErr := os.ReadFile(keyPath); readErr == nil && len(raw) > 0 {
		decoded, decodeErr := base64.StdEncoding.DecodeString(string(raw))
		if decodeErr == nil {
			_ = credentials.Set(keyringService, keyringUser, string(raw))
			return decoded, nil
		}
	}

	if refuseKeyRegen(err, runtimeGOOS) {
		return nil, fmt.Errorf("credential master key is present but unreadable (keychain locked or access denied); refusing to regenerate to avoid destroying saved credentials: %w", err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	encoded = base64.StdEncoding.EncodeToString(key)
	if err := credentials.Set(keyringService, keyringUser, encoded); err != nil {
		if err := os.WriteFile(keyPath, []byte(encoded), 0o600); err != nil {
			return nil, err
		}
	}
	return key, nil
}

// FleetKey returns the stored fleet bearer token, mirroring MasterKey's
// keychain-preferred / file-fallback storage. It tries the OS keychain first, then a
// 0600 <appDir>/.fleet_api_key file (migrating that file's value into the keychain
// when found). An empty string with a nil error means "not stored yet" so the caller
// can generate a fresh token or migrate a legacy config.json value and persist it via
// SetFleetKey.
func FleetKey(appDir string) (string, error) {
	value, err := credentials.Get(keyringService, keyringUserFleet)
	if err == nil && value != "" {
		return value, nil
	}
	keyPath := filepath.Join(appDir, ".fleet_api_key")
	if raw, readErr := os.ReadFile(keyPath); readErr == nil && len(raw) > 0 {
		value = string(raw)
		_ = credentials.Set(keyringService, keyringUserFleet, value)
		return value, nil
	}

	if refuseKeyRegen(err, runtimeGOOS) {
		return "", fmt.Errorf("fleet key is present but unreadable (keychain locked or access denied): %w", err)
	}
	return "", nil
}

// SetFleetKey persists the fleet bearer token keychain-first, falling back to a 0600
// <appDir>/.fleet_api_key file when the keychain is unavailable (headless Linux / CI),
// exactly like MasterKey's storage tail.
func SetFleetKey(appDir, value string) error {
	return writeSecret(appDir, keyringUserFleet, ".fleet_api_key", value)
}

// LegacyEnrolmentKey reads the shared token saved before pairing revisions.
func LegacyEnrolmentKey(appDir string) (string, error) {
	raw, fileErr := os.ReadFile(filepath.Join(appDir, ".upstream_enrolment_key"))
	if fileErr == nil {
		return strings.TrimSpace(string(raw)), nil
	}
	value, err := credentials.Get(keyringService, keyringUserUpstreamEnrol)
	if refuseKeyRegen(err, runtimeGOOS) {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

// readSecret is the shared body of the keychain-first readers.
//
// A missing secret is ("", nil) — "not stored yet" — so a caller can tell it
// apart from a keychain that is present but LOCKED, which returns an error. The
// difference matters: treating a locked keychain as "not stored" would silently
// re-enrol the machine and orphan its existing worker identity.
func readSecret(appDir, keyringUser, fileName string) (string, error) {
	value, err := credentials.Get(keyringService, keyringUser)
	if err == nil && value != "" {
		return value, nil
	}
	keyPath := filepath.Join(appDir, fileName)
	if raw, readErr := os.ReadFile(keyPath); readErr == nil && len(raw) > 0 {
		value = strings.TrimSpace(string(raw))
		_ = credentials.Set(keyringService, keyringUser, value)
		return value, nil
	}
	if refuseKeyRegen(err, runtimeGOOS) {
		return "", fmt.Errorf("%s is present but unreadable (keychain locked or access denied): %w", keyringUser, err)
	}
	return "", nil
}

// writeSecret is the shared body of the keychain-first writers.
//
// Once the keychain holds the value, the 0600 fallback file is removed. The
// readers treat an empty keychain value as "not stored" and fall through to
// that file, so a file left over from a time the keychain refused a write
// would bring an old secret back after it was cleared (repointing at a new
// server would keep sending the old server's worker key).
//
// Only an explicit write removes the file. The readers copy a file's value
// into the keychain but keep the file: on Linux the secret service can be
// locked or absent at a later start, and the file is then the only copy. A
// hub that lost its fleet key that way would mint a new one and unpair every
// phone.
func writeSecret(appDir, keyringUser, fileName, value string) error {
	keyPath := filepath.Join(appDir, fileName)
	if err := credentials.Set(keyringService, keyringUser, value); err != nil {
		return os.WriteFile(keyPath, []byte(value), 0o600)
	}
	dropFallback(keyPath)
	return nil
}

// dropFallback removes a 0600 fallback file once the keychain holds its value.
// Best effort: a file that cannot be removed only leaves the old behaviour.
func dropFallback(keyPath string) {
	_ = os.Remove(keyPath)
}

func appDataDir() (string, error) {
	if override := os.Getenv("CASHPILOT_DESKTOP_DATA_DIR"); override != "" {
		return override, nil
	}

	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", appID), nil
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "CashPilot Desktop"), nil
	default:
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(base, "cashpilot-desktop"), nil
	}
}
