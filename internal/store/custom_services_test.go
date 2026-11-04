package store

import (
	"strings"
	"testing"
)

// A definition is stored encrypted, a deleted one leaves a tombstone whose slug is
// never free again, and only live ones are listed, read, edited or deleted.
func TestCustomServicesStore(t *testing.T) {
	s := openTestStore(t)
	def := []byte(`{"name":"Mine","env":[{"key":"TOKEN","value":"s3cret-token","secret":true}]}`)
	if err := s.InsertCustomService("custom-mine", def); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCustomService("custom-mine", def); err == nil {
		t.Fatal("a second insert under the same slug succeeded")
	}
	var raw string
	if err := s.db.QueryRow(`SELECT value FROM custom_services WHERE slug = 'custom-mine'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "s3cret-token") || strings.Contains(raw, "TOKEN") {
		t.Fatalf("the definition is stored in clear: %q", raw)
	}
	got, ok, err := s.GetCustomService("custom-mine")
	if err != nil || !ok || string(got.Definition) != string(def) {
		t.Fatalf("GetCustomService = %q, %v, %v", got.Definition, ok, err)
	}
	if err := s.UpdateCustomService("custom-mine", []byte(`{"name":"Renamed"}`)); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListCustomServices(); err != nil || len(list) != 1 || string(list[0].Definition) != `{"name":"Renamed"}` {
		t.Fatalf("ListCustomServices = %v, %v", list, err)
	}
	if err := s.DeleteCustomService("custom-mine"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListCustomServices(); len(list) != 0 {
		t.Fatalf("a deleted service is still listed: %v", list)
	}
	if _, ok, _ := s.GetCustomService("custom-mine"); ok {
		t.Fatal("a deleted service is still readable")
	}
	if err := s.UpdateCustomService("custom-mine", def); err != ErrCustomServiceNotFound {
		t.Fatalf("update of a deleted service = %v", err)
	}
	if err := s.DeleteCustomService("custom-mine"); err != ErrCustomServiceNotFound {
		t.Fatalf("second delete = %v", err)
	}
	if err := s.InsertCustomService("custom-mine", def); err == nil {
		t.Fatal("a deleted slug was taken again")
	}
	if taken, err := s.CustomServiceSlugs(); err != nil || !taken["custom-mine"] {
		t.Fatalf("CustomServiceSlugs = %v, %v, want the tombstone in it", taken, err)
	}
	if err := s.db.QueryRow(`SELECT value FROM custom_services WHERE slug = 'custom-mine'`).Scan(&raw); err != nil || raw != "" {
		t.Fatalf("a tombstone kept its definition: %q, %v", raw, err)
	}
}
