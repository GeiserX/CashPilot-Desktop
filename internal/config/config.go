package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/zalando/go-keyring"
)

const (
	appID          = "com.cashpilot.desktop"
	keyringService = "CashPilot Desktop"
	keyringUser    = "credential-master-key"
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
	FleetAPIKey            string `json:"fleetApiKey"`
	FleetBindAddress       string `json:"fleetBindAddress"`
	FleetPort              int    `json:"fleetPort"`
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
}

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
