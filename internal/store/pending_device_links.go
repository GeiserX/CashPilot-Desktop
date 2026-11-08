package store

import "time"

// A device registration the provider asked to retry later (a new ProxyRack UUID is
// unknown for about five minutes). The app retries it with backoff until the device
// is on the account, the device is gone, or a day has passed (gave_up = 1, and the
// user is told to add it by hand). Times are Unix seconds.
const pendingDeviceLinksDDL = `
	CREATE TABLE IF NOT EXISTS pending_device_links (
		slug TEXT NOT NULL,
		device_id TEXT NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0,
		first_at INTEGER NOT NULL,
		next_at INTEGER NOT NULL,
		gave_up INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (slug, device_id)
	);
`

// PendingDeviceLink is one registration waiting to be retried.
type PendingDeviceLink struct {
	Slug     string
	DeviceID string
	Attempts int
	FirstAt  time.Time
	NextAt   time.Time
	GaveUp   bool
}

// SavePendingDeviceLink remembers a registration to retry. A second save of a live
// one keeps its window and its earliest next try; one that had given up starts a
// new day of retries.
func (s *Store) SavePendingDeviceLink(slug, deviceID string, now, nextAt time.Time) error {
	_, err := s.execWrite(`
		INSERT INTO pending_device_links (slug, device_id, first_at, next_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(slug, device_id) DO UPDATE SET
			first_at = CASE WHEN gave_up THEN excluded.first_at ELSE first_at END,
			attempts = CASE WHEN gave_up THEN 0 ELSE attempts END,
			next_at = CASE WHEN gave_up THEN excluded.next_at ELSE MIN(next_at, excluded.next_at) END,
			gave_up = 0`,
		slug, deviceID, now.Unix(), nextAt.Unix())
	return err
}

// ListPendingDeviceLinks returns every stored registration, soonest retry first.
func (s *Store) ListPendingDeviceLinks() ([]PendingDeviceLink, error) {
	rows, err := s.db.Query(`SELECT slug, device_id, attempts, first_at, next_at, gave_up FROM pending_device_links ORDER BY next_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingDeviceLink
	for rows.Next() {
		var p PendingDeviceLink
		var first, next int64
		var gaveUp int
		if err := rows.Scan(&p.Slug, &p.DeviceID, &p.Attempts, &first, &next, &gaveUp); err != nil {
			return nil, err
		}
		p.FirstAt, p.NextAt, p.GaveUp = time.Unix(first, 0), time.Unix(next, 0), gaveUp != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePendingDeviceLink records the outcome of a retry that did not add the device.
func (s *Store) UpdatePendingDeviceLink(slug, deviceID string, attempts int, nextAt time.Time, gaveUp bool) error {
	_, err := s.execWrite(`UPDATE pending_device_links SET attempts = ?, next_at = ?, gave_up = ? WHERE slug = ? AND device_id = ?`,
		attempts, nextAt.Unix(), boolInt(gaveUp), slug, deviceID)
	return err
}

// DeletePendingDeviceLink forgets a registration: the device is on the account or gone.
func (s *Store) DeletePendingDeviceLink(slug, deviceID string) error {
	_, err := s.execWrite(`DELETE FROM pending_device_links WHERE slug = ? AND device_id = ?`, slug, deviceID)
	return err
}
