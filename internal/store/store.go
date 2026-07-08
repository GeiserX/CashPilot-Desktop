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
	return out
}

func (s *Store) RecordEvent(slug, event, detail string) {
	_, _ = s.db.Exec(`INSERT INTO runtime_events(slug, event, detail, created_at) VALUES(?, ?, ?, datetime('now'))`, slug, event, detail)
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

	eventsRes, err := s.db.Exec(`DELETE FROM runtime_events WHERE created_at < ?`, cutoff)
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
	result, err := s.db.Exec(`
		INSERT INTO fleet_devices(name, kind, endpoint, os, arch, status, services, last_seen, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))
	`, device.Name, device.Kind, device.Endpoint, device.OS, device.Arch, device.Status, string(servicesRaw), device.LastSeen)
	if err != nil {
		return FleetDevice{}, err
	}
	device.ID, _ = result.LastInsertId()
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
	var id int64
	err = s.db.QueryRow(`SELECT id FROM fleet_devices WHERE kind = ? AND name = ?`, device.Kind, device.Name).Scan(&id)
	if err == nil {
		device.ID = id
		_, err = s.db.Exec(`
			UPDATE fleet_devices
			SET endpoint = ?, os = ?, arch = ?, status = ?, services = ?, last_seen = ?, updated_at = datetime('now')
			WHERE id = ?
		`, device.Endpoint, device.OS, device.Arch, device.Status, string(servicesRaw), device.LastSeen, id)
		return device, err
	}
	if err != sql.ErrNoRows {
		return FleetDevice{}, err
	}
	return s.UpsertFleetDevice(device)
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
			_ = json.Unmarshal([]byte(servicesRaw), &device.Services)
			out = append(out, device)
		}
	}
	return out
}

func (s *Store) DeleteFleetDevice(id int64) error {
	_, err := s.db.Exec(`DELETE FROM fleet_devices WHERE id = ?`, id)
	return err
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
