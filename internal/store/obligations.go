package store

import "database/sql"

// ObligationState is what the user did with one reminder (internal/obligations),
// and the clock start the app saw. One row per (service, rule). Rule is a catalog
// obligation id, "cash-out", or "*" (every reminder of the service). Dates are
// "YYYY-MM-DD"; "" is none. AnchorAt outlives the earnings purge, which a
// 730-day clock needs.
type ObligationState struct {
	Slug           string
	Rule           string
	AnchorAt       string
	AnchorCensored bool
	DoneFor        string
	DoneAt         string
	SnoozedUntil   string
	Muted          bool
}

// obligationStateDDL is created by migrate. Forward-only: a new table, nothing
// existing is touched. The same columns as the web app's obligation_state, less
// updated_by: Desktop has one user. PurgeOldData never reaches it.
const obligationStateDDL = `
	CREATE TABLE IF NOT EXISTS obligation_state (
		slug TEXT NOT NULL,
		rule TEXT NOT NULL,
		anchor_at TEXT,
		anchor_censored INTEGER NOT NULL DEFAULT 0,
		done_for TEXT,
		done_at TEXT,
		snoozed_until TEXT,
		muted INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (slug, rule)
	);
`

// ListObligationState returns every row. Unlike the earnings reads it returns its
// error: state that could not be read is unknown, and an empty list would bring
// back every reminder the user closed.
func (s *Store) ListObligationState() ([]ObligationState, error) {
	rows, err := s.db.Query(`
		SELECT slug, rule, COALESCE(anchor_at, ''), anchor_censored, COALESCE(done_for, ''),
		       COALESCE(done_at, ''), COALESCE(snoozed_until, ''), muted
		FROM obligation_state ORDER BY slug, rule
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ObligationState{}
	for rows.Next() {
		var r ObligationState
		if err := rows.Scan(&r.Slug, &r.Rule, &r.AnchorAt, &r.AnchorCensored, &r.DoneFor, &r.DoneAt, &r.SnoozedUntil, &r.Muted); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullable(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: *s != ""}
}

// SetObligationAnchor stores the clock start of one (service, rule), or clears it
// when anchorAt is "". Only the anchor columns change: a Done, Snooze or mute on
// the same row stays.
func (s *Store) SetObligationAnchor(slug, rule, anchorAt string, censored bool) error {
	_, err := s.execWrite(`
		INSERT INTO obligation_state(slug, rule, anchor_at, anchor_censored) VALUES(?, ?, ?, ?)
		ON CONFLICT(slug, rule) DO UPDATE SET anchor_at = excluded.anchor_at,
			anchor_censored = excluded.anchor_censored, updated_at = datetime('now')
	`, slug, rule, nullable(&anchorAt), censored)
	return err
}

// SetObligationAction records what the user did with one reminder. A nil field
// keeps its stored value, and the anchor columns are never touched.
func (s *Store) SetObligationAction(slug, rule string, doneFor, doneAt, snoozedUntil *string, muted *bool) error {
	var mute sql.NullBool
	if muted != nil {
		mute = sql.NullBool{Bool: *muted, Valid: true}
	}
	_, err := s.execWrite(`
		INSERT INTO obligation_state(slug, rule, done_for, done_at, snoozed_until, muted)
		VALUES(?1, ?2, ?3, ?4, ?5, COALESCE(?6, 0))
		ON CONFLICT(slug, rule) DO UPDATE SET
			done_for = COALESCE(?3, done_for),
			done_at = COALESCE(?4, done_at),
			snoozed_until = COALESCE(?5, snoozed_until),
			muted = COALESCE(?6, muted),
			updated_at = datetime('now')
	`, slug, rule, nullable(doneFor), nullable(doneAt), nullable(snoozedUntil), mute)
	return err
}
