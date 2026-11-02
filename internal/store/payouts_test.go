package store

import "testing"

// The payout ledger round trip at the store level: a cash-out is stored with its
// rate (or none), listed newest first per platform or all together, and removed
// only by its own id.
func TestPayoutLedgerRoundTrip(t *testing.T) {
	s := openTestStore(t)

	rate := 0.25
	myst, err := s.AddPayout(Payout{Platform: "mysterium", Amount: 40, Currency: "MYST", FxRateUSD: &rate, PaidOn: "2026-10-01"})
	if err != nil {
		t.Fatal(err)
	}
	if myst.ID == 0 || myst.CreatedAt == "" {
		t.Fatalf("AddPayout did not fill id and created_at: %+v", myst)
	}
	today, err := s.AddPayout(Payout{Platform: "earnapp", Amount: 37.54, Currency: "USD", Method: "PayPal"})
	if err != nil {
		t.Fatal(err)
	}
	if today.PaidOn == "" {
		t.Fatal("an empty PaidOn must default to today")
	}
	if _, err := s.AddPayout(Payout{Platform: "mysterium", Amount: 5, Currency: "MYST", PaidOn: "2026-10-20"}); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListPayouts("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Platform != "earnapp" {
		t.Fatalf("all payouts, newest first: got %+v", all)
	}
	mine, err := s.ListPayouts("mysterium")
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 || mine[0].PaidOn != "2026-10-20" || mine[1].PaidOn != "2026-10-01" {
		t.Fatalf("mysterium payouts, newest first: got %+v", mine)
	}
	if mine[0].FxRateUSD != nil {
		t.Fatalf("a payout stored without a rate must come back unpriced, got %v", *mine[0].FxRateUSD)
	}
	if mine[1].FxRateUSD == nil || *mine[1].FxRateUSD != 0.25 {
		t.Fatalf("the stored rate must come back: got %v", mine[1].FxRateUSD)
	}

	removed, err := s.DeletePayout(myst.ID)
	if err != nil || !removed {
		t.Fatalf("DeletePayout(%d) = %v, %v; want true, nil", myst.ID, removed, err)
	}
	removed, err = s.DeletePayout(myst.ID)
	if err != nil || removed {
		t.Fatalf("deleting the same id twice = %v, %v; want false, nil", removed, err)
	}
	if left, _ := s.ListPayouts("mysterium"); len(left) != 1 {
		t.Fatalf("one mysterium payout should remain, got %+v", left)
	}
}

// A store that cannot be read reports the ledger as an error, never as an empty
// list: empty would claim nothing was ever cashed out.
func TestPayoutLedgerUnreadableIsAnError(t *testing.T) {
	s := openTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListPayouts(""); err == nil {
		t.Fatalf("ListPayouts on a closed store = %+v, nil; want an error", got)
	}
}
