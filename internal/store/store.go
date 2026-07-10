package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	aead cipher.AEAD
}

type Deployment struct {
	Slug        string  `json:"slug"`
	ContainerID string  `json:"containerId"`
	Name        string  `json:"name"`
	Image       string  `json:"image"`
	Status      string  `json:"status"`
	Runtime     string  `json:"runtime"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryMB    float64 `json:"memoryMb"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
}

type EarningsRecord struct {
	Platform  string  `json:"platform"`
	Balance   float64 `json:"balance"`
	Currency  string  `json:"currency"`
	Error     string  `json:"error,omitempty"`
	CreatedAt string  `json:"createdAt"`
}

type DailyBalance struct {
	Platform string
	Currency string
	Day      string // "YYYY-MM-DD" (UTC)
	Balance  float64
}

// HealthScore is one service's rolling health over the query window: a 0-100
// Score, the uptime percentage it was blended from, and the raw event counts the
// score penalises. Samples is the number of health_up/health_down datapoints in
// the window; a service with lifecycle events but no samples yet (uptime sampling
// has not run) reports Samples==0 and a Score equal to its event-score, so it is
// not dragged to 0 merely for lacking sampling data.
type HealthScore struct {
	Score         int     `json:"score"`
	UptimePercent float64 `json:"uptimePercent"`
	Samples       int     `json:"samples"`
	Restarts      int     `json:"restarts"`
	Crashes       int     `json:"crashes"`
	Stops         int     `json:"stops"`
}

type FleetDevice struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Endpoint  string   `json:"endpoint"`
	OS        string   `json:"os"`
	Arch      string   `json:"arch"`
	Status    string   `json:"status"`
	Services  []string `json:"services"`
	LastSeen  string   `json:"lastSeen"`
	CreatedAt string   `json:"createdAt"`
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "cashpilot-desktop.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	key, err := config.MasterKey(filepath.Dir(dataDir))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	s := &Store{db: db, aead: aead}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) SaveCredentials(slug string, values map[string]string) error {
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	encrypted, err := s.encrypt(raw)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO credentials(slug, value, updated_at)
		VALUES(?, ?, datetime('now'))
		ON CONFLICT(slug) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
	`, slug, encrypted)
	return err
}

func (s *Store) GetCredentials(slug string) (map[string]string, error) {
	var encrypted string
	err := s.db.QueryRow(`SELECT value FROM credentials WHERE slug = ?`, slug).Scan(&encrypted)
	if err == sql.ErrNoRows {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	raw, err := s.decrypt(encrypted)
	if err != nil {
		return nil, err
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	return values, nil
}

// ListCredentialSlugs returns the slug of every service that has saved
// credentials, ordered by slug. collectAll unions this with the deployment set
// so imageless supported services (which never create a deployment row) still
// participate in the scheduled collection cycle.
func (s *Store) ListCredentialSlugs() []string {
	rows, err := s.db.Query(`SELECT slug FROM credentials ORDER BY slug`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err == nil {
			out = append(out, slug)
		}
	}
	// A mid-iteration rows error means the read was truncated. Fail closed (return
	// nil, as the query-error path does) rather than a partial slice that looks
	// complete: this list feeds collectAll, so a silent truncation would skip
	// services. Mirrors HealthScores' rows.Err() handling.
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

// SaveServiceDetail upserts a per-service JSON detail blob keyed by slug. It is a
// generic sidecar to the flat earnings row: any collector can stash a JSON document
// (e.g. the MystNodes per-node earnings breakdown) alongside its balance without
// widening the earnings schema. The blob is stored verbatim — the collector owns its
// shape and the frontend parses it — so this method never inspects detailJSON.
func (s *Store) SaveServiceDetail(slug, detailJSON string) error {
	_, err := s.db.Exec(`
		INSERT INTO service_details(slug, detail, updated_at)
		VALUES(?, ?, datetime('now'))
		ON CONFLICT(slug) DO UPDATE SET detail=excluded.detail, updated_at=excluded.updated_at
	`, slug, detailJSON)
	return err
}

// GetServiceDetail returns the stored JSON detail blob for a slug, or "" (and no
// error) when the slug has no detail row yet — an absent detail is a normal state,
// not a failure, mirroring GetCredentials' empty-map-on-ErrNoRows contract.
func (s *Store) GetServiceDetail(slug string) (string, error) {
	var detail string
	err := s.db.QueryRow(`SELECT detail FROM service_details WHERE slug = ?`, slug).Scan(&detail)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return detail, nil
}

// ListServiceDetails returns every stored per-service detail blob keyed by slug, so
// GetAppState can hand the frontend all detail documents in one read. A query or scan
// error yields an empty (non-nil) map rather than failing, matching the best-effort
// intent of the other list methods; callers can always range over the result safely.
func (s *Store) ListServiceDetails() map[string]string {
	out := make(map[string]string)
	rows, err := s.db.Query(`SELECT slug, detail FROM service_details ORDER BY slug`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var slug, detail string
		if err := rows.Scan(&slug, &detail); err == nil {
			out[slug] = detail
		}
	}
	// A truncated read must not masquerade as a complete detail set; on a rows error
	// return the (best-effort) map gathered so far, matching HealthScores.
	if err := rows.Err(); err != nil {
		return out
	}
	return out
}

func (s *Store) UpsertDeployment(dep Deployment) error {
	now := time.Now().UTC()
	if dep.CreatedAt == "" {
		dep.CreatedAt = now.Format(time.RFC3339Nano)
	}
	dep.UpdatedAt = now.Format(time.RFC3339Nano)
	_, err := s.db.Exec(`
		INSERT INTO deployments(slug, container_id, name, image, status, runtime, cpu_percent, memory_mb, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
			container_id=excluded.container_id,
			name=excluded.name,
			image=excluded.image,
			status=excluded.status,
			runtime=excluded.runtime,
			cpu_percent=excluded.cpu_percent,
			memory_mb=excluded.memory_mb,
			updated_at=excluded.updated_at
	`, dep.Slug, dep.ContainerID, dep.Name, dep.Image, dep.Status, dep.Runtime, dep.CPUPercent, dep.MemoryMB, dep.CreatedAt, dep.UpdatedAt)
	return err
}

func (s *Store) DeleteDeployment(slug string) error {
	_, err := s.db.Exec(`DELETE FROM deployments WHERE slug = ?`, slug)
	return err
}

func (s *Store) GetDeployment(slug string) (Deployment, bool, error) {
	row := s.db.QueryRow(`
		SELECT slug, container_id, name, image, status, runtime, cpu_percent, memory_mb, created_at, updated_at
		FROM deployments WHERE slug = ?
	`, slug)
	dep, err := scanDeployment(row)
	if err == sql.ErrNoRows {
		return Deployment{}, false, nil
	}
	if err != nil {
		return Deployment{}, false, err
	}
	return dep, true, nil
}

func (s *Store) ListDeployments() []Deployment {
	rows, err := s.db.Query(`
		SELECT slug, container_id, name, image, status, runtime, cpu_percent, memory_mb, created_at, updated_at
		FROM deployments ORDER BY slug
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		dep, err := scanDeployment(rows)
		if err == nil {
			out = append(out, dep)
		}
	}
	// Fail closed on a truncated read rather than returning a partial list. Mirrors
	// HealthScores' rows.Err() handling.
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

func (s *Store) RecordEvent(slug, event, detail string) {
	if _, err := s.db.Exec(`INSERT INTO runtime_events(slug, event, detail, created_at) VALUES(?, ?, ?, datetime('now'))`, slug, event, detail); err != nil {
		log.Printf("store: record event %s/%s: %v", slug, event, err)
	}
}

func (s *Store) SaveEarnings(record EarningsRecord) (EarningsRecord, error) {
	if record.CreatedAt == "" {
		record.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		INSERT INTO earnings(platform, balance, currency, error, created_at)
		VALUES(?, ?, ?, ?, ?)
	`, record.Platform, record.Balance, record.Currency, record.Error, record.CreatedAt)
	return record, err
}

// ListLatestEarnings returns the most recently written row per platform,
// INCLUDING error rows (the dashboard breakdown and notifications rely on the
// latest row even when it failed). The latest row per platform is chosen by
// MAX(id), not MAX(created_at): created_at is RFC3339Nano whose variable-length
// fraction makes a lexicographic MAX(created_at) mis-order same-second rows
// (e.g. "…:00Z" sorts ABOVE "…:00.5Z" because 'Z' > '.'), so the monotonic
// AUTOINCREMENT id is the deterministic "last written wins" key — matching the
// shape ListDailyBalances already uses.
func (s *Store) ListLatestEarnings() []EarningsRecord {
	rows, err := s.db.Query(`
		SELECT e.platform, e.balance, e.currency, e.error, e.created_at
		FROM earnings e
		INNER JOIN (
			SELECT platform, MAX(id) AS mx FROM earnings GROUP BY platform
		) latest ON e.id = latest.mx
		ORDER BY e.platform
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []EarningsRecord
	for rows.Next() {
		var record EarningsRecord
		if err := rows.Scan(&record.Platform, &record.Balance, &record.Currency, &record.Error, &record.CreatedAt); err == nil {
			out = append(out, record)
		}
	}
	// Fail closed on a truncated read rather than returning a partial list that would
	// show wrong dashboard totals. Mirrors HealthScores' rows.Err() handling.
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

// ListDailyBalances returns the latest successful balance (rows whose error column
// is empty) for each (platform, day) over the last daysBack days. The latest row
// per (platform, day) is chosen by MAX(id), not MAX(created_at): created_at is
// stored as RFC3339Nano whose variable-length fraction makes a lexicographic
// MAX(created_at) mis-order same-second rows (e.g. "…:00Z" sorts AFTER "…:00.5Z"
// because 'Z' > '.'), so the monotonic AUTOINCREMENT id is the deterministic
// "last written wins" key.
func (s *Store) ListDailyBalances(daysBack int) []DailyBalance {
	rows, err := s.db.Query(`
		SELECT e.platform, e.currency, date(e.created_at) AS day, e.balance
		FROM earnings e
		JOIN (
			SELECT platform, date(created_at) AS day, MAX(id) AS mx
			FROM earnings
			WHERE error = '' AND date(created_at) >= date('now', ?)
			GROUP BY platform, date(created_at)
		) latest
			ON e.id = latest.mx
		WHERE e.error = ''
		ORDER BY day, e.platform
	`, fmt.Sprintf("-%d days", daysBack))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []DailyBalance
	for rows.Next() {
		var record DailyBalance
		if err := rows.Scan(&record.Platform, &record.Currency, &record.Day, &record.Balance); err == nil {
			out = append(out, record)
		}
	}
	// Fail closed on a truncated read rather than returning a partial series. Mirrors
	// HealthScores' rows.Err() handling.
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

// HealthScores computes a 0-100 health score per service from the runtime_events
// written over the last `days` days, keyed by slug. It adapts the production
// CashPilot health formula to the Desktop's event vocabulary:
//
//   - event-score: start at 100, then -5 per restart (a "restarted" event), -20
//     per crash (any "*_error" or "missing_from_runtime"), -2 per stop (a
//     "stopped" event); floored at 0 (and capped at 100, a no-op while subtracting).
//   - uptime%: health_up / (health_up + health_down) * 100 over the window, from
//     the per-cycle up/down sampling. Zero samples -> 0.
//   - final score: once there is at least one uptime sample, blend 40% event-score
//     with 60% uptime% (full-precision uptime, as the original does); with NO
//     samples yet the score is the event-score alone, so a service is not punished
//     to 0 merely because sampling has not produced data.
//
// runtime_events.created_at is written as datetime('now') ("YYYY-MM-DD HH:MM:SS",
// UTC), so the window is compared against datetime('now', '-N days') in that same
// format — NOT the RFC3339Nano earnings uses. Only slugs with at least one event
// in the window appear in the returned map.
func (s *Store) HealthScores(days int) map[string]HealthScore {
	rows, err := s.db.Query(`
		SELECT
			slug,
			SUM(CASE WHEN event = 'restarted' THEN 1 ELSE 0 END) AS restarts,
			SUM(CASE WHEN event LIKE '%\_error' ESCAPE '\' OR event = 'missing_from_runtime' THEN 1 ELSE 0 END) AS crashes,
			SUM(CASE WHEN event = 'stopped' THEN 1 ELSE 0 END) AS stops,
			SUM(CASE WHEN event = 'health_up' THEN 1 ELSE 0 END) AS ups,
			SUM(CASE WHEN event = 'health_down' THEN 1 ELSE 0 END) AS downs
		FROM runtime_events
		WHERE created_at >= datetime('now', ?)
		GROUP BY slug
	`, fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make(map[string]HealthScore)
	for rows.Next() {
		var (
			slug                     string
			restarts, crashes, stops int
			ups, downs               int
		)
		if err := rows.Scan(&slug, &restarts, &crashes, &stops, &ups, &downs); err != nil {
			continue
		}
		samples := ups + downs

		// event-score: 100 minus the weighted penalties, floored at 0.
		hs := HealthScore{
			Score:    clampScore(100 - 5*restarts - 20*crashes - 2*stops),
			Samples:  samples,
			Restarts: restarts,
			Crashes:  crashes,
			Stops:    stops,
		}
		if samples > 0 {
			uptime := float64(ups) / float64(samples) * 100
			hs.UptimePercent = round1(uptime)
			// Blend only once uptime data exists: 40% event-score, 60% uptime%
			// (full-precision uptime, mirroring the original).
			hs.Score = clampScore(int(math.Round(0.4*float64(hs.Score) + 0.6*uptime)))
		}
		out[slug] = hs
	}
	if err := rows.Err(); err != nil {
		// A mid-iteration error means the result may be partial. Health is
		// informational, so return what scored cleanly rather than failing the
		// whole read (matches the best-effort intent of the other list methods).
		return out
	}
	return out
}

// clampScore bounds a health score to the 0-100 range.
func clampScore(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// round1 rounds a percentage to one decimal place for display. The blended score
// deliberately uses the full-precision percentage, not this rounded value.
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// PurgeOldData deletes earnings and runtime_events rows older than the cutoff,
// but NEVER the most-recent earnings row per platform (so ListLatestEarnings and
// the dashboard breakdown keep working for a service that hasn't updated in a
// long time). Returns the number of rows deleted. A retentionDays <= 0 is a no-op
// (retention disabled).
func (s *Store) PurgeOldData(retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	// created_at is stored as RFC3339Nano (see SaveEarnings); format the cutoff the
	// same way so the string comparison below is apples-to-apples.
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339Nano)

	// Keep the most-recent row per platform regardless of age: a service that has
	// not reported in longer than the retention window must still contribute its
	// last-known balance to ListLatestEarnings and the dashboard breakdown.
	earningsRes, err := s.db.Exec(`
		DELETE FROM earnings
		WHERE created_at < ? AND id NOT IN (SELECT MAX(id) FROM earnings GROUP BY platform)
	`, cutoff)
	if err != nil {
		return 0, err
	}
	deleted, _ := earningsRes.RowsAffected()

	// runtime_events.created_at is written as datetime('now') ("YYYY-MM-DD HH:MM:SS",
	// UTC) — NOT the RFC3339Nano the earnings cutoff above uses — so its cutoff must be
	// computed with SQLite datetime() in that same format (mirroring HealthScores).
	// Comparing it against the RFC3339Nano `cutoff` mis-sorts same-day rows (a space
	// sorts below the 'T' separator), purging up to ~a day of in-window events early.
	eventsRes, err := s.db.Exec(
		`DELETE FROM runtime_events WHERE created_at < datetime('now', ?)`,
		fmt.Sprintf("-%d days", retentionDays),
	)
	if err != nil {
		return deleted, err
	}
	events, _ := eventsRes.RowsAffected()
	return deleted + events, nil
}

func (s *Store) UpsertFleetDevice(device FleetDevice) (FleetDevice, error) {
	if device.Kind == "" {
		device.Kind = "worker"
	}
	if device.Status == "" {
		device.Status = "offline"
	}
	servicesRaw, err := json.Marshal(device.Services)
	if err != nil {
		return FleetDevice{}, err
	}
	if device.ID > 0 {
		_, err = s.db.Exec(`
			UPDATE fleet_devices
			SET name = ?, kind = ?, endpoint = ?, os = ?, arch = ?, status = ?, services = ?, last_seen = ?, updated_at = datetime('now')
			WHERE id = ?
		`, device.Name, device.Kind, device.Endpoint, device.OS, device.Arch, device.Status, string(servicesRaw), device.LastSeen, device.ID)
		return device, err
	}
	// ON CONFLICT(kind, name) mirrors UpsertFleetHeartbeat: with the UNIQUE(kind, name)
	// index, a plain INSERT here would throw a raw "UNIQUE constraint failed" when the
	// manual-add path (App.AddFleetDevice) is given a (kind, name) that already
	// exists. Upserting instead keeps this no-id branch idempotent and consistent
	// with the heartbeat path — refreshing the mutable fields onto the existing row
	// rather than erroring or duplicating.
	err = s.db.QueryRow(`
		INSERT INTO fleet_devices(name, kind, endpoint, os, arch, status, services, last_seen, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))
		ON CONFLICT(kind, name) DO UPDATE SET
			endpoint=excluded.endpoint,
			os=excluded.os,
			arch=excluded.arch,
			status=excluded.status,
			services=excluded.services,
			last_seen=excluded.last_seen,
			updated_at=datetime('now')
		RETURNING id
	`, device.Name, device.Kind, device.Endpoint, device.OS, device.Arch, device.Status, string(servicesRaw), device.LastSeen).Scan(&device.ID)
	if err != nil {
		return FleetDevice{}, err
	}
	return device, nil
}

func (s *Store) UpsertFleetHeartbeat(device FleetDevice) (FleetDevice, error) {
	if device.Name == "" {
		return FleetDevice{}, fmt.Errorf("device name is required")
	}
	if device.Kind == "" {
		device.Kind = "worker"
	}
	if device.Status == "" {
		device.Status = "online"
	}
	if device.LastSeen == "" {
		device.LastSeen = time.Now().UTC().Format(time.RFC3339Nano)
	}
	servicesRaw, err := json.Marshal(device.Services)
	if err != nil {
		return FleetDevice{}, err
	}
	// Single atomic upsert keyed by the UNIQUE(kind, name) index. The old
	// SELECT-then-INSERT raced: two concurrent first-contact heartbeats for the same
	// (kind, name) both missed the SELECT and both INSERTed, producing duplicate
	// devices. ON CONFLICT(kind, name) preserves the existing row's id and created_at
	// and refreshes only the mutable fields — exactly what the prior UPDATE branch did
	// — while RETURNING id yields the row id for both the insert and the update path.
	err = s.db.QueryRow(`
		INSERT INTO fleet_devices(name, kind, endpoint, os, arch, status, services, last_seen, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))
		ON CONFLICT(kind, name) DO UPDATE SET
			endpoint=excluded.endpoint,
			os=excluded.os,
			arch=excluded.arch,
			status=excluded.status,
			services=excluded.services,
			last_seen=excluded.last_seen,
			updated_at=datetime('now')
		RETURNING id
	`, device.Name, device.Kind, device.Endpoint, device.OS, device.Arch, device.Status, string(servicesRaw), device.LastSeen).Scan(&device.ID)
	if err != nil {
		return FleetDevice{}, err
	}
	return device, nil
}

func (s *Store) ListFleetDevices() []FleetDevice {
	rows, err := s.db.Query(`
		SELECT id, name, kind, endpoint, os, arch, status, services, last_seen, created_at
		FROM fleet_devices ORDER BY kind, name
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []FleetDevice
	for rows.Next() {
		var device FleetDevice
		var servicesRaw string
		if err := rows.Scan(&device.ID, &device.Name, &device.Kind, &device.Endpoint, &device.OS, &device.Arch, &device.Status, &servicesRaw, &device.LastSeen, &device.CreatedAt); err == nil {
			// best-effort: a corrupt services blob just yields no badges
			_ = json.Unmarshal([]byte(servicesRaw), &device.Services)
			out = append(out, device)
		}
	}
	// Fail closed on a truncated read rather than returning a partial device list.
	// Mirrors HealthScores' rows.Err() handling.
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

func (s *Store) DeleteFleetDevice(id int64) error {
	_, err := s.db.Exec(`DELETE FROM fleet_devices WHERE id = ?`, id)
	return err
}

// SweepStaleFleetDevices ages fleet devices out of the online set as heartbeats stop
// arriving, mirroring production CashPilot's device lifecycle: a device whose last_seen
// is older than offlineAfter is flipped to "offline" (the ~180s / 3-missed-heartbeat
// grace), and one already "offline" whose last_seen is older than reapAfter is deleted
// (the ~1h reap). It returns how many rows were offlined and how many were reaped.
//
// last_seen is written by UpsertFleetHeartbeat / fleet_server as an RFC3339 timestamp
// WITHOUT a fractional part, so it is fixed-length and UTC; formatting the cutoffs the
// same way makes the lexicographic string comparison a correct chronological order. A
// non-timestamp placeholder such as AddFleetDevice's "not connected yet" sorts above
// any real cutoff, so a never-connected device is never swept.
func (s *Store) SweepStaleFleetDevices(offlineAfter, reapAfter time.Duration) (offlined int64, reaped int64, err error) {
	now := time.Now().UTC()
	offlineCutoff := now.Add(-offlineAfter).Format(time.RFC3339)
	reapCutoff := now.Add(-reapAfter).Format(time.RFC3339)

	offRes, err := s.db.Exec(`
		UPDATE fleet_devices SET status = 'offline', updated_at = datetime('now')
		WHERE status != 'offline' AND last_seen < ?
	`, offlineCutoff)
	if err != nil {
		return 0, 0, err
	}
	offlined, _ = offRes.RowsAffected()

	reapRes, err := s.db.Exec(`
		DELETE FROM fleet_devices WHERE status = 'offline' AND last_seen < ?
	`, reapCutoff)
	if err != nil {
		return offlined, 0, err
	}
	reaped, _ = reapRes.RowsAffected()
	return offlined, reaped, nil
}

// EffectiveFleetStatus returns the status a fleet device should DISPLAY given how long
// ago it last checked in. It is the read-path sibling of SweepStaleFleetDevices' SQL:
// an "online" device whose lastSeen is older than threshold is shown as "offline"
// immediately, so the Fleet view is accurate between scheduler ticks without the read
// path ever mutating storage. lastSeen is parsed as RFC3339 (the format the heartbeat
// writes); a value that does not parse (e.g. AddFleetDevice's "not connected yet", or
// an empty last_seen) leaves the status unchanged, as does any non-"online" status.
func EffectiveFleetStatus(status, lastSeen string, threshold time.Duration) string {
	if status != "online" {
		return status
	}
	seen, err := time.Parse(time.RFC3339, lastSeen)
	if err != nil {
		return status
	}
	if time.Since(seen) > threshold {
		return "offline"
	}
	return status
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDeployment(row scanner) (Deployment, error) {
	var dep Deployment
	err := row.Scan(&dep.Slug, &dep.ContainerID, &dep.Name, &dep.Image, &dep.Status, &dep.Runtime, &dep.CPUPercent, &dep.MemoryMB, &dep.CreatedAt, &dep.UpdatedAt)
	if err != nil {
		return Deployment{}, err
	}
	return dep, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA foreign_keys=ON;
		CREATE TABLE IF NOT EXISTS credentials (
			slug TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS service_details (
			slug TEXT PRIMARY KEY,
			detail TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS deployments (
			slug TEXT PRIMARY KEY,
			container_id TEXT NOT NULL,
			name TEXT NOT NULL,
			image TEXT NOT NULL,
			status TEXT NOT NULL,
			runtime TEXT NOT NULL,
			cpu_percent REAL NOT NULL DEFAULT 0,
			memory_mb REAL NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS earnings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			platform TEXT NOT NULL,
			balance REAL NOT NULL,
			currency TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_earnings_platform_created ON earnings(platform, created_at);
		CREATE TABLE IF NOT EXISTS runtime_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slug TEXT NOT NULL,
			event TEXT NOT NULL,
			detail TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_runtime_events_slug_created ON runtime_events(slug, created_at);
		CREATE TABLE IF NOT EXISTS fleet_devices (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			kind TEXT NOT NULL,
			endpoint TEXT NOT NULL DEFAULT '',
			os TEXT NOT NULL DEFAULT '',
			arch TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'offline',
			services TEXT NOT NULL DEFAULT '[]',
			last_seen TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		-- Forward-only, idempotent: collapse any pre-existing duplicate (kind, name)
		-- fleet_devices rows (which the old racy SELECT-then-INSERT UpsertFleetHeartbeat
		-- could produce) down to the single most-recently-seen row BEFORE adding the
		-- unique index — adding it to a table that already holds duplicates would fail.
		-- "Most-recently-seen" is the greatest last_seen (tie-broken by the highest id,
		-- the last row written), so the row heartbeats have been refreshing survives.
		DELETE FROM fleet_devices
		WHERE id NOT IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY kind, name ORDER BY last_seen DESC, id DESC
				) AS rn
				FROM fleet_devices
			) WHERE rn = 1
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_fleet_devices_kind_name ON fleet_devices(kind, name);
	`)
	return err
}

func (s *Store) encrypt(raw []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, raw, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (s *Store) decrypt(encoded string) ([]byte, error) {
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if len(sealed) < s.aead.NonceSize() {
		return nil, fmt.Errorf("encrypted payload too short")
	}
	nonce := sealed[:s.aead.NonceSize()]
	ciphertext := sealed[s.aead.NonceSize():]
	return s.aead.Open(nil, nonce, ciphertext, nil)
}
