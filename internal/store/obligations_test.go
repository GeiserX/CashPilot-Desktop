package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// An install from before the reminders has no obligation_state table. Opening it
// must add the table and keep what was there.
func TestObligationStateMigratesAnOlderDatabase(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveEarnings(EarningsRecord{Platform: "honeygain", Balance: 21, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	raw, err := sql.Open("sqlite", filepath.Join(dir, "cashpilot-desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE obligation_state`); err != nil {
		t.Fatalf("the table should exist after Open: %v", err)
	}
	_ = raw.Close()

	s, err = Open(dir)
	if err != nil {
		t.Fatalf("reopening an older database: %v", err)
	}
	defer s.Close()
	rows, err := s.ListObligationState()
	if err != nil || len(rows) != 0 {
		t.Fatalf("the migrated table lists %+v, %v", rows, err)
	}
	if got := s.ListLatestEarnings(); len(got) != 1 || got[0].Balance != 21 {
		t.Fatalf("the migration lost the earnings: %+v", got)
	}
}

// The anchor and the user's actions share a row and must not wipe each other.
func TestObligationAnchorAndActionsKeepEachOther(t *testing.T) {
	s := openTestStore(t)
	str := func(v string) *string { return &v }
	yes, no := true, false

	if err := s.SetObligationAnchor("honeygain", "keep", "2026-01-01", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetObligationAction("honeygain", "keep", str("2026-01-01"), str("2026-11-24"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetObligationAction("honeygain", "keep", nil, nil, str("2026-12-01"), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetObligationAction("honeygain", "*", nil, nil, nil, &yes); err != nil {
		t.Fatal(err)
	}
	if err := s.SetObligationAnchor("honeygain", "keep", "2026-03-01", false); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListObligationState()
	if err != nil || len(rows) != 2 {
		t.Fatalf("got %+v, %v", rows, err)
	}
	want := ObligationState{Slug: "honeygain", Rule: "keep", AnchorAt: "2026-03-01", DoneFor: "2026-01-01", DoneAt: "2026-11-24", SnoozedUntil: "2026-12-01"}
	if rows[1] != want {
		t.Fatalf("got %+v, want %+v", rows[1], want)
	}
	if rows[0].Rule != "*" || !rows[0].Muted {
		t.Fatalf("mute all lost: %+v", rows[0])
	}

	// Clearing the anchor and unmuting change only those columns.
	if err := s.SetObligationAnchor("honeygain", "keep", "", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetObligationAction("honeygain", "*", nil, nil, nil, &no); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListObligationState()
	if rows[1].AnchorAt != "" || rows[1].DoneFor != "2026-01-01" || rows[0].Muted {
		t.Fatalf("got %+v", rows)
	}
}
