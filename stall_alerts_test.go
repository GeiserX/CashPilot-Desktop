package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/stall"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// stallNow sits 25 minutes into an hour, so the hour in progress is never judged.
var stallNow = time.Unix(1_800_000_000/3600*3600+1500, 0)

func newStallApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatal(err)
	}
	if svc, _ := cat.Get("honeygain"); svc.Docker.TrafficStall == nil {
		t.Fatal("the catalog's honeygain declares no traffic_stall; these tests need it")
	}
	if svc, _ := cat.Get("earnapp"); svc.Docker.TrafficStall != nil {
		t.Fatal("the catalog's earnapp declares traffic_stall; pick another service without it")
	}
	return &App{cfg: cfg, store: st, catalog: cat}
}

// copyName is the container name of copy n of slug.
func copyName(slug string, n int) string {
	if n == 1 {
		return "cashpilot-" + slug
	}
	return fmt.Sprintf("cashpilot-%s-%d", slug, n)
}

// sent stores, for copy n of slug, perHour bytes sent in each of the hours whole
// hours that end at the last whole hour before now.
func sent(t *testing.T, a *App, slug string, n int, now time.Time, hours int, perHour int64) {
	t.Helper()
	last := stall.LastWholeHour(now.Unix())
	var rows []store.UsageHour
	for i := 0; i < hours; i++ {
		tx := perHour
		rows = append(rows, store.UsageHour{Container: copyName(slug, n), Slug: slug, Hour: last - 3600*int64(i), Beats: 12, TxBytes: &tx, NetSeconds: 3600})
	}
	if err := a.store.RecordUsage(func(map[string]store.UsageBaseline) ([]store.UsageHour, map[string]store.UsageBaseline) {
		return rows, nil
	}); err != nil {
		t.Fatal(err)
	}
}

// listing is what the runtime lists: copies 1..n of slug, all running unless
// status names another state for a copy.
func listing(slug string, n int, status map[int]string) []runtime.ContainerInfo {
	var out []runtime.ContainerInfo
	for i := 1; i <= n; i++ {
		st := status[i]
		if st == "" {
			st = "running"
		}
		out = append(out, runtime.ContainerInfo{Slug: slug, Name: copyName(slug, i), ContainerID: fmt.Sprintf("%s-%d", slug, i), Status: st, Instance: i})
	}
	return out
}

func stallTitles(a *App) []string {
	var out []string
	for _, n := range a.stallNotifications() {
		out = append(out, n.Title)
	}
	return out
}

// Copy 2 of three sends almost nothing for 12 hours while copies 1 and 3 send
// 30 MB an hour: one alert, for copy 2, raised once however often the check runs,
// in the Alerts menu and the app state.
func TestStallRaisesOneAlertForTheQuietCopy(t *testing.T) {
	a := newStallApp(t)
	sent(t, a, "honeygain", 1, stallNow, 12, 30_000_000)
	sent(t, a, "honeygain", 2, stallNow, 12, 20_000)
	sent(t, a, "honeygain", 3, stallNow, 12, 30_000_000)
	for i := 0; i < 3; i++ {
		a.checkStalls(listing("honeygain", 3, nil), stallNow.Add(time.Duration(i)*time.Minute))
	}
	got := a.currentStalls()
	if len(got) != 1 {
		t.Fatalf("stalls = %+v, want one", got)
	}
	al := got[0]
	if al.Slug != "honeygain" || al.Service != "Honeygain" || al.Container != "cashpilot-honeygain-2" || al.Copy != 2 || al.Hours != 12 {
		t.Fatalf("alert = %+v, want Honeygain copy 2 for 12 hours", al)
	}
	if want := stall.LastWholeHour(stallNow.Unix()) - 11*3600; al.Since != want {
		t.Fatalf("since = %d, want %d (the first low hour)", al.Since, want)
	}
	for _, part := range []string{"Honeygain copy 2 (cashpilot-honeygain-2)", "under 0.1 MB an hour for 12 hours", "30.0 MB an hour", "Look at its logs", "does not restart it"} {
		if !strings.Contains(al.Message, part) {
			t.Errorf("message %q lacks %q", al.Message, part)
		}
	}
	if titles := stallTitles(a); len(titles) != 1 || titles[0] != "Honeygain copy 2: almost no traffic" {
		t.Fatalf("notifications = %q, want one for copy 2", titles)
	}
}

// Each quiet copy gets its own alert.
func TestStallAlertsEachQuietCopy(t *testing.T) {
	a := newStallApp(t)
	for n, perHour := range map[int]int64{1: 30_000_000, 2: 0, 3: 25_000_000, 4: 100_000} {
		sent(t, a, "honeygain", n, stallNow, 12, perHour)
	}
	a.checkStalls(listing("honeygain", 4, nil), stallNow)
	got := a.currentStalls()
	if len(got) != 2 || got[0].Copy != 2 || got[1].Copy != 4 {
		t.Fatalf("stalls = %+v, want copies 2 and 4", got)
	}
}

// Traffic back in the last whole hour clears the alert; an hour with no reading
// leaves it, since a gap says nothing.
func TestStallClearsWhenTrafficComesBack(t *testing.T) {
	a := newStallApp(t)
	sent(t, a, "honeygain", 1, stallNow, 12, 30_000_000)
	sent(t, a, "honeygain", 2, stallNow, 12, 0)
	a.checkStalls(listing("honeygain", 2, nil), stallNow)
	if len(a.currentStalls()) != 1 {
		t.Fatalf("stalls = %+v, want one to start from", a.currentStalls())
	}

	// An hour later, copy 2 has no reading for the hour that ended: undecided.
	later := stallNow.Add(time.Hour)
	sent(t, a, "honeygain", 1, later, 1, 30_000_000)
	a.checkStalls(listing("honeygain", 2, nil), later)
	if len(a.currentStalls()) != 1 {
		t.Fatalf("an hour with no reading cleared the alert: %+v", a.currentStalls())
	}

	// Another hour later it sent 5 MB, over 5% of its peer's 30 MB: cleared.
	later = later.Add(time.Hour)
	sent(t, a, "honeygain", 1, later, 1, 30_000_000)
	sent(t, a, "honeygain", 2, later, 1, 5_000_000)
	a.checkStalls(listing("honeygain", 2, nil), later)
	if got := a.currentStalls(); len(got) != 0 {
		t.Fatalf("stalls = %+v, want none once traffic came back", got)
	}
	if titles := stallTitles(a); len(titles) != 0 {
		t.Fatalf("notifications = %q, want none", titles)
	}
}

// A service whose catalog entry has no traffic_stall block is never flagged, however
// quiet one of its copies is.
func TestStallIgnoresAServiceWithoutTheBlock(t *testing.T) {
	a := newStallApp(t)
	sent(t, a, "earnapp", 1, stallNow, 24, 30_000_000)
	sent(t, a, "earnapp", 2, stallNow, 24, 0)
	a.checkStalls(listing("earnapp", 2, nil), stallNow)
	if got := a.currentStalls(); len(got) != 0 {
		t.Fatalf("stalls = %+v, want none for a service without the block", got)
	}
}

// A stopped copy is not flagged, and stopping a flagged one clears its alert. A
// restarting copy (a crash loop) neither raises nor clears, and a copy gone from
// the list clears.
func TestStallSkipsStoppedCopies(t *testing.T) {
	a := newStallApp(t)
	sent(t, a, "honeygain", 1, stallNow, 12, 30_000_000)
	sent(t, a, "honeygain", 2, stallNow, 12, 0)

	a.checkStalls(listing("honeygain", 2, map[int]string{2: "exited"}), stallNow)
	if got := a.currentStalls(); len(got) != 0 {
		t.Fatalf("stalls = %+v, want none for a stopped copy", got)
	}
	a.checkStalls(listing("honeygain", 2, map[int]string{2: "restarting"}), stallNow)
	if got := a.currentStalls(); len(got) != 0 {
		t.Fatalf("stalls = %+v, want none for a restarting copy", got)
	}

	a.checkStalls(listing("honeygain", 2, nil), stallNow)
	if len(a.currentStalls()) != 1 {
		t.Fatalf("stalls = %+v, want one while it runs", a.currentStalls())
	}
	a.checkStalls(listing("honeygain", 2, map[int]string{2: "restarting"}), stallNow)
	if len(a.currentStalls()) != 1 {
		t.Fatalf("a restarting copy cleared its alert: %+v", a.currentStalls())
	}
	a.checkStalls(listing("honeygain", 2, map[int]string{2: "exited"}), stallNow)
	if got := a.currentStalls(); len(got) != 0 {
		t.Fatalf("stalls = %+v, want none once it stopped", got)
	}

	a.checkStalls(listing("honeygain", 2, nil), stallNow)
	a.checkStalls(listing("honeygain", 1, nil), stallNow)
	if got := a.currentStalls(); len(got) != 0 {
		t.Fatalf("stalls = %+v, want none once it is gone from the list", got)
	}
}

// The usage reading runs the check: a reading after twelve quiet hours raises the
// alert, so it shows without any other step.
func TestSampleUsageRunsTheStallCheck(t *testing.T) {
	a := newStallApp(t)
	now := time.Now()
	sent(t, a, "honeygain", 1, now, 12, 30_000_000)
	sent(t, a, "honeygain", 2, now, 12, 0)
	a.sampleUsage(t.Context(), func(context.Context) ([]runtime.ContainerInfo, error) {
		return listing("honeygain", 2, nil), nil
	})
	if got := a.currentStalls(); len(got) != 1 || got[0].Copy != 2 {
		t.Fatalf("stalls = %+v, want copy 2 after a usage reading", got)
	}
}
