package winvm

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// IfClass is what an interface that holds a route is, for the tunnel rule.
type IfClass string

const (
	IfOurs            IfClass = "ours"             // host vNIC of our switch (by switch ID)
	IfLoopback        IfClass = "loopback"         // IfType 24
	IfPhysical        IfClass = "physical"         // the set Get-NetAdapter -Physical lists
	IfExternalVSwitch IfClass = "external-vswitch" // vms_mp vNIC of an External switch bound to a physical NIC
	IfTunnel          IfClass = "tunnel-or-virtual"
)

// IANA interface types the rule reads.
const (
	ifTypeLoopback = 24
)

// Interface is one interface as the host reports it. Only numbers, booleans and
// component IDs decide; Description is for messages only.
type Interface struct {
	IfIndex              int    `json:"ifIndex"`
	IfType               uint32 `json:"ifType"`         // IANA: 6 ethernet, 71 wifi, 53 propVirtual, 23 ppp, 131 tunnel, 24 loopback
	PhysicalMedium       uint32 `json:"physicalMedium"` // NDIS_PHYSICAL_MEDIUM
	HardwareInterface    bool   `json:"hardwareInterface"`
	ConnectorPresent     bool   `json:"connectorPresent"`
	Hidden               bool   `json:"hidden"`      // seen only in MSFT_NetIPInterface (RAS PPP)
	ComponentID          string `json:"componentId"` // wintun, wireguard, tap0901, ovpn-dco, vms_mp, ms_sstpminiport ...
	Description          string `json:"description"` // display only, never matched
	VMSwitchID           string `json:"vmSwitchId"`
	SwitchType           string `json:"switchType"`
	ExternalOverPhysical bool   `json:"externalOverPhysical"`
}

// Route is one IPv4 route from the ActiveStore.
type Route struct {
	Prefix  netip.Prefix `json:"prefix"`
	IfIndex int          `json:"ifIndex"`
	NextHop netip.Addr   `json:"nextHop"`
	Metric  uint32       `json:"metric"`
}

// Egress is where the host's own route lookup sends one sample address.
type Egress struct {
	Dest    netip.Addr `json:"dest"`
	IfIndex int        `json:"ifIndex"`
}

// HostNet is one reading of the host's interfaces, routes and addresses.
type HostNet struct {
	Interfaces []Interface    `json:"interfaces"`
	Routes     []Route        `json:"routes"` // IPv4 ActiveStore
	Egress     []Egress       `json:"egress"` // Find-NetRoute for EgressSamples
	Addrs      []netip.Prefix `json:"addrs"`  // every host IPv4 with its prefix length
}

// EgressSamples are public addresses in both halves of the IPv4 space. A VPN that
// takes over with 0.0.0.0/1 plus 128.0.0.0/1 moves both halves, and an App
// Connector that pushes one public range moves only its own. The reader adds the
// addresses of both echo services to these.
var EgressSamples = []netip.Addr{
	netip.MustParseAddr("1.1.1.1"),
	netip.MustParseAddr("9.9.9.9"),
	netip.MustParseAddr("23.0.0.1"),
	netip.MustParseAddr("64.0.0.1"),
	netip.MustParseAddr("104.16.0.1"),
	netip.MustParseAddr("151.101.1.1"),
	netip.MustParseAddr("185.1.1.1"),
	netip.MustParseAddr("200.0.0.1"),
}

// fullTunnelBits is the shortest prefix that still counts as a split route. A
// tunnel route of /4 or shorter covers a sixteenth of the internet or more, so it
// is a full tunnel; this catches 0.0.0.0/1 plus 128.0.0.0/1.
const fullTunnelBits = 4

// Classify says what an interface is. Anything that cannot be shown to be ours,
// loopback, physical or an External vSwitch over a physical NIC is a tunnel, so
// the rule fails closed.
func Classify(i Interface, ourSwitchID string) IfClass {
	if ourSwitchID != "" && strings.EqualFold(i.VMSwitchID, ourSwitchID) {
		return IfOurs
	}
	if i.IfType == ifTypeLoopback {
		return IfLoopback
	}
	if i.Hidden {
		return IfTunnel
	}
	if strings.EqualFold(i.ComponentID, "vms_mp") {
		if strings.EqualFold(i.SwitchType, "External") && i.ExternalOverPhysical {
			return IfExternalVSwitch
		}
		return IfTunnel
	}
	if i.ConnectorPresent && i.HardwareInterface {
		return IfPhysical
	}
	return IfTunnel
}

// classOf classifies the interface behind an ifIndex. An ifIndex the reading
// does not know is a tunnel.
func classOf(h HostNet, ifIndex int, ourSwitchID string) (IfClass, Interface) {
	for _, i := range h.Interfaces {
		if i.IfIndex == ifIndex {
			return Classify(i, ourSwitchID), i
		}
	}
	return IfTunnel, Interface{IfIndex: ifIndex}
}

// egressOK is whether traffic may leave through a class of interface.
func egressOK(c IfClass) bool { return c == IfPhysical || c == IfExternalVSwitch }

// TunnelPrefixes lists every IPv4 route prefix whose interface is tunnel-class
// and that no prefix in covered already contains. The guard denies each one.
func TunnelPrefixes(h HostNet, ourSwitchID string, covered []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, r := range h.Routes {
		p := r.Prefix.Masked()
		if !p.IsValid() || !p.Addr().Is4() {
			continue
		}
		if c, _ := classOf(h, r.IfIndex, ourSwitchID); c != IfTunnel {
			continue
		}
		if coveredBy(p, covered) || slices.Contains(out, p) {
			continue
		}
		out = append(out, p)
	}
	sortPrefixes(out)
	return out
}

// coveredBy is whether some prefix in set contains all of p.
func coveredBy(p netip.Prefix, set []netip.Prefix) bool {
	for _, q := range set {
		if q.IsValid() && q.Bits() <= p.Bits() && q.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// FullTunnel says whether the host sends the internet through a tunnel: a sample
// address that leaves through a tunnel-class interface, a sample the reading has
// no answer for, or a tunnel route of /4 or shorter outside the static Denies.
// The reason names the case
// in plain words for the status.
func FullTunnel(h HostNet, ourSwitchID string) (bool, string) {
	static := StaticDeny()
	for _, r := range h.Routes {
		p := r.Prefix.Masked()
		// Windows puts 224.0.0.0/4 on every interface. A short route that sits
		// wholly inside a static Deny carries nothing to the internet.
		if !p.IsValid() || !p.Addr().Is4() || p.Bits() > fullTunnelBits || coveredBy(p, static) {
			continue
		}
		if c, i := classOf(h, r.IfIndex, ourSwitchID); c == IfTunnel {
			return true, fmt.Sprintf("Your PC sends all traffic through a VPN (%s via %s), so the apps would sell the VPN's address.", p, ifName(i))
		}
	}
	for _, s := range EgressSamples {
		e, ok := egressFor(h, s)
		if !ok {
			return true, fmt.Sprintf("Could not check where traffic to %s leaves this PC.", s)
		}
		if c, i := classOf(h, e.IfIndex, ourSwitchID); !egressOK(c) {
			return true, fmt.Sprintf("Your PC sends traffic to %s through a VPN (%s), so the apps would sell the VPN's address.", s, ifName(i))
		}
	}
	return false, ""
}

func egressFor(h HostNet, dest netip.Addr) (Egress, bool) {
	for _, e := range h.Egress {
		if e.Dest == dest {
			return e, true
		}
	}
	return Egress{}, false
}

func ifName(i Interface) string {
	if i.Description != "" {
		return i.Description
	}
	return fmt.Sprintf("interface %d", i.IfIndex)
}
