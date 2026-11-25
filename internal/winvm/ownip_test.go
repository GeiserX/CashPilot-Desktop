package winvm

import (
	"encoding/json"
	"net/netip"
	"slices"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)

func TestOwnIPAddedAtOnce(t *testing.T) {
	var o OwnIPs
	o.Observe(t0, []netip.Addr{addr("203.0.113.7")})
	o.Observe(t0.Add(time.Minute), []netip.Addr{addr("203.0.113.7"), addr("198.51.100.4")}) // services disagree
	got := o.Active()
	if !slices.Equal(got, []netip.Addr{addr("198.51.100.4"), addr("203.0.113.7")}) {
		t.Errorf("Active = %v", got)
	}
	if !slices.Equal(o.Prefixes(), []netip.Prefix{pfx("198.51.100.4/32"), pfx("203.0.113.7/32")}) {
		t.Errorf("Prefixes = %v", o.Prefixes())
	}
	// IPv6, unspecified and invalid answers are ignored; a 4-in-6 one is unmapped.
	o.Observe(t0.Add(2*time.Minute), []netip.Addr{addr("2001:db8::1"), addr("0.0.0.0"), {}, addr("::ffff:192.0.2.1")})
	if !slices.Contains(o.Active(), addr("192.0.2.1")) || len(o.Active()) != 3 {
		t.Errorf("Active after odd answers = %v", o.Active())
	}
}

func TestOwnIPGoesOnly24HoursAfterLastSighting(t *testing.T) {
	var o OwnIPs
	old, cur := addr("203.0.113.7"), addr("203.0.113.8")
	o.Observe(t0, []netip.Addr{old})
	o.Observe(t0.Add(time.Hour), []netip.Addr{cur})
	o.Observe(t0.Add(23*time.Hour+59*time.Minute), []netip.Addr{cur})
	if !slices.Contains(o.Active(), old) {
		t.Errorf("the old IP went before 24 h")
	}
	o.Observe(t0.Add(24*time.Hour), []netip.Addr{cur})
	if slices.Contains(o.Active(), old) || !slices.Contains(o.Active(), cur) {
		t.Errorf("after 24 h Active = %v, want only %v", o.Active(), cur)
	}
	if _, kept := o.Seen[old]; kept {
		t.Errorf("the expired IP is still stored")
	}
}

func TestOwnIPsKeptWhenBothServicesFail(t *testing.T) {
	var o OwnIPs
	o.Observe(t0, []netip.Addr{addr("203.0.113.7")})
	for h := 1; h <= 72; h++ {
		o.Observe(t0.Add(time.Duration(h)*time.Hour), nil) // both failed
	}
	if !slices.Equal(o.Active(), []netip.Addr{addr("203.0.113.7")}) || !o.Filled {
		t.Errorf("a 3-day outage changed the list: %v filled=%v", o.Active(), o.Filled)
	}
}

func TestNeverFilledKeepsVMDisconnected(t *testing.T) {
	var o OwnIPs
	o.Observe(t0, nil)
	if o.Filled {
		t.Fatalf("an empty round filled the list")
	}
	a := Decide(Boot, okInputs(func(in *Inputs) { in.OwnIPsFilled = o.Filled }))
	if a.Connect || !a.Disconnect {
		t.Errorf("Boot with a never-filled list = %+v", a)
	}
}

func TestOwnIPsJSONRoundTrip(t *testing.T) {
	var o OwnIPs
	o.Observe(t0, []netip.Addr{addr("203.0.113.7")})
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var back OwnIPs
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(back.Active(), o.Active()) || back.Filled != o.Filled {
		t.Errorf("round trip: %s", b)
	}
}
