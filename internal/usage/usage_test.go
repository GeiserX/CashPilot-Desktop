package usage

import (
	"os"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/zalando/go-keyring"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func u64(v uint64) *uint64 { return &v }

// running is a running bridged container with every number read.
func running(name, id string, cpu, mem float64, rx, tx uint64) runtime.ContainerInfo {
	return runtime.ContainerInfo{
		Slug: "honeygain", Name: name, ContainerID: id, Status: "running",
		CPUPercent: cpu, MemoryMB: mem, CPUKnown: true, MemoryKnown: true,
		NetRxBytes: u64(rx), NetTxBytes: u64(tx),
	}
}

// A fixed hour start, so the readings below never straddle an hour boundary.
var t0 = time.Unix(1_790_000_000/3600*3600, 0)

func one(t *testing.T, rows []store.UsageHour) store.UsageHour {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one", rows)
	}
	return rows[0]
}

func TestCounterDeltaRestartCountsTheNewReading(t *testing.T) {
	if got := CounterDelta(1000, 1500); got != 500 {
		t.Fatalf("forward delta = %d, want 500", got)
	}
	// Went backwards: restarted, and everything since the restart is the reading.
	if got := CounterDelta(1000, 300); got != 300 {
		t.Fatalf("restart delta = %d, want 300", got)
	}
}

func TestFoldFirstSightOnlyBecomesTheBaseline(t *testing.T) {
	rows, next := Fold([]runtime.ContainerInfo{running("cashpilot-honeygain", "c1", 12.5, 80, 1000, 100)}, nil, t0)
	r := one(t, rows)
	if r.RxBytes != nil || r.TxBytes != nil || r.NetSeconds != 0 {
		t.Fatalf("first sight counted traffic: %+v", r)
	}
	if r.Beats != 1 || r.CPUN != 1 || r.CPUSum != 12.5 || r.MemN != 1 || r.MemSum != 80 {
		t.Fatalf("first sight cpu/mem = %+v", r)
	}
	if b := next["c1"]; b.Rx != 1000 || b.Tx != 100 {
		t.Fatalf("baseline = %+v, want 1000/100", b)
	}
}

func TestFoldDifferencesARecentBaseline(t *testing.T) {
	base := map[string]store.UsageBaseline{"c1": {Rx: 1000, Tx: 100, SeenAt: float64(t0.Unix())}}
	now := t0.Add(300 * time.Second)
	r := one(t, mustRows(Fold([]runtime.ContainerInfo{running("cashpilot-honeygain", "c1", 1, 1, 4000, 700)}, base, now)))
	if r.RxBytes == nil || *r.RxBytes != 3000 || *r.TxBytes != 600 || r.NetSeconds != 300 {
		t.Fatalf("delta row = %+v, want 3000/600 over 300 s", r)
	}
}

func TestFoldRestartCountsTheNewReading(t *testing.T) {
	base := map[string]store.UsageBaseline{"c1": {Rx: 9000, Tx: 900, SeenAt: float64(t0.Unix())}}
	r := one(t, mustRows(Fold([]runtime.ContainerInfo{running("cashpilot-honeygain", "c1", 1, 1, 250, 40)}, base, t0.Add(time.Minute))))
	if r.RxBytes == nil || *r.RxBytes != 250 || *r.TxBytes != 40 {
		t.Fatalf("restart row = %+v, want the new reading 250/40", r)
	}
}

func TestFoldStaleBaselineCountsNothing(t *testing.T) {
	base := map[string]store.UsageBaseline{"c1": {Rx: 1000, Tx: 100, SeenAt: float64(t0.Unix())}}
	rows, next := Fold([]runtime.ContainerInfo{running("cashpilot-honeygain", "c1", 1, 1, 999_999, 99_999)}, base, t0.Add(2*time.Hour))
	r := one(t, rows)
	if r.RxBytes != nil || r.NetSeconds != 0 || r.TrafficLost != 1 {
		t.Fatalf("stale baseline row = %+v, want no traffic and TrafficLost", r)
	}
	if next["c1"].Rx != 999_999 {
		t.Fatalf("stale baseline not replaced: %+v", next["c1"])
	}
}

func TestFoldHostNetworkIsUnknownNotZero(t *testing.T) {
	c := running("cashpilot-mysterium", "c9", 3, 40, 0, 0)
	c.NetRxBytes, c.NetTxBytes = nil, nil
	rows, next := Fold([]runtime.ContainerInfo{c}, nil, t0)
	r := one(t, rows)
	if r.NoCounters != 1 || r.RxBytes != nil || len(next) != 0 {
		t.Fatalf("host network row = %+v next %+v, want NoCounters and no baseline", r, next)
	}
}

func TestFoldSkipsStoppedAndUnreadNumbers(t *testing.T) {
	stopped := running("cashpilot-stopped", "c2", 0, 0, 1, 1)
	stopped.Status = "exited"
	unread := running("cashpilot-honeygain", "c1", 0, 0, 1, 1)
	unread.CPUKnown, unread.MemoryKnown = false, false
	r := one(t, mustRows(Fold([]runtime.ContainerInfo{stopped, unread}, nil, t0)))
	if r.Container != "cashpilot-honeygain" || r.CPUN != 0 || r.MemN != 0 || r.Beats != 1 {
		t.Fatalf("row = %+v, want the running one with no cpu/mem reading", r)
	}
}

func mustRows(rows []store.UsageHour, _ map[string]store.UsageBaseline) []store.UsageHour {
	return rows
}

func i64(v int64) *int64 { return &v }

// The hour with no rows is null, not 0, and the series lines up with Hours.
func TestBuildGapIsNullNotZero(t *testing.T) {
	now := t0.Add(30 * time.Minute)
	h := t0.Unix()
	rows := []store.UsageHour{
		{Container: "a", Slug: "honeygain", Hour: h - 2*3600, Beats: 12, CPUSum: 120, CPUN: 12, MemSum: 1200, MemN: 12, RxBytes: i64(3600), TxBytes: i64(360), NetSeconds: 3600},
		{Container: "a", Slug: "honeygain", Hour: h, Beats: 12, CPUSum: 240, CPUN: 12, MemSum: 1200, MemN: 12, RxBytes: i64(7200), TxBytes: i64(720), NetSeconds: 3600},
	}
	v := Build(rows, 7, "", now)
	if len(v.Hours) != 168 || len(v.CPUPercent) != 168 || v.Step != 3600 {
		t.Fatalf("7-day window = %d points step %d, want 168 hourly", len(v.Hours), v.Step)
	}
	last := len(v.Hours) - 1
	if v.Hours[last] != h {
		t.Fatalf("last point = %d, want the current hour %d", v.Hours[last], h)
	}
	if v.CPUPercent[last-1] != nil || v.MemoryMB[last-1] != nil || v.RxRate[last-1] != nil {
		t.Fatalf("hour with no reading = cpu %v mem %v rx %v, want null", v.CPUPercent[last-1], v.MemoryMB[last-1], v.RxRate[last-1])
	}
	if *v.CPUPercent[last] != 20 || *v.CPUPercent[last-2] != 10 || *v.RxRate[last] != 2 || *v.TxRate[last] != 0.2 {
		t.Fatalf("points = cpu %v/%v rx %v tx %v", *v.CPUPercent[last], *v.CPUPercent[last-2], *v.RxRate[last], *v.TxRate[last])
	}
	if v.TrafficPartial[last] {
		t.Fatal("fully measured hour marked partial")
	}
}

// A total that misses a host-network container's traffic is a minimum.
func TestBuildTotalWithoutCountersIsAMinimum(t *testing.T) {
	h := t0.Unix()
	rows := []store.UsageHour{
		{Container: "a", Slug: "honeygain", Hour: h, Beats: 6, CPUSum: 60, CPUN: 6, MemSum: 600, MemN: 6, RxBytes: i64(600), TxBytes: i64(60), NetSeconds: 300},
		{Container: "m", Slug: "mysterium", Hour: h, Beats: 6, CPUSum: 30, CPUN: 6, MemSum: 300, MemN: 6, NoCounters: 6},
	}
	v := Build(rows, 7, "", t0)
	last := len(v.Hours) - 1
	if !v.TrafficPartial[last] || *v.RxRate[last] != 2 {
		t.Fatalf("total = rx %v partial %v, want 2 B/s marked partial", *v.RxRate[last], v.TrafficPartial[last])
	}
	if *v.CPUPercent[last] != 15 || *v.MemoryMB[last] != 150 {
		t.Fatalf("cpu/mem total = %v/%v, want 15/150", *v.CPUPercent[last], *v.MemoryMB[last])
	}
	if !v.Containers[1].NoCounters || v.Containers[0].NoCounters {
		t.Fatalf("containers = %+v, want only m without counters", v.Containers)
	}
	// One container: only its line, and the host-network one has no traffic line.
	m := Build(rows, 7, "m", t0)
	if m.Container != "m" || *m.CPUPercent[last] != 5 || m.RxRate[last] != nil || !m.TrafficPartial[last] {
		t.Fatalf("container m = %+v", m)
	}
	// An unknown pick falls back to all containers.
	if Build(rows, 7, "gone", t0).Container != "" {
		t.Fatal("unknown container not reset to all")
	}
}

// 30 days merges six hours into one point; a container that ran one hour of the
// six adds a sixth of its load, and a failed stats read leaves a minimum.
func TestBuildThirtyDaysMergesSixHours(t *testing.T) {
	step := int64(6 * 3600)
	p := t0.Unix() / step * step
	rows := []store.UsageHour{
		{Container: "a", Slug: "x", Hour: p, Beats: 12, CPUSum: 120, CPUN: 12, MemN: 11, MemSum: 110},
		{Container: "a", Slug: "x", Hour: p + 3600, Beats: 12, CPUSum: 360, CPUN: 12, MemN: 12, MemSum: 120},
		{Container: "b", Slug: "y", Hour: p + 3600, Beats: 4, CPUSum: 240, CPUN: 4, MemN: 4, MemSum: 40},
	}
	v := Build(rows, 30, "", time.Unix(p+3600, 0))
	if v.Step != step || len(v.Hours) != 120 {
		t.Fatalf("30-day window = %d points step %d, want 120 six-hourly", len(v.Hours), v.Step)
	}
	last := len(v.Hours) - 1
	// a: 480/24 = 20; b: 60 * 4/24 = 10.
	if *v.CPUPercent[last] != 30 {
		t.Fatalf("merged cpu = %v, want 30", *v.CPUPercent[last])
	}
	if !v.MemoryPartial[last] || v.CPUPartial[last] {
		t.Fatalf("partial flags = mem %v cpu %v, want memory only", v.MemoryPartial[last], v.CPUPartial[last])
	}
}

// The whole path the sampler takes: Fold inside RecordUsage, read back, a redeploy
// under the same name continuing the line, and the retention purge.
func TestRecordUsageRoundTrip(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	record := func(infos []runtime.ContainerInfo, at time.Time) {
		t.Helper()
		if err := st.RecordUsage(func(b map[string]store.UsageBaseline) ([]store.UsageHour, map[string]store.UsageBaseline) {
			return Fold(infos, b, at)
		}); err != nil {
			t.Fatal(err)
		}
	}
	record([]runtime.ContainerInfo{running("cashpilot-honeygain", "c1", 10, 100, 1000, 100)}, t0)
	record([]runtime.ContainerInfo{running("cashpilot-honeygain", "c1", 20, 200, 4000, 400)}, t0.Add(5*time.Minute))
	// Redeployed: a new id under the same name. Its first reading is a baseline.
	record([]runtime.ContainerInfo{running("cashpilot-honeygain", "c2", 30, 300, 50, 5)}, t0.Add(10*time.Minute))
	record([]runtime.ContainerInfo{running("cashpilot-honeygain", "c2", 30, 300, 650, 65)}, t0.Add(15*time.Minute))

	rows, err := st.UsageHours(t0.Unix())
	if err != nil {
		t.Fatal(err)
	}
	r := one(t, rows)
	if r.Beats != 4 || r.CPUSum != 90 || r.CPUN != 4 || r.MemSum != 900 {
		t.Fatalf("row = %+v, want 4 readings summing cpu 90 mem 900", r)
	}
	if r.RxBytes == nil || *r.RxBytes != 3600 || *r.TxBytes != 360 || r.NetSeconds != 600 {
		t.Fatalf("traffic = %v/%v over %v, want 3600/360 over 600 s", r.RxBytes, r.TxBytes, r.NetSeconds)
	}
	if err := st.PurgeUsage(t0.Add(store.UsageRetention + 2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.UsageHours(0); len(rows) != 0 {
		t.Fatalf("rows after retention = %+v, want none", rows)
	}
}
