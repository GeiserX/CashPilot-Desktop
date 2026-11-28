package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// An install from before the copies kept their ticks has proxied_copies without the
// column. Opening it adds the column, and the copies it held have no ticks.
func TestProxiedCopiesMigrateAnOlderDatabase(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "cashpilot-desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE proxied_copies`,
		`CREATE TABLE proxied_copies (proxy_id INTEGER NOT NULL, slug TEXT NOT NULL,
			stopped_by_cap INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, PRIMARY KEY (proxy_id, slug))`,
		`INSERT INTO proxied_copies(proxy_id, slug, stopped_by_cap, created_at) VALUES(1, 'honeygain', 1, datetime('now'))`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = raw.Close()

	s, err = Open(dir)
	if err != nil {
		t.Fatalf("reopening an older database: %v", err)
	}
	defer s.Close()
	copies, err := s.ListProxiedCopies(0)
	if err != nil || len(copies) != 1 || !copies[0].StoppedByCap || len(copies[0].Acknowledged) != 0 {
		t.Fatalf("the old copy: %+v %v", copies, err)
	}
	if err := s.SaveProxiedCopy(1, "honeygain", []string{"account_risk"}); err != nil {
		t.Fatal(err)
	}
	copies, _ = s.ListProxiedCopies(1)
	if len(copies) != 1 || copies[0].StoppedByCap || len(copies[0].Acknowledged) != 1 || copies[0].Acknowledged[0] != "account_risk" {
		t.Fatalf("added again with the tick: %+v", copies)
	}
}
