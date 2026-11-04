package store

import (
	"testing"
	"time"
)

func one(v int64) *int64 { return &v }

func activityRow(container string) ActivitySample {
	return ActivitySample{
		Container: container, Status: "ok", Members: []string{container}, Opens: one(3), EstMax: one(2),
		Dests: []ActivityDest{
			{Dir: "out", Proto: "tcp", IP: "1.2.3.4", Port: 443, Class: "public", ASN: "AS1 X", CC: "US", Seen: 2},
			{Dir: "out", Proto: "tcp", IP: "192.168.1.1", Port: 80, Class: "lan", ASN: "AS9 never", CC: "ZZ", Seen: 1},
			{Dir: "in", Proto: "tcp", IP: "5.6.7.8", Port: 9000, Class: "public", Seen: 1},
		},
	}
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecordActivityAggregatesAndIgnoresARepeat(t *testing.T) {
	s := openTestStore(t)
	now := time.Unix(1_800_000_000, 0)
	end := now.Unix() / 300 * 300
	n, err := s.RecordActivity(end, []ActivitySample{activityRow("a")}, now, nil)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if n, _ := s.RecordActivity(end, []ActivitySample{activityRow("a")}, now, nil); n != 0 {
		t.Fatal("a repeated window must be ignored whole")
	}
	if got := countRows(t, s, "activity_dest_raw"); got != 3 {
		t.Fatalf("raw rows %d", got)
	}
	dims, _ := s.ActivityDims(0, "a")
	got := map[string]int64{}
	for _, d := range dims {
		got[d.Dim+"="+d.Value] = d.Sightings
	}
	if got["port=tcp/443"] != 2 || got["oclass=lan"] != 1 || got["dir=in"] != 1 || got["asn=AS1 X"] != 2 || got["cc=US"] != 2 {
		t.Fatalf("dims %v", got)
	}
	if got["asn=AS9 never"] != 0 || got["cc=ZZ"] != 0 {
		t.Fatal("a network name is only kept for a public address")
	}
	if _, ok := got["port=tcp/9000"]; ok {
		t.Fatal("inbound rows have no port dimension")
	}
	latest, _ := s.ActivityLatest(0)
	if len(latest) != 1 || latest[0].Opens == nil || *latest[0].Opens != 3 || latest[0].AttemptFails != nil {
		t.Fatalf("latest %+v", latest)
	}
}

func TestRecordActivityRefusedAfterSwitchOff(t *testing.T) {
	s := openTestStore(t)
	now := time.Unix(1_800_000_000, 0)
	n, err := s.RecordActivity(now.Unix()/300*300, []ActivitySample{activityRow("a")}, now, func() bool { return false })
	if err != nil || n != 0 || countRows(t, s, "activity_samples") != 0 {
		t.Fatalf("stored after switch-off: n=%d err=%v", n, err)
	}
}

func TestActivityPurgeBoundaries(t *testing.T) {
	s := openTestStore(t)
	now := time.Unix(1_800_000_000, 0)
	young := now.Unix() - ActivityRawSeconds + 60
	old := now.Unix() - ActivityRawSeconds - 60
	for _, end := range []int64{young, old} {
		if _, err := s.RecordActivity(end, []ActivitySample{activityRow("a")}, time.Unix(end, 0), nil); err != nil {
			t.Fatal(err)
		}
	}
	ancient := now.Add(-ActivityRetention - time.Hour).Unix()
	if _, err := s.RecordActivity(ancient, []ActivitySample{activityRow("b")}, time.Unix(ancient, 0), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RaiseActivityAlert(ActivityAlert{Kind: "activity_lan", Subject: "b:lan", Message: "m", CreatedAt: ancient}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeActivity(now); err != nil {
		t.Fatal(err)
	}
	var minEnd int64
	_ = s.db.QueryRow("SELECT MIN(window_end) FROM activity_dest_raw").Scan(&minEnd)
	if countRows(t, s, "activity_dest_raw") != 3 || minEnd != young {
		t.Fatalf("raw purge kept %d rows, oldest %d", countRows(t, s, "activity_dest_raw"), minEnd)
	}
	if countRows(t, s, "activity_samples") != 2 {
		t.Fatalf("samples older than 35 days must go, %d left", countRows(t, s, "activity_samples"))
	}
	var hourly int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM activity_hourly WHERE container = 'b'").Scan(&hourly)
	if hourly != 0 || countRows(t, s, "activity_alerts") != 0 {
		t.Fatal("hourly totals and alerts older than 35 days must go")
	}
}

func TestDeleteActivity(t *testing.T) {
	s := openTestStore(t)
	now := time.Unix(1_800_000_000, 0)
	if _, err := s.RecordActivity(now.Unix()/300*300, []ActivitySample{activityRow("a")}, now, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteActivity(false); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, "activity_dest_raw") != 0 || countRows(t, s, "activity_samples") != 1 {
		t.Fatal("switching off deletes the addresses only")
	}
	if err := s.DeleteActivity(true); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, "activity_samples")+countRows(t, s, "activity_hourly") != 0 {
		t.Fatal("Delete data deletes everything")
	}
}

func TestActivityAlertCooldown(t *testing.T) {
	s := openTestStore(t)
	a := ActivityAlert{Kind: "activity_mail", Subject: "x:", Message: "m", CreatedAt: 1000}
	if ok, _ := s.RaiseActivityAlert(a, time.Hour); !ok {
		t.Fatal("first alert must be raised")
	}
	a.CreatedAt = 1000 + 3599
	if ok, _ := s.RaiseActivityAlert(a, time.Hour); ok {
		t.Fatal("inside the cooldown")
	}
	a.CreatedAt = 1000 + 3601
	if ok, _ := s.RaiseActivityAlert(a, time.Hour); !ok {
		t.Fatal("after the cooldown")
	}
}

func TestActivitySeriesKeepsUnknownHoursUnknown(t *testing.T) {
	s := openTestStore(t)
	base := int64(1_800_000_000) / 3600 * 3600
	row := activityRow("a")
	row.Opens = nil
	if _, err := s.RecordActivity(base+300, []ActivitySample{row}, time.Unix(base+300, 0), nil); err != nil {
		t.Fatal(err)
	}
	pts, err := s.ActivitySeries(0, true, "a")
	if err != nil || len(pts) != 1 || pts[0].Opens != nil {
		t.Fatalf("an hour with no counted window must stay unknown: %+v %v", pts, err)
	}
}
