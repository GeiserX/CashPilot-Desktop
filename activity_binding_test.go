package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/activity"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

func i64(v int64) *int64 { return &v }

// newActivityTestApp is an App with a real store and config and a network
// table that can never download, so no test reaches the internet.
func newActivityTestApp(t *testing.T) *App {
	t.Helper()
	app := newRemovalTestApp(t, &recordingProvider{})
	// No ctx: SetActivityEnabled(true) then starts no background download.
	app.ctx = nil
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no table here", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	app.activity.deltas = activity.NewDeltas()
	app.activity.geo = &activity.IPGeo{Dir: t.TempDir(), URL: srv.URL}
	return app
}

func earnerWindow(end int64) activity.Window {
	return activity.Window{WindowEnd: end, Samples: []activity.Sample{{
		Container: "earner", Members: []string{"earner"}, NetworkMode: "bridge", Status: "ok", Opens: i64(3),
		ListenPorts: []int{}, Out: []activity.Dest{{Proto: "tcp", IP: "1.2.3.4", Port: 443, Class: "public", Seen: 2}},
		In: []activity.Dest{{IP: "5.6.7.8", Port: 9000, Class: "public", Seen: 1}},
	}}}
}

func TestActivityBindingsRefuseBeforeTheAppIsReady(t *testing.T) {
	a := &App{}
	if _, err := a.GetActivity(1); err == nil {
		t.Fatal("GetActivity must refuse before startup")
	}
	if _, err := a.GetActivityDetail("x", 1); err == nil {
		t.Fatal("GetActivityDetail must refuse before startup")
	}
	if _, err := a.SetActivityEnabled(true); err == nil {
		t.Fatal("SetActivityEnabled must refuse before startup")
	}
	if _, err := a.DeleteActivityData(); err == nil {
		t.Fatal("DeleteActivityData must refuse before startup")
	}
	if _, err := a.RefreshActivityNetworks(); err == nil {
		t.Fatal("RefreshActivityNetworks must refuse before startup")
	}
	if a.activityNotifications() != nil {
		t.Fatal("no store, no notifications")
	}
}

// The switch, a stored round, the detail view, the alerts and both deletes,
// through the bindings the frontend calls.
func TestActivityBindingsRoundTrip(t *testing.T) {
	a := newActivityTestApp(t)
	view, err := a.SetActivityEnabled(true)
	if err != nil || !view.Enabled || len(view.Containers) != 0 || view.Alerts == nil || view.Round.LeftoverProbes == nil {
		t.Fatalf("switch on: %+v %v", view, err)
	}
	now := time.Now()
	n, err := a.recordActivityWindow(earnerWindow(now.Unix()/300*300), now)
	if err != nil || n != 1 {
		t.Fatalf("record: n=%d err=%v", n, err)
	}
	a.setActivityRound(activityRound{At: now.Unix(), WorkerStatus: "ok"})
	finding := activity.Finding{Kind: "activity_lan", Container: "earner", Detail: "lan", Message: "earner reached the LAN"}
	a.raiseActivityAlerts([]activity.Finding{finding, finding}, now)

	view, err = a.GetActivity(7)
	if err != nil || view.Days != 7 || len(view.Containers) != 1 || view.Round.WorkerStatus != "ok" {
		t.Fatalf("view: %+v %v", view, err)
	}
	row := view.Containers[0]
	if row.Container != "earner" || row.Alerts != 1 || len(view.Alerts) != 1 || view.LastWindow != now.Unix()/300*300 {
		t.Fatalf("a repeated finding inside its cooldown must be stored once: %+v alerts=%+v", row, view.Alerts)
	}
	if got := a.activityNotifications(); len(got) != 1 || got[0].Message != finding.Message {
		t.Fatalf("bell: %+v", got)
	}
	detail, err := a.GetActivityDetail("earner", 1)
	if err != nil || len(detail.Raw) != 2 || len(detail.Ports) != 1 || detail.Ports[0].Value != "tcp/443" {
		t.Fatalf("detail: %+v %v", detail, err)
	}

	// Off deletes the raw addresses at once and keeps the totals.
	view, err = a.SetActivityEnabled(false)
	if err != nil || view.Enabled || len(view.Containers) != 1 {
		t.Fatalf("switch off: %+v %v", view, err)
	}
	if detail, _ := a.GetActivityDetail("earner", 1); len(detail.Raw) != 0 {
		t.Fatalf("switching off must delete every raw address: %+v", detail.Raw)
	}
	if n, _ := a.recordActivityWindow(earnerWindow(now.Unix()/300*300+300), now); n != 0 {
		t.Fatal("nothing may be stored while the switch is off")
	}

	view, err = a.DeleteActivityData()
	if err != nil || len(view.Containers) != 0 || len(view.Alerts) != 0 {
		t.Fatalf("delete all: %+v %v", view, err)
	}
}

func TestRefreshActivityNetworks(t *testing.T) {
	a := newActivityTestApp(t)
	if _, err := a.RefreshActivityNetworks(); err == nil {
		t.Fatal("a failed download must reach the caller")
	}
	a.activity.geoBusy.Lock()
	if err := a.refreshActivityNetworks(t.Context()); !errors.Is(err, activity.ErrDownloadBusy) {
		t.Fatalf("a second download must be refused while one runs, got %v", err)
	}
	a.activity.geoBusy.Unlock()
	a.activity.geo = nil
	if err := a.refreshActivityNetworks(t.Context()); err == nil {
		t.Fatal("no network table yet must be an error")
	}
}

// The hourly purge runs once per hour, with the switch off too.
func TestActivityTickPurgesOncePerHour(t *testing.T) {
	a := newActivityTestApp(t)
	now := time.Now()
	old := now.Add(-store.ActivityRetention - time.Hour).Unix()
	raise := func() {
		t.Helper()
		if _, err := a.store.RaiseActivityAlert(store.ActivityAlert{Kind: "activity_lan", Subject: "x:lan", Message: "old", CreatedAt: old}, 0); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		t.Helper()
		alerts, err := a.store.ActivityAlerts(0)
		if err != nil {
			t.Fatal(err)
		}
		return len(alerts)
	}
	raise()
	a.activityTick(t.Context(), now)
	if count() != 0 {
		t.Fatal("the first tick of an hour must purge rows past retention")
	}
	raise()
	a.activityTick(t.Context(), now)
	if count() != 1 {
		t.Fatal("a second tick in the same hour must not purge again")
	}
}

// A container whose last hour opened far more connections than its usual
// hour raises one "unusual hour" alert; a steady one raises none.
func TestActivitySpikesRaisesOnlyTheSpikingContainer(t *testing.T) {
	a := newActivityTestApp(t)
	now := time.Unix(1_800_000_000/3600*3600+600, 0)
	last := now.Unix()/3600*3600 - 3600
	record := func(container string, hour int64, opens int64) {
		t.Helper()
		row := store.ActivitySample{Container: container, Status: "ok", Opens: i64(opens)}
		if _, err := a.store.RecordActivity(hour+300, []store.ActivitySample{row}, now, nil); err != nil {
			t.Fatal(err)
		}
	}
	for h := last - 100*3600; h < last; h += 3600 {
		record("spiky", h, 10)
		record("steady", h, 10)
	}
	record("spiky", last, 50_000)
	record("steady", last, 12)

	a.activitySpikes(now, a.activityGen())
	alerts, err := a.store.ActivityAlerts(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].Kind != "activity_spike" || alerts[0].Subject != "spiky:opens" {
		t.Fatalf("alerts %+v", alerts)
	}
	a.activitySpikes(now, a.activityGen())
	if alerts, _ := a.store.ActivityAlerts(0); len(alerts) != 1 {
		t.Fatal("a spike inside its cooldown must not be raised twice")
	}
}

func TestStopActivitySamplerWithoutStartIsSafe(t *testing.T) {
	a := &App{}
	a.stopActivitySampler()
	a.startActivitySampler(nil) //nolint:staticcheck // a nil context must be a no-op
	if a.activity.stop != nil {
		t.Fatal("no context, no loop")
	}
	if a.activityOn() {
		t.Fatal("no config means the switch is off")
	}
	if w := a.activityWorker(); len(w) != len("desktop-")+12 {
		t.Fatalf("worker label %q", w)
	}
}

// A round whose window was not stored, because the switch went off while it
// ran, must raise no alert either: nothing new is kept after the opt-out.
func TestStoreActivityRoundRaisesNothingOnceSwitchedOff(t *testing.T) {
	a := newActivityTestApp(t)
	now := time.Now()
	mail := func(end int64) activity.Window {
		w := earnerWindow(end)
		w.Samples[0].Out = []activity.Dest{{Proto: "tcp", IP: "1.2.3.4", Port: 25, Class: "public", Seen: 1}}
		return w
	}
	a.storeActivityRound(mail(now.Unix()/300*300), now, a.activityGen())
	if alerts, _ := a.store.ActivityAlerts(0); len(alerts) != 0 {
		t.Fatalf("the switch is off: nothing may be stored, got %+v", alerts)
	}
	if _, err := a.SetActivityEnabled(true); err != nil {
		t.Fatal(err)
	}
	a.storeActivityRound(mail(now.Unix()/300*300), now, a.activityGen())
	if alerts, _ := a.store.ActivityAlerts(0); len(alerts) != 1 || alerts[0].Kind != "activity_mail" {
		t.Fatalf("a stored window raises its alerts: %+v", alerts)
	}
}

// A delete while a round runs wins: the round started before it stores no
// window and raises no alert or spike afterwards, even with the switch on.
func TestADeleteDuringARoundDropsTheRoundsWrites(t *testing.T) {
	a := newActivityTestApp(t)
	if _, err := a.SetActivityEnabled(true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	w := earnerWindow(now.Unix() / 300 * 300)
	w.Samples[0].Out = []activity.Dest{{Proto: "tcp", IP: "1.2.3.4", Port: 25, Class: "public", Seen: 1}}

	gen := a.activityGen()
	if _, err := a.DeleteActivityData(); err != nil {
		t.Fatal(err)
	}
	a.storeActivityRound(w, now, gen)
	if a.activityWrite(gen, func() {}) {
		t.Fatal("a stale generation must refuse every write")
	}
	if alerts, _ := a.store.ActivityAlerts(0); len(alerts) != 0 {
		t.Fatalf("a round older than the delete raised %+v", alerts)
	}
	if latest, _ := a.store.ActivityLatest(0); len(latest) != 0 {
		t.Fatalf("a round older than the delete stored %+v", latest)
	}

	// Switching off moves the generation on too.
	gen = a.activityGen()
	if _, err := a.SetActivityEnabled(false); err != nil {
		t.Fatal(err)
	}
	if a.activityWrite(gen, func() {}) {
		t.Fatal("a round older than the switch-off must write nothing")
	}

	// The next round, in the new generation, stores as usual.
	if _, err := a.SetActivityEnabled(true); err != nil {
		t.Fatal(err)
	}
	a.storeActivityRound(w, now, a.activityGen())
	if latest, _ := a.store.ActivityLatest(0); len(latest) != 1 {
		t.Fatalf("a current round must be stored: %+v", latest)
	}
}

// A switch-off whose delete failed is retried by the tick while the switch
// stays off, and never once it is back on.
func TestAFailedSwitchOffDeleteIsRetried(t *testing.T) {
	a := newActivityTestApp(t)
	if _, err := a.SetActivityEnabled(true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if n, err := a.recordActivityWindow(earnerWindow(now.Unix()/300*300), now); err != nil || n != 1 {
		t.Fatalf("record: %d %v", n, err)
	}
	raw := func() int {
		t.Helper()
		d, err := a.GetActivityDetail("earner", 1)
		if err != nil {
			t.Fatal(err)
		}
		return len(d.Raw)
	}
	// While the switch is on, a pending retry does nothing.
	a.activity.deletePending = true
	a.retryActivityDelete()
	if raw() != 2 {
		t.Fatal("a retry must never delete while Activity is on")
	}
	// Switched off by config alone, as if the delete had failed after the save.
	cfg := a.cfg.Config()
	cfg.ActivityEnabled = false
	if err := a.cfg.Save(cfg); err != nil {
		t.Fatal(err)
	}
	gen := a.activityGen()
	a.activityTick(t.Context(), now)
	if a.activity.deletePending || a.activityGen() == gen {
		t.Fatal("the tick must retry the delete and move the generation on")
	}
	if _, err := a.SetActivityEnabled(true); err != nil {
		t.Fatal(err)
	}
	if d, _ := a.GetActivityDetail("earner", 1); len(d.Raw) != 0 {
		t.Fatalf("the retried delete must have removed the addresses: %+v", d.Raw)
	}
}

// After a restart with Activity off, the first tick deletes any addresses a
// failed switch-off left behind: the pending flag does not survive a restart.
func TestStartWithActivityOffQueuesTheDelete(t *testing.T) {
	a := newActivityTestApp(t)
	a.startActivitySampler(t.Context())
	t.Cleanup(a.stopActivitySampler)
	if !a.activity.deletePending {
		t.Fatal("starting with Activity off must queue the switch-off delete")
	}
}
