package store

import (
	"database/sql"
	"errors"
)

// Services routed through a proxy from their first deploy (internal/services
// routes.go). A row means the service must never run from this machine's own IP
// until the owner confirms otherwise, so it outlives the service's removal: a
// removal and a fresh deploy cannot skip that confirmation.
const routesDDL = `
	CREATE TABLE IF NOT EXISTS routes (
		slug TEXT PRIMARY KEY,
		proxy_id INTEGER NOT NULL,
		created_at TEXT NOT NULL
	);
`

// SetRoute records that slug runs through proxyID. It returns the proxy it was
// routed through before, and whether there was one.
func (s *Store) SetRoute(slug string, proxyID int64) (int64, bool, error) {
	before, had, err := s.GetRoute(slug)
	if err != nil {
		return 0, false, err
	}
	_, err = s.execWrite(`
		INSERT INTO routes(slug, proxy_id, created_at) VALUES(?, ?, datetime('now'))
		ON CONFLICT(slug) DO UPDATE SET proxy_id = excluded.proxy_id
	`, slug, proxyID)
	return before, had, err
}

// GetRoute returns the proxy slug is routed through, and whether it is routed.
func (s *Store) GetRoute(slug string) (int64, bool, error) {
	var id int64
	err := s.db.QueryRow(`SELECT proxy_id FROM routes WHERE slug = ?`, slug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// DeleteRoute forgets the route of slug, once it runs from this machine's IP as the
// owner confirmed, or after a routed deploy that failed before it ran.
func (s *Store) DeleteRoute(slug string) error {
	_, err := s.execWrite(`DELETE FROM routes WHERE slug = ?`, slug)
	return err
}

// ListRoutes returns slug -> proxy id of every routed service.
func (s *Store) ListRoutes() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT slug, proxy_id FROM routes ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var slug string
		var id int64
		if err := rows.Scan(&slug, &id); err != nil {
			return nil, err
		}
		out[slug] = id
	}
	return out, rows.Err()
}
