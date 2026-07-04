package config

import (
	"bytes"
	"strings"
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
		Timezone:               "UTC",
		FleetBindAddress:       "0.0.0.0",
		FleetPort:              8085,
	}
	if cfg != want {
		t.Fatalf("unexpected default config:\n got %+v\nwant %+v", cfg, want)
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
		Timezone:               "UTC",
		FleetBindAddress:       "0.0.0.0",
		FleetPort:              8085,
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
			name: "non-positive interval and port coerce to defaults",
			in:   AppConfig{CollectIntervalMinutes: -5, FleetPort: 0},
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
			if got := m.Config(); got != tc.want {
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
		Timezone:               "Europe/Madrid",
		FleetAPIKey:            "token-123",
		FleetBindAddress:       "127.0.0.1",
		FleetPort:              9000,
	}
	if err := m.Save(in); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if got := m.Config(); got != in {
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
