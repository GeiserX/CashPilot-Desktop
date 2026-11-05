package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"
)

// Activity (internal/activity): what each earner did on the network, per
// 5-minute window. The same three tables as the web repository, without a
// worker column: this machine is the only one.
//
// activity_samples holds counts and socket peaks, never an address; a NULL
// counter means no earlier reading to difference against, never zero.
// activity_dest_raw holds the sampled destinations WITH the remote address:
// other people's traffic, kept 24 hours, purged on every write and hourly with
// secure_delete on, then the WAL is folded back. activity_hourly folds them per
// hour and dimension so history outlives the raw rows with no address.
const activitySchema = `
	CREATE TABLE IF NOT EXISTS activity_samples (
		container      TEXT    NOT NULL,
		window_end     INTEGER NOT NULL,
		status         TEXT    NOT NULL,
		reason         TEXT    NOT NULL DEFAULT '',
		members        TEXT    NOT NULL DEFAULT '[]',
		network_mode   TEXT    NOT NULL DEFAULT '',
		opens          INTEGER,
		passive_opens  INTEGER,
		attempt_fails  INTEGER,
		resets         INTEGER,
		udp_in         INTEGER,
		udp_out        INTEGER,
		est_max        INTEGER,
		syn_sent_max   INTEGER,
		udp_bound      INTEGER,
		listen_ports   TEXT    NOT NULL DEFAULT '[]',
		remotes_total  INTEGER,
		truncated      INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (container, window_end)
	);
	CREATE INDEX IF NOT EXISTS idx_activity_samples_window ON activity_samples(window_end);
	CREATE TABLE IF NOT EXISTS activity_dest_raw (
		container   TEXT    NOT NULL,
		window_end  INTEGER NOT NULL,
		dir         TEXT    NOT NULL,
		proto       TEXT    NOT NULL,
		ip          TEXT    NOT NULL,
		port        INTEGER NOT NULL,
		class       TEXT    NOT NULL,
		asn         TEXT    NOT NULL DEFAULT '',
		cc          TEXT    NOT NULL DEFAULT '',
		seen        INTEGER NOT NULL DEFAULT 1
	);
	CREATE INDEX IF NOT EXISTS idx_activity_dest_raw_window ON activity_dest_raw(window_end);
	CREATE INDEX IF NOT EXISTS idx_activity_dest_raw_container ON activity_dest_raw(container, window_end);
	CREATE TABLE IF NOT EXISTS activity_hourly (
		container  TEXT    NOT NULL,
		hour       INTEGER NOT NULL,
		dim        TEXT    NOT NULL CHECK (dim IN ('port', 'asn', 'cc', 'class', 'oclass', 'dir')),
		value      TEXT    NOT NULL,
		sightings  INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (container, hour, dim, value)
	);
	CREATE INDEX IF NOT EXISTS idx_activity_hourly_hour ON activity_hourly(hour);
	CREATE TABLE IF NOT EXISTS activity_alerts (
		kind        TEXT    NOT NULL,
		subject     TEXT    NOT NULL,
		message     TEXT    NOT NULL,
		created_at  INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_activity_alerts_subject ON activity_alerts(kind, subject, created_at);
`

const (
	// ActivityRawSeconds is how long a remote address is kept.
	ActivityRawSeconds = 86400
	// ActivityRetention is how long samples, hourly totals and alerts are kept.
	ActivityRetention = 35 * 24 * time.Hour
)

func (s *Store) migrateActivity() error {
	_, err := s.execWrite(activitySchema)
	return err
}

// ActivityDest is one destination to store, with its network and country.
type ActivityDest struct {
	Dir, Proto, IP, Class, ASN, CC string
	Port, Seen                     int
}

// ActivitySample is one stored row of activity_samples.
type ActivitySample struct {
	Container    string         `json:"container"`
	WindowEnd    int64          `json:"windowEnd"`
	Status       string         `json:"status"`
	Reason       string         `json:"reason"`
	Members      []string       `json:"members"`
	NetworkMode  string         `json:"networkMode"`
	Opens        *int64         `json:"opens"`
	PassiveOpens *int64         `json:"passiveOpens"`
	AttemptFails *int64         `json:"attemptFails"`
	Resets       *int64         `json:"resets"`
	UDPIn        *int64         `json:"udpIn"`
	UDPOut       *int64         `json:"udpOut"`
	EstMax       *int64         `json:"estMax"`
	SynSentMax   *int64         `json:"synSentMax"`
	UDPBound     *int64         `json:"udpBound"`
	ListenPorts  []int          `json:"listenPorts"`
	RemotesTotal *int64         `json:"remotesTotal"`
	Truncated    bool           `json:"truncated"`
	Dests        []ActivityDest `json:"-"`
}

func activityHour(windowEnd int64) int64 { return (windowEnd - 1) / 3600 * 3600 }

// withSecureDelete runs fn on the store's one connection with secure_delete on,
// so a deleted address is overwritten on disk and not left in a free page.
func (s *Store) withSecureDelete(fn func(c *sql.Conn) error) error {
	return retryBusy(func() error {
		ctx := context.Background()
		c, err := s.db.Conn(ctx)
		if err != nil {
			return err
		}
		defer c.Close()
		if _, err := c.ExecContext(ctx, "PRAGMA secure_delete=ON"); err != nil {
			return err
		}
		defer func() { _, _ = c.ExecContext(ctx, "PRAGMA secure_delete=OFF") }()
		return fn(c)
	})
}

// RecordActivity stores one window. A window already stored for a container is
// ignored whole, so a retried write cannot count its destinations twice. The
// raw purge runs in the same transaction. wanted is read inside the write lock:
// when it says no (Activity was switched off meanwhile), nothing is stored.
func (s *Store) RecordActivity(windowEnd int64, samples []ActivitySample, now time.Time, wanted func() bool) (int, error) {
	written := 0
	hour := activityHour(windowEnd)
	err := s.withSecureDelete(func(c *sql.Conn) error {
		written = 0
		ctx := context.Background()
		tx, err := c.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if wanted != nil && !wanted() {
			return nil
		}
		for _, sm := range samples {
			members, _ := json.Marshal(nonNilStrings(sm.Members))
			ports, _ := json.Marshal(nonNilInts(sm.ListenPorts))
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO activity_samples (container, window_end, status, reason, members,
				network_mode, opens, passive_opens, attempt_fails, resets, udp_in, udp_out, est_max, syn_sent_max, udp_bound,
				listen_ports, remotes_total, truncated) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				sm.Container, windowEnd, sm.Status, sm.Reason, string(members), sm.NetworkMode, sm.Opens, sm.PassiveOpens,
				sm.AttemptFails, sm.Resets, sm.UDPIn, sm.UDPOut, sm.EstMax, sm.SynSentMax, sm.UDPBound, string(ports),
				sm.RemotesTotal, boolInt(sm.Truncated))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			written++
			dims := map[[2]string]int{}
			var order [][2]string
			add := func(k [2]string, n int) {
				if _, ok := dims[k]; !ok {
					order = append(order, k)
				}
				dims[k] += n
			}
			for _, d := range sm.Dests {
				asn, cc := "", ""
				if d.Class == "public" {
					asn, cc = d.ASN, d.CC
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO activity_dest_raw (container, window_end, dir, proto, ip, port, class, asn, cc, seen)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, sm.Container, windowEnd, d.Dir, d.Proto, d.IP, d.Port, d.Class, asn, cc, d.Seen); err != nil {
					return err
				}
				add([2]string{"dir", d.Dir}, d.Seen)
				add([2]string{"class", d.Class}, d.Seen)
				if d.Dir == "out" {
					add([2]string{"port", d.Proto + "/" + strconv.Itoa(d.Port)}, d.Seen)
					add([2]string{"oclass", d.Class}, d.Seen)
				}
				if asn != "" {
					add([2]string{"asn", asn}, d.Seen)
				}
				if cc != "" {
					add([2]string{"cc", cc}, d.Seen)
				}
			}
			for _, k := range order {
				if _, err := tx.ExecContext(ctx, `INSERT INTO activity_hourly (container, hour, dim, value, sightings) VALUES (?, ?, ?, ?, ?)
					ON CONFLICT(container, hour, dim, value) DO UPDATE SET sightings = sightings + excluded.sightings`,
					sm.Container, hour, k[0], k[1], dims[k]); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM activity_dest_raw WHERE window_end < ?", now.Unix()-ActivityRawSeconds); err != nil {
			return err
		}
		return tx.Commit()
	})
	return written, err
}

// PurgeActivity deletes raw destinations older than a day and every other
// Activity row older than ActivityRetention, then folds the WAL back into the
// file so the deleted addresses are not left in it. busy is true when a reader
// kept the checkpoint from finishing; the next purge tries again.
func (s *Store) PurgeActivity(now time.Time) (busy bool, err error) {
	cutoff := now.Add(-ActivityRetention).Unix()
	err = s.withSecureDelete(func(c *sql.Conn) error {
		ctx := context.Background()
		for _, q := range []struct {
			sql string
			arg int64
		}{
			{"DELETE FROM activity_dest_raw WHERE window_end < ?", now.Unix() - ActivityRawSeconds},
			{"DELETE FROM activity_samples WHERE window_end < ?", cutoff},
			{"DELETE FROM activity_hourly WHERE hour < ?", cutoff},
			{"DELETE FROM activity_alerts WHERE created_at < ?", cutoff},
		} {
			if _, err := c.ExecContext(ctx, q.sql, q.arg); err != nil {
				return err
			}
		}
		var cerr error
		busy, cerr = checkpoint(ctx, c)
		return cerr
	})
	return busy, err
}

// DeleteActivity deletes the raw destinations, or with all every Activity row.
// busy is true when a reader kept the database log from being emptied: the
// deleted rows may stay in it until the next hourly purge.
func (s *Store) DeleteActivity(all bool) (busy bool, err error) {
	err = s.withSecureDelete(func(c *sql.Conn) error {
		ctx := context.Background()
		tables := []string{"activity_dest_raw"}
		if all {
			tables = append(tables, "activity_samples", "activity_hourly", "activity_alerts")
		}
		for _, t := range tables {
			if _, err := c.ExecContext(ctx, "DELETE FROM "+t); err != nil {
				return err
			}
		}
		var cerr error
		busy, cerr = checkpoint(ctx, c)
		return cerr
	})
	return busy, err
}

// checkpoint empties the database log. busy is SQLite's own answer that a
// reader kept it from finishing; a failed query is an error, not busy.
func checkpoint(ctx context.Context, c *sql.Conn) (bool, error) {
	var busy, logFrames, done int
	if err := c.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &done); err != nil {
		return false, err
	}
	return busy != 0, nil
}

// ActivityLatest is the newest sample of every container seen since since.
func (s *Store) ActivityLatest(since int64) ([]ActivitySample, error) {
	rows, err := s.db.Query(`SELECT s.container, s.window_end, s.status, s.reason, s.members, s.network_mode, s.opens, s.passive_opens,
		s.attempt_fails, s.resets, s.udp_in, s.udp_out, s.est_max, s.syn_sent_max, s.udp_bound, s.listen_ports, s.remotes_total, s.truncated
		FROM activity_samples s JOIN (SELECT container, MAX(window_end) AS last FROM activity_samples WHERE window_end >= ? GROUP BY container) t
		ON s.container = t.container AND s.window_end = t.last ORDER BY s.container`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivitySample
	for rows.Next() {
		var a ActivitySample
		var members, ports string
		var trunc int
		if err := rows.Scan(&a.Container, &a.WindowEnd, &a.Status, &a.Reason, &members, &a.NetworkMode, &a.Opens, &a.PassiveOpens,
			&a.AttemptFails, &a.Resets, &a.UDPIn, &a.UDPOut, &a.EstMax, &a.SynSentMax, &a.UDPBound, &ports, &a.RemotesTotal, &trunc); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(members), &a.Members)
		_ = json.Unmarshal([]byte(ports), &a.ListenPorts)
		a.Members, a.ListenPorts = nonNilStrings(a.Members), nonNilInts(a.ListenPorts)
		a.Truncated = trunc != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActivityPoint is one point of a container's series: opens, failures and peak open.
type ActivityPoint struct {
	Container    string `json:"-"`
	T            int64  `json:"t"`
	Opens        *int64 `json:"opens"`
	AttemptFails *int64 `json:"attemptFails"`
	EstMax       *int64 `json:"estMax"`
}

// ActivitySeries is per window, or summed per hour in SQL: SUM over only NULLs
// is NULL, so an hour with no counted window stays unknown.
func (s *Store) ActivitySeries(since int64, hourly bool, container string) ([]ActivityPoint, error) {
	q := `SELECT container, window_end, opens, attempt_fails, est_max FROM activity_samples WHERE window_end >= ?`
	if hourly {
		q = `SELECT container, (window_end - 1) / 3600 * 3600 AS t, SUM(opens), SUM(attempt_fails), MAX(est_max)
			FROM activity_samples WHERE window_end >= ?`
	}
	args := []any{since}
	if container != "" {
		q += " AND container = ?"
		args = append(args, container)
	}
	if hourly {
		q += " GROUP BY container, t"
	}
	q += " ORDER BY 1, 2"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityPoint
	for rows.Next() {
		var p ActivityPoint
		if err := rows.Scan(&p.Container, &p.T, &p.Opens, &p.AttemptFails, &p.EstMax); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ActivityDim is the sightings of one value of one dimension over a period.
type ActivityDim struct {
	Container string
	Dim       string
	Value     string
	Sightings int64
}

// ActivityDims sums sightings per container, dimension and value since sinceHour.
func (s *Store) ActivityDims(sinceHour int64, container string) ([]ActivityDim, error) {
	q := `SELECT container, dim, value, SUM(sightings) FROM activity_hourly WHERE hour >= ?`
	args := []any{sinceHour}
	if container != "" {
		q += " AND container = ?"
		args = append(args, container)
	}
	q += " GROUP BY container, dim, value"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityDim
	for rows.Next() {
		var d ActivityDim
		if err := rows.Scan(&d.Container, &d.Dim, &d.Value, &d.Sightings); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ActivityRawRow is one raw destination still inside the one-day window.
type ActivityRawRow struct {
	WindowEnd int64  `json:"windowEnd"`
	Dir       string `json:"dir"`
	Proto     string `json:"proto"`
	IP        string `json:"ip"`
	Port      int    `json:"port"`
	Class     string `json:"class"`
	ASN       string `json:"asn"`
	CC        string `json:"cc"`
	Seen      int    `json:"seen"`
}

// ActivityRaw is the newest raw destinations of one container, at most 2000.
func (s *Store) ActivityRaw(container string, now time.Time) ([]ActivityRawRow, error) {
	rows, err := s.db.Query(`SELECT window_end, dir, proto, ip, port, class, asn, cc, seen FROM activity_dest_raw
		WHERE container = ? AND window_end >= ? ORDER BY window_end DESC, seen DESC LIMIT 2000`, container, now.Unix()-ActivityRawSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityRawRow
	for rows.Next() {
		var r ActivityRawRow
		if err := rows.Scan(&r.WindowEnd, &r.Dir, &r.Proto, &r.IP, &r.Port, &r.Class, &r.ASN, &r.CC, &r.Seen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActivityOpensHourly is connections opened per container and hour since
// sinceHour; nil when most windows of the hour had no count.
func (s *Store) ActivityOpensHourly(sinceHour int64) (map[string]map[int64]*int64, error) {
	rows, err := s.db.Query(`SELECT container, (window_end - 1) / 3600 * 3600 AS hour,
		CASE WHEN COUNT(opens) * 2 >= COUNT(*) THEN SUM(opens) END FROM activity_samples WHERE window_end > ?
		GROUP BY container, hour`, sinceHour)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[int64]*int64{}
	for rows.Next() {
		var c string
		var h int64
		var v *int64
		if err := rows.Scan(&c, &h, &v); err != nil {
			return nil, err
		}
		if out[c] == nil {
			out[c] = map[int64]*int64{}
		}
		out[c][h] = v
	}
	return out, rows.Err()
}

// ActivityAlert is one alert raised.
type ActivityAlert struct {
	Kind      string `json:"kind"`
	Subject   string `json:"subject"`
	Message   string `json:"message"`
	CreatedAt int64  `json:"createdAt"`
}

// RaiseActivityAlert stores an alert unless the same kind and subject was
// raised within cooldown. It reports whether it stored one.
func (s *Store) RaiseActivityAlert(a ActivityAlert, cooldown time.Duration) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM activity_alerts WHERE kind = ? AND subject = ? AND created_at > ?`,
		a.Kind, a.Subject, a.CreatedAt-int64(cooldown.Seconds())).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	_, err := s.execWrite(`INSERT INTO activity_alerts (kind, subject, message, created_at) VALUES (?, ?, ?, ?)`,
		a.Kind, a.Subject, a.Message, a.CreatedAt)
	return err == nil, err
}

// ActivityAlerts is every alert raised since since, newest first.
func (s *Store) ActivityAlerts(since int64) ([]ActivityAlert, error) {
	rows, err := s.db.Query(`SELECT kind, subject, message, created_at FROM activity_alerts WHERE created_at >= ? ORDER BY created_at DESC LIMIT 200`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityAlert
	for rows.Next() {
		var a ActivityAlert
		if err := rows.Scan(&a.Kind, &a.Subject, &a.Message, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilInts(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}
