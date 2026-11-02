package store

import (
	"database/sql"
	"time"
)

// What each container used on this machine, per UTC hour (internal/usage). Sums
// plus sample counts, so an average is sum/n. Traffic is bytes over the
// net_seconds the counted deltas cover, so the line is a rate and half an hour of
// readings is not drawn as half the traffic. NULL traffic means no reading carried
// a usable delta, never zero traffic; an hour with no row at all is a gap.
//
// Containers are keyed by NAME, so a redeploy (new id, same name) continues the
// same line. The traffic baselines in usage_counters stay per container id: a new
// container's counters start again from zero.
const usageSchema = `
	CREATE TABLE IF NOT EXISTS usage_hourly (
		container    TEXT    NOT NULL,
		hour         INTEGER NOT NULL,
		slug         TEXT    NOT NULL,
		beats        INTEGER NOT NULL DEFAULT 0,
		cpu_sum      REAL    NOT NULL DEFAULT 0,
		cpu_n        INTEGER NOT NULL DEFAULT 0,
		mem_sum      REAL    NOT NULL DEFAULT 0,
		mem_n        INTEGER NOT NULL DEFAULT 0,
		rx_bytes     INTEGER,
		tx_bytes     INTEGER,
		net_seconds  REAL    NOT NULL DEFAULT 0,
		no_counters  INTEGER NOT NULL DEFAULT 0,
		traffic_lost INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (container, hour)
	);
	CREATE INDEX IF NOT EXISTS idx_usage_hourly_hour ON usage_hourly(hour);
	CREATE TABLE IF NOT EXISTS usage_counters (
		container_id TEXT PRIMARY KEY,
		rx_bytes     INTEGER NOT NULL,
		tx_bytes     INTEGER NOT NULL,
		seen_at      REAL    NOT NULL
	);
`

// UsageRetention is how long hourly usage is kept. The graphs read 30 days; a few
// more so the oldest point is never half-trimmed.
const UsageRetention = 35 * 24 * time.Hour

// UsageHour is one container's row for one UTC hour (Hour is its start, in Unix
// seconds). RecordUsage takes one heartbeat's worth per container (Beats 1) and
// adds it to the stored row.
type UsageHour struct {
	Container string
	Hour      int64
	Slug      string
	// Beats is how many readings saw the container running. CPUN or MemN below it
	// means a stats read failed in that hour, so a total without it is a floor.
	Beats  int
	CPUSum float64
	CPUN   int
	MemSum float64
	MemN   int
	// Bytes moved over NetSeconds, nil when no reading carried a usable delta.
	RxBytes    *int64
	TxBytes    *int64
	NetSeconds float64
	// Readings with no counters at all (host network, a native program).
	NoCounters int
	// Readings whose traffic since the last one was lost (a stale baseline).
	TrafficLost int
}

// UsageBaseline is the last cumulative network counters seen for a container id,
// and when (Unix seconds), so the next reading can be differenced.
type UsageBaseline struct {
	Rx, Tx int64
	SeenAt float64
}

func (s *Store) migrateUsage() error {
	_, err := s.execWrite(usageSchema)
	return err
}

// RecordUsage reads the baselines, lets fold turn them and one reading into hourly
// rows and new baselines, and stores both, all in one transaction. Reading the
// baselines and storing the new ones must be one step: a failure halfway would
// otherwise keep the rows without the baselines, and the next reading would count
// the same traffic again. The window and the background daemon can both sample
// the same database, so the read has to be inside the write lock.
func (s *Store) RecordUsage(fold func(map[string]UsageBaseline) ([]UsageHour, map[string]UsageBaseline)) error {
	return retryBusy(func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		// A no-op write takes the write lock now, before the baselines are read,
		// so two processes cannot both difference against the same baseline.
		if _, err := tx.Exec(`DELETE FROM usage_counters WHERE 0`); err != nil {
			return err
		}
		baselines, err := readBaselines(tx)
		if err != nil {
			return err
		}
		rows, next := fold(baselines)
		for _, r := range rows {
			if _, err := tx.Exec(`
				INSERT INTO usage_hourly (container, hour, slug, beats, cpu_sum, cpu_n, mem_sum, mem_n,
				                          rx_bytes, tx_bytes, net_seconds, no_counters, traffic_lost)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(container, hour) DO UPDATE SET
					slug = excluded.slug,
					beats = beats + excluded.beats,
					cpu_sum = cpu_sum + excluded.cpu_sum,
					cpu_n = cpu_n + excluded.cpu_n,
					mem_sum = mem_sum + excluded.mem_sum,
					mem_n = mem_n + excluded.mem_n,
					rx_bytes = CASE WHEN excluded.rx_bytes IS NULL THEN rx_bytes
					                ELSE COALESCE(rx_bytes, 0) + excluded.rx_bytes END,
					tx_bytes = CASE WHEN excluded.tx_bytes IS NULL THEN tx_bytes
					                ELSE COALESCE(tx_bytes, 0) + excluded.tx_bytes END,
					net_seconds = net_seconds + excluded.net_seconds,
					no_counters = no_counters + excluded.no_counters,
					traffic_lost = traffic_lost + excluded.traffic_lost
			`, r.Container, r.Hour, r.Slug, r.Beats, r.CPUSum, r.CPUN, r.MemSum, r.MemN,
				nullInt(r.RxBytes), nullInt(r.TxBytes), r.NetSeconds, r.NoCounters, r.TrafficLost); err != nil {
				return err
			}
		}
		for id, b := range next {
			if _, err := tx.Exec(`
				INSERT INTO usage_counters (container_id, rx_bytes, tx_bytes, seen_at) VALUES (?, ?, ?, ?)
				ON CONFLICT(container_id) DO UPDATE SET
					rx_bytes = excluded.rx_bytes, tx_bytes = excluded.tx_bytes, seen_at = excluded.seen_at
			`, id, b.Rx, b.Tx, b.SeenAt); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

func readBaselines(tx *sql.Tx) (map[string]UsageBaseline, error) {
	rows, err := tx.Query(`SELECT container_id, rx_bytes, tx_bytes, seen_at FROM usage_counters`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]UsageBaseline{}
	for rows.Next() {
		var id string
		var b UsageBaseline
		if err := rows.Scan(&id, &b.Rx, &b.Tx, &b.SeenAt); err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, rows.Err()
}

// UsageHours returns every hourly row from since (Unix seconds) on, oldest first.
func (s *Store) UsageHours(since int64) ([]UsageHour, error) {
	rows, err := s.db.Query(`
		SELECT container, hour, slug, beats, cpu_sum, cpu_n, mem_sum, mem_n,
		       rx_bytes, tx_bytes, net_seconds, no_counters, traffic_lost
		FROM usage_hourly WHERE hour >= ? ORDER BY container, hour
	`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageHour
	for rows.Next() {
		var r UsageHour
		var rx, tx sql.NullInt64
		if err := rows.Scan(&r.Container, &r.Hour, &r.Slug, &r.Beats, &r.CPUSum, &r.CPUN, &r.MemSum, &r.MemN,
			&rx, &tx, &r.NetSeconds, &r.NoCounters, &r.TrafficLost); err != nil {
			return nil, err
		}
		if rx.Valid {
			r.RxBytes = &rx.Int64
		}
		if tx.Valid {
			r.TxBytes = &tx.Int64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PurgeUsage drops hourly rows older than UsageRetention and baselines older than
// a day (a baseline is useless after an hour; internal/usage refuses it).
func (s *Store) PurgeUsage(now time.Time) error {
	if _, err := s.execWrite(`DELETE FROM usage_hourly WHERE hour < ?`, now.Add(-UsageRetention).Unix()); err != nil {
		return err
	}
	_, err := s.execWrite(`DELETE FROM usage_counters WHERE seen_at < ?`, float64(now.Add(-24*time.Hour).Unix()))
	return err
}

func nullInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
