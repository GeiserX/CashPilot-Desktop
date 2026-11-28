package store

import (
	"testing"
	"time"
)

func TestImageUpdates(t *testing.T) {
	s := openTestStore(t)
	if rows, err := s.ListImageUpdates(10); err != nil || len(rows) != 0 {
		t.Fatalf("a new store lists %+v, %v", rows, err)
	}
	t0 := time.Date(2026, 11, 28, 4, 0, 0, 0, time.UTC)
	for i, u := range []ImageUpdate{
		{At: t0, Slug: "honeygain", Result: "updated", OldImage: "sha256:a", NewImage: "sha256:b"},
		{At: t0.Add(time.Minute), Slug: "iproyal", Result: "rolled_back", NewImage: "sha256:c", Detail: "it stopped"},
	} {
		if err := s.RecordImageUpdate(u); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
	rows, err := s.ListImageUpdates(10)
	if err != nil || len(rows) != 2 || rows[0].Slug != "iproyal" || rows[1].Slug != "honeygain" {
		t.Fatalf("want both rows, newest first, got %+v, %v", rows, err)
	}
	if r := rows[0]; r.Result != "rolled_back" || r.Detail != "it stopped" || !r.At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("row = %+v", r)
	}
	for _, c := range []struct {
		slug, image string
		results     []string
		want        bool
	}{
		{"iproyal", "sha256:c", []string{"rolled_back"}, true},
		{"iproyal", "sha256:c", []string{"available", "rolled_back"}, true},
		{"iproyal", "sha256:c", []string{"available"}, false},
		{"iproyal", "sha256:d", []string{"rolled_back"}, false},
		{"honeygain", "sha256:c", []string{"rolled_back"}, false},
	} {
		if got, err := s.ImageUpdateSeen(c.slug, c.image, c.results...); err != nil || got != c.want {
			t.Errorf("seen(%s, %s, %v) = %v, %v; want %v", c.slug, c.image, c.results, got, err, c.want)
		}
	}
}

func TestImageUpdatesKeepsTheNewest(t *testing.T) {
	s := openTestStore(t)
	if err := s.RecordImageUpdate(ImageUpdate{Slug: "a", Result: "rolled_back", NewImage: "sha256:bad"}); err != nil {
		t.Fatal(err)
	}
	for i := range imageUpdatesKept + 3 {
		if err := s.RecordImageUpdate(ImageUpdate{Slug: "a", Result: "available", NewImage: string(rune('a' + i%26))}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListImageUpdates(imageUpdatesKept + 10)
	if err != nil || len(rows) != imageUpdatesKept+1 {
		t.Fatalf("kept %d rows, %v; want %d and the held build", len(rows), err, imageUpdatesKept)
	}
	// Pruning never releases a held build.
	if held, err := s.ImageUpdateSeen("a", "sha256:bad", "rolled_back"); err != nil || !held {
		t.Fatalf("the held build was pruned: %v, %v", held, err)
	}
}

func TestImageUpdatesLatestAndCleared(t *testing.T) {
	s := openTestStore(t)
	for _, u := range []ImageUpdate{
		{Slug: "a", Result: "available", NewImage: "sha256:1"},
		{Slug: "b", Result: "failed", NewImage: "sha256:2"},
		{Slug: "a", Result: "rolled_back", NewImage: "sha256:3"},
	} {
		if err := s.RecordImageUpdate(u); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := s.LatestImageUpdates()
	if err != nil || len(latest) != 2 || latest[0].Slug != "a" || latest[0].Result != "rolled_back" || latest[1].Slug != "b" {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	if err := s.ClearImageUpdates("a"); err != nil {
		t.Fatal(err)
	}
	latest, _ = s.LatestImageUpdates()
	if !latest[0].Cleared || latest[1].Cleared {
		t.Fatalf("only a's rows are answered: %+v", latest)
	}
	// Clearing leaves the hold in place.
	if held, _ := s.ImageUpdateSeen("a", "sha256:3", "rolled_back"); !held {
		t.Fatal("clearing released the held build")
	}
	// A cleared offer does not hide the same build offered again.
	if seen, _ := s.ImageUpdateSeen("a", "sha256:1", "available"); seen {
		t.Fatal("a cleared offer still counts, so the same build offered again never reaches the bell")
	}
	if seen, _ := s.ImageUpdateSeen("b", "sha256:2", "failed"); !seen {
		t.Fatal("an offer nobody cleared no longer counts")
	}
	// A newer row comes back into the bell.
	if err := s.RecordImageUpdate(ImageUpdate{Slug: "a", Result: "available", NewImage: "sha256:4"}); err != nil {
		t.Fatal(err)
	}
	if latest, _ = s.LatestImageUpdates(); latest[0].Cleared {
		t.Fatal("a new row starts cleared")
	}
}
