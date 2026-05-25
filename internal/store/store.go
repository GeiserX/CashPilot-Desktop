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

func (s *Store) ListLatestEarnings() []EarningsRecord {
	rows, err := s.db.Query(`
		SELECT e.platform, e.balance, e.currency, e.error, e.created_at
		FROM earnings e
		INNER JOIN (
			SELECT platform, max(created_at) AS created_at FROM earnings GROUP BY platform
		) latest ON latest.platform = e.platform AND latest.created_at = e.created_at
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
