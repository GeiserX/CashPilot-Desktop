package store

import (
	"database/sql"
	"errors"
)

// Proxies the owner runs copies of services through (internal/proxies). The
// definition holds the proxy's user, password or Tailscale auth key, so it is stored
// encrypted exactly like a credential blob. The state (traffic used, the cap, the
// last exit IP) holds no secret and stays plain JSON.
const proxiesDDL = `
	CREATE TABLE IF NOT EXISTS proxies (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		value TEXT NOT NULL,
		state TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS proxied_copies (
		proxy_id INTEGER NOT NULL,
		slug TEXT NOT NULL,
		stopped_by_cap INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		PRIMARY KEY (proxy_id, slug)
	);
`

// ErrProxyNotFound is returned when an id names no proxy.
var ErrProxyNotFound = errors.New("no such proxy")

// Proxy is one stored proxy, decrypted. Definition and State are the JSON the
// proxies package wrote; the store never reads inside them.
type Proxy struct {
	ID         int64
	Definition []byte
	State      []byte
}

// ProxiedCopy is the record of a copy of a service running through a proxy.
type ProxiedCopy struct {
	ProxyID      int64
	Slug         string
	StoppedByCap bool
}

// InsertProxy stores a new proxy and returns its id.
func (s *Store) InsertProxy(definition []byte) (int64, error) {
	encrypted, err := s.encrypt(definition)
	if err != nil {
		return 0, err
	}
	res, err := s.execWrite(`
		INSERT INTO proxies(value, state, created_at, updated_at)
		VALUES(?, '{}', datetime('now'), datetime('now'))
	`, encrypted)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateProxyDefinition replaces a proxy's definition.
func (s *Store) UpdateProxyDefinition(id int64, definition []byte) error {
	encrypted, err := s.encrypt(definition)
	if err != nil {
		return err
	}
	return s.oneRow(`UPDATE proxies SET value = ?, updated_at = datetime('now') WHERE id = ?`, encrypted, id)
}

// UpdateProxyState replaces a proxy's state.
func (s *Store) UpdateProxyState(id int64, state []byte) error {
	return s.oneRow(`UPDATE proxies SET state = ?, updated_at = datetime('now') WHERE id = ?`, string(state), id)
}

func (s *Store) oneRow(query string, args ...any) error {
	res, err := s.execWrite(query, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrProxyNotFound
	}
	return nil
}

// DeleteProxy drops a proxy and its copy records.
func (s *Store) DeleteProxy(id int64) error {
	if err := s.oneRow(`DELETE FROM proxies WHERE id = ?`, id); err != nil {
		return err
	}
	_, err := s.execWrite(`DELETE FROM proxied_copies WHERE proxy_id = ?`, id)
	return err
}

// GetProxy returns one proxy.
func (s *Store) GetProxy(id int64) (Proxy, bool, error) {
	var encrypted, state string
	err := s.db.QueryRow(`SELECT value, state FROM proxies WHERE id = ?`, id).Scan(&encrypted, &state)
	if err == sql.ErrNoRows {
		return Proxy{}, false, nil
	}
	if err != nil {
		return Proxy{}, false, err
	}
	raw, err := s.decrypt(encrypted)
	if err != nil {
		return Proxy{}, false, err
	}
	return Proxy{ID: id, Definition: raw, State: []byte(state)}, true, nil
}

// ListProxies returns every proxy, by id. A definition that fails to decrypt fails
// the whole read rather than vanishing from the list.
func (s *Store) ListProxies() ([]Proxy, error) {
	rows, err := s.db.Query(`SELECT id, value, state FROM proxies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proxy
	for rows.Next() {
		var id int64
		var encrypted, state string
		if err := rows.Scan(&id, &encrypted, &state); err != nil {
			return nil, err
		}
		raw, err := s.decrypt(encrypted)
		if err != nil {
			return nil, err
		}
		out = append(out, Proxy{ID: id, Definition: raw, State: []byte(state)})
	}
	return out, rows.Err()
}

// SaveProxiedCopy records a copy of slug through a proxy.
func (s *Store) SaveProxiedCopy(proxyID int64, slug string) error {
	_, err := s.execWrite(`
		INSERT INTO proxied_copies(proxy_id, slug, stopped_by_cap, created_at)
		VALUES(?, ?, 0, datetime('now'))
		ON CONFLICT(proxy_id, slug) DO UPDATE SET stopped_by_cap = 0
	`, proxyID, slug)
	return err
}

// SetProxiedCopyCapped marks whether the cap (rather than the owner) stopped a copy.
func (s *Store) SetProxiedCopyCapped(proxyID int64, slug string, capped bool) error {
	v := 0
	if capped {
		v = 1
	}
	_, err := s.execWrite(`UPDATE proxied_copies SET stopped_by_cap = ? WHERE proxy_id = ? AND slug = ?`, v, proxyID, slug)
	return err
}

// DeleteProxiedCopy drops one copy record.
func (s *Store) DeleteProxiedCopy(proxyID int64, slug string) error {
	_, err := s.execWrite(`DELETE FROM proxied_copies WHERE proxy_id = ? AND slug = ?`, proxyID, slug)
	return err
}

// DeleteProxiedCopiesOf drops every copy record of a service, when it is removed.
func (s *Store) DeleteProxiedCopiesOf(slug string) error {
	_, err := s.execWrite(`DELETE FROM proxied_copies WHERE slug = ?`, slug)
	return err
}

// ListProxiedCopies returns the copy records, of one proxy when proxyID > 0.
func (s *Store) ListProxiedCopies(proxyID int64) ([]ProxiedCopy, error) {
	query := `SELECT proxy_id, slug, stopped_by_cap FROM proxied_copies`
	var args []any
	if proxyID > 0 {
		query += ` WHERE proxy_id = ?`
		args = append(args, proxyID)
	}
	rows, err := s.db.Query(query+` ORDER BY proxy_id, slug`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProxiedCopy
	for rows.Next() {
		var c ProxiedCopy
		var capped int
		if err := rows.Scan(&c.ProxyID, &c.Slug, &capped); err != nil {
			return nil, err
		}
		c.StoppedByCap = capped != 0
		out = append(out, c)
	}
	return out, rows.Err()
}
