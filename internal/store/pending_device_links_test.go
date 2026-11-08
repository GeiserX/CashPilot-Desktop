package store

import (
	"testing"
	"time"
)

func TestPendingDeviceLinks(t *testing.T) {
	s := openTestStore(t)
	t0 := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

	if rows, err := s.ListPendingDeviceLinks(); err != nil || len(rows) != 0 {
		t.Fatalf("a new store lists %+v, %v", rows, err)
	}
	if err := s.SavePendingDeviceLink("proxyrack", "AA", t0, t0.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePendingDeviceLink("proxyrack", "BB", t0, t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListPendingDeviceLinks()
	if err != nil || len(rows) != 2 || rows[0].DeviceID != "BB" || rows[1].DeviceID != "AA" {
		t.Fatalf("want both rows, soonest retry first, got %+v, %v", rows, err)
	}
	if r := rows[1]; r.Slug != "proxyrack" || r.Attempts != 0 || r.GaveUp || !r.FirstAt.Equal(t0) || !r.NextAt.Equal(t0.Add(6*time.Minute)) {
		t.Fatalf("row AA = %+v", r)
	}

	// A second save of a live row keeps its window, its attempts and its earliest try.
	if err := s.UpdatePendingDeviceLink("proxyrack", "AA", 2, t0.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	later := t0.Add(2 * time.Hour)
	if err := s.SavePendingDeviceLink("proxyrack", "AA", later, later.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	aa := find(t, s, "AA")
	if aa.Attempts != 2 || !aa.FirstAt.Equal(t0) || !aa.NextAt.Equal(t0.Add(time.Hour)) || aa.GaveUp {
		t.Fatalf("a live row was reset by a second save: %+v", aa)
	}

	// One that gave up starts a new window.
	if err := s.UpdatePendingDeviceLink("proxyrack", "AA", 9, t0.Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	if !find(t, s, "AA").GaveUp {
		t.Fatal("UpdatePendingDeviceLink did not mark the row given up")
	}
	if err := s.SavePendingDeviceLink("proxyrack", "AA", later, later.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	aa = find(t, s, "AA")
	if aa.Attempts != 0 || !aa.FirstAt.Equal(later) || !aa.NextAt.Equal(later.Add(6*time.Minute)) || aa.GaveUp {
		t.Fatalf("a given-up row did not start a new window: %+v", aa)
	}

	if err := s.DeletePendingDeviceLink("proxyrack", "AA"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.ListPendingDeviceLinks(); len(rows) != 1 || rows[0].DeviceID != "BB" {
		t.Fatalf("after deleting AA, want only BB, got %+v", rows)
	}
}

func find(t *testing.T, s *Store, id string) PendingDeviceLink {
	t.Helper()
	rows, err := s.ListPendingDeviceLinks()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.DeviceID == id {
			return r
		}
	}
	t.Fatalf("no row for %s in %+v", id, rows)
	return PendingDeviceLink{}
}
