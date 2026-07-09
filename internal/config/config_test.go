package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager error: %v", err)
	}
	return m
}

func TestNewManagerAppliesDefaults(t *testing.T) {
	m := newTestManager(t)
	cfg := m.Config()

	want := AppConfig{
		DisplayCurrency:        "USD",
		RuntimeProvider:        "existing-docker",
		AutoUpdate:             true,
		HostnamePrefix:         "cashpilot",
		CollectIntervalMinutes: 60,
		RetentionDays:          400,
		Timezone:               "UTC",
		FleetBindAddress:       "127.0.0.1",
		FleetPort:              8085,
		WorkerURLPolicy:        "private",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("unexpected default config:\n got %+v\nwant %+v", cfg, want)
	}
	if cfg.RetentionDays != 400 {
		t.Fatalf("expected RetentionDays to default to 400, got %d", cfg.RetentionDays)
	}
	if m.AppDir() == "" || m.DataDir() == "" {
		t.Fatal("expected AppDir and DataDir to be set")
	}
}

func TestSaveCoercesEmptyAndInvalidValues(t *testing.T) {
	m := newTestManager(t)

	defaults := AppConfig{
		DisplayCurrency:        "USD",
		RuntimeProvider:        "existing-docker",
		HostnamePrefix:         "cashpilot",
		CollectIntervalMinutes: 60,
		RetentionDays:          400,
		Timezone:               "UTC",
		FleetBindAddress:       "127.0.0.1",
		FleetPort:              8085,
		WorkerURLPolicy:        "private",
	}

	cases := []struct {
		name string
		in   AppConfig
		want AppConfig
	}{
		{
			name: "all empty coerces to defaults",
			in:   AppConfig{},
			want: defaults,
		},
		{
			name: "non-positive interval, retention and port coerce to defaults",
			in:   AppConfig{CollectIntervalMinutes: -5, RetentionDays: -3, FleetPort: 0},
			want: defaults,
		},
		{
			name: "negative port coerces to default",
			in:   AppConfig{FleetPort: -1},
			want: defaults,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := m.Save(tc.in); err != nil {
				t.Fatalf("Save error: %v", err)
			}
			if got := m.Config(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("coercion mismatch:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestSavePreservesValidValues(t *testing.T) {
	m := newTestManager(t)

	in := AppConfig{
		FirstRunComplete:       true,
		DisplayCurrency:        "EUR",
		RuntimeProvider:        "podman",
		AutoUpdate:             true,
		HostnamePrefix:         "myrig",
		CollectIntervalMinutes: 15,
		RetentionDays:          180,
		Timezone:               "Europe/Madrid",
		FleetAPIKey:            "token-123",
		FleetBindAddress:       "127.0.0.1",
		FleetPort:              9000,
		WorkerURLPolicy:        "strict",
		WorkerAllowedHosts:     []string{"10.0.0.0/8", "*.mango-alpha.ts.net"},
		WorkerAllowMetadata:    true,
	}
	if err := m.Save(in); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if got := m.Config(); !reflect.DeepEqual(got, in) {
		t.Fatalf("valid values were not preserved:\n got %+v\nwant %+v", got, in)
	}
}

func TestConfigPersistsAcrossManagers(t *testing.T) {
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())

	m1, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager(m1) error: %v", err)
	}
	saved := m1.Config()
	saved.DisplayCurrency = "GBP"
	saved.FleetPort = 9100
	saved.FleetAPIKey = "persisted-key"
	if err := m1.Save(saved); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// A fresh Manager over the same directory loads the persisted config.json
	// (the non-first-run load branch).
	m2, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager(m2) error: %v", err)
	}
	got := m2.Config()
	if got.DisplayCurrency != "GBP" || got.FleetPort != 9100 || got.FleetAPIKey != "persisted-key" {
		t.Fatalf("config did not persist across managers: %+v", got)
	}
}

// TestMetricsEnabledOptInDefault pins the Prometheus metrics flag as opt-in: it
// defaults to false (disabled) on a fresh config, survives a Save (is not coerced
// off), and persists across a reload — so enabling the /metrics endpoint is a
// deliberate, durable choice rather than the default.
func TestMetricsEnabledOptInDefault(t *testing.T) {
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())

	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager error: %v", err)
	}
	if m.Config().MetricsEnabled {
		t.Fatal("expected MetricsEnabled to default to false (opt-in)")
	}

	cfg := m.Config()
	cfg.MetricsEnabled = true
	if err := m.Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if !m.Config().MetricsEnabled {
		t.Fatal("expected MetricsEnabled=true to be preserved by Save, not coerced off")
	}

	// A fresh Manager over the same directory reloads the persisted flag.
	m2, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager(reload) error: %v", err)
	}
	if !m2.Config().MetricsEnabled {
		t.Fatal("expected MetricsEnabled=true to persist across a reload")
	}
}

// TestWorkerURLPolicyDefaults pins the SSRF worker-URL policy surface consumed by
// internal/fleetnet: a fresh config defaults WorkerURLPolicy to "private" (the safe
// homelab default), WorkerAllowMetadata to false (metadata always blocked), and
// WorkerAllowedHosts to empty; a non-empty policy is preserved by Save (not coerced)
// and the fields round-trip across a reload.
func TestWorkerURLPolicyDefaults(t *testing.T) {
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())

	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager error: %v", err)
	}
	cfg := m.Config()
	if cfg.WorkerURLPolicy != "private" {
		t.Fatalf("expected WorkerURLPolicy to default to \"private\", got %q", cfg.WorkerURLPolicy)
	}
	if cfg.WorkerAllowMetadata {
		t.Fatal("expected WorkerAllowMetadata to default to false")
	}
	if len(cfg.WorkerAllowedHosts) != 0 {
		t.Fatalf("expected WorkerAllowedHosts to default to empty, got %v", cfg.WorkerAllowedHosts)
	}

	cfg.WorkerURLPolicy = "strict"
	cfg.WorkerAllowedHosts = []string{"192.168.0.0/16", "*.mango-alpha.ts.net"}
	cfg.WorkerAllowMetadata = true
	if err := m.Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if got := m.Config(); got.WorkerURLPolicy != "strict" {
		t.Fatalf("expected WorkerURLPolicy=strict to be preserved by Save, got %q", got.WorkerURLPolicy)
	}

	// A fresh Manager over the same directory reloads the persisted fields.
	m2, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager(reload) error: %v", err)
	}
	got := m2.Config()
	if got.WorkerURLPolicy != "strict" || !got.WorkerAllowMetadata {
		t.Fatalf("worker-URL policy did not persist across reload: %+v", got)
	}
	if !reflect.DeepEqual(got.WorkerAllowedHosts, []string{"192.168.0.0/16", "*.mango-alpha.ts.net"}) {
		t.Fatalf("WorkerAllowedHosts did not persist across reload: %v", got.WorkerAllowedHosts)
	}
}

func TestMasterKeyGeneratesAndReuses(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()

	key1, err := MasterKey(dir)
	if err != nil {
		t.Fatalf("MasterKey error: %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("expected a 32-byte key, got %d", len(key1))
	}
	key2, err := MasterKey(dir)
	if err != nil {
		t.Fatalf("MasterKey (reuse) error: %v", err)
	}
	if !bytes.Equal(key1, key2) {
		t.Fatal("expected MasterKey to return a stable key on repeat calls")
	}
}

func TestAppDataDirOverrideAndDefault(t *testing.T) {
	// An explicit override wins.
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", "/tmp/cashpilot-test-override")
	dir, err := appDataDir()
	if err != nil {
		t.Fatalf("appDataDir error: %v", err)
	}
	if dir != "/tmp/cashpilot-test-override" {
		t.Fatalf("expected the override dir, got %q", dir)
	}

	// With no override, the platform default path includes the app identifier.
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", "")
	dir, err = appDataDir()
	if err != nil {
		t.Fatalf("appDataDir (default) error: %v", err)
	}
	if !strings.Contains(strings.ToLower(dir), "cashpilot") {
		t.Fatalf("expected the default data dir to include the app identifier, got %q", dir)
	}
}

// TestConfigConcurrentAccessIsRaceFree pins the RWMutex added to Manager: a
// writer goroutine hammering Config()+Save() while the main goroutine reads
// Config() must be clean under `go test -race`.
func TestConfigConcurrentAccessIsRaceFree(t *testing.T) {
	m := newTestManager(t)

	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			cfg := m.Config()
			cfg.CollectIntervalMinutes = (i % 30) + 1
			if err := m.Save(cfg); err != nil {
				// t.Errorf is safe from another goroutine; t.Fatalf is not.
				t.Errorf("Save error: %v", err)
				return
			}
		}
	}()

	for i := 0; i < iterations; i++ {
		_ = m.Config()
	}
	wg.Wait()
}

// TestLoadCoercesInvalidOnDisk pins that load() runs applyDefaults over the
// on-disk config, so out-of-range values persisted by an older build (or a
// hand-edited file) are corrected when a fresh Manager reads them.
func TestLoadCoercesInvalidOnDisk(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", dir)

	raw := []byte(`{"fleetPort":0,"collectIntervalMinutes":-5}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager error: %v", err)
	}
	cfg := m.Config()
	if cfg.FleetPort != 8085 {
		t.Fatalf("expected on-disk fleetPort=0 coerced to 8085, got %d", cfg.FleetPort)
	}
	if cfg.CollectIntervalMinutes != 60 {
		t.Fatalf("expected on-disk collectIntervalMinutes=-5 coerced to 60, got %d", cfg.CollectIntervalMinutes)
	}
}

// TestMasterKeyFileFallbackWhenKeyringUnavailable pins the file-backed fallback:
// when the OS keyring is unavailable, MasterKey persists a base64 32-byte key to
// <appDir>/.credential_key and reuses it on subsequent calls.
func TestMasterKeyFileFallbackWhenKeyringUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring"))
	defer keyring.MockInit()

	dir := t.TempDir()
	key1, err := MasterKey(dir)
	if err != nil {
		t.Fatalf("MasterKey error: %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("expected a 32-byte key, got %d", len(key1))
	}

	keyPath := filepath.Join(dir, ".credential_key")
	stored, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("expected the fallback key file %q to exist: %v", keyPath, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(stored))
	if err != nil {
		t.Fatalf("fallback key file is not valid base64: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("expected the persisted key to decode to 32 bytes, got %d", len(decoded))
	}
	if !bytes.Equal(decoded, key1) {
		t.Fatal("expected the persisted key file to hold the returned key")
	}

	key2, err := MasterKey(dir)
	if err != nil {
		t.Fatalf("MasterKey (reuse) error: %v", err)
	}
	if !bytes.Equal(key1, key2) {
		t.Fatal("expected the file-backed key to be byte-identical across calls")
	}
}
