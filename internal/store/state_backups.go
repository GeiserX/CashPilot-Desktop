package store

import (
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
)

// The user's word that a service's irreplaceable state is backed up. One row per
// service: Desktop is one machine. state_id is the fingerprint of where the state
// lived when it was confirmed (” when it could not be read); method is 'manual'
// (the button) or 'export' (a backup saved from the app). confirmed_at is RFC 3339.
const stateBackupsDDL = `
	CREATE TABLE IF NOT EXISTS state_backups (
		slug TEXT PRIMARY KEY,
		state_id TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL DEFAULT 'manual',
		confirmed_at TEXT NOT NULL
	);
`

// ConfirmStateBackup records that slug's state, as fingerprinted by stateID, is
// backed up as of at.
func (s *Store) ConfirmStateBackup(slug, stateID, method string, at time.Time) error {
	_, err := s.execWrite(`
		INSERT INTO state_backups(slug, state_id, method, confirmed_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET state_id=excluded.state_id, method=excluded.method, confirmed_at=excluded.confirmed_at
	`, slug, stateID, method, at.UTC().Format(time.RFC3339))
	return err
}

// ClearStateBackup withdraws a confirmation, so the service shows as not backed up.
func (s *Store) ClearStateBackup(slug string) error {
	_, err := s.execWrite(`DELETE FROM state_backups WHERE slug = ?`, slug)
	return err
}

// StateBackup returns slug's confirmation, or nil when there is none.
func (s *Store) StateBackup(slug string) (*statebackup.Confirmation, error) {
	var c statebackup.Confirmation
	var at string
	err := s.db.QueryRow(`SELECT slug, state_id, method, confirmed_at FROM state_backups WHERE slug = ?`, slug).
		Scan(&c.Slug, &c.StateID, &c.Method, &at)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// An unreadable time stays zero, which judges as expired: unknown must never
	// read as confirmed.
	c.ConfirmedAt, _ = time.Parse(time.RFC3339, at)
	return &c, nil
}

// Every backup file this app saved, by the SHA-256 of the file. A restore of a file
// listed here needs no typed confirmation: this app made it, so it knows whose
// identity is inside. Rows are never removed; a hash names one file.
const exportedBackupsDDL = `
	CREATE TABLE IF NOT EXISTS exported_backups (
		sha256 TEXT PRIMARY KEY,
		slug TEXT NOT NULL,
		exported_at TEXT NOT NULL
	);
`

// RecordExportedBackup records that this app saved the file with this SHA-256.
func (s *Store) RecordExportedBackup(sha256, slug string, at time.Time) error {
	_, err := s.execWrite(`INSERT INTO exported_backups(sha256, slug, exported_at) VALUES(?, ?, ?) ON CONFLICT(sha256) DO NOTHING`,
		sha256, slug, at.UTC().Format(time.RFC3339))
	return err
}

// ExportedBackup returns the service this app saved the file with this SHA-256
// for, and whether it did.
func (s *Store) ExportedBackup(sha256 string) (string, bool, error) {
	var slug string
	err := s.db.QueryRow(`SELECT slug FROM exported_backups WHERE sha256 = ?`, sha256).Scan(&slug)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return slug, true, nil
}

// A restore that is under way or did not finish, one row per service, with the
// volumes it creates, each as name:label value (runtime.LabelRestore). Written before
// the restore makes anything and removed once its data read back as the backup, or
// once its undo or Delete the partial restore removed what it made. While a row is
// here, Deploy and Start refuse the service: its volume may be half-written.
const restoreHoldsDDL = `
	CREATE TABLE IF NOT EXISTS restore_holds (
		slug TEXT PRIMARY KEY,
		volumes TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL
	);
`

// RestoreHold is one restore that has not finished.
type RestoreHold struct {
	Slug      string            `json:"slug"`
	Volumes   map[string]string `json:"-"`
	StartedAt time.Time         `json:"startedAt"`
}

// HoldRestore records that a restore of slug is under way and makes these volumes.
func (s *Store) HoldRestore(slug string, volumes map[string]string, at time.Time) error {
	names := make([]string, 0, len(volumes))
	for name := range volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+":"+volumes[name])
	}
	_, err := s.execWrite(`
		INSERT INTO restore_holds(slug, volumes, started_at) VALUES(?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET volumes=excluded.volumes, started_at=excluded.started_at
	`, slug, strings.Join(parts, " "), at.UTC().Format(time.RFC3339))
	return err
}

// ReleaseRestore removes slug's hold.
func (s *Store) ReleaseRestore(slug string) error {
	_, err := s.execWrite(`DELETE FROM restore_holds WHERE slug = ?`, slug)
	return err
}

// RestoreHolds lists every restore that has not finished, by slug.
func (s *Store) RestoreHolds() ([]RestoreHold, error) {
	rows, err := s.db.Query(`SELECT slug, volumes, started_at FROM restore_holds ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RestoreHold
	for rows.Next() {
		var h RestoreHold
		var volumes, at string
		if err := rows.Scan(&h.Slug, &volumes, &at); err != nil {
			return nil, err
		}
		h.Volumes = map[string]string{}
		for _, part := range strings.Fields(volumes) {
			name, nonce, _ := strings.Cut(part, ":")
			h.Volumes[name] = nonce
		}
		h.StartedAt, _ = time.Parse(time.RFC3339, at)
		out = append(out, h)
	}
	return out, rows.Err()
}

// RestoreHeld returns slug's hold, or nil when no restore of it is unfinished.
func (s *Store) RestoreHeld(slug string) (*RestoreHold, error) {
	holds, err := s.RestoreHolds()
	if err != nil {
		return nil, err
	}
	for i := range holds {
		if holds[i].Slug == slug {
			return &holds[i], nil
		}
	}
	return nil, nil
}
