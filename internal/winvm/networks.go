package winvm

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// NetworkID is how the guard tells one network from another: the default
// gateway's MAC address and the profile ID Windows gives the network.
type NetworkID struct {
	GatewayMAC string `json:"gatewayMac"`
	ProfileID  string `json:"profileId"`
}

// CurrentNetwork is the network the host is on now.
type CurrentNetwork struct {
	ID      NetworkID `json:"id"`
	Metered bool      `json:"metered"`
}

// Reasons NetworkOK refuses a network.
const (
	NetworkMetered    = "metered"
	NetworkUnapproved = "unapproved"
	NetworkUnknown    = "unknown"
)

// HostDenies lists what must be denied because the host itself holds it: each
// host IPv4 address as a /32, and each on-link prefix of a physical-class
// adapter. A LAN that uses public or unusual addresses is outside the private
// ranges of StaticDeny, and this denies it anyway.
func HostDenies(h HostNet, ourSwitchID string) []netip.Prefix {
	var out []netip.Prefix
	add := func(p netip.Prefix) {
		if p.IsValid() && p.Addr().Is4() && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	for _, a := range h.Addrs {
		if a.IsValid() {
			add(netip.PrefixFrom(a.Addr().Unmap(), 32))
		}
	}
	for _, r := range h.Routes {
		if !onLink(r) || r.Prefix.Bits() == 0 {
			continue
		}
		if c, _ := classOf(h, r.IfIndex, ourSwitchID); egressOK(c) {
			add(r.Prefix.Masked())
		}
	}
	sortPrefixes(out)
	return out
}

// onLink is whether a route has no gateway: its prefix is the adapter's own
// network.
func onLink(r Route) bool { return !r.NextHop.IsValid() || r.NextHop.IsUnspecified() }

// NetworkOK says whether the VM may earn on the current network: never on a
// metered one, never on one it cannot identify, and otherwise only on an
// approved one.
func NetworkOK(cur CurrentNetwork, approved []NetworkID) (bool, string) {
	if cur.Metered {
		return false, NetworkMetered
	}
	id, ok := normalizeNetwork(cur.ID)
	if !ok {
		return false, NetworkUnknown
	}
	for _, a := range approved {
		if n, ok := normalizeNetwork(a); ok && n == id {
			return true, ""
		}
	}
	return false, NetworkUnapproved
}

// normalizeNetwork puts both parts in one form; a network is identified only
// when both are known.
func normalizeNetwork(n NetworkID) (NetworkID, bool) {
	mac, err := NormalizeMAC(n.GatewayMAC)
	profile := strings.ToUpper(strings.Trim(strings.TrimSpace(n.ProfileID), "{}"))
	if err != nil || profile == "" {
		return NetworkID{}, false
	}
	return NetworkID{GatewayMAC: mac, ProfileID: profile}, true
}

// SubnetCollision says whether something other than our switch now uses our
// /29. The Default Switch and WSL pick a random /20 inside 172.16/12 at boot,
// and one can land on ours. It looks at host addresses (only our gateway may sit
// inside the /29, and no wider prefix may contain it) and at on-link routes of
// every other interface.
func SubnetCollision(h HostNet, ourSwitchID string, sub netip.Prefix) (bool, string) {
	gw := sub.Addr().Next()
	for _, a := range h.Addrs {
		if !a.IsValid() || !a.Addr().Is4() {
			continue
		}
		if a.Addr() == gw && a.Bits() == sub.Bits() {
			continue
		}
		if a.Masked().Overlaps(sub) {
			return true, fmt.Sprintf("Another network on this PC (%s) now uses the VM's addresses (%s).", a.Masked(), sub)
		}
	}
	for _, r := range h.Routes {
		if !onLink(r) || r.Prefix.Bits() == 0 || !r.Prefix.Masked().Overlaps(sub) {
			continue
		}
		if c, i := classOf(h, r.IfIndex, ourSwitchID); c != IfOurs {
			return true, fmt.Sprintf("Another network on this PC (%s on %s) now uses the VM's addresses (%s).", r.Prefix.Masked(), ifName(i), sub)
		}
	}
	return false, ""
}
