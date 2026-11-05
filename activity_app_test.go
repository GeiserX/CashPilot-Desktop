package main

import (
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

func TestBuildActivityRows(t *testing.T) {
	opens := int64(4)
	latest := []store.ActivitySample{
		{Container: "a", Status: "ok", WindowEnd: 600, Opens: &opens},
		{Container: "ab", Status: "ok", WindowEnd: 600},
	}
	series := []store.ActivityPoint{{Container: "a", T: 300}, {Container: "ab", T: 300}, {Container: "a", T: 600}}
	dims := []store.ActivityDim{
		{Container: "a", Dim: "oclass", Value: "lan", Sightings: 2},
		{Container: "a", Dim: "class", Value: "public", Sightings: 9},
		{Container: "a", Dim: "port", Value: "tcp/443", Sightings: 5},
		{Container: "a", Dim: "port", Value: "tcp/80", Sightings: 7},
	}
	alerts := []store.ActivityAlert{{Subject: "a:lan"}, {Subject: "a:"}, {Subject: "ab:lan"}}
	rows := buildActivityRows(latest, series, dims, alerts)
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	a := rows[0]
	if len(a.Series) != 2 || a.Alerts != 2 || rows[1].Alerts != 1 {
		t.Fatalf("series and alerts must be the container's own, not a prefix match: %+v", rows)
	}
	if a.Classes["lan"] != 2 || a.Classes["public"] != 0 {
		t.Fatalf("classes must be the outbound dimension only: %v", a.Classes)
	}
	if a.TopPorts[0].Value != "tcp/80" || a.OpensLast == nil || *a.OpensLast != 4 || rows[1].OpensLast != nil {
		t.Fatalf("got %+v", a)
	}
}

func TestClampActivityDays(t *testing.T) {
	for in, want := range map[int]int{-5: 1, 1: 1, 3: 7, 7: 7, 30: 30, 400: 30} {
		if got := clampActivityDays(in); got != want {
			t.Errorf("%d: got %d want %d", in, got, want)
		}
	}
}
