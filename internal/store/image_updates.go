package store

import "time"

// What each app image update did (internal/imageupdates): one row per decision
// that the user sees, newest last. The table on the Settings page and the bell
// read it, and a rolled_back row holds that build back for good.
const imageUpdatesDDL = `
	CREATE TABLE IF NOT EXISTS image_updates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		at INTEGER NOT NULL,
		slug TEXT NOT NULL,
		result TEXT NOT NULL,
		old_image TEXT NOT NULL DEFAULT '',
		new_image TEXT NOT NULL DEFAULT '',
		old_digest TEXT NOT NULL DEFAULT '',
		new_digest TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '',
		cleared INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_image_updates_slug ON image_updates(slug, new_image, result);
`

// ImageUpdate is one row of the image update history.
type ImageUpdate struct {
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	Slug      string    `json:"slug"`
	Result    string    `json:"result"`
	OldImage  string    `json:"oldImage"`
	NewImage  string    `json:"newImage"`
	OldDigest string    `json:"oldDigest"`
	NewDigest string    `json:"newDigest"`
	Detail    string    `json:"detail"`
	// Cleared is a later check answering this row: the app is current again, so
	// the row stays in the history and leaves the bell.
	Cleared bool `json:"cleared"`
}

// imageUpdatesKept is how many rows the history keeps; older ones are dropped,
// except a rolled_back row: it holds a failed build back for good.
const imageUpdatesKept = 500

// RecordImageUpdate writes one row and drops the oldest beyond imageUpdatesKept.
func (s *Store) RecordImageUpdate(u ImageUpdate) error {
	if u.At.IsZero() {
		u.At = time.Now()
	}
	if _, err := s.execWrite(`INSERT INTO image_updates (at, slug, result, old_image, new_image, old_digest, new_digest, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.At.Unix(), u.Slug, u.Result, u.OldImage, u.NewImage, u.OldDigest, u.NewDigest, u.Detail); err != nil {
		return err
	}
	_, err := s.execWrite(`DELETE FROM image_updates WHERE result != 'rolled_back' AND id <= (SELECT MAX(id) FROM image_updates) - ?`, imageUpdatesKept)
	return err
}

// ImageUpdateSeen says whether a row with one of results already names newImage
// for slug. A cleared row still counts for rolled_back, so an undone build stays
// held; for any other result it does not, so a build offered again after a
// later check cleared it reaches the bell again.
func (s *Store) ImageUpdateSeen(slug, newImage string, results ...string) (bool, error) {
	for _, result := range results {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM image_updates WHERE slug = ? AND new_image = ? AND result = ?
			AND (cleared = 0 OR result = 'rolled_back')`,
			slug, newImage, result).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// ListImageUpdates returns the newest limit rows, newest first.
func (s *Store) ListImageUpdates(limit int) ([]ImageUpdate, error) {
	return s.queryImageUpdates(`SELECT id, at, slug, result, old_image, new_image, old_digest, new_digest, detail, cleared
		FROM image_updates ORDER BY id DESC LIMIT ?`, limit)
}

// LatestImageUpdates returns the newest row of each app, newest first.
func (s *Store) LatestImageUpdates() ([]ImageUpdate, error) {
	return s.queryImageUpdates(`SELECT id, at, slug, result, old_image, new_image, old_digest, new_digest, detail, cleared
		FROM image_updates WHERE id IN (SELECT MAX(id) FROM image_updates GROUP BY slug) ORDER BY id DESC`)
}

// ClearImageUpdates marks every row of slug answered: a check found it current.
func (s *Store) ClearImageUpdates(slug string) error {
	_, err := s.execWrite(`UPDATE image_updates SET cleared = 1 WHERE slug = ? AND cleared = 0`, slug)
	return err
}

func (s *Store) queryImageUpdates(query string, args ...any) ([]ImageUpdate, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ImageUpdate{}
	for rows.Next() {
		var u ImageUpdate
		var at int64
		var cleared int
		if err := rows.Scan(&u.ID, &at, &u.Slug, &u.Result, &u.OldImage, &u.NewImage, &u.OldDigest, &u.NewDigest, &u.Detail, &cleared); err != nil {
			return nil, err
		}
		u.At, u.Cleared = time.Unix(at, 0), cleared != 0
		out = append(out, u)
	}
	return out, rows.Err()
}
