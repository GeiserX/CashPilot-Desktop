package store

import (
	"database/sql"
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
