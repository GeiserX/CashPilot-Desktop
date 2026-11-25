package winvm

// DropSample is one reading of the host's drop counter for our adapter: packets
// dropped by a port ACL (FailedSecurityPolicy), out and in. Source says which
// reader answered, "wmi" or "pdh".
type DropSample struct {
	Out    uint64 `json:"out"`
	In     uint64 `json:"in"`
	Source string `json:"source"`
}

// DropTracker turns raw counter readings into a running total of blocked
// packets. The counter is raw and restarts at 0 when the VM or the host
// restarts.
type DropTracker struct {
	Last  *DropSample `json:"last"`
	Total uint64      `json:"total"`
}

// Add records a sample and returns how many packets were dropped since the last
// one. The first sample only sets the baseline, so it gives 0. A value lower
// than the last one means the counter restarted, so the whole sample is new
// drops. A change of reader also only sets a new baseline, because two readers
// need not start from the same value.
func (t *DropTracker) Add(s DropSample) (delta uint64) {
	switch {
	case t.Last == nil || t.Last.Source != s.Source:
		delta = 0
	case s.Out < t.Last.Out || s.In < t.Last.In:
		delta = s.Out + s.In
	default:
		delta = (s.Out - t.Last.Out) + (s.In - t.Last.In)
	}
	t.Total += delta
	last := s
	t.Last = &last
	return delta
}
