package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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
		// A new install updates its apps by itself (image_updates_test.go).
		ImageUpdates: &ImageUpdates{Mode: ImageUpdatesAuto, Hour: ImageUpdatesDefaultHour},
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
		FleetBindAddress:       "127.0.0.1",
		FleetPort:              9000,
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
	// The fleet bearer token is no longer a config.json field — it lives in the OS
	// keychain (0600 file fallback) via FleetKey/SetFleetKey — so it must round-trip
	// empty here regardless of the other persisted values.
	if got.DisplayCurrency != "GBP" || got.FleetPort != 9100 || got.FleetAPIKey != "" {
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

	// On platforms whose keychain is always present (macOS, Windows), a non-ErrNotFound
	// keyring error means "locked or access denied", so MasterKey now refuses to mint a
	// replacement rather than silently overwriting an existing key. The mint+file
	// fallback asserted below only applies where the Secret Service can be genuinely
	// absent (Linux/CI, the authoritative gate this fallback path targets).
	if refuseKeyRegen(errors.New("no keyring"), runtime.GOOS) {
		if _, err := MasterKey(dir); err == nil {
			t.Fatal("expected MasterKey to refuse regeneration when the keychain is locked/denied")
		}
		if _, statErr := os.Stat(filepath.Join(dir, ".credential_key")); !os.IsNotExist(statErr) {
			t.Fatal("expected no fallback key file to be written when regeneration is refused")
		}
		return
	}

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

// TestFleetKeyRoundTripsThroughKeyring pins the keychain-backed fleet-token store:
// an unstored token reads as empty with a nil error (so the caller can generate or
// migrate one), and SetFleetKey then FleetKey returns the same value.
func TestFleetKeyRoundTripsThroughKeyring(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()

	got, err := FleetKey(dir)
	if err != nil {
		t.Fatalf("FleetKey (empty) error: %v", err)
	}
	if got != "" {
		t.Fatalf("expected an unstored fleet token to read as empty, got %q", got)
	}

	if err := SetFleetKey(dir, "fleet-secret-token"); err != nil {
		t.Fatalf("SetFleetKey error: %v", err)
	}
	got, err = FleetKey(dir)
	if err != nil {
		t.Fatalf("FleetKey error: %v", err)
	}
	if got != "fleet-secret-token" {
		t.Fatalf("expected the stored fleet token, got %q", got)
	}
}

// TestFleetKeyFileFallbackWhenKeyringUnavailable pins the file-backed fallback used
// by headless Linux / CI: with no keyring, SetFleetKey writes a 0600
// <appDir>/.fleet_api_key file and FleetKey reads the token back.
func TestFleetKeyFileFallbackWhenKeyringUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring"))
	defer keyring.MockInit()

	dir := t.TempDir()
	if err := SetFleetKey(dir, "fleet-secret-token"); err != nil {
		t.Fatalf("SetFleetKey error: %v", err)
	}

	keyPath := filepath.Join(dir, ".fleet_api_key")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("expected the fallback token file %q to exist: %v", keyPath, err)
	}
	// Windows has no Unix permission bits (Go reports 0666 for a writable file),
	// so the 0600 guarantee is asserted only off Windows.
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Fatalf("expected the fallback token file to be 0600, got %o", perm)
	}

	got, err := FleetKey(dir)
	if err != nil {
		t.Fatalf("FleetKey error: %v", err)
	}
	if got != "fleet-secret-token" {
		t.Fatalf("expected the file-backed fleet token to round-trip, got %q", got)
	}
}

// TestRefuseKeyRegen unit-tests the pure platform gate that guards MasterKey/FleetKey
// from silently overwriting a key that exists but is momentarily unreadable. Because it
// takes goos as a parameter, the whole darwin/windows/linux matrix is exercised on any
// runner (including CI's Linux host) with no real keychain.
func TestRefuseKeyRegen(t *testing.T) {
	generic := errors.New("keychain locked")
	cases := []struct {
		name string
		err  error
		goos string
		want bool
	}{
		{"not found on darwin does not refuse", keyring.ErrNotFound, "darwin", false},
		{"not found on windows does not refuse", keyring.ErrNotFound, "windows", false},
		{"not found on linux does not refuse", keyring.ErrNotFound, "linux", false},
		{"generic error on darwin refuses", generic, "darwin", true},
		{"generic error on windows refuses", generic, "windows", true},
		{"generic error on linux does not refuse", generic, "linux", false},
		{"wrapped not found on darwin does not refuse", fmt.Errorf("get: %w", keyring.ErrNotFound), "darwin", false},
		{"nil error never refuses", nil, "darwin", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := refuseKeyRegen(tc.err, tc.goos); got != tc.want {
				t.Fatalf("refuseKeyRegen(%v, %q) = %v, want %v", tc.err, tc.goos, got, tc.want)
			}
		})
	}
}

// TestMasterKeyReturnsStoredFileKeyDespiteKeyringError proves the 0600 file fallback
// still shields the user on ANY platform: even when the keychain returns a
// non-ErrNotFound error (locked/denied) — the case the refuse-to-regen guard targets —
// a valid .credential_key file is read and returned verbatim, and no new key is minted.
func TestMasterKeyReturnsStoredFileKeyDespiteKeyringError(t *testing.T) {
	keyring.MockInitWithError(errors.New("keychain locked"))
	defer keyring.MockInit()

	dir := t.TempDir()
	want := bytes.Repeat([]byte{0x42}, 32)
	encoded := base64.StdEncoding.EncodeToString(want)
	keyPath := filepath.Join(dir, ".credential_key")
	if err := os.WriteFile(keyPath, []byte(encoded), 0o600); err != nil {
		t.Fatalf("write fallback key file: %v", err)
	}

	got, err := MasterKey(dir)
	if err != nil {
		t.Fatalf("MasterKey error: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("expected MasterKey to return the existing file key, not a freshly minted one")
	}

	// The file must be untouched — a mint would have rewritten it with a new key.
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read fallback key file: %v", err)
	}
	if string(after) != encoded {
		t.Fatal("expected the fallback key file to be left untouched")
	}
}

func TestMasterKeyRefusesRegenWhenKeychainLockedOnDarwin(t *testing.T) {
	keyring.MockInitWithError(errors.New("keychain locked"))
	defer keyring.MockInit()
	old := runtimeGOOS
	runtimeGOOS = "darwin"
	defer func() { runtimeGOOS = old }()

	dir := t.TempDir() // no .credential_key file — without the guard this would mint a new key

	if _, err := MasterKey(dir); err == nil {
		t.Fatal("expected MasterKey to refuse regeneration (return an error) when the keychain is locked, got nil")
	}
	if _, err := os.Stat(filepath.Join(dir, ".credential_key")); !os.IsNotExist(err) {
		t.Fatal("expected no key file to be written when regeneration is refused")
	}
}

func TestFleetKeyRefusesRegenWhenKeychainLockedOnDarwin(t *testing.T) {
	keyring.MockInitWithError(errors.New("keychain locked"))
	defer keyring.MockInit()
	old := runtimeGOOS
	runtimeGOOS = "darwin"
	defer func() { runtimeGOOS = old }()

	dir := t.TempDir() // no .fleet_api_key file

	got, err := FleetKey(dir)
	if err == nil {
		t.Fatal("expected FleetKey to refuse regeneration (return an error) when the keychain is locked, got nil")
	}
	if got != "" {
		t.Fatalf("expected an empty key on refusal, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".fleet_api_key")); !os.IsNotExist(err) {
		t.Fatal("expected no fleet key file to be written when regeneration is refused")
	}
}

// TestClearedSecretStaysClearedAfterAFallbackWrite pins clients-2: a secret
// written to the 0600 fallback file while the keychain refused writes must not
// come back once the keychain works again and the secret is cleared. The
// readers treat an empty keychain value as "not stored" and fall through to the
// file, so a leftover file resurrected the old server's worker key.
func TestClearedSecretStaysClearedAfterAFallbackWrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(dir, v string) error
		get  func(dir string) (string, error)
	}{
		{"fleet key", SetFleetKey, FleetKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			keyring.MockInitWithError(errors.New("keychain locked"))
			if err := tc.set(dir, "OLD-SERVER-KEY"); err != nil {
				t.Fatalf("fallback write: %v", err)
			}
			keyring.MockInit() // the keychain works again
			defer keyring.MockInit()

			if err := tc.set(dir, ""); err != nil {
				t.Fatalf("clear: %v", err)
			}
			got, err := tc.get(dir)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got != "" {
				t.Fatalf("a cleared secret came back from the fallback file: %q", got)
			}
		})
	}
}

// TestMigratingASecretKeepsTheFallbackFile: a reader copies a fallback value
// into the keychain but keeps the file. On Linux the secret service can be
// locked at a later start; the file is then the only copy, and a hub that lost
// its fleet key would mint a new one and unpair every phone.
func TestMigratingASecretKeepsTheFallbackFile(t *testing.T) {
	prev := runtimeGOOS
	runtimeGOOS = "linux"
	defer func() { runtimeGOOS = prev }()
	keyring.MockInit()
	defer keyring.MockInit()
	dir := t.TempDir()
	for _, f := range []string{".upstream_worker_key", ".fleet_api_key"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("from-file"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := LegacyWorkerKey(dir, ""); got != "from-file" {
		t.Fatalf("worker key migration read %q", got)
	}
	if got, _ := FleetKey(dir); got != "from-file" {
		t.Fatalf("fleet key migration read %q", got)
	}

	keyring.MockInitWithError(errors.New("secret service is locked")) // a later start
	if got, err := LegacyWorkerKey(dir, ""); err != nil || got != "from-file" {
		t.Fatalf("worker key lost when the keychain is locked: %q, %v", got, err)
	}
	if got, err := FleetKey(dir); err != nil || got != "from-file" {
		t.Fatalf("fleet key lost when the keychain is locked: %q, %v", got, err)
	}
}

// A session consent must survive a restart with its text version and date intact:
// the wizard shows install steps only while both are there, so a consent lost on
// reload would ask again, and a consent that lost its version would never match.
func TestSessionConsentPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", dir)
	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	at := time.Date(2026, 11, 24, 18, 30, 0, 0, time.UTC)
	if err := m.Update(func(c *AppConfig) {
		c.SessionConsent = map[string]SessionConsent{"wipter": {TextVersion: "2026-11-24", AcceptedAt: at}}
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	reloaded, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager after restart: %v", err)
	}
	got, ok := reloaded.Config().SessionConsent["wipter"]
	if !ok {
		t.Fatalf("the consent was lost on reload: %+v", reloaded.Config().SessionConsent)
	}
	if got.TextVersion != "2026-11-24" || !got.AcceptedAt.Equal(at) {
		t.Errorf("reloaded consent %+v, want version 2026-11-24 accepted at %v", got, at)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"textVersion": "2026-11-24"`)) || !bytes.Contains(raw, []byte(`"acceptedAt": "2026-11-24T18:30:00Z"`)) {
		t.Errorf("config.json does not carry the version and date:\n%s", raw)
	}
}

// An install from before this field has no consent at all, and a config with no
// consent writes no key, so an older build reading the file sees nothing new.
func TestSessionConsentAbsentByDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"firstRunComplete":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if len(m.Config().SessionConsent) != 0 {
		t.Fatalf("an old config gained consents: %+v", m.Config().SessionConsent)
	}
	if err := m.Save(m.Config()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if bytes.Contains(raw, []byte("sessionConsent")) {
		t.Errorf("an empty consent map was written:\n%s", raw)
	}
}

// TestModeIsInferredOnceForAnExistingInstall: an install from before the mode
// existed must not see the first run again, and must keep doing what it did. A
// saved server address means it reported to a CashPilot (follower); a finished
// first run without one means it ran everything itself (lead). A new install
// keeps no mode, so its first run asks. The answer is written to disk once.
func TestModeIsInferredOnceForAnExistingInstall(t *testing.T) {
	cases := []struct {
		name string
		disk string // config.json before the upgrade; "" = no file (new install)
		want string
	}{
		{"new install", "", ""},
		{"first run not finished, no address", `{"firstRunComplete":false}`, ""},
		{"finished, no address", `{"firstRunComplete":true}`, ModeLead},
		{"finished, address saved", `{"firstRunComplete":true,"upstreamUrl":"https://cp.example"}`, ModeFollower},
		{"address saved before the first run finished", `{"upstreamUrl":"http://192.168.1.5:8080"}`, ModeFollower},
		{"blank address is no address", `{"firstRunComplete":true,"upstreamUrl":"  "}`, ModeLead},
		{"mode already chosen wins", `{"firstRunComplete":true,"upstreamUrl":"https://cp.example","mode":"lead"}`, ModeLead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", dir)
			if tc.disk != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.disk), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			m, err := NewManager()
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			if got := m.Config().Mode; got != tc.want {
				t.Fatalf("mode = %q, want %q", got, tc.want)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			var onDisk AppConfig
			if err := json.Unmarshal(raw, &onDisk); err != nil {
				t.Fatal(err)
			}
			if onDisk.Mode != tc.want {
				t.Fatalf("mode on disk = %q, want %q: the inferred mode was not written once", onDisk.Mode, tc.want)
			}
			// Clearing the address by hand afterwards must not flip the mode back:
			// only the user's own switch changes it.
			if tc.want == ModeFollower {
				if err := m.Update(func(c *AppConfig) { c.UpstreamURL = "" }); err != nil {
					t.Fatal(err)
				}
				again, err := NewManager()
				if err != nil {
					t.Fatal(err)
				}
				if got := again.Config().Mode; got != ModeFollower {
					t.Fatalf("mode changed on a later start: %q", got)
				}
			}
		})
	}
}

// TestAManagerSeesAndKeepsAChangeAnotherProcessSaved: the window and the
// background helper each hold a Manager on the same file. A join saved by one
// must reach the other (Reload), and a later write by the other must not put
// its older copy back over the join (Update starts from the file).
func TestAManagerSeesAndKeepsAChangeAnotherProcessSaved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", dir)
	window, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	helper, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := helper.Reload(); err != nil || changed {
		t.Fatalf("Reload with nothing new: changed %v err %v", changed, err)
	}

	if err := window.Update(func(c *AppConfig) { c.Mode = ModeFollower; c.UpstreamURL = "http://192.168.1.10:8080" }); err != nil {
		t.Fatal(err)
	}
	// The helper writes something of its own before it looked again.
	if err := helper.Update(func(c *AppConfig) { c.IsolationOn = true }); err != nil {
		t.Fatal(err)
	}
	again, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	got := again.Config()
	if got.Mode != ModeFollower || got.UpstreamURL != "http://192.168.1.10:8080" || !got.IsolationOn {
		t.Fatalf("on disk: mode %q url %q isolation %v; the helper's write undid the join or lost its own", got.Mode, got.UpstreamURL, got.IsolationOn)
	}

	if err := window.Update(func(c *AppConfig) { c.Mode = ModeLead; c.UpstreamURL = "" }); err != nil {
		t.Fatal(err)
	}
	if changed, err := helper.Reload(); err != nil || !changed {
		t.Fatalf("Reload after the window left: changed %v err %v", changed, err)
	}
	if c := helper.Config(); c.Mode != ModeLead || c.UpstreamURL != "" || !c.IsolationOn {
		t.Fatalf("the helper reads mode %q url %q isolation %v", c.Mode, c.UpstreamURL, c.IsolationOn)
	}
	if changed, _ := helper.Reload(); changed {
		t.Fatal("a second Reload with nothing new reported a change")
	}

	// A file caught halfway through a write keeps what is in memory.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"mode":"fol`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := helper.Reload(); err == nil {
		t.Fatal("a broken file read as no error")
	}
	if c := helper.Config(); c.Mode != ModeLead || !c.IsolationOn {
		t.Fatalf("a broken file changed the config in memory: %+v", c)
	}
}

// TestAConcurrentReaderNeverSeesAHalfWrittenConfig: the window and the
// background helper each read and write config.json. A reader in the other
// process must always see a whole file, the old one or the new one, never an
// empty or partial one.
func TestAConcurrentReaderNeverSeesAHalfWrittenConfig(t *testing.T) {
	m := newTestManager(t)
	big := map[string]string{}
	for i := 0; i < 2000; i++ {
		big[fmt.Sprintf("service-%04d", i)] = strings.Repeat("f", 64)
	}
	path := filepath.Join(m.AppDir(), "config.json")

	stop := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		defer close(writerDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			err := m.Update(func(c *AppConfig) {
				if i%2 == 0 {
					c.DriftIgnored = big
				} else {
					c.DriftIgnored = nil
				}
			})
			if err != nil {
				writerDone <- err
				return
			}
		}
	}()

	deadline := time.Now().Add(400 * time.Millisecond)
	reads := 0
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // Windows: the rename can briefly hold the file
		}
		reads++
		var cfg AppConfig
		if len(raw) == 0 || json.Unmarshal(raw, &cfg) != nil {
			close(stop)
			<-writerDone
			t.Fatalf("read %d saw a half-written config (%d bytes)", reads, len(raw))
		}
	}
	close(stop)
	if err := <-writerDone; err != nil {
		t.Fatalf("Update: %v", err)
	}
	if reads == 0 {
		t.Fatal("the reader never read the file, so this proves nothing")
	}
	entries, err := os.ReadDir(m.AppDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("a temp file was left behind: %s", e.Name())
		}
	}
}
