package winvm

import (
	"net/netip"
	"slices"
	"time"
)

// OwnIPTTL is how long an own public IP stays denied after the echo services
// last reported it.
const OwnIPTTL = 24 * time.Hour

// OwnIPs is the user's own public IPv4 addresses, as two echo services run by
// different companies report them. Each one is denied as a /32: NAT loopback
// through it would reach the LAN's port forwards.
type OwnIPs struct {
	Seen map[netip.Addr]time.Time `json:"seen"`
	// Filled is whether any answer ever arrived. Until then the VM stays
	// disconnected.
	Filled bool `json:"filled"`
}

// Observe records one round of answers. Any new IPv4 address is added at once:
// a wrong answer only denies one extra address, so no vote is needed. An address
// goes OwnIPTTL after its last sighting, counted only on rounds that got an
// answer: an empty round (both services failed) changes nothing, so a long
// outage keeps the list as it is.
func (o *OwnIPs) Observe(now time.Time, answers []netip.Addr) {
	var got []netip.Addr
	for _, a := range answers {
		a = a.Unmap()
		if a.Is4() && !a.IsUnspecified() {
			got = append(got, a)
		}
	}
	if len(got) == 0 {
		return
	}
	if o.Seen == nil {
		o.Seen = map[netip.Addr]time.Time{}
	}
	for _, a := range got {
		o.Seen[a] = now
	}
	o.Filled = true
	for a, t := range o.Seen {
		if now.Sub(t) >= OwnIPTTL {
			delete(o.Seen, a)
		}
	}
}

// Active is the addresses to deny now, sorted. Observe already dropped every
// one not reported within OwnIPTTL of the last answer.
func (o OwnIPs) Active() []netip.Addr {
	var out []netip.Addr
	for a := range o.Seen {
		out = append(out, a)
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return out
}

// Prefixes is Active as /32s, for Dynamic.OwnIPs.
func (o OwnIPs) Prefixes() []netip.Prefix {
	var out []netip.Prefix
	for _, a := range o.Active() {
		out = append(out, netip.PrefixFrom(a, 32))
	}
	return out
}
