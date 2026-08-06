package config

import (
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
	// WorkerURLPolicy selects how the SSRF worker-URL validator (internal/fleetnet)
	// classifies a remote worker endpoint before the desktop makes an authenticated
	// request to it. One of "strict" (only WorkerAllowedHosts), "private" (RFC1918 +
	// Tailscale CGNAT 100.64.0.0/10 + IPv6 ULA fc00::/7 + the allowlist — the sensible
	// homelab default), or "public" (any non-loopback/link-local/metadata/unspecified
	// address). Defaults to "private" via applyDefaults.
	WorkerURLPolicy string `json:"workerUrlPolicy"`
	// WorkerAllowedHosts is an explicit allowlist of worker hosts/IPs. It is the sole
	// allow-source in "strict" mode and is always honored in the other modes. Entries
	// may be an exact hostname, a "*.suffix" DNS suffix (e.g. Tailscale MagicDNS
	// "*.mango-alpha.ts.net"), a CIDR, or a literal IP. Defaults to empty (nil).
	WorkerAllowedHosts []string `json:"workerAllowedHosts"`
	// WorkerAllowMetadata, when false (the default, the bool zero value), makes the
	// cloud instance-metadata endpoints (169.254.169.254, fd00:ec2::254,
	// metadata.google.internal) ALWAYS blocked regardless of policy or allowlist.
	// Setting it true is the only way to reach a metadata address and should be rare.
	WorkerAllowMetadata bool `json:"workerAllowMetadata"`
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
	// (0600 file fallback) via UpstreamEnrolmentKey/UpstreamWorkerKey, the same
	// way the fleet token does, so config.json never carries a bearer token.
	UpstreamURL string `json:"upstreamUrl,omitempty"`
	// UpstreamIntervalMinutes is how often to heartbeat when paired. Zero means
	// use the default; see DefaultUpstreamIntervalMinutes.
	UpstreamIntervalMinutes int `json:"upstreamIntervalMinutes,omitempty"`
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
	if cfg.WorkerURLPolicy == "" {
		cfg.WorkerURLPolicy = "private"
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
	return m, nil
}

func (m *Manager) Config() AppConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

func (m *Manager) AppDir() string {
	return m.appDir
}

func (m *Manager) DataDir() string {
	return m.dataDir
}

func (m *Manager) Save(cfg AppConfig) error {
	cfg = applyDefaults(cfg)
	if err := os.MkdirAll(m.appDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.path, raw, 0o600); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
	return nil
}

// load runs once during NewManager (single-threaded, before the Manager is
// shared with the background fleet HTTP server), so it does not take the lock.
func (m *Manager) load() error {
	raw, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return m.Save(m.cfg)
	}
	if err != nil {
		return err
	}
	var cfg AppConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	m.cfg = applyDefaults(cfg)
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
	encoded, err := keyring.Get(keyringService, keyringUser)
	if err == nil && encoded != "" {
		return base64.StdEncoding.DecodeString(encoded)
	}

	keyPath := filepath.Join(appDir, ".credential_key")
	if raw, readErr := os.ReadFile(keyPath); readErr == nil && len(raw) > 0 {
		decoded, decodeErr := base64.StdEncoding.DecodeString(string(raw))
		if decodeErr == nil {
			_ = keyring.Set(keyringService, keyringUser, string(raw))
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
	if err := keyring.Set(keyringService, keyringUser, encoded); err != nil {
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
	value, err := keyring.Get(keyringService, keyringUserFleet)
	if err == nil && value != "" {
		return value, nil
	}
	keyPath := filepath.Join(appDir, ".fleet_api_key")
	if raw, readErr := os.ReadFile(keyPath); readErr == nil && len(raw) > 0 {
		value = string(raw)
		_ = keyring.Set(keyringService, keyringUserFleet, value)
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
	if err := keyring.Set(keyringService, keyringUserFleet, value); err != nil {
		keyPath := filepath.Join(appDir, ".fleet_api_key")
		if err := os.WriteFile(keyPath, []byte(value), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// UpstreamEnrolmentKey returns the shared CashPilot token used to enrol this
// machine, or "" when not stored. Same keychain-first, 0600-file-fallback
// storage as FleetKey.
func UpstreamEnrolmentKey(appDir string) (string, error) {
	return readSecret(appDir, keyringUserUpstreamEnrol, ".upstream_enrolment_key")
}

// SetUpstreamEnrolmentKey persists the shared enrolment token.
func SetUpstreamEnrolmentKey(appDir, value string) error {
	return writeSecret(appDir, keyringUserUpstreamEnrol, ".upstream_enrolment_key", value)
}

// UpstreamWorkerKey returns the per-worker key the server issued to this
// machine, or "" before enrolment completes.
func UpstreamWorkerKey(appDir string) (string, error) {
	return readSecret(appDir, keyringUserUpstreamWorker, ".upstream_worker_key")
}

// SetUpstreamWorkerKey persists the per-worker key issued by the server.
func SetUpstreamWorkerKey(appDir, value string) error {
	return writeSecret(appDir, keyringUserUpstreamWorker, ".upstream_worker_key", value)
}

// readSecret is the shared body of the keychain-first readers.
//
// A missing secret is ("", nil) — "not stored yet" — so a caller can tell it
// apart from a keychain that is present but LOCKED, which returns an error. The
// difference matters: treating a locked keychain as "not stored" would silently
// re-enrol the machine and orphan its existing worker identity.
func readSecret(appDir, keyringUser, fileName string) (string, error) {
	value, err := keyring.Get(keyringService, keyringUser)
	if err == nil && value != "" {
		return value, nil
	}
	keyPath := filepath.Join(appDir, fileName)
	if raw, readErr := os.ReadFile(keyPath); readErr == nil && len(raw) > 0 {
		value = strings.TrimSpace(string(raw))
		_ = keyring.Set(keyringService, keyringUser, value)
		return value, nil
	}
	if refuseKeyRegen(err, runtimeGOOS) {
		return "", fmt.Errorf("%s is present but unreadable (keychain locked or access denied): %w", keyringUser, err)
	}
	return "", nil
}

// writeSecret is the shared body of the keychain-first writers.
func writeSecret(appDir, keyringUser, fileName, value string) error {
	if err := keyring.Set(keyringService, keyringUser, value); err != nil {
		if err := os.WriteFile(filepath.Join(appDir, fileName), []byte(value), 0o600); err != nil {
			return err
		}
	}
	return nil
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
