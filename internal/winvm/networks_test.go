package winvm

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestNetworkOK(t *testing.T) {
	home := NetworkID{GatewayMAC: "00-11-22-33-44-55", ProfileID: "{6F1C2D3E-0000-4000-8000-000000000001}"}
	approved := []NetworkID{home}
	cases := []struct {
		name string
		cur  CurrentNetwork
		ok   bool
		why  string
	}{
		{"approved, other spelling", CurrentNetwork{ID: NetworkID{GatewayMAC: "00:11:22:33:44:55", ProfileID: "6f1c2d3e-0000-4000-8000-000000000001"}}, true, ""},
		{"metered even when approved", CurrentNetwork{ID: home, Metered: true}, false, NetworkMetered},
		{"other gateway", CurrentNetwork{ID: NetworkID{GatewayMAC: "00-11-22-33-44-66", ProfileID: home.ProfileID}}, false, NetworkUnapproved},
		{"other profile", CurrentNetwork{ID: NetworkID{GatewayMAC: home.GatewayMAC, ProfileID: "{00000000-0000-4000-8000-000000000002}"}}, false, NetworkUnapproved},
		{"no gateway MAC", CurrentNetwork{ID: NetworkID{ProfileID: home.ProfileID}}, false, NetworkUnknown},
		{"no profile", CurrentNetwork{ID: NetworkID{GatewayMAC: home.GatewayMAC}}, false, NetworkUnknown},
		{"nothing read", CurrentNetwork{}, false, NetworkUnknown},
	}
	for _, c := range cases {
		ok, why := NetworkOK(c.cur, approved)
		if ok != c.ok || why != c.why {
			t.Errorf("%s: NetworkOK = %v %q, want %v %q", c.name, ok, why, c.ok, c.why)
		}
	}
	// An approved entry that is itself unreadable never matches an unreadable network.
	if ok, _ := NetworkOK(CurrentNetwork{}, []NetworkID{{}}); ok {
		t.Errorf("an empty network matched an empty approval")
	}
	// And the guard keeps the VM off on each refusal.
	for _, why := range []string{NetworkMetered, NetworkUnapproved, NetworkUnknown} {
		a := Decide(Boot, okInputs(func(in *Inputs) { in.NetworkOK = false; in.NetworkWhy = why }))
		if a.Connect || !a.Disconnect {
			t.Errorf("Boot on a %s network = %+v", why, a)
		}
	}
}

func TestHostDenies(t *testing.T) {
	got := HostDenies(testHost(), ourSwitchID)
	want := []netip.Prefix{
		pfx("100.70.1.2/32"), pfx("127.0.0.1/32"), pfx("172.20.0.9/32"), pfx("172.20.16.1/32"),
		pfx("192.168.77.0/24"), pfx("192.168.77.20/32"), pfx("192.168.77.255/32"),
		// Windows' multicast and broadcast routes are on-link too; WantACLs
		// drops them because 224/4 and 240/4 already cover them.
		pfx("224.0.0.0/4"), pfx("255.255.255.255/32"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("HostDenies = %v\nwant %v", got, want)
	}
	// The Default Switch's /20 and our /29 are on-link but not physical-class,
	// so they are not on-link Denies (the static 172.16/12 covers them anyway).
	if slices.Contains(got, pfx("172.20.16.0/20")) || slices.Contains(got, pfx("172.20.0.8/29")) {
		t.Errorf("a non-physical on-link prefix became a host Deny")
	}
}

// A LAN on public addresses is outside every static range. Its host address
// and its on-link prefix must still become Denies.
func TestNewHostAddressAndOnLinkPrefixBecomeDenies(t *testing.T) {
	h := testHost()
	h.Addrs = append(h.Addrs, pfx("198.51.100.20/24"))
	h.Routes = append(h.Routes, route("198.51.100.0/24", ifEthernet, "", 256))
	denies := HostDenies(h, ourSwitchID)
	for _, want := range []string{"198.51.100.20/32", "198.51.100.0/24"} {
		if !slices.Contains(denies, pfx(want)) {
			t.Errorf("HostDenies lacks %s: %v", want, denies)
		}
	}
	p := testPlan(t, nil)
	acls := p.WantACLs(Dynamic{HostAddrs: denies})
	if !hasRow(acls, "198.51.100.0/24") {
		t.Errorf("the public LAN has no Deny row")
	}
	// The /32 inside it is covered by the /24 and gets no row of its own.
	if hasRow(acls, "198.51.100.20/32") {
		t.Errorf("a covered host /32 got its own row")
	}
	// The same prefix on a tunnel interface is not an on-link Deny; the tunnel
	// rule denies it instead.
	h2 := testHost()
	h2.Routes = append(h2.Routes, route("203.0.113.0/24", ifWireGuard, "", 256))
	if slices.Contains(HostDenies(h2, ourSwitchID), pfx("203.0.113.0/24")) {
		t.Errorf("a tunnel's on-link prefix counted as the host's LAN")
	}
	if !slices.Contains(TunnelPrefixes(h2, ourSwitchID, StaticDeny()), pfx("203.0.113.0/24")) {
		t.Errorf("the tunnel rule missed it")
	}
}

func TestSubnetCollision(t *testing.T) {
	if hit, why := SubnetCollision(testHost(), ourSwitchID, testSubnet); hit {
		t.Fatalf("base table collides: %s", why)
	}
	// The Default Switch moves onto our /29 after a reboot: both its address and
	// its on-link route say so.
	moved := testHost()
	moved.Addrs = append(moved.Addrs, pfx("172.20.0.1/20"))
	moved.Routes = append(moved.Routes, route("172.20.0.0/20", ifDefaultSw, "", 256))
	hit, why := SubnetCollision(moved, ourSwitchID, testSubnet)
	if !hit || !strings.Contains(why, testSubnet.String()) {
		t.Errorf("Default Switch on our /29: %v %q", hit, why)
	}
	// Either signal alone is enough.
	onlyAddr := testHost()
	onlyAddr.Addrs = append(onlyAddr.Addrs, pfx("172.20.0.1/20"))
	if hit, _ := SubnetCollision(onlyAddr, ourSwitchID, testSubnet); !hit {
		t.Errorf("an overlapping host address alone is not a collision")
	}
	onlyRoute := testHost()
	onlyRoute.Routes = append(onlyRoute.Routes, route("172.20.0.0/20", ifDefaultSw, "", 256))
	if hit, _ := SubnetCollision(onlyRoute, ourSwitchID, testSubnet); !hit {
		t.Errorf("an overlapping on-link route alone is not a collision")
	}
	// A second address inside our /29 on any adapter collides.
	inside := testHost()
	inside.Addrs = append(inside.Addrs, pfx("172.20.0.12/32"))
	if hit, _ := SubnetCollision(inside, ourSwitchID, testSubnet); !hit {
		t.Errorf("a stray address inside our /29 is not a collision")
	}
	// Our gateway address held at a wider prefix is a collision: our vNIC was
	// readdressed, or another adapter took the address.
	for _, held := range []string{"172.20.0.9/24", "172.20.0.9/16"} {
		wider := testHost()
		for i, a := range wider.Addrs {
			if a == pfx("172.20.0.9/29") {
				wider.Addrs[i] = pfx(held)
			}
		}
		if hit, _ := SubnetCollision(wider, ourSwitchID, testSubnet); !hit {
			t.Errorf("our gateway held as %s is not a collision", held)
		}
		another := testHost()
		another.Addrs = append(another.Addrs, pfx(held))
		if hit, _ := SubnetCollision(another, ourSwitchID, testSubnet); !hit {
			t.Errorf("our gateway also held as %s on another adapter is not a collision", held)
		}
	}
	// The default route via a gateway is not a collision.
	if hit, _ := SubnetCollision(testHost(), ourSwitchID, testSubnet); hit {
		t.Errorf("the default route counted as a collision")
	}
	// And the guard disconnects on one.
	if a := Decide(Periodic, okInputs(func(in *Inputs) { in.Collision = why })); !a.Disconnect {
		t.Errorf("a collision did not disconnect")
	}
}
