package store

import (
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// TestMain installs an in-memory keyring mock so that Open -> config.MasterKey
// never touches the real OS keychain (which would prompt or fail on a headless
// CI machine). The mock is process-global and set once here; every Open in this
// test binary therefore derives the same master key, which is what lets the
// "reopen and decrypt" tests work.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCredentialRoundTrip(t *testing.T) {
	s := openTestStore(t)

	// A missing slug returns an empty map, not an error (sql.ErrNoRows branch).
	got, err := s.GetCredentials("absent")
	if err != nil {
		t.Fatalf("GetCredentials(absent) error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map for absent slug, got %v", got)
	}

	want := map[string]string{"user": "alice", "token": "SUPERSECRETVALUE123"}
	if err := s.SaveCredentials("mysterium", want); err != nil {
		t.Fatalf("SaveCredentials error: %v", err)
	}
	got, err = s.GetCredentials("mysterium")
	if err != nil {
		t.Fatalf("GetCredentials error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("credential mismatch: got %v want %v", got, want)
	}

	// Saving again upserts (ON CONFLICT) and replaces the stored value.
	updated := map[string]string{"user": "bob"}
	if err := s.SaveCredentials("mysterium", updated); err != nil {
		t.Fatalf("SaveCredentials overwrite error: %v", err)
	}
	got, err = s.GetCredentials("mysterium")
	if err != nil {
		t.Fatalf("GetCredentials after overwrite error: %v", err)
	}
	if !reflect.DeepEqual(got, updated) {
		t.Fatalf("expected overwritten credentials %v, got %v", updated, got)
	}
}

func TestCredentialsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	secret := "SUPERSECRETVALUE123"

	s1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(s1) error: %v", err)
	}
	if err := s1.SaveCredentials("earnapp", map[string]string{"api_key": secret}); err != nil {
		t.Fatalf("SaveCredentials error: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close(s1) error: %v", err)
	}

	// Reopening with the same master key must decrypt back to the original.
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(s2) error: %v", err)
	}
	defer s2.Close()
	got, err := s2.GetCredentials("earnapp")
	if err != nil {
		t.Fatalf("GetCredentials after reopen error: %v", err)
	}
	if got["api_key"] != secret {
		t.Fatalf("expected decrypted secret %q, got %q", secret, got["api_key"])
	}
}

func TestCredentialsEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	secret := "SUPERSECRETVALUE123"

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	if err := s.SaveCredentials("earnapp", map[string]string{"api_key": secret}); err != nil {
		t.Fatalf("SaveCredentials error: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// Read the raw column directly: it must be AES-GCM ciphertext (base64 of
	// nonce + ciphertext), never the plaintext secret. The "sqlite" driver is
	// registered by store.go's blank import, shared with this test package.
	raw, err := sql.Open("sqlite", filepath.Join(dir, "cashpilot-desktop.db"))
	if err != nil {
		t.Fatalf("raw sql.Open error: %v", err)
	}
	defer raw.Close()
	var stored string
	if err := raw.QueryRow(`SELECT value FROM credentials WHERE slug = ?`, "earnapp").Scan(&stored); err != nil {
		t.Fatalf("raw query error: %v", err)
	}
	if stored == "" {
		t.Fatal("stored credential value is empty")
	}
	if strings.Contains(stored, secret) {
		t.Fatalf("stored value contains plaintext secret: %q", stored)
	}
	decoded, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		t.Fatalf("stored value is not valid base64: %v", err)
	}
	if len(decoded) < 12 {
		t.Fatalf("ciphertext too short to contain a GCM nonce: %d bytes", len(decoded))
	}
}

// TestListCredentialSlugs pins the credential-set source used by collectAll's
// union: every slug with a saved credential row is returned, ordered by slug, an
// empty store returns nothing, and overwriting an existing slug's credentials
// (ON CONFLICT upsert) never duplicates it in the list.
func TestListCredentialSlugs(t *testing.T) {
	s := openTestStore(t)

	// An empty store has no credential slugs.
	if got := s.ListCredentialSlugs(); len(got) != 0 {
		t.Fatalf("expected no credential slugs on an empty store, got %v", got)
	}

	if err := s.SaveCredentials("vast-ai", map[string]string{"api_key": "x"}); err != nil {
		t.Fatalf("SaveCredentials(vast-ai) error: %v", err)
	}
	if err := s.SaveCredentials("grass", map[string]string{"user": "a", "pass": "b"}); err != nil {
		t.Fatalf("SaveCredentials(grass) error: %v", err)
	}

	// Returned ordered by slug: "grass" < "vast-ai".
	if got, want := s.ListCredentialSlugs(), []string{"grass", "vast-ai"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListCredentialSlugs = %v, want %v", got, want)
	}

	// Overwriting an existing slug upserts the same row; it must not duplicate.
	if err := s.SaveCredentials("vast-ai", map[string]string{"api_key": "y"}); err != nil {
		t.Fatalf("SaveCredentials(vast-ai overwrite) error: %v", err)
	}
	if got, want := s.ListCredentialSlugs(), []string{"grass", "vast-ai"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after overwrite ListCredentialSlugs = %v, want %v (no duplicate)", got, want)
	}
}

// TestServiceDetails pins the generic per-service detail store used to stash a
// collector's JSON side-document (e.g. MystNodes per-node earnings) keyed by slug:
// an absent slug reads back "" with no error, an empty store lists nothing, a save
// round-trips the blob verbatim, a second save upserts (overwrites) rather than
// duplicating, and ListServiceDetails returns every slug's blob keyed by slug.
func TestServiceDetails(t *testing.T) {
	s := openTestStore(t)

	// Absent slug -> "" and no error (not a failure state).
	got, err := s.GetServiceDetail("mysterium")
	if err != nil {
		t.Fatalf("GetServiceDetail(absent) error: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty detail for an absent slug, got %q", got)
	}

	// An empty store lists nothing (non-nil, rangeable map).
	if m := s.ListServiceDetails(); len(m) != 0 {
		t.Fatalf("expected no service details on an empty store, got %v", m)
	}

	// Save then get round-trips the JSON blob byte-for-byte.
	first := `[{"identity":"0xabc","online":true,"earnings30dMyst":0.75}]`
	if err := s.SaveServiceDetail("mysterium", first); err != nil {
		t.Fatalf("SaveServiceDetail error: %v", err)
	}
	got, err = s.GetServiceDetail("mysterium")
	if err != nil {
		t.Fatalf("GetServiceDetail error: %v", err)
	}
	if got != first {
		t.Fatalf("detail did not round-trip: got %q want %q", got, first)
	}

	// Saving the same slug again upserts (ON CONFLICT) and overwrites the prior blob.
	second := `[{"identity":"0xdef","online":false,"earnings30dMyst":0}]`
	if err := s.SaveServiceDetail("mysterium", second); err != nil {
		t.Fatalf("SaveServiceDetail overwrite error: %v", err)
	}
	got, err = s.GetServiceDetail("mysterium")
	if err != nil {
		t.Fatalf("GetServiceDetail after overwrite error: %v", err)
	}
	if got != second {
		t.Fatalf("expected overwritten detail %q, got %q", second, got)
	}

	// A second slug coexists; ListServiceDetails returns both, keyed by slug, with no
	// duplicate for the upserted "mysterium".
	storjBlob := `{"nodeId":"abc"}`
	if err := s.SaveServiceDetail("storj", storjBlob); err != nil {
		t.Fatalf("SaveServiceDetail(storj) error: %v", err)
	}
	all := s.ListServiceDetails()
	if len(all) != 2 {
		t.Fatalf("expected 2 detail rows, got %d: %v", len(all), all)
	}
	if all["mysterium"] != second {
		t.Fatalf("ListServiceDetails[mysterium] = %q, want %q (latest upserted blob)", all["mysterium"], second)
	}
	if all["storj"] != storjBlob {
		t.Fatalf("ListServiceDetails[storj] = %q, want %q", all["storj"], storjBlob)
	}
}

func TestFleetDeviceInsertListAndDelete(t *testing.T) {
	s := openTestStore(t)

	dev, err := s.UpsertFleetDevice(FleetDevice{
		Name:     "worker-1",
		Endpoint: "http://192.168.1.5:8081",
		OS:       "linux",
		Arch:     "amd64",
		Services: []string{"storj", "mysterium"},
	})
	if err != nil {
		t.Fatalf("UpsertFleetDevice error: %v", err)
	}
	if dev.ID <= 0 {
		t.Fatalf("expected an assigned device ID, got %d", dev.ID)
	}
	if dev.Kind != "worker" {
		t.Fatalf("expected default kind 'worker', got %q", dev.Kind)
	}
	if dev.Status != "offline" {
		t.Fatalf("expected default status 'offline', got %q", dev.Status)
	}

	devices := s.ListFleetDevices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	listed := devices[0]
	if listed.ID != dev.ID || listed.Name != "worker-1" || listed.OS != "linux" || listed.Arch != "amd64" {
		t.Fatalf("unexpected listed device: %+v", listed)
	}
	if listed.Endpoint != "http://192.168.1.5:8081" {
		t.Fatalf("unexpected endpoint: %q", listed.Endpoint)
	}
	if !reflect.DeepEqual(listed.Services, []string{"storj", "mysterium"}) {
		t.Fatalf("services did not round-trip: %v", listed.Services)
	}

	if err := s.DeleteFleetDevice(dev.ID); err != nil {
		t.Fatalf("DeleteFleetDevice error: %v", err)
	}
	if devices := s.ListFleetDevices(); len(devices) != 0 {
		t.Fatalf("expected no devices after delete, got %d", len(devices))
	}
}

func TestFleetDeviceUpdateExisting(t *testing.T) {
	s := openTestStore(t)

	dev, err := s.UpsertFleetDevice(FleetDevice{Name: "worker-1", Status: "offline"})
	if err != nil {
		t.Fatalf("insert error: %v", err)
	}

	dev.Status = "online"
	dev.Endpoint = "http://10.0.0.9:8081"
	updated, err := s.UpsertFleetDevice(dev) // ID > 0 -> UPDATE branch
	if err != nil {
		t.Fatalf("update error: %v", err)
	}
	if updated.ID != dev.ID {
		t.Fatalf("expected the same ID after update, got %d want %d", updated.ID, dev.ID)
	}
	devices := s.ListFleetDevices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 device after update, got %d", len(devices))
	}
	if devices[0].Status != "online" || devices[0].Endpoint != "http://10.0.0.9:8081" {
		t.Fatalf("update was not persisted: %+v", devices[0])
	}
}

// TestUpsertFleetDeviceNoIDUpsertsByKindName pins the follow-up fix to
// UpsertFleetDevice's no-id branch: with the UNIQUE(kind, name) index in place, a
// second manual-add (ID == 0) for the same (kind, name) must upsert onto the
// existing row — no error, no duplicate row — refreshing its mutable fields and
// returning the SAME id. A different kind or a different name is a distinct device.
func TestUpsertFleetDeviceNoIDUpsertsByKindName(t *testing.T) {
	s := openTestStore(t)

	first, err := s.UpsertFleetDevice(FleetDevice{
		Name:     "worker-1",
		Endpoint: "http://192.168.1.5:8081",
		Status:   "offline",
		Services: []string{"storj"},
	})
	if err != nil {
		t.Fatalf("first UpsertFleetDevice error: %v", err)
	}
	if first.ID <= 0 {
		t.Fatalf("expected an assigned device ID, got %d", first.ID)
	}

	// Second call: same (kind, name) defaults ("worker"/"worker-1"), ID left at zero
	// (a fresh manual-add value, as App.AddFleetDevice always sends), but different
	// field values. Must upsert onto the same row, not error and not duplicate.
	second, err := s.UpsertFleetDevice(FleetDevice{
		Name:     "worker-1",
		Endpoint: "http://10.0.0.9:8081",
		Status:   "online",
		Services: []string{"storj", "mysterium"},
	})
	if err != nil {
		t.Fatalf("second UpsertFleetDevice (same kind,name) error: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected the same ID on a (kind,name) upsert, got %d want %d", second.ID, first.ID)
	}

	devices := s.ListFleetDevices()
	if len(devices) != 1 {
		t.Fatalf("expected exactly 1 row after the repeat no-id upsert, got %d: %+v", len(devices), devices)
	}
	if devices[0].Endpoint != "http://10.0.0.9:8081" || devices[0].Status != "online" {
		t.Fatalf("expected the second call's fields to win, got %+v", devices[0])
	}
	if !reflect.DeepEqual(devices[0].Services, []string{"storj", "mysterium"}) {
		t.Fatalf("expected updated services, got %v", devices[0].Services)
	}

	// A different NAME is a distinct device.
	other, err := s.UpsertFleetDevice(FleetDevice{Name: "worker-2"})
	if err != nil {
		t.Fatalf("UpsertFleetDevice(worker-2) error: %v", err)
	}
	if other.ID == first.ID {
		t.Fatal("a different name must be a distinct device")
	}

	// The same NAME under a different KIND is also a distinct device.
	mobile, err := s.UpsertFleetDevice(FleetDevice{Name: "worker-1", Kind: "mobile"})
	if err != nil {
		t.Fatalf("UpsertFleetDevice(mobile/worker-1) error: %v", err)
	}
	if mobile.ID == first.ID {
		t.Fatal("a different kind with the same name must be a distinct device")
	}

	if got := len(s.ListFleetDevices()); got != 3 {
		t.Fatalf("expected 3 distinct devices (worker/worker-1, worker/worker-2, mobile/worker-1), got %d", got)
	}
}

func TestFleetHeartbeatInsertsThenUpdatesByKindAndName(t *testing.T) {
	s := openTestStore(t)

	first, err := s.UpsertFleetHeartbeat(FleetDevice{
		Name:     "node-a",
		OS:       "linux",
		Services: []string{"svc1"},
	})
	if err != nil {
		t.Fatalf("first heartbeat error: %v", err)
	}
	if first.ID <= 0 {
		t.Fatalf("expected an inserted ID, got %d", first.ID)
	}
	if first.Kind != "worker" {
		t.Fatalf("expected default kind 'worker', got %q", first.Kind)
	}
	if first.Status != "online" {
		t.Fatalf("expected default status 'online', got %q", first.Status)
	}
	if first.LastSeen == "" {
		t.Fatal("expected LastSeen to default to now")
	}

	// Same kind+name -> updates the existing row (no new device), refreshing
	// last_seen and services.
	second, err := s.UpsertFleetHeartbeat(FleetDevice{
		Name:     "node-a",
		Status:   "online",
		Services: []string{"svc1", "svc2"},
		LastSeen: "2026-01-02T03:04:05Z",
	})
	if err != nil {
		t.Fatalf("second heartbeat error: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected heartbeat to update existing row %d, got %d", first.ID, second.ID)
	}
	devices := s.ListFleetDevices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 device after repeat heartbeat, got %d", len(devices))
	}
	if devices[0].LastSeen != "2026-01-02T03:04:05Z" {
		t.Fatalf("expected updated LastSeen, got %q", devices[0].LastSeen)
	}
	if !reflect.DeepEqual(devices[0].Services, []string{"svc1", "svc2"}) {
		t.Fatalf("expected updated services, got %v", devices[0].Services)
	}
}

func TestFleetHeartbeatRequiresName(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.UpsertFleetHeartbeat(FleetDevice{Name: ""}); err == nil {
		t.Fatal("expected an error when the heartbeat has no name")
	}
}

// TestFleetHeartbeatUpsertOneRowPerKindName pins the M3 rewrite: repeated heartbeats
// for the same (kind, name) resolve to exactly ONE row reflecting the latest
// heartbeat (no duplicate devices), while the SAME name under a DIFFERENT kind is a
// distinct device — uniqueness is on the composite (kind, name), not name alone.
func TestFleetHeartbeatUpsertOneRowPerKindName(t *testing.T) {
	s := openTestStore(t)

	first, err := s.UpsertFleetHeartbeat(FleetDevice{Name: "node-x", Kind: "worker", Endpoint: "ep1", Services: []string{"a"}, LastSeen: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	second, err := s.UpsertFleetHeartbeat(FleetDevice{Name: "node-x", Kind: "worker", Endpoint: "ep2", Services: []string{"a", "b"}, LastSeen: "2026-02-02T00:00:00Z"})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("a repeat (kind,name) heartbeat must reuse row %d, got %d", first.ID, second.ID)
	}

	// The same NAME under a different KIND is a separate device (composite key).
	mobile, err := s.UpsertFleetHeartbeat(FleetDevice{Name: "node-x", Kind: "mobile", LastSeen: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("mobile upsert: %v", err)
	}
	if mobile.ID == first.ID {
		t.Fatal("a different kind with the same name must be a distinct device")
	}

	byKey := map[string]FleetDevice{}
	for _, d := range s.ListFleetDevices() {
		byKey[d.Kind+"/"+d.Name] = d
	}
	if len(byKey) != 2 {
		t.Fatalf("expected exactly 2 devices (worker/node-x + mobile/node-x), got %d: %+v", len(byKey), byKey)
	}
	w := byKey["worker/node-x"]
	if w.Endpoint != "ep2" || w.LastSeen != "2026-02-02T00:00:00Z" || !reflect.DeepEqual(w.Services, []string{"a", "b"}) {
		t.Fatalf("worker/node-x did not reflect the latest heartbeat: %+v", w)
	}
}

// TestFleetDedupeMigrationCollapsesDuplicates pins the M3 forward-only migration. A
// fleet_devices table that already holds duplicate (kind, name) rows — the shape the
// old racy SELECT-then-INSERT UpsertFleetHeartbeat could produce — must be collapsed
// to a single row per (kind, name), keeping the most-recently-seen one (greatest
// last_seen), and the UNIQUE(kind, name) index must be (re)built so no further
// duplicate can be inserted. The dedupe+index migration is also idempotent.
func TestFleetDedupeMigrationCollapsesDuplicates(t *testing.T) {
	s := openTestStore(t)

	countFleet := func() int {
		t.Helper()
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM fleet_devices`).Scan(&n); err != nil {
			t.Fatalf("count fleet_devices: %v", err)
		}
		return n
	}

	// Drop the unique index so the pre-migration duplicate shape can be seeded, then
	// insert three (worker, node-a) rows differing only in last_seen and endpoint. The
	// 03:00 row is the most-recently-seen and must be the sole survivor.
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS idx_fleet_devices_kind_name`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	dups := []struct{ lastSeen, endpoint string }{
		{"2026-01-02T01:00:00Z", "ep-old"},
		{"2026-01-02T03:00:00Z", "ep-newest"},
		{"2026-01-02T02:00:00Z", "ep-mid"},
	}
	for _, d := range dups {
		if _, err := s.db.Exec(`
			INSERT INTO fleet_devices(name, kind, endpoint, os, arch, status, services, last_seen, created_at, updated_at)
			VALUES('node-a', 'worker', ?, '', '', 'online', '[]', ?, datetime('now'), datetime('now'))
		`, d.endpoint, d.lastSeen); err != nil {
			t.Fatalf("seed duplicate: %v", err)
		}
	}
	if n := countFleet(); n != 3 {
		t.Fatalf("expected 3 seeded duplicates, got %d", n)
	}

	// Re-run the migration: it dedupes, then rebuilds the unique index.
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate (dedupe) error: %v", err)
	}

	devices := s.ListFleetDevices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 row after dedupe, got %d: %+v", len(devices), devices)
	}
	if devices[0].LastSeen != "2026-01-02T03:00:00Z" || devices[0].Endpoint != "ep-newest" {
		t.Fatalf("dedupe kept the wrong row: %+v (want the greatest last_seen, 'ep-newest')", devices[0])
	}

	// The unique index is back: a raw duplicate INSERT must now fail.
	if _, err := s.db.Exec(`
		INSERT INTO fleet_devices(name, kind, endpoint, os, arch, status, services, last_seen, created_at, updated_at)
		VALUES('node-a', 'worker', 'dup', '', '', 'online', '[]', '2026-01-02T09:00:00Z', datetime('now'), datetime('now'))
	`); err == nil {
		t.Fatal("expected a UNIQUE(kind, name) violation inserting a duplicate, got nil")
	}

	// Idempotent: re-running the migration removes nothing and keeps the index.
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate error: %v", err)
	}
	if n := countFleet(); n != 1 {
		t.Fatalf("second migrate must be a no-op, got %d rows", n)
	}
}

// TestSweepStaleFleetDevices pins the fleet device lifecycle: a device silent longer
// than offlineAfter is flipped online->offline (counted as offlined), a device already
// offline and silent longer than reapAfter is deleted (counted as reaped), a fresh
// device stays online, and a device offline but NOT yet past the reap window survives
// untouched. It also proves ListFleetDevices reflects the post-sweep state and that a
// second sweep is a no-op.
func TestSweepStaleFleetDevices(t *testing.T) {
	s := openTestStore(t)

	now := time.Now().UTC()
	rfc := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

	// fresh: online, last seen now -> must stay online.
	if _, err := s.UpsertFleetHeartbeat(FleetDevice{Name: "fresh", LastSeen: rfc(0)}); err != nil {
		t.Fatalf("seed fresh error: %v", err)
	}
	// stale-online: online but silent ~5 min -> must flip to offline (offlined).
	if _, err := s.UpsertFleetHeartbeat(FleetDevice{Name: "stale-online", LastSeen: rfc(-5 * time.Minute)}); err != nil {
		t.Fatalf("seed stale-online error: %v", err)
	}
	// recent-offline: already offline but silent only ~5 min -> survives (not past reap).
	if _, err := s.UpsertFleetDevice(FleetDevice{Name: "recent-offline", Status: "offline", LastSeen: rfc(-5 * time.Minute)}); err != nil {
		t.Fatalf("seed recent-offline error: %v", err)
	}
	// long-dead: offline and silent ~2 h -> must be reaped.
	if _, err := s.UpsertFleetDevice(FleetDevice{Name: "long-dead", Status: "offline", LastSeen: rfc(-2 * time.Hour)}); err != nil {
		t.Fatalf("seed long-dead error: %v", err)
	}

	offlined, reaped, err := s.SweepStaleFleetDevices(180*time.Second, time.Hour)
	if err != nil {
		t.Fatalf("SweepStaleFleetDevices error: %v", err)
	}
	if offlined != 1 {
		t.Fatalf("offlined = %d, want 1 (only stale-online flips)", offlined)
	}
	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1 (only long-dead is deleted)", reaped)
	}

	byName := map[string]FleetDevice{}
	for _, d := range s.ListFleetDevices() {
		byName[d.Name] = d
	}
	if len(byName) != 3 {
		t.Fatalf("expected 3 devices after sweep, got %d: %+v", len(byName), byName)
	}
	if d, ok := byName["long-dead"]; ok {
		t.Fatalf("long-dead should have been reaped, still present: %+v", d)
	}
	if d, ok := byName["fresh"]; !ok || d.Status != "online" {
		t.Fatalf("fresh should remain online, got %+v (present=%v)", d, ok)
	}
	if d, ok := byName["stale-online"]; !ok || d.Status != "offline" {
		t.Fatalf("stale-online should be flipped offline, got %+v (present=%v)", d, ok)
	}
	if d, ok := byName["recent-offline"]; !ok || d.Status != "offline" {
		t.Fatalf("recent-offline should survive as offline, got %+v (present=%v)", d, ok)
	}

	// Idempotent: nothing new to age out on a second sweep.
	offlined, reaped, err = s.SweepStaleFleetDevices(180*time.Second, time.Hour)
	if err != nil {
		t.Fatalf("second SweepStaleFleetDevices error: %v", err)
	}
	if offlined != 0 || reaped != 0 {
		t.Fatalf("second sweep should be a no-op, got offlined=%d reaped=%d", offlined, reaped)
	}
}

// TestEffectiveFleetStatus pins the read-path display helper GetFleetState uses so the
// Fleet view is accurate between background sweeps: a fresh online device stays online,
// an online device silent past the threshold shows offline, an unparseable/empty
// last_seen leaves the status unchanged, and a non-online status is returned verbatim.
func TestEffectiveFleetStatus(t *testing.T) {
	now := time.Now().UTC()
	const threshold = 180 * time.Second

	if got := EffectiveFleetStatus("online", now.Format(time.RFC3339), threshold); got != "online" {
		t.Fatalf("fresh online: got %q, want online", got)
	}
	if got := EffectiveFleetStatus("online", now.Add(-5*time.Minute).Format(time.RFC3339), threshold); got != "offline" {
		t.Fatalf("stale online: got %q, want offline", got)
	}
	if got := EffectiveFleetStatus("online", "not connected yet", threshold); got != "online" {
		t.Fatalf("unparseable last_seen: got %q, want unchanged online", got)
	}
	if got := EffectiveFleetStatus("online", "", threshold); got != "online" {
		t.Fatalf("empty last_seen: got %q, want unchanged online", got)
	}
	if got := EffectiveFleetStatus("offline", now.Format(time.RFC3339), threshold); got != "offline" {
		t.Fatalf("already offline: got %q, want offline", got)
	}
}

func TestDeploymentRoundTrip(t *testing.T) {
	s := openTestStore(t)

	if err := s.UpsertDeployment(Deployment{
		Slug:        "storj",
		ContainerID: "c1",
		Name:        "cashpilot-storj",
		Image:       "storjlabs/storagenode:1.0.0",
		Status:      "running",
		Runtime:     "existing-docker",
	}); err != nil {
		t.Fatalf("UpsertDeployment error: %v", err)
	}

	dep, ok, err := s.GetDeployment("storj")
	if err != nil {
		t.Fatalf("GetDeployment error: %v", err)
	}
	if !ok {
		t.Fatal("expected the deployment to exist")
	}
	if dep.Status != "running" || dep.ContainerID != "c1" {
		t.Fatalf("unexpected deployment: %+v", dep)
	}
	if dep.CreatedAt == "" || dep.UpdatedAt == "" {
		t.Fatalf("expected created/updated timestamps to be set: %+v", dep)
	}

	// Upsert conflict on the same slug updates status.
	if err := s.UpsertDeployment(Deployment{
		Slug:        "storj",
		ContainerID: "c1",
		Name:        "cashpilot-storj",
		Image:       "storjlabs/storagenode:1.0.0",
		Status:      "stopped",
		Runtime:     "existing-docker",
	}); err != nil {
		t.Fatalf("UpsertDeployment (update) error: %v", err)
	}
	dep, _, _ = s.GetDeployment("storj")
	if dep.Status != "stopped" {
		t.Fatalf("expected status 'stopped' after upsert, got %q", dep.Status)
	}

	if list := s.ListDeployments(); len(list) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(list))
	}

	// RecordEvent is fire-and-forget; just exercise it.
	s.RecordEvent("storj", "test-event", "detail")

	if err := s.DeleteDeployment("storj"); err != nil {
		t.Fatalf("DeleteDeployment error: %v", err)
	}
	if _, ok, _ := s.GetDeployment("storj"); ok {
		t.Fatal("expected the deployment to be gone after delete")
	}
	if list := s.ListDeployments(); len(list) != 0 {
		t.Fatalf("expected empty deployments, got %d", len(list))
	}
}

func TestEarningsLatest(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.SaveEarnings(EarningsRecord{Platform: "storj", Balance: 1.0, Currency: "USD", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("SaveEarnings error: %v", err)
	}
	if _, err := s.SaveEarnings(EarningsRecord{Platform: "storj", Balance: 2.5, Currency: "USD", CreatedAt: "2026-01-02T00:00:00Z"}); err != nil {
		t.Fatalf("SaveEarnings error: %v", err)
	}

	latest := s.ListLatestEarnings()
	if len(latest) != 1 {
		t.Fatalf("expected 1 latest record, got %d", len(latest))
	}
	if latest[0].Balance != 2.5 {
		t.Fatalf("expected the latest balance 2.5, got %v", latest[0].Balance)
	}
}

// TestListLatestEarningsTieBreakByID pins the deterministic intra-second
// tie-break for ListLatestEarnings. Two successful rows for one platform land in
// the SAME second, differing only in sub-second fraction and balance. Because
// created_at is RFC3339Nano (variable length: trailing zeros trimmed), a
// lexicographic MAX(created_at) mis-orders them — "…:00.9Z" sorts ABOVE
// "…:00.1Z" — so the row returned must be the last-written one (highest
// AUTOINCREMENT id), independent of that string ordering. This FAILS under the
// old MAX(created_at) subquery (which would return the .9 row, balance 5.0).
func TestListLatestEarningsTieBreakByID(t *testing.T) {
	s := openTestStore(t)

	now := time.Now().UTC()
	sec := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
	// Insert the ".9" row FIRST (lower id, balance 5.0), then the ".1" row (higher
	// id, balance 9.0). Under a string MAX(created_at) the ".9" row wins (5.0);
	// under MAX(id) the later-written ".1" row wins (9.0).
	firstWritten := sec.Add(900 * time.Millisecond).Format(time.RFC3339Nano)
	lastWritten := sec.Add(100 * time.Millisecond).Format(time.RFC3339Nano)
	if _, err := s.SaveEarnings(EarningsRecord{Platform: "alpha", Balance: 5.0, Currency: "USD", CreatedAt: firstWritten}); err != nil {
		t.Fatalf("SaveEarnings(first) error: %v", err)
	}
	if _, err := s.SaveEarnings(EarningsRecord{Platform: "alpha", Balance: 9.0, Currency: "USD", CreatedAt: lastWritten}); err != nil {
		t.Fatalf("SaveEarnings(second) error: %v", err)
	}

	latest := s.ListLatestEarnings()
	if len(latest) != 1 {
		t.Fatalf("expected exactly one latest row per platform, got %d: %+v", len(latest), latest)
	}
	if latest[0].Balance != 9.0 {
		t.Fatalf("intra-second tie-break did not pick the higher-id row: got balance=%v, want 9.0", latest[0].Balance)
	}
}

func TestListDailyBalances(t *testing.T) {
	s := openTestStore(t)

	// Build timestamps relative to "now" so the rows land inside the window that
	// ListDailyBalances computes with SQLite's date('now', '-N days'). The latest
	// row per (platform, day) is chosen by MAX(id); rows are inserted in ascending
	// balance order so the last-written (highest-id) row is the expected winner.
	ts := func(daysAgo, hour int) string {
		d := time.Now().UTC().AddDate(0, 0, -daysAgo)
		return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, time.UTC).Format(time.RFC3339)
	}
	day := func(daysAgo int) string {
		return time.Now().UTC().AddDate(0, 0, -daysAgo).Format("2006-01-02")
	}

	seed := []EarningsRecord{
		// alpha, busy day (2 days ago): three successful rows with increasing
		// timestamps -> the last-written (9.0) has the max created_at.
		{Platform: "alpha", Balance: 5.0, Currency: "USD", CreatedAt: ts(2, 10)},
		{Platform: "alpha", Balance: 7.0, Currency: "USD", CreatedAt: ts(2, 11)},
		{Platform: "alpha", Balance: 9.0, Currency: "USD", CreatedAt: ts(2, 12)},
		// alpha, busy day: a FAILED scrape with the newest timestamp of the day;
		// it must be ignored by both the inner MAX and the outer filter.
		{Platform: "alpha", Balance: 999.0, Currency: "USD", Error: "boom", CreatedAt: ts(2, 13)},
		// alpha, two earlier in-window days.
		{Platform: "alpha", Balance: 3.0, Currency: "USD", CreatedAt: ts(10, 9)},
		{Platform: "alpha", Balance: 2.0, Currency: "USD", CreatedAt: ts(20, 9)},
		// beta, a single in-window row (different currency).
		{Platform: "beta", Balance: 100.0, Currency: "EUR", CreatedAt: ts(5, 8)},
		// alpha, out of the 40-day window: must be excluded.
		{Platform: "alpha", Balance: 1.0, Currency: "USD", CreatedAt: ts(50, 8)},
	}
	for _, r := range seed {
		if _, err := s.SaveEarnings(r); err != nil {
			t.Fatalf("SaveEarnings(%+v) error: %v", r, err)
		}
	}

	got := s.ListDailyBalances(40)

	// Exactly one row per (platform, day) inside the window: alpha has 3 days and
	// beta has 1; the error row and the out-of-window row contribute nothing.
	if len(got) != 4 {
		t.Fatalf("expected 4 daily balances, got %d: %+v", len(got), got)
	}

	type key struct{ platform, day string }
	byKey := map[key]DailyBalance{}
	for _, b := range got {
		byKey[key{b.Platform, b.Day}] = b
		if b.Balance == 999.0 {
			t.Fatalf("the error row leaked into the results: %+v", b)
		}
		if b.Balance == 1.0 {
			t.Fatalf("the out-of-window row leaked into the results: %+v", b)
		}
	}
	// One entry per (platform, day) means no key collisions.
	if len(byKey) != len(got) {
		t.Fatalf("expected one row per (platform, day), got duplicates: %+v", got)
	}

	want := []DailyBalance{
		{Platform: "alpha", Currency: "USD", Day: day(20), Balance: 2.0},
		{Platform: "alpha", Currency: "USD", Day: day(10), Balance: 3.0},
		{Platform: "beta", Currency: "EUR", Day: day(5), Balance: 100.0},
		{Platform: "alpha", Currency: "USD", Day: day(2), Balance: 9.0}, // last-written wins on the busy day
	}
	for _, w := range want {
		g, ok := byKey[key{w.Platform, w.Day}]
		if !ok {
			t.Fatalf("missing daily balance for %s on %s; got %+v", w.Platform, w.Day, got)
		}
		if g.Balance != w.Balance || g.Currency != w.Currency {
			t.Fatalf("for %s on %s: got balance=%v currency=%q, want balance=%v currency=%q",
				w.Platform, w.Day, g.Balance, g.Currency, w.Balance, w.Currency)
		}
	}

	// Both platforms are represented.
	platforms := map[string]bool{}
	for _, b := range got {
		platforms[b.Platform] = true
	}
	if !platforms["alpha"] || !platforms["beta"] {
		t.Fatalf("expected both platforms present, got %v", platforms)
	}

	// Results are ordered ascending by day.
	for i := 1; i < len(got); i++ {
		if got[i-1].Day > got[i].Day {
			t.Fatalf("results not ordered ascending by day: %+v", got)
		}
	}
}

// TestListDailyBalancesTieBreakByID pins the deterministic intra-day tie-break.
// Two successful rows land in the SAME second of the same day, differing only in
// sub-second fraction and balance. Because created_at is RFC3339Nano (variable
// length: trailing zeros trimmed, the fraction omitted entirely at .0), a
// lexicographic MAX(created_at) mis-orders them — "…:00.9Z" sorts ABOVE "…:00.1Z"
// yet a plain "…:00Z" would sort above BOTH ('Z' > '.'). The row returned must be
// the last-written one (highest AUTOINCREMENT id), independent of that string
// ordering.
func TestListDailyBalancesTieBreakByID(t *testing.T) {
	s := openTestStore(t)

	now := time.Now().UTC()
	sec := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
	// Insert the ".9" row FIRST (lower id, balance 5.0), then the ".1" row (higher
	// id, balance 9.0). Under a string MAX(created_at) the ".9" row wins (5.0);
	// under MAX(id) the later-written ".1" row wins (9.0).
	firstWritten := sec.Add(900 * time.Millisecond).Format(time.RFC3339Nano)
	lastWritten := sec.Add(100 * time.Millisecond).Format(time.RFC3339Nano)
	if _, err := s.SaveEarnings(EarningsRecord{Platform: "alpha", Balance: 5.0, Currency: "USD", CreatedAt: firstWritten}); err != nil {
		t.Fatalf("SaveEarnings(first) error: %v", err)
	}
	if _, err := s.SaveEarnings(EarningsRecord{Platform: "alpha", Balance: 9.0, Currency: "USD", CreatedAt: lastWritten}); err != nil {
		t.Fatalf("SaveEarnings(second) error: %v", err)
	}

	got := s.ListDailyBalances(2)
	if len(got) != 1 {
		t.Fatalf("expected exactly one (platform, day) row, got %d: %+v", len(got), got)
	}
	if got[0].Balance != 9.0 {
		t.Fatalf("intra-day tie-break did not pick the higher-id row: got balance=%v, want 9.0", got[0].Balance)
	}
}

// TestPurgeOldData pins the retention purge: with a 400-day window it deletes
// earnings and runtime_events rows older than the cutoff, KEEPS the most-recent
// earnings row per platform even when that row is itself older than the cutoff
// (so a long-stale service still shows its last balance), KEEPS rows newer than
// the cutoff (even non-latest ones), returns the total number of rows deleted, is
// idempotent, and does nothing when retention is disabled (retentionDays <= 0).
func TestPurgeOldData(t *testing.T) {
	s := openTestStore(t)

	// Timestamps relative to now so the test is stable regardless of the calendar
	// date it runs on; all seeds are days apart, well clear of any intra-second
	// RFC3339Nano ordering subtlety.
	ts := func(daysAgo int) string {
		return time.Now().UTC().AddDate(0, 0, -daysAgo).Format(time.RFC3339Nano)
	}

	// "grows" is an active platform; its latest row (ts(5), highest id) is recent.
	// "stale" has not reported in a long time: its latest row (ts(500), highest id)
	// is OLDER than the 400-day cutoff and must survive via the keep-latest clause.
	// Insertion order fixes the AUTOINCREMENT ids, so the last insert per platform
	// is that platform's latest (MAX(id)).
	seed := []EarningsRecord{
		{Platform: "grows", Balance: 1.0, Currency: "USD", CreatedAt: ts(500)},  // old, not latest -> DELETE
		{Platform: "grows", Balance: 2.0, Currency: "USD", CreatedAt: ts(450)},  // old, not latest -> DELETE
		{Platform: "grows", Balance: 2.5, Currency: "USD", CreatedAt: ts(20)},   // newer than cutoff, not latest -> KEEP
		{Platform: "grows", Balance: 3.0, Currency: "USD", CreatedAt: ts(5)},    // newest -> latest -> KEEP
		{Platform: "stale", Balance: 10.0, Currency: "USD", CreatedAt: ts(600)}, // old, not latest -> DELETE
		{Platform: "stale", Balance: 20.0, Currency: "USD", CreatedAt: ts(500)}, // old, but latest -> KEEP
	}
	for _, r := range seed {
		if _, err := s.SaveEarnings(r); err != nil {
			t.Fatalf("SaveEarnings(%+v) error: %v", r, err)
		}
	}

	// runtime_events: one clearly-old row inserted directly (RecordEvent always
	// stamps datetime('now'), so an old event can't be produced through it), plus a
	// fresh RecordEvent row that must survive.
	if _, err := s.db.Exec(
		`INSERT INTO runtime_events(slug, event, detail, created_at) VALUES(?, ?, ?, ?)`,
		"grows", "old-event", "", "2020-01-01 00:00:00",
	); err != nil {
		t.Fatalf("insert old runtime_event error: %v", err)
	}
	s.RecordEvent("grows", "recent-event", "detail")

	countRows := func(table string) int {
		t.Helper()
		var n int
		// table is a test-local literal, never external input.
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s error: %v", table, err)
		}
		return n
	}

	// Preconditions: everything seeded is present.
	if got := countRows("earnings"); got != 6 {
		t.Fatalf("expected 6 seeded earnings rows, got %d", got)
	}
	if got := countRows("runtime_events"); got != 2 {
		t.Fatalf("expected 2 seeded runtime_events rows, got %d", got)
	}

	// Retention disabled: a non-positive window deletes nothing and returns 0.
	for _, disabled := range []int{0, -1} {
		n, err := s.PurgeOldData(disabled)
		if err != nil {
			t.Fatalf("PurgeOldData(%d) error: %v", disabled, err)
		}
		if n != 0 {
			t.Fatalf("PurgeOldData(%d) = %d, want 0 (retention disabled)", disabled, n)
		}
	}
	if countRows("earnings") != 6 || countRows("runtime_events") != 2 {
		t.Fatalf("a disabled purge must not delete anything: earnings=%d events=%d",
			countRows("earnings"), countRows("runtime_events"))
	}

	// Real purge: 3 old earnings (grows ts500, grows ts450, stale ts600) + 1 old
	// event = 4 rows deleted.
	deleted, err := s.PurgeOldData(400)
	if err != nil {
		t.Fatalf("PurgeOldData(400) error: %v", err)
	}
	if deleted != 4 {
		t.Fatalf("PurgeOldData(400) = %d, want 4 (3 earnings + 1 event)", deleted)
	}

	// 3 earnings survive: grows ts20 (2.5), grows ts5 (3.0), stale ts500 (20.0).
	if got := countRows("earnings"); got != 3 {
		t.Fatalf("expected 3 surviving earnings rows, got %d", got)
	}
	if got := countRows("runtime_events"); got != 1 {
		t.Fatalf("expected 1 surviving runtime_event, got %d", got)
	}

	// The exact surviving balances prove: a row newer than the cutoff survives even
	// when it is NOT the latest (grows 2.5), and the latest row survives even though
	// it is older than the cutoff (stale 20.0).
	survived := map[float64]bool{}
	rows, err := s.db.Query(`SELECT balance FROM earnings`)
	if err != nil {
		t.Fatalf("query surviving balances error: %v", err)
	}
	for rows.Next() {
		var b float64
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			t.Fatalf("scan balance error: %v", err)
		}
		survived[b] = true
	}
	rows.Close()
	for _, want := range []float64{2.5, 3.0, 20.0} {
		if !survived[want] {
			t.Fatalf("expected balance %v to survive the purge, survivors=%v", want, survived)
		}
	}
	for _, gone := range []float64{1.0, 2.0, 10.0} {
		if survived[gone] {
			t.Fatalf("expected balance %v to be purged, survivors=%v", gone, survived)
		}
	}

	// The surviving runtime_event is the recent one, not the 2020 row.
	var lastEvent string
	if err := s.db.QueryRow(`SELECT event FROM runtime_events`).Scan(&lastEvent); err != nil {
		t.Fatalf("query surviving event error: %v", err)
	}
	if lastEvent != "recent-event" {
		t.Fatalf("expected the recent runtime_event to survive, got %q", lastEvent)
	}

	// Both platforms still have a latest balance for the dashboard: grows -> 3.0
	// (recent), stale -> 20.0 (old but preserved).
	latest := map[string]float64{}
	for _, rec := range s.ListLatestEarnings() {
		latest[rec.Platform] = rec.Balance
	}
	if latest["grows"] != 3.0 {
		t.Fatalf("grows latest balance = %v, want 3.0", latest["grows"])
	}
	if latest["stale"] != 20.0 {
		t.Fatalf("stale latest balance = %v, want 20.0 (old latest row preserved)", latest["stale"])
	}

	// Idempotent: a second purge finds nothing new to delete (the only rows still
	// older than the cutoff are the per-platform latest rows, which are protected).
	deleted, err = s.PurgeOldData(400)
	if err != nil {
		t.Fatalf("second PurgeOldData(400) error: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("second PurgeOldData(400) = %d, want 0 (idempotent)", deleted)
	}
}

// TestPurgeOldDataEventsUseDatetimeFormat pins the L2 fix: runtime_events.created_at
// is stored as datetime('now') ("YYYY-MM-DD HH:MM:SS"), NOT the RFC3339Nano earnings
// uses, so the events purge must compare it with SQLite datetime() semantics. A row
// at the very end (23:59:59) of the cutoff DAY is inside the retention window and must
// survive; the old RFC3339Nano string cutoff ("…T…Z") sorted every same-day row below
// it (a space sorts under 'T') and purged it up to a day early. An ancient row is
// always purged, proving the events purge still works.
func TestPurgeOldDataEventsUseDatetimeFormat(t *testing.T) {
	s := openTestStore(t)

	// Computed via SQLite's own clock so it shares the purge's notion of "now":
	// 23:59:59 on the day the 30-day cutoff falls on. 23:59:59 is the latest instant
	// of that day, so it is always >= the cutoff's wall-clock time -> retained.
	if _, err := s.db.Exec(`INSERT INTO runtime_events(slug, event, detail, created_at)
		VALUES('svc', 'edge-in-window', '', strftime('%Y-%m-%d 23:59:59', 'now', '-30 days'))`); err != nil {
		t.Fatalf("insert edge event: %v", err)
	}
	// A genuinely ancient event that must always be purged.
	if _, err := s.db.Exec(`INSERT INTO runtime_events(slug, event, detail, created_at)
		VALUES('svc', 'ancient', '', '2000-01-01 00:00:00')`); err != nil {
		t.Fatalf("insert ancient event: %v", err)
	}

	if _, err := s.PurgeOldData(30); err != nil {
		t.Fatalf("PurgeOldData(30): %v", err)
	}

	var survivors []string
	rows, err := s.db.Query(`SELECT event FROM runtime_events ORDER BY event`)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		survivors = append(survivors, e)
	}
	rows.Close()

	// The same-day in-window edge row survives (the fix); the ancient row is purged.
	if len(survivors) != 1 || survivors[0] != "edge-in-window" {
		t.Fatalf("events after purge = %v, want [edge-in-window] (same-day in-window row kept, ancient purged)", survivors)
	}
}

// TestHealthScores pins the health-score + uptime% aggregation over runtime_events.
// runtime_events.created_at is written like datetime('now') ("YYYY-MM-DD HH:MM:SS",
// UTC), so events are inserted directly with explicit timestamps in that format
// (RecordEvent only ever stamps "now", and it differs from the RFC3339Nano earnings
// uses). It checks: uptime% from health_up/health_down counts; the restart/crash/
// stop penalties (a crash counted from BOTH a *_error event and missing_from_runtime);
// the 40/60 event-score/uptime blend; a service with events but ZERO uptime samples
// scoring its event-score (not 0); out-of-window events excluded; and a service with
// no in-window events being absent from the map.
func TestHealthScores(t *testing.T) {
	s := openTestStore(t)

	// Build timestamps in the datetime('now') format the window comparison uses.
	// Inside-window events sit at 1-3 days ago; out-of-window ones at 30-60 days,
	// both comfortably clear of the 7-day cutoff so the test never flakes at the
	// boundary.
	ts := func(daysAgo int) string {
		return time.Now().UTC().AddDate(0, 0, -daysAgo).Format("2006-01-02 15:04:05")
	}
	ev := func(slug, event string, daysAgo int) {
		t.Helper()
		if _, err := s.db.Exec(
			`INSERT INTO runtime_events(slug, event, detail, created_at) VALUES(?, ?, '', ?)`,
			slug, event, ts(daysAgo),
		); err != nil {
			t.Fatalf("insert runtime_event(%s, %s) error: %v", slug, event, err)
		}
	}

	// "blend": one of every penalty (a crash from BOTH a *_error event and
	// missing_from_runtime) plus uptime samples, all inside the 7-day window.
	//   eventScore = 100 - 5*1(restart) - 20*2(crash) - 2*1(stop) = 53
	//   uptime%    = 3 up / 4 samples * 100 = 75.0
	//   score      = round(0.4*53 + 0.6*75) = round(66.2) = 66
	ev("blend", "restarted", 1)
	ev("blend", "start_error", 1)
	ev("blend", "missing_from_runtime", 2)
	ev("blend", "stopped", 2)
	ev("blend", "health_up", 1)
	ev("blend", "health_up", 1)
	ev("blend", "health_up", 2)
	ev("blend", "health_down", 2)

	// "windowed": only the in-window rows count; the 30-days-ago rows are excluded.
	//   in-window: 1 restart, 2 up -> eventScore=95, uptime=100, score=round(98)=98
	ev("windowed", "restarted", 1)
	ev("windowed", "health_up", 2)
	ev("windowed", "health_up", 3)
	ev("windowed", "restarted", 30)   // excluded
	ev("windowed", "restarted", 30)   // excluded
	ev("windowed", "health_down", 30) // excluded

	// "nosample": lifecycle events but NO uptime samples yet. The score must be the
	// event-score (93), NOT blended down to 0 for lack of sampling data.
	//   eventScore = 100 - 5*1 - 2*1 = 93
	ev("nosample", "restarted", 1)
	ev("nosample", "stopped", 3)

	// "perfect": only uptime, all up, no penalties -> 100 with 100% uptime.
	ev("perfect", "health_up", 1)
	ev("perfect", "health_up", 1)
	ev("perfect", "health_up", 2)
	ev("perfect", "health_up", 3)

	// "expired": ONLY out-of-window events -> must be absent from the result.
	ev("expired", "restarted", 30)
	ev("expired", "health_up", 60)

	got := s.HealthScores(7)

	// Only slugs with at least one in-window event appear: blend, windowed,
	// nosample, perfect. "expired" (out-of-window only) and "absent" (never seen)
	// must not appear.
	if len(got) != 4 {
		t.Fatalf("expected 4 scored slugs, got %d: %+v", len(got), got)
	}
	if _, ok := got["expired"]; ok {
		t.Fatalf("a slug with only out-of-window events must be absent: %+v", got["expired"])
	}
	if _, ok := got["absent"]; ok {
		t.Fatal("a slug with no events must be absent from the map")
	}

	blend, ok := got["blend"]
	if !ok {
		t.Fatal("expected 'blend' to be scored")
	}
	if blend.Restarts != 1 || blend.Crashes != 2 || blend.Stops != 1 {
		t.Fatalf("blend penalties: got restarts=%d crashes=%d stops=%d, want 1/2/1",
			blend.Restarts, blend.Crashes, blend.Stops)
	}
	if blend.Samples != 4 {
		t.Fatalf("blend samples: got %d, want 4", blend.Samples)
	}
	if blend.UptimePercent != 75.0 {
		t.Fatalf("blend uptime%%: got %v, want 75.0", blend.UptimePercent)
	}
	if blend.Score != 66 {
		t.Fatalf("blend score: got %d, want 66 (round(0.4*53 + 0.6*75))", blend.Score)
	}

	// windowed proves the out-of-window rows are excluded: counting them would give
	// restarts=3 and samples=3, a very different score.
	win := got["windowed"]
	if win.Restarts != 1 || win.Samples != 2 || win.UptimePercent != 100.0 {
		t.Fatalf("windowed excluded-window mismatch: got restarts=%d samples=%d uptime=%v, want 1/2/100.0",
			win.Restarts, win.Samples, win.UptimePercent)
	}
	if win.Score != 98 {
		t.Fatalf("windowed score: got %d, want 98 (round(0.4*95 + 0.6*100))", win.Score)
	}

	// nosample: zero uptime samples -> the event-score is used verbatim (not 0).
	ns := got["nosample"]
	if ns.Samples != 0 {
		t.Fatalf("nosample samples: got %d, want 0", ns.Samples)
	}
	if ns.UptimePercent != 0 {
		t.Fatalf("nosample uptime%%: got %v, want 0", ns.UptimePercent)
	}
	if ns.Score != 93 {
		t.Fatalf("nosample score: got %d, want 93 (event-score, NOT blended to 0)", ns.Score)
	}
	if ns.Restarts != 1 || ns.Stops != 1 || ns.Crashes != 0 {
		t.Fatalf("nosample penalties: got restarts=%d stops=%d crashes=%d, want 1/1/0",
			ns.Restarts, ns.Stops, ns.Crashes)
	}

	// perfect: all up, no penalties -> 100 with 100% uptime.
	pf := got["perfect"]
	if pf.Score != 100 || pf.UptimePercent != 100.0 || pf.Samples != 4 {
		t.Fatalf("perfect: got score=%d uptime=%v samples=%d, want 100/100.0/4",
			pf.Score, pf.UptimePercent, pf.Samples)
	}
}

func TestHashFleetKeyStable(t *testing.T) {
	a := HashFleetKey("abc")
	if a != HashFleetKey("abc") {
		t.Fatal("HashFleetKey must be deterministic")
	}
	if len(a) != 64 { // sha256 hex
		t.Fatalf("want 64-char hex digest, got %d", len(a))
	}
	if a == HashFleetKey("different") {
		t.Fatal("different inputs must hash differently")
	}
}

func TestFleetDeviceKeyLifecycle(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.UpsertFleetHeartbeat(FleetDevice{Name: "phone", Kind: "android", Status: "online"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Unenrolled: no key, unconfirmed.
	hash, confirmed, err := s.FleetDeviceKeyState("android", "phone")
	if err != nil || hash != "" || confirmed {
		t.Fatalf("unenrolled: want \"\"/false, got %q/%v (err %v)", hash, confirmed, err)
	}

	// Set a key -> stored, unconfirmed.
	h1 := HashFleetKey("k1")
	if err := s.SetFleetDeviceKey("android", "phone", h1); err != nil {
		t.Fatalf("set: %v", err)
	}
	if hash, confirmed, _ = s.FleetDeviceKeyState("android", "phone"); hash != h1 || confirmed {
		t.Fatalf("after set: want %q/false, got %q/%v", h1, hash, confirmed)
	}

	// Confirm.
	if err := s.ConfirmFleetDeviceKey("android", "phone"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, confirmed, _ = s.FleetDeviceKeyState("android", "phone"); !confirmed {
		t.Fatal("after confirm: want confirmed=true")
	}

	// Re-setting a key rotates it and resets confirmed.
	h2 := HashFleetKey("k2")
	if err := s.SetFleetDeviceKey("android", "phone", h2); err != nil {
		t.Fatalf("re-set: %v", err)
	}
	if hash, confirmed, _ = s.FleetDeviceKeyState("android", "phone"); hash != h2 || confirmed {
		t.Fatalf("after re-set: want %q/false, got %q/%v", h2, hash, confirmed)
	}

	// Missing device -> empty state, no error.
	if hash, _, err = s.FleetDeviceKeyState("android", "ghost"); err != nil || hash != "" {
		t.Fatalf("missing device: want \"\"/nil, got %q (%v)", hash, err)
	}
}

func TestSetFleetDeviceKeyMissingRowErrors(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetFleetDeviceKey("android", "ghost", HashFleetKey("k")); err == nil {
		t.Fatal("SetFleetDeviceKey must error when the device row does not exist")
	}
	if err := s.ConfirmFleetDeviceKey("android", "ghost"); err == nil {
		t.Fatal("ConfirmFleetDeviceKey must error when the device row does not exist")
	}
}
