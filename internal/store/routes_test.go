package store

import (
	"database/sql"
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

// A route says a service must never run from this machine's own IP until the owner
// confirms otherwise, so a lost or misread row would let it start unrouted.

func TestRouteRoundTrip(t *testing.T) {
	s := openTestStore(t)

	if id, ok, err := s.GetRoute("earnapp"); err != nil || ok || id != 0 {
		t.Fatalf("a service with no row is not routed: %d %v %v", id, ok, err)
	}
	if before, had, err := s.SetRoute("earnapp", 3); err != nil || had || before != 0 {
		t.Fatalf("a first route has nothing before it: %d %v %v", before, had, err)
	}
	if id, ok, err := s.GetRoute("earnapp"); err != nil || !ok || id != 3 {
		t.Fatalf("GetRoute = %d %v %v, want 3 true", id, ok, err)
	}

	// A second route replaces the first and reports it, so a caller can put it back.
	if before, had, err := s.SetRoute("earnapp", 7); err != nil || !had || before != 3 {
		t.Fatalf("SetRoute over a route = %d %v %v, want 3 true", before, had, err)
	}
	if id, ok, _ := s.GetRoute("earnapp"); !ok || id != 7 {
		t.Fatalf("the newer route wins: %d %v", id, ok)
	}
	if _, _, err := s.SetRoute("honeygain", 3); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListRoutes()
	if err != nil || !maps.Equal(all, map[string]int64{"earnapp": 7, "honeygain": 3}) {
		t.Fatalf("ListRoutes = %v %v", all, err)
	}

	// Deleting one leaves the other, and deleting a missing row is not an error.
	if err := s.DeleteRoute("earnapp"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetRoute("earnapp"); err != nil || ok {
		t.Fatalf("a deleted route is gone: %v %v", ok, err)
	}
	if err := s.DeleteRoute("earnapp"); err != nil {
		t.Fatalf("a second delete: %v", err)
	}
	if all, _ := s.ListRoutes(); !maps.Equal(all, map[string]int64{"honeygain": 3}) {
		t.Fatalf("the other route stays: %v", all)
	}
	// A route deleted and set again starts fresh: nothing before it.
	if before, had, err := s.SetRoute("earnapp", 9); err != nil || had || before != 0 {
		t.Fatalf("SetRoute after a delete = %d %v %v", before, had, err)
	}
}

func TestListRoutesOfANewStoreIsEmpty(t *testing.T) {
	s := openTestStore(t)
	all, err := s.ListRoutes()
	if err != nil || all == nil || len(all) != 0 {
		t.Fatalf("ListRoutes = %#v %v, want an empty map", all, err)
	}
}

// The route outlives the app: a restart must still refuse the service from this IP.
func TestARouteSurvivesAReopen(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s1.SetRoute("earnapp", 4); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if id, ok, err := s2.GetRoute("earnapp"); err != nil || !ok || id != 4 {
		t.Fatalf("after a reopen GetRoute = %d %v %v", id, ok, err)
	}
}

// A row that cannot be read is an error, never "not routed": the service layer fails
// closed on the error (route_unknown), but would start the service on "not routed".
func TestAnUnreadableRouteIsAnErrorNotAbsent(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO routes(slug, proxy_id, created_at) VALUES('earnapp', 'not-a-number', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetRoute("earnapp"); err == nil || ok {
		t.Fatalf("GetRoute of a bad row = %v %v, want an error", ok, err)
	}
	// SetRoute reads the row it replaces first, so it refuses rather than guess.
	if _, _, err := s.SetRoute("earnapp", 2); err == nil {
		t.Fatal("SetRoute over a bad row must report it")
	}
	if all, err := s.ListRoutes(); err == nil {
		t.Fatalf("ListRoutes with a bad row = %v, want an error", all)
	}
}

func TestRouteCallsOnAClosedStoreFail(t *testing.T) {
	s := openTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetRoute("earnapp"); err == nil || ok {
		t.Fatalf("GetRoute = %v %v", ok, err)
	}
	if _, err := s.ListRoutes(); err == nil {
		t.Fatal("ListRoutes on a closed store")
	}
	if err := s.DeleteRoute("earnapp"); err == nil {
		t.Fatal("DeleteRoute on a closed store")
	}
}

// A database whose routes table cannot be made does not open: the app would otherwise
// run with no way to keep a service routed.
func TestOpenFailsWhenTheRoutesTableCannotBeMade(t *testing.T) {
	dir := t.TempDir()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "cashpilot-desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE squat(x); CREATE INDEX routes ON squat(x);`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err == nil {
		s.Close()
		t.Fatal("Open succeeded without a routes table")
	}
	if !strings.Contains(err.Error(), "routes") {
		t.Fatalf("the error names the table: %v", err)
	}
}
