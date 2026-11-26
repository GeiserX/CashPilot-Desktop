package store

import (
	"testing"
	"time"
)

// ListPoolBalances keeps every successful reading of one pool, in write order,
// with the payout address each reading came from.
func TestListPoolBalancesKeepsEachReadingWithItsAddress(t *testing.T) {
	s := openTestStore(t)
	ts := func(daysAgo, hour int) string {
		d := time.Now().UTC().AddDate(0, 0, -daysAgo)
		return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, time.UTC).Format(time.RFC3339)
	}
	day := func(daysAgo int) string { return time.Now().UTC().AddDate(0, 0, -daysAgo).Format("2006-01-02") }

	seed := []EarningsRecord{
		{Platform: "supportxmr", Currency: "XMR", Balance: 9, PoolAddressHash: "old", CreatedAt: ts(40, 8)}, // outside the window
		{Platform: "supportxmr", Currency: "XMR", Balance: 0.5, PoolAddressHash: "first", CreatedAt: ts(2, 8)},
		{Platform: "supportxmr", Currency: "XMR", Balance: 0.7, PoolAddressHash: "first", CreatedAt: ts(0, 1)},
		{Platform: "supportxmr", Currency: "XMR", Error: "pool down", CreatedAt: ts(0, 2)},
		{Platform: "ocean", Currency: "BTC", Balance: 0.01, PoolAddressHash: "btc", CreatedAt: ts(0, 3)},
		{Platform: "supportxmr", Currency: "XMR", Balance: 0, PoolAddressHash: "second", CreatedAt: ts(0, 4)},
		// Written last with an earlier clock: the write order still decides.
		{Platform: "supportxmr", Currency: "XMR", Balance: 0.1, PoolAddressHash: "second", CreatedAt: ts(0, 0)},
	}
	for _, r := range seed {
		if _, err := s.SaveEarnings(r); err != nil {
			t.Fatal(err)
		}
	}

	want := []DailyBalance{
		{Platform: "supportxmr", Currency: "XMR", Day: day(2), Balance: 0.5, PoolAddressHash: "first"},
		{Platform: "supportxmr", Currency: "XMR", Day: day(0), Balance: 0.7, PoolAddressHash: "first"},
		{Platform: "supportxmr", Currency: "XMR", Day: day(0), Balance: 0, PoolAddressHash: "second"},
		{Platform: "supportxmr", Currency: "XMR", Day: day(0), Balance: 0.1, PoolAddressHash: "second"},
	}
	got := s.ListPoolBalances("supportxmr", 30)
	if len(got) != len(want) {
		t.Fatalf("ListPoolBalances = %+v; want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reading %d = %+v; want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}
	// The daily series keeps one reading a day, so it cannot show the address change.
	if daily := s.ListDailyBalances(30); len(daily) != 3 {
		t.Fatalf("ListDailyBalances = %+v; want one reading per platform and day", daily)
	}
}

// A row that does not read back fails the whole list, so no partial history
// reaches the monthly accrual.
func TestListPoolBalancesFailsClosed(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.SaveEarnings(EarningsRecord{Platform: "supportxmr", Currency: "XMR", Balance: 0.5, PoolAddressHash: "a"}); err != nil {
		t.Fatal(err)
	}
	if got := s.ListPoolBalances("supportxmr", 1); len(got) != 1 {
		t.Fatalf("ListPoolBalances = %+v; want the valid reading before the bad row", got)
	}
	if _, err := s.db.Exec(`INSERT INTO earnings(platform, balance, currency, created_at) VALUES('supportxmr', 'not a number', 'XMR', ?)`,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if got := s.ListPoolBalances("supportxmr", 1); got != nil {
		t.Fatalf("ListPoolBalances = %+v; want nil when a row does not scan", got)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := s.ListPoolBalances("supportxmr", 1); got != nil {
		t.Fatalf("ListPoolBalances on a closed store = %+v; want nil", got)
	}
}

func TestSaveEarningsIfCredentialsMatchReportsAStoreError(t *testing.T) {
	s := openTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	saved, matched, err := s.SaveEarningsIfCredentialsMatch(EarningsRecord{Platform: "supportxmr", Currency: "XMR"}, func(map[string]string) bool {
		called = true
		return true
	})
	if err == nil || matched || called || saved != (EarningsRecord{}) {
		t.Fatalf("closed store: saved=%+v matched=%v err=%v called=%v; want an error and no comparison", saved, matched, err, called)
	}
}
