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
		{Platform: "grows", Balance: 1.0, Currency: "USD", CreatedAt: ts(500)}, // old, not latest -> DELETE
		{Platform: "grows", Balance: 2.0, Currency: "USD", CreatedAt: ts(450)}, // old, not latest -> DELETE
		{Platform: "grows", Balance: 2.5, Currency: "USD", CreatedAt: ts(20)},  // newer than cutoff, not latest -> KEEP
		{Platform: "grows", Balance: 3.0, Currency: "USD", CreatedAt: ts(5)},   // newest -> latest -> KEEP
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
