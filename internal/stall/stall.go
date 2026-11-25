// Package stall finds a running copy that carries almost no traffic while its
// service's other copies are busy. It follows app/traffic_stall.py of the web
// repository (GeiserX/CashPilot#808), with one machine instead of a fleet: the
// copies compared are this computer's own copies of the service.
//
// Some clients stay connected after a dropped session and then get no work. The
// container is up, its log says it runs, and the account balance still grows from
// the service's other copies, so no other check sees it. Only the traffic shows
// it: that one copy sends next to nothing, hour after hour, while the other copies
// keep sending their usual amount.
//
// A service opts in with docker.traffic_stall in its catalog entry. The check
// reads the bytes each copy SENT per hour (usage_hourly tx_bytes over
// net_seconds). Received bytes also count traffic the copy never asked for, such
// as broadcasts on the network it sits on, enough to hide a stall.
//
// It only finds stalls. Nothing here restarts anything.
package stall

import (
	"math"
	"sort"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// The web defaults for the fields a declaration leaves out.
const (
	DefaultShare            = 0.05
	DefaultPeerMinMBPerHour = 10.0
	DefaultHours            = 12
	// MaxHours is the longest window a declaration may ask for, and how far back
	// the start of a stall is looked for.
	MaxHours = 168
	// MB is a million bytes, the unit of peer_min_mb_per_hour.
	MB = 1_000_000
)

// Spec is a service's declared check, with its defaults filled in.
type Spec struct {
	Share        float64
	PeerMinBytes float64 // bytes sent per hour
	Hours        int
}

// Stall is a copy judged stalled.
type Stall struct {
	// Sent is its mean bytes sent per hour since the stall began, Peers the median
	// of the other copies' hourly median over the same hours.
	Sent, Peers float64
	// Since is the start (Unix seconds) of the first low hour of the run, and
	// Hours how many whole low hours in a row it has had up to the last one.
	Since int64
	Hours int
}

// SpecOf is a service's declared check, or nil when it declares none or one the
// web loader refuses (services/_schema.yml): a share outside (0, 1), a floor that
// is not positive, or hours outside 1..MaxHours. Zero stands for a field the
// block leaves out, as the YAML decode leaves it.
func SpecOf(raw *catalog.TrafficStall) *Spec {
	if raw == nil {
		return nil
	}
	spec := Spec{Share: raw.Share, PeerMinBytes: raw.PeerMinMBPerHour, Hours: raw.Hours}
	if spec.Share == 0 {
		spec.Share = DefaultShare
	}
	if spec.PeerMinBytes == 0 {
		spec.PeerMinBytes = DefaultPeerMinMBPerHour
	}
	if spec.Hours == 0 {
		spec.Hours = DefaultHours
	}
	if !finite(spec.Share) || spec.Share <= 0 || spec.Share >= 1 ||
		!finite(spec.PeerMinBytes) || spec.PeerMinBytes <= 0 ||
		spec.Hours < 1 || spec.Hours > MaxHours {
		return nil
	}
	spec.PeerMinBytes *= MB
	return &spec
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// LastWholeHour is the start, in Unix seconds, of the last UTC hour that has ended.
func LastWholeHour(now int64) int64 {
	return now/3600*3600 - 3600
}

// SentPerHour is each copy's bytes sent per hour, by hour, from usage rows, keyed
// by container name. An hour with no counter or no measured interval is left out,
// never read as zero.
func SentPerHour(rows []store.UsageHour) map[string]map[int64]float64 {
	out := map[string]map[int64]float64{}
	for _, r := range rows {
		if r.TxBytes == nil || r.NetSeconds <= 0 {
			continue
		}
		if out[r.Container] == nil {
			out[r.Container] = map[int64]float64{}
		}
		out[r.Container][r.Hour] = float64(*r.TxBytes) * 3600 / r.NetSeconds
	}
	return out
}

// Judge returns the stalled copies of one service and the copies that carried
// traffic in the last whole hour, both keyed by container name.
//
// rows are the service's usage rows. A copy is stalled when, in each of the last
// spec.Hours whole hours, it has a reading, at least one other copy has one, the
// median of the others is at least spec.PeerMinBytes, and the copy sent less than
// spec.Share of that median. A copy carries traffic when, in the last whole hour,
// it sent at least the floor itself, or at least that share of a median that
// reached the floor. Any other copy is undecided: an hour without a reading says
// nothing, and neither does an hour in which the other copies are quiet too.
func Judge(rows []store.UsageHour, spec Spec, now int64) (map[string]Stall, map[string]bool) {
	last := LastWholeHour(now)
	sent := SentPerHour(rows)
	stalled := map[string]Stall{}
	carrying := map[string]bool{}
	for name, mine := range sent {
		peerMedian := func(hour int64) (float64, bool) {
			var others []float64
			for other, s := range sent {
				if other == name {
					continue
				}
				if v, ok := s[hour]; ok {
					others = append(others, v)
				}
			}
			if len(others) == 0 {
				return 0, false
			}
			return median(others), true
		}
		if v, ok := mine[last]; ok {
			m, has := peerMedian(last)
			if v >= spec.PeerMinBytes || (has && m >= spec.PeerMinBytes && v >= spec.Share*m) {
				carrying[name] = true
				continue
			}
		}
		low := func(hour int64) (float64, bool) {
			v, ok := mine[hour]
			m, has := peerMedian(hour)
			if !ok || !has || m < spec.PeerMinBytes || v >= spec.Share*m {
				return 0, false
			}
			return m, true
		}
		var mineRun, medians []float64
		hour := last
		for ; hour > last-3600*MaxHours; hour -= 3600 {
			m, ok := low(hour)
			if !ok {
				break
			}
			mineRun = append(mineRun, mine[hour])
			medians = append(medians, m)
		}
		if len(medians) < spec.Hours {
			continue
		}
		stalled[name] = Stall{
			Sent:  mean(mineRun),
			Peers: median(medians),
			Since: hour + 3600,
			Hours: len(medians),
		}
	}
	return stalled, carrying
}

func median(values []float64) float64 {
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func mean(values []float64) float64 {
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}
