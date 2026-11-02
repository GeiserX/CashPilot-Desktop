package store

import (
	"database/sql"
	"time"
)

// Payout is a cash-out: money that left a service's balance for the user.
//
// The dashboard total is the sum of current balances, so it drops by the amount
// of every cash-out, and on its own it says what is waiting, never what the
// services have made. The payout ledger is the rest of that answer: lifetime
// earned is the current balance plus what was cashed out.
//
// FxRateUSD is what one unit of Currency was worth in USD when the cash-out
// landed, so a token payout keeps the value it had that day instead of being
// restated at today's price. Nil when no rate for that day was known: such a
// payout is counted as unpriced, never as zero.
type Payout struct {
	ID        int64    `json:"id"`
	Platform  string   `json:"platform"`
	Amount    float64  `json:"amount"`
	Currency  string   `json:"currency"`
	FxRateUSD *float64 `json:"fxRateUsd"`
	Method    string   `json:"method"`
	// PaidOn is the day the cash-out happened, YYYY-MM-DD.
	PaidOn    string `json:"paidOn"`
	CreatedAt string `json:"createdAt"`
}

// payoutsSchema is created by migrate. Forward-only: a new table, nothing
// existing is touched. PurgeOldData never reaches it, because a cash-out is part
// of a lifetime figure and dropping old ones would shrink it.
const payoutsSchema = `
	CREATE TABLE IF NOT EXISTS payouts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		platform TEXT NOT NULL,
		amount REAL NOT NULL,
		currency TEXT NOT NULL,
		fx_rate_usd REAL,
		method TEXT NOT NULL DEFAULT '',
		paid_on TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_payouts_platform ON payouts(platform);
`

// AddPayout records a cash-out and returns it with its id. An empty PaidOn is
// today (UTC).
func (s *Store) AddPayout(p Payout) (Payout, error) {
	now := time.Now().UTC()
	if p.PaidOn == "" {
		p.PaidOn = now.Format("2006-01-02")
	}
	p.CreatedAt = now.Format(time.RFC3339Nano)
	var rate sql.NullFloat64
	if p.FxRateUSD != nil {
		rate = sql.NullFloat64{Float64: *p.FxRateUSD, Valid: true}
	}
	err := s.scanWrite(`
		INSERT INTO payouts(platform, amount, currency, fx_rate_usd, method, paid_on, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?) RETURNING id
	`, []any{p.Platform, p.Amount, p.Currency, rate, p.Method, p.PaidOn, p.CreatedAt}, &p.ID)
	return p, err
}

// DeletePayout removes a cash-out recorded by mistake, the way back out of
// AddPayout: a wrong entry would otherwise inflate lifetime earnings with no way
// to correct it. False when no payout has that id.
func (s *Store) DeletePayout(id int64) (bool, error) {
	res, err := s.execWrite(`DELETE FROM payouts WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ListPayouts returns the cash-outs of one platform, or of every platform when
// platform is "", newest first. Unlike the earnings reads it returns its error:
// a ledger that could not be read is unknown, and an empty list would claim
// nothing was ever cashed out.
func (s *Store) ListPayouts(platform string) ([]Payout, error) {
	rows, err := s.db.Query(`
		SELECT id, platform, amount, currency, fx_rate_usd, method, paid_on, created_at
		FROM payouts
		WHERE ? = '' OR platform = ?
		ORDER BY paid_on DESC, id DESC
	`, platform, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Payout{}
	for rows.Next() {
		var p Payout
		var rate sql.NullFloat64
		if err := rows.Scan(&p.ID, &p.Platform, &p.Amount, &p.Currency, &rate, &p.Method, &p.PaidOn, &p.CreatedAt); err != nil {
			return nil, err
		}
		if rate.Valid {
			v := rate.Float64
			p.FxRateUSD = &v
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
