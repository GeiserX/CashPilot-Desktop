package winvm

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// The fixture below has the shape of the lab PC's real table, with neutral
// addresses: the LAN NIC sits under an External vSwitch, so the default route
// leaves through that switch's vEthernet; a tailnet client routes the LAN, a
// second private LAN, 100.64/10 and MagicDNS at metric 0; the Default Switch
// holds a /20 in 172.16/12; and our own switch holds the /29.
const (
	extSwitchID = "11111111-1111-1111-1111-111111111111"
	ourSwitchID = "22222222-2222-2222-2222-222222222222"
	dsSwitchID  = "33333333-3333-3333-3333-333333333333"

	ifLoop      = 1
	ifUplink    = 10 // vEthernet of the External switch
	ifBoundNIC  = 11 // the physical NIC under it; holds no IP
	ifWiFi      = 12
	ifEthernet  = 13
	ifTailnet   = 20
	ifWireGuard = 21
	ifTAP       = 22
	ifPPP       = 23
	ifDefaultSw = 30
	ifOurs      = 40
)

var testSubnet = netip.MustParsePrefix("172.20.0.8/29")

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }
func addr(s string) netip.Addr  { return netip.MustParseAddr(s) }

func testInterfaces() []Interface {
	return []Interface{
		{IfIndex: ifLoop, IfType: 24, Description: "Loopback"},
		{IfIndex: ifUplink, IfType: 6, ComponentID: "vms_mp", VMSwitchID: extSwitchID, SwitchType: "External", ExternalOverPhysical: true, Description: "Hyper-V Virtual Ethernet Adapter #2"},
		{IfIndex: ifBoundNIC, IfType: 6, PhysicalMedium: 14, HardwareInterface: true, ConnectorPresent: true, Description: "10GbE NIC"},
		{IfIndex: ifWiFi, IfType: 71, PhysicalMedium: 9, HardwareInterface: true, ConnectorPresent: true, Description: "Wi-Fi"},
		{IfIndex: ifEthernet, IfType: 6, PhysicalMedium: 14, HardwareInterface: true, ConnectorPresent: true, Description: "Ethernet 2"},
		{IfIndex: ifTailnet, IfType: 53, ComponentID: "wintun", Description: "Tailscale Tunnel"},
		{IfIndex: ifWireGuard, IfType: 53, ComponentID: "wireguard", Description: "WireGuard Tunnel"},
		{IfIndex: ifTAP, IfType: 6, ComponentID: "tap0901", Description: "TAP-Windows Adapter V9"},
		{IfIndex: ifPPP, IfType: 23, Hidden: true, ComponentID: "ms_sstpminiport", Description: "WAN Miniport (SSTP)"},
		{IfIndex: ifDefaultSw, IfType: 6, ComponentID: "vms_mp", VMSwitchID: dsSwitchID, SwitchType: "Internal", Description: "vEthernet (Default Switch)"},
		{IfIndex: ifOurs, IfType: 6, ComponentID: "vms_mp", VMSwitchID: ourSwitchID, SwitchType: "Internal", Description: "vEthernet (CashPilot-VM)"},
	}
}

func route(prefix string, ifIndex int, nextHop string, metric uint32) Route {
	r := Route{Prefix: pfx(prefix), IfIndex: ifIndex, Metric: metric}
	if nextHop != "" {
		r.NextHop = addr(nextHop)
	} else {
		r.NextHop = addr("0.0.0.0")
	}
	return r
}

func egressAll(ifIndex int) []Egress {
	var es []Egress
	for _, s := range EgressSamples {
		es = append(es, Egress{Dest: s, IfIndex: ifIndex})
	}
	return es
}

// testHost is the base table. Each test changes one thing.
func testHost() HostNet {
	return HostNet{
		Interfaces: testInterfaces(),
		Routes: []Route{
			route("0.0.0.0/0", ifUplink, "192.168.77.1", 256),
			route("192.168.77.0/24", ifUplink, "", 256),
			route("192.168.77.20/32", ifUplink, "", 256),
			route("192.168.77.255/32", ifUplink, "", 256),
			route("192.168.77.0/24", ifTailnet, "", 0),
			route("192.168.88.0/24", ifTailnet, "", 0),
			route("100.64.0.0/10", ifTailnet, "", 0),
			route("100.100.100.100/32", ifTailnet, "", 0),
			route("100.70.1.2/32", ifTailnet, "", 0),
			route("172.20.16.0/20", ifDefaultSw, "", 256),
			route("172.20.0.8/29", ifOurs, "", 256),
			route("172.20.0.9/32", ifOurs, "", 256),
			route("127.0.0.0/8", ifLoop, "", 256),
			route("224.0.0.0/4", ifUplink, "", 256),
			route("224.0.0.0/4", ifTailnet, "", 256),
			route("255.255.255.255/32", ifUplink, "", 256),
			route("255.255.255.255/32", ifTailnet, "", 256),
		},
		Egress: egressAll(ifUplink),
		Addrs: []netip.Prefix{
			pfx("192.168.77.20/24"),
			pfx("100.70.1.2/32"),
			pfx("172.20.16.1/20"),
			pfx("172.20.0.9/29"),
			pfx("127.0.0.1/8"),
		},
	}
}

func TestClassify(t *testing.T) {
	byIndex := map[int]IfClass{
		ifLoop:      IfLoopback,
		ifUplink:    IfExternalVSwitch,
		ifBoundNIC:  IfPhysical,
		ifWiFi:      IfPhysical,
		ifEthernet:  IfPhysical,
		ifTailnet:   IfTunnel,
		ifWireGuard: IfTunnel,
		ifTAP:       IfTunnel,
		ifPPP:       IfTunnel,
		ifDefaultSw: IfTunnel,
		ifOurs:      IfOurs,
	}
	for _, i := range testInterfaces() {
		if got := Classify(i, ourSwitchID); got != byIndex[i.IfIndex] {
			t.Errorf("Classify(%s) = %s, want %s", i.Description, got, byIndex[i.IfIndex])
		}
	}

	odd := []struct {
		name string
		i    Interface
		want IfClass
	}{
		{"External switch whose bound NIC is not physical", Interface{ComponentID: "vms_mp", SwitchType: "External"}, IfTunnel},
		{"Private switch", Interface{ComponentID: "vms_mp", SwitchType: "Private", ExternalOverPhysical: true}, IfTunnel},
		{"hidden adapter that claims hardware", Interface{Hidden: true, HardwareInterface: true, ConnectorPresent: true}, IfTunnel},
		{"Bluetooth PAN: hardware but no connector", Interface{IfType: 6, HardwareInterface: true}, IfTunnel},
		{"connector but not hardware", Interface{IfType: 6, ConnectorPresent: true}, IfTunnel},
		{"6to4 / Teredo", Interface{IfType: 131}, IfTunnel},
		{"empty switch ID never matches ours", Interface{ComponentID: "vms_mp", SwitchType: "Internal"}, IfTunnel},
		{"our switch ID in other case", Interface{ComponentID: "vms_mp", VMSwitchID: strings.ToUpper(ourSwitchID)}, IfOurs},
	}
	for _, c := range odd {
		if got := Classify(c.i, ourSwitchID); got != c.want {
			t.Errorf("%s: Classify = %s, want %s", c.name, got, c.want)
		}
	}
	// With no switch yet, an empty VMSwitchID must not read as ours.
	if got := Classify(Interface{ComponentID: "vms_mp"}, ""); got == IfOurs {
		t.Errorf("an empty switch ID matched an empty ours")
	}
}

// The default route through an External vSwitch over a physical NIC is
// physical-class: no full tunnel, and the tailnet's private routes are
// tunnel-class but every one sits inside a static Deny.
func TestRealTableExternalVSwitchDefaultIsNoTunnel(t *testing.T) {
	h := testHost()
	if full, why := FullTunnel(h, ourSwitchID); full {
		t.Fatalf("FullTunnel = true (%s), want false", why)
	}
	raw := TunnelPrefixes(h, ourSwitchID, nil)
	for _, p := range []string{"192.168.77.0/24", "192.168.88.0/24", "100.64.0.0/10", "100.100.100.100/32", "172.20.16.0/20"} {
		if !slices.Contains(raw, pfx(p)) {
			t.Errorf("%s is not tunnel-class in %v", p, raw)
		}
	}
	if got := TunnelPrefixes(h, ourSwitchID, StaticDeny()); len(got) != 0 {
		t.Errorf("covered tunnel routes gave new Denies: %v", got)
	}
	// So the ACL set is the static set alone.
	p := testPlan(t, nil)
	d := Dynamic{Tunnels: TunnelPrefixes(h, ourSwitchID, StaticDeny())}
	if got, want := len(p.WantACLs(d)), len(p.WantACLs(Dynamic{})); got != want {
		t.Errorf("WantACLs has %d rows with the tailnet routes, want %d", got, want)
	}
}

func TestTunnelRouteForPublicPrefixGetsDeny(t *testing.T) {
	h := testHost()
	h.Routes = append(h.Routes, route("198.51.100.0/24", ifWireGuard, "", 5))
	got := TunnelPrefixes(h, ourSwitchID, StaticDeny())
	if !slices.Equal(got, []netip.Prefix{pfx("198.51.100.0/24")}) {
		t.Fatalf("TunnelPrefixes = %v, want [198.51.100.0/24]", got)
	}
	if full, why := FullTunnel(h, ourSwitchID); full {
		t.Errorf("a split route is not a full tunnel, got %s", why)
	}
	p := testPlan(t, nil)
	if !hasRow(p.WantACLs(Dynamic{Tunnels: got}), "198.51.100.0/24") {
		t.Errorf("WantACLs has no Deny for the tunnel prefix")
	}
}

func TestSplitDefaultThroughWireGuardIsFullTunnel(t *testing.T) {
	h := testHost()
	h.Routes = append(h.Routes, route("0.0.0.0/1", ifWireGuard, "", 0), route("128.0.0.0/1", ifWireGuard, "", 0))
	// Route rule alone: the samples still say physical.
	if full, _ := FullTunnel(h, ourSwitchID); !full {
		t.Errorf("0/1 + 128/1 through WireGuard is not a full tunnel by the route rule")
	}
	// Egress rule alone: no short route, but the lookups go through the tunnel.
	h2 := testHost()
	h2.Egress = egressAll(ifWireGuard)
	if full, _ := FullTunnel(h2, ourSwitchID); !full {
		t.Errorf("samples leaving through WireGuard are not a full tunnel")
	}
	// Both, as a real client does it.
	h.Egress = egressAll(ifWireGuard)
	full, why := FullTunnel(h, ourSwitchID)
	if !full || !strings.Contains(why, "VPN") {
		t.Errorf("FullTunnel = %v %q, want true with the VPN reason", full, why)
	}
	// And the guard disconnects on it.
	a := Decide(Periodic, okInputs(func(in *Inputs) { in.FullTunnel = why }))
	if !a.Disconnect || a.Connect {
		t.Errorf("Decide on a full tunnel = %+v, want a disconnect", a)
	}
}

func TestExitNodeDefaultRouteIsFullTunnel(t *testing.T) {
	h := testHost()
	h.Routes = append(h.Routes, route("0.0.0.0/0", ifTailnet, "100.100.100.100", 0))
	h.Egress = egressAll(ifTailnet)
	if full, _ := FullTunnel(h, ourSwitchID); !full {
		t.Fatalf("0/0 through the tailnet (exit node) is not a full tunnel")
	}
	// The route alone is enough even if the lookups were not refreshed.
	h.Egress = egressAll(ifUplink)
	if full, _ := FullTunnel(h, ourSwitchID); !full {
		t.Errorf("0/0 through the tailnet route alone is not a full tunnel")
	}
}

func TestOneSampleThroughTunnelIsFullTunnel(t *testing.T) {
	// An App Connector pushes a public range; one sample falls inside it.
	h := testHost()
	h.Routes = append(h.Routes, route("104.16.0.0/13", ifTailnet, "", 0))
	for i := range h.Egress {
		if h.Egress[i].Dest == addr("104.16.0.1") {
			h.Egress[i].IfIndex = ifTailnet
		}
	}
	if full, _ := FullTunnel(h, ourSwitchID); !full {
		t.Errorf("a sample leaving through the tailnet is not caught")
	}
	if got := TunnelPrefixes(h, ourSwitchID, StaticDeny()); !slices.Contains(got, pfx("104.16.0.0/13")) {
		t.Errorf("the App Connector prefix gets no Deny: %v", got)
	}
}

func TestHiddenPPPAndUnknownIfIndexAreTunnels(t *testing.T) {
	h := testHost()
	h.Routes = append(h.Routes,
		route("203.0.113.0/24", ifPPP, "", 1),
		route("198.51.100.0/25", 99, "", 1), // no interface knows 99
	)
	got := TunnelPrefixes(h, ourSwitchID, StaticDeny())
	want := []netip.Prefix{pfx("198.51.100.0/25"), pfx("203.0.113.0/24")}
	if !slices.Equal(got, want) {
		t.Errorf("TunnelPrefixes = %v, want %v", got, want)
	}

	// The default route on a hidden PPP interface or an unknown ifIndex is a
	// full tunnel too.
	for _, idx := range []int{ifPPP, 99} {
		h := testHost()
		h.Egress = egressAll(idx)
		if full, _ := FullTunnel(h, ourSwitchID); !full {
			t.Errorf("egress through ifIndex %d is not a full tunnel", idx)
		}
	}
}

func TestMissingEgressFailsClosed(t *testing.T) {
	h := testHost()
	h.Egress = h.Egress[1:]
	full, why := FullTunnel(h, ourSwitchID)
	if !full || !strings.Contains(why, "Could not check") {
		t.Errorf("FullTunnel with a missing sample = %v %q, want true", full, why)
	}
	h.Egress = nil
	if full, _ := FullTunnel(h, ourSwitchID); !full {
		t.Errorf("FullTunnel with no lookups at all = false, want true")
	}
}

func TestEgressThroughOursOrLoopbackIsNotPhysical(t *testing.T) {
	for _, idx := range []int{ifOurs, ifLoop, ifDefaultSw} {
		h := testHost()
		h.Egress = egressAll(idx)
		if full, _ := FullTunnel(h, ourSwitchID); !full {
			t.Errorf("egress through ifIndex %d counted as physical", idx)
		}
	}
}

func TestWiFiToEthernetChangesNothing(t *testing.T) {
	onNIC := func(idx int) HostNet {
		h := testHost()
		h.Routes = []Route{
			route("0.0.0.0/0", idx, "192.168.77.1", 25),
			route("192.168.77.0/24", idx, "", 256),
			route("100.64.0.0/10", ifTailnet, "", 0),
		}
		h.Egress = egressAll(idx)
		return h
	}
	wifi, eth := onNIC(ifWiFi), onNIC(ifEthernet)
	for name, h := range map[string]HostNet{"wifi": wifi, "ethernet": eth} {
		if full, why := FullTunnel(h, ourSwitchID); full {
			t.Errorf("%s: FullTunnel = true (%s)", name, why)
		}
	}
	if a, b := TunnelPrefixes(wifi, ourSwitchID, StaticDeny()), TunnelPrefixes(eth, ourSwitchID, StaticDeny()); !slices.Equal(a, b) || len(a) != 0 {
		t.Errorf("TunnelPrefixes differ: wifi %v, ethernet %v", a, b)
	}
}

func TestEgressSamplesCoverBothHalves(t *testing.T) {
	var low, high bool
	for _, s := range EgressSamples {
		if pfx("0.0.0.0/1").Contains(s) {
			low = true
		}
		if pfx("128.0.0.0/1").Contains(s) {
			high = true
		}
		if coveredBy(netip.PrefixFrom(s, 32), StaticDeny()) {
			t.Errorf("sample %s is inside a static Deny, so it is not public", s)
		}
	}
	if !low || !high {
		t.Errorf("samples cover low half %v, high half %v; want both", low, high)
	}
}

func hasRow(acls []ACL, prefix string) bool {
	for _, a := range acls {
		if a.Remote == pfx(prefix) && a.Action == "Deny" {
			return true
		}
	}
	return false
}
