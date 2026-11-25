package winvm

import "testing"

func TestDropTracker(t *testing.T) {
	var d DropTracker
	steps := []struct {
		s     DropSample
		delta uint64
		why   string
	}{
		{DropSample{Out: 500, In: 20, Source: "wmi"}, 0, "the first sample is only the baseline"},
		{DropSample{Out: 1500, In: 20, Source: "wmi"}, 1000, "1000 denied datagrams"},
		{DropSample{Out: 1537, In: 20, Source: "wmi"}, 37, "37 denied connects"},
		{DropSample{Out: 1537, In: 20, Source: "wmi"}, 0, "allowed traffic adds nothing"},
		{DropSample{Out: 12, In: 3, Source: "wmi"}, 15, "a lower value is a restart: all of it is new"},
		{DropSample{Out: 20, In: 1, Source: "wmi"}, 21, "only In went lower: still a restart, all of it is new"},
		{DropSample{Out: 900, In: 900, Source: "pdh"}, 0, "a new reader sets a new baseline"},
		{DropSample{Out: 905, In: 900, Source: "pdh"}, 5, "then counts on from it"},
	}
	var total uint64
	for _, st := range steps {
		got := d.Add(st.s)
		total += got
		if got != st.delta {
			t.Errorf("%s: delta %d, want %d", st.why, got, st.delta)
		}
		if d.Total != total {
			t.Errorf("%s: total %d, want %d", st.why, d.Total, total)
		}
	}
	// Last is a copy, not the caller's value.
	s := DropSample{Out: 1, Source: "pdh"}
	var e DropTracker
	e.Add(s)
	s.Out = 99
	if e.Last.Out != 1 {
		t.Errorf("Last aliases the caller's sample")
	}
}
