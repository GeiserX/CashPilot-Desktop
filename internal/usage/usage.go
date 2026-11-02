// Package usage keeps what each container costs this machine over time: CPU,
// memory and network traffic, per container per UTC hour, drawn as the three
// small line charts under the earnings chart. It follows app/usage.py and the
// /api/usage route of the web repository (GeiserX/CashPilot#515), with one
// machine instead of a fleet, so there is no server picker.
//
// The network counters are CUMULATIVE since the container started, so traffic is
// the difference between two readings, with three rules:
//
//   - A counter that went backwards is a restart. Docker resets the counters when
//     the container restarts, so the new reading IS the traffic since the
//     restart. Counting it keeps a busy service that restarts often from looking
//     idle.
//   - A baseline older than MaxBaselineAge is replaced, not differenced. After a
//     long gap (the machine asleep, the app closed) the container may have
//     restarted several times, and the difference would land hours of traffic on
//     the hour the app came back. We start over and count nothing for that gap.
//   - Missing counters are not zero. A host-network container (or a native
//     program) has no counters of its own. Its traffic is unknown and stays
//     unknown: its line has a gap, and a total without it is marked as a minimum.
package usage

import (
	"math"
	"sort"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// MaxBaselineAge is how old a counter baseline may be and still be differenced.
// Readings arrive every few minutes; an hour covers a missed one or a short
// sleep, anything longer is a gap we refuse to put on one hour.
const MaxBaselineAge = 3600.0

// CounterDelta is the bytes moved between two cumulative readings of the same
// container. A counter that went backwards means the container restarted, and
// the new reading is everything since the restart.
func CounterDelta(previous, current int64) int64 {
	if current < previous {
		return current
	}
	return current - previous
}

// Fold turns one reading of every unit into this hour's rows (one per running
// container, by name, Beats 1) and the next counter baselines (per container id).
// It is the fold store.RecordUsage runs inside its transaction. Only running units
// count: a stopped one uses nothing, and draws a gap rather than a zero.
func Fold(infos []runtime.ContainerInfo, baselines map[string]store.UsageBaseline, now time.Time) ([]store.UsageHour, map[string]store.UsageBaseline) {
	at := float64(now.UnixNano()) / 1e9
	hour := now.Unix() / 3600 * 3600
	byName := map[string]*store.UsageHour{}
	var order []string
	next := map[string]store.UsageBaseline{}
	for _, c := range infos {
		if c.Slug == "" || c.Status != "running" {
			continue
		}
		name := c.Name
		if name == "" {
			name = c.ContainerID
		}
		if name == "" {
			continue
		}
		row, ok := byName[name]
		if !ok {
			row = &store.UsageHour{Container: name, Hour: hour}
			byName[name] = row
			order = append(order, name)
		}
		row.Slug = c.Slug
		row.Beats = 1
		if c.CPUKnown && isReading(c.CPUPercent) {
			row.CPUSum, row.CPUN = c.CPUPercent, 1
		}
		if c.MemoryKnown && isReading(c.MemoryMB) {
			row.MemSum, row.MemN = c.MemoryMB, 1
		}
		if c.ContainerID == "" || c.NetRxBytes == nil || c.NetTxBytes == nil ||
			*c.NetRxBytes > math.MaxInt64 || *c.NetTxBytes > math.MaxInt64 {
			row.NoCounters = 1
			continue
		}
		rx, tx := int64(*c.NetRxBytes), int64(*c.NetTxBytes)
		next[c.ContainerID] = store.UsageBaseline{Rx: rx, Tx: tx, SeenAt: at}
		prev, ok := baselines[c.ContainerID]
		if !ok {
			continue // first sight: this reading only becomes the baseline
		}
		age := at - prev.SeenAt
		if age <= 0 {
			continue
		}
		if age > MaxBaselineAge {
			row.TrafficLost = 1
			continue
		}
		drx, dtx := CounterDelta(prev.Rx, rx), CounterDelta(prev.Tx, tx)
		row.RxBytes, row.TxBytes = &drx, &dtx
		row.NetSeconds = age
	}
	rows := make([]store.UsageHour, 0, len(order))
	for _, name := range order {
		rows = append(rows, *byName[name])
	}
	return rows, next
}

func isReading(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0
}

// Container is one entry of the container picker: a container that has a reading
// in the window. NoCounters marks one whose traffic is unknown (host network).
type Container struct {
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Service    string `json:"service"`
	NoCounters bool   `json:"noCounters"`
}

// View is what the three charts draw. Hours is the start of every point in the
// window (Unix seconds), oldest first: one per hour on 7 days, one per Step (6
// hours) on 30, where 720 hourly points would be noise on a small chart. Every
// series has one value per point; null is "no reading", drawn as a gap, never 0.
type View struct {
	Days       int         `json:"days"`
	Step       int64       `json:"step"`
	Hours      []int64     `json:"hours"`
	Containers []Container `json:"containers"`
	// Container is the picked container, "" for all of them (the machine total).
	Container string `json:"container"`
	// CPU where 100% is one core, memory in MB, traffic in bytes per second.
	CPUPercent []*float64 `json:"cpuPercent"`
	MemoryMB   []*float64 `json:"memoryMb"`
	RxRate     []*float64 `json:"rxRate"`
	TxRate     []*float64 `json:"txRate"`
	RxBytes    []*int64   `json:"rxBytes"`
	TxBytes    []*int64   `json:"txBytes"`
	// A point whose total leaves out a container it could not read: the value is
	// a minimum, shown with ≥.
	TrafficPartial []bool `json:"trafficPartial"`
	CPUPartial     []bool `json:"cpuPartial"`
	MemoryPartial  []bool `json:"memoryPartial"`
}

// Step is the width of one point for a window of days.
func Step(days int) int64 {
	if clampDays(days) <= 7 {
		return 3600
	}
	return 6 * 3600
}

func clampDays(days int) int {
	return min(max(days, 1), 30)
}

// Since is the first second of the window Build draws, for the store query.
func Since(days int, now time.Time) int64 {
	days = clampDays(days)
	step := Step(days)
	return now.Unix()/step*step - (int64(days)*86400/step-1)*step
}

// Build draws the window of days ending now, for the picked container ("" or a
// name with no reading in the window means all of them).
func Build(rows []store.UsageHour, days int, container string, now time.Time) View {
	days = clampDays(days)
	step := Step(days)
	first := Since(days, now)
	n := int(int64(days) * 86400 / step)
	hours := make([]int64, n)
	for i := range hours {
		hours[i] = first + int64(i)*step
	}

	seen := map[string]*Container{}
	for _, r := range rows {
		if r.Hour < first {
			continue
		}
		c, ok := seen[r.Container]
		if !ok {
			c = &Container{Name: r.Container}
			seen[r.Container] = c
		}
		c.Slug = r.Slug
		c.NoCounters = c.NoCounters || r.NoCounters > 0
	}
	containers := make([]Container, 0, len(seen))
	for _, c := range seen {
		containers = append(containers, *c)
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	if _, ok := seen[container]; !ok {
		container = ""
	}

	// One merged row per container and point: sums and counts add, so a 6-hour
	// point is the average over the hours that had readings.
	type key struct {
		point     int64
		container string
	}
	merged := map[key]*store.UsageHour{}
	for _, r := range rows {
		if r.Hour < first || (container != "" && r.Container != container) {
			continue
		}
		k := key{r.Hour / step * step, r.Container}
		m, ok := merged[k]
		if !ok {
			cp := r
			cp.RxBytes, cp.TxBytes = copyInt(r.RxBytes), copyInt(r.TxBytes)
			merged[k] = &cp
			continue
		}
		m.Beats += r.Beats
		m.CPUSum += r.CPUSum
		m.CPUN += r.CPUN
		m.MemSum += r.MemSum
		m.MemN += r.MemN
		m.NetSeconds += r.NetSeconds
		m.NoCounters += r.NoCounters
		m.TrafficLost += r.TrafficLost
		m.RxBytes = addInt(m.RxBytes, r.RxBytes)
		m.TxBytes = addInt(m.TxBytes, r.TxBytes)
	}
	perPoint := map[int64][]*store.UsageHour{}
	for k, m := range merged {
		perPoint[k.point] = append(perPoint[k.point], m)
	}

	v := View{
		Days: days, Step: step, Hours: hours, Containers: containers, Container: container,
		CPUPercent: make([]*float64, n), MemoryMB: make([]*float64, n),
		RxRate: make([]*float64, n), TxRate: make([]*float64, n),
		RxBytes: make([]*int64, n), TxBytes: make([]*int64, n),
		TrafficPartial: make([]bool, n), CPUPartial: make([]bool, n), MemoryPartial: make([]bool, n),
	}
	for i, h := range hours {
		if group, ok := perPoint[h]; ok {
			v.fillPoint(i, group)
		}
	}
	return v
}

// fillPoint is one point (an hour, or 6 on 30 days) of the machine or of one
// container. CPU and memory are each container's average while it was read,
// weighted by the share of the point it ran (its readings over the most any
// container had), then added up: a container that ran one hour of six adds a
// sixth of its load, not all of it. A running container whose stats read failed
// leaves the sum a minimum. Traffic is each container's bytes over the seconds
// its deltas cover, added up as a rate; a container with no counters (host
// network) or lost traffic leaves the traffic a minimum.
func (v *View) fillPoint(i int, group []*store.UsageHour) {
	full := 0
	for _, r := range group {
		full = max(full, r.Beats)
	}
	var cpu, mem, rxRate, txRate *float64
	var rxBytes, txBytes *int64
	for _, r := range group {
		share := 1.0
		if full > 0 {
			share = float64(r.Beats) / float64(full)
		}
		if r.CPUN > 0 {
			cpu = addFloat(cpu, r.CPUSum/float64(r.CPUN)*share)
		}
		if r.MemN > 0 {
			mem = addFloat(mem, r.MemSum/float64(r.MemN)*share)
		}
		if r.CPUN < r.Beats {
			v.CPUPartial[i] = true
		}
		if r.MemN < r.Beats {
			v.MemoryPartial[i] = true
		}
		rx, tx := rate(r.RxBytes, r.NetSeconds), rate(r.TxBytes, r.NetSeconds)
		if rx == nil || tx == nil || r.NoCounters > 0 || r.TrafficLost > 0 {
			v.TrafficPartial[i] = true
		}
		if rx != nil && tx != nil {
			rxRate = addFloat(rxRate, *rx)
			txRate = addFloat(txRate, *tx)
			rxBytes = addInt(rxBytes, r.RxBytes)
			txBytes = addInt(txBytes, r.TxBytes)
		}
	}
	v.CPUPercent[i] = round(cpu, 100)
	v.MemoryMB[i] = round(mem, 10)
	v.RxRate[i] = round(rxRate, 100)
	v.TxRate[i] = round(txRate, 100)
	v.RxBytes[i], v.TxBytes[i] = rxBytes, txBytes
}

// rate is bytes per second, or nil when there is no measured interval.
func rate(bytes *int64, seconds float64) *float64 {
	if bytes == nil || seconds <= 0 {
		return nil
	}
	r := float64(*bytes) / seconds
	return &r
}

func addFloat(a *float64, b float64) *float64 {
	if a == nil {
		return &b
	}
	s := *a + b
	return &s
}

func addInt(a, b *int64) *int64 {
	if b == nil {
		return a
	}
	if a == nil {
		return copyInt(b)
	}
	s := *a + *b
	return &s
}

func copyInt(a *int64) *int64 {
	if a == nil {
		return nil
	}
	c := *a
	return &c
}

func round(v *float64, scale float64) *float64 {
	if v == nil {
		return nil
	}
	r := math.Round(*v*scale) / scale
	return &r
}
