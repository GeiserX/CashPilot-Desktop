package store

import (
	"database/sql"
	"errors"
)

// Services the user added from an image reference (internal/services/custom.go).
// The definition, environment values included, is stored encrypted exactly like a
// credential blob. A deleted service keeps its row with deleted=1 and no definition:
// its slug names a container and a data volume, so it is never handed out again.
const customServicesDDL = `
	CREATE TABLE IF NOT EXISTS custom_services (
		slug TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT '',
		deleted INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
`

// ErrCustomServiceNotFound is returned when an update names no live custom service.
var ErrCustomServiceNotFound = errors.New("custom service not found")

// CustomService is one stored definition, decrypted. Definition is the JSON the
// services package wrote; the store never reads inside it.
type CustomService struct {
	Slug       string
	Definition []byte
}

// InsertCustomService stores a new custom service. It fails when the slug was ever
// used, deleted ones included.
func (s *Store) InsertCustomService(slug string, definition []byte) error {
	encrypted, err := s.encrypt(definition)
	if err != nil {
		return err
	}
	_, err = s.execWrite(`
		INSERT INTO custom_services(slug, value, deleted, created_at, updated_at)
		VALUES(?, ?, 0, datetime('now'), datetime('now'))
	`, slug, encrypted)
	return err
}

// UpdateCustomService replaces a live custom service's definition.
func (s *Store) UpdateCustomService(slug string, definition []byte) error {
	encrypted, err := s.encrypt(definition)
	if err != nil {
		return err
	}
	res, err := s.execWrite(`
		UPDATE custom_services SET value = ?, updated_at = datetime('now')
		WHERE slug = ? AND deleted = 0
	`, encrypted, slug)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrCustomServiceNotFound
	}
	return nil
}

// DeleteCustomService drops the definition and keeps the slug as a tombstone.
func (s *Store) DeleteCustomService(slug string) error {
	res, err := s.execWrite(`
		UPDATE custom_services SET value = '', deleted = 1, updated_at = datetime('now')
		WHERE slug = ? AND deleted = 0
	`, slug)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrCustomServiceNotFound
	}
	return nil
}

// GetCustomService returns a live custom service's definition.
func (s *Store) GetCustomService(slug string) (CustomService, bool, error) {
	var encrypted string
	err := s.db.QueryRow(`SELECT value FROM custom_services WHERE slug = ? AND deleted = 0`, slug).Scan(&encrypted)
	if err == sql.ErrNoRows {
		return CustomService{}, false, nil
	}
	if err != nil {
		return CustomService{}, false, err
	}
	raw, err := s.decrypt(encrypted)
	if err != nil {
		return CustomService{}, false, err
	}
	return CustomService{Slug: slug, Definition: raw}, true, nil
}

// ListCustomServices returns every live custom service, by slug. A definition that
// fails to decrypt fails the whole read rather than vanishing from the list.
func (s *Store) ListCustomServices() ([]CustomService, error) {
	rows, err := s.db.Query(`SELECT slug, value FROM custom_services WHERE deleted = 0 ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomService
	for rows.Next() {
		var slug, encrypted string
		if err := rows.Scan(&slug, &encrypted); err != nil {
			return nil, err
		}
		raw, err := s.decrypt(encrypted)
		if err != nil {
			return nil, err
		}
		out = append(out, CustomService{Slug: slug, Definition: raw})
	}
	return out, rows.Err()
}

// CustomServiceSlugs is every slug ever used for a custom service, deleted ones
// included, so a new one never takes a slug whose data volume may still exist.
func (s *Store) CustomServiceSlugs() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT slug FROM custom_services`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out[slug] = true
	}
	return out, rows.Err()
}
