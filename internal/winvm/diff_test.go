package winvm

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// testDynamic is one pass's Denies: an own IP, a host address on a public LAN,
// and a tunnel prefix.
func testDynamic() Dynamic {
	return Dynamic{
		HostAddrs: []netip.Prefix{pfx("198.51.100.20/32")},
		OnLink:    []netip.Prefix{pfx("198.51.100.0/24")},
		OwnIPs:    []netip.Prefix{pfx("203.0.113.50/32")},
		Tunnels:   []netip.Prefix{pfx("192.0.2.0/24")},
	}
}

// liveFor is the live state that matches p exactly, as the reader would report
// it right after setup.
func liveFor(p Plan, d Dynamic) LiveState {
	bindings := map[string]bool{"ms_tcpip": true, "ms_pacer": true}
	for _, c := range p.BindingsOff {
		bindings[c] = false
	}
	integration := map[string]bool{}
	for id, on := range p.Integration {
		integration[strings.ToLower(id)] = on // the reader's case must not matter
	}
	const vnic = 40
	return LiveState{
		VMExists:     true,
		VMState:      "Running",
		Adapters:     []LiveAdapter{{ID: p.AdapterID, SwitchID: p.SwitchID, MAC: "00-15-5D-00-00-02"}},
		ACLs:         p.WantACLs(d),
		DhcpGuard:    true,
		RouterGuard:  true,
		StaticMAC:    "00155D000002",
		ProcCount:    2,
		ProcMax:      75,
		ProcWeight:   50,
		MemoryBytes:  p.MemoryBytes,
		Checkpoints:  "Disabled",
		AutoStart:    "Nothing",
		Integration:  integration,
		Disks:        1,
		SwitchType:   "Internal",
		SwitchPorts:  1,
		VNICIfIndex:  vnic,
		VNICMAC:      "00-15-5D-00-00-01",
		Bindings:     bindings,
		ProtocolsOn:  []string{"ms_tcpip"},
		NATs:         []LiveNAT{{Name: p.Names.NAT, Prefix: p.Subnet}, {Name: "OtherLab", Prefix: pfx("10.99.0.0/30")}},
		Firewall:     []LiveRule{{Direction: "Inbound", Action: "Block", Enabled: true, IfIndex: vnic}},
		HostNeighbor: &Neighbor{IP: p.GuestIP, MAC: "00-15-5d-00-00-02", State: "Permanent"},
		GCS:          []string{"999E53D4-3D5C-4C3E-8779-BED06EC056E1"},
		HostBuild:    p.HostBuild,
	}
}

type driftCase struct {
	name   string
	field  string // the Change.Field this mutation must produce
	mutate func(*LiveState, *Dynamic)
}

// driftCases mutates each compared field in turn. Every one must give its own
// change, so dropping any one comparison from Diff turns a case red, and a
// disconnect.
func driftCases() []driftCase {
	cs := []driftCase{
		{"vm gone", "vm", func(l *LiveState, _ *Dynamic) { l.VMExists = false }},
		{"second adapter", "adapters", func(l *LiveState, _ *Dynamic) {
			l.Adapters = append(l.Adapters, LiveAdapter{ID: "bbbbbbbb-0000-4000-8000-000000000009", SwitchID: extSwitchID})
		}},
		{"no adapter", "adapters", func(l *LiveState, _ *Dynamic) { l.Adapters = nil }},
		{"adapter replaced", "adapters", func(l *LiveState, _ *Dynamic) { l.Adapters[0].ID = "bbbbbbbb-0000-4000-8000-000000000009" }},
		{"adapter on another switch", "adapterSwitch", func(l *LiveState, _ *Dynamic) { l.Adapters[0].SwitchID = extSwitchID }},
		{"adapter MAC", "adapterMac", func(l *LiveState, _ *Dynamic) { l.Adapters[0].MAC = "00-15-5D-00-00-99" }},
		{"one Deny deleted", "acl", func(l *LiveState, _ *Dynamic) { l.ACLs = l.ACLs[1:] }},
		{"ipv6 Deny deleted", "acl", func(l *LiveState, _ *Dynamic) {
			for i, a := range l.ACLs {
				if a.Remote == pfx("::/0") {
					l.ACLs = append(l.ACLs[:i:i], l.ACLs[i+1:]...)
					return
				}
			}
		}},
		{"Deny turned Allow", "acl", func(l *LiveState, _ *Dynamic) { l.ACLs[0].Action = "Allow" }},
		{"Deny one-way", "acl", func(l *LiveState, _ *Dynamic) { l.ACLs[0].Direction = "Outbound" }},
		{"Deny inbound only", "acl", func(l *LiveState, _ *Dynamic) { l.ACLs[0].Direction = "Inbound" }},
		{"extra Allow row", "acl", func(l *LiveState, _ *Dynamic) {
			l.ACLs = append(l.ACLs, ACL{Action: "Allow", Direction: "Both", Remote: pfx("192.168.77.1/32")})
		}},
		{"Meter row", "meterAcl", func(l *LiveState, _ *Dynamic) {
			l.ACLs = append(l.ACLs, ACL{Action: "Meter", Direction: "Both", Remote: pfx("::/0")})
		}},
		{"new own IP not yet denied", "acl", func(_ *LiveState, d *Dynamic) {
			d.OwnIPs = append(d.OwnIPs, pfx("203.0.113.51/32"))
		}},
		{"new tunnel prefix not yet denied", "acl", func(_ *LiveState, d *Dynamic) {
			d.Tunnels = append(d.Tunnels, pfx("203.0.113.128/25"))
		}},
		{"extended ACL", "extAcl", func(l *LiveState, _ *Dynamic) { l.ExtACLs = 1 }},
		{"dhcp guard off", "dhcpGuard", func(l *LiveState, _ *Dynamic) { l.DhcpGuard = false }},
		{"router guard off", "routerGuard", func(l *LiveState, _ *Dynamic) { l.RouterGuard = false }},
		{"mac spoofing on", "macSpoofing", func(l *LiveState, _ *Dynamic) { l.MacSpoofing = true }},
		{"static MAC changed", "staticMac", func(l *LiveState, _ *Dynamic) { l.StaticMAC = "00155D000003" }},
		{"static MAC unread", "staticMac", func(l *LiveState, _ *Dynamic) { l.StaticMAC = "" }},
		{"vCPU count", "procCount", func(l *LiveState, _ *Dynamic) { l.ProcCount = 4 }},
		{"vCPU cap", "procMax", func(l *LiveState, _ *Dynamic) { l.ProcMax = 100 }},
		{"vCPU weight", "procWeight", func(l *LiveState, _ *Dynamic) { l.ProcWeight = 200 }},
		{"nested virtualization", "exposeVirt", func(l *LiveState, _ *Dynamic) { l.ExposeVirt = true }},
		{"memory", "memory", func(l *LiveState, _ *Dynamic) { l.MemoryBytes = 8 * GiB }},
		{"dynamic memory", "dynamicMemory", func(l *LiveState, _ *Dynamic) { l.DynamicMemory = true }},
		{"checkpoints", "checkpoints", func(l *LiveState, _ *Dynamic) { l.Checkpoints = "Standard" }},
		{"auto start", "autoStart", func(l *LiveState, _ *Dynamic) { l.AutoStart = "StartIfRunning" }},
		{"KVP on", "integration", func(l *LiveState, _ *Dynamic) { l.Integration[strings.ToLower(ISKVP)] = true }},
		{"guest service interface on", "integration", func(l *LiveState, _ *Dynamic) { l.Integration[strings.ToLower(ISGuestService)] = true }},
		{"VSS on", "integration", func(l *LiveState, _ *Dynamic) { l.Integration[strings.ToLower(ISVSS)] = true }},
		{"heartbeat unread", "integration", func(l *LiveState, _ *Dynamic) { delete(l.Integration, strings.ToLower(ISHeartbeat)) }},
		{"unknown integration service on", "integration", func(l *LiveState, _ *Dynamic) {
			l.Integration["ABCDEF00-0000-4000-8000-000000000000"] = true
		}},
		{"DVD attached", "dvd", func(l *LiveState, _ *Dynamic) { l.DVDs = 1 }},
		{"extra disk", "disks", func(l *LiveState, _ *Dynamic) { l.Disks = 2 }},
		{"no disk", "disks", func(l *LiveState, _ *Dynamic) { l.Disks = 0 }},
		{"GPU-P", "passthrough", func(l *LiveState, _ *Dynamic) { l.Passthrough = []string{"gpu-p"} }},
		{"switch made External", "switchType", func(l *LiveState, _ *Dynamic) { l.SwitchType = "External" }},
		{"second VM on our switch", "switchPorts", func(l *LiveState, _ *Dynamic) { l.SwitchPorts = 2 }},
		{"our switch reads empty while connected", "switchPorts", func(l *LiveState, _ *Dynamic) { l.SwitchPorts = 0 }},
		{"another VM on our switch while ours is disconnected", "switchPorts", func(l *LiveState, _ *Dynamic) {
			l.Adapters[0].SwitchID = ""
			l.SwitchPorts = 1
		}},
		{"host vNIC MAC", "vnicMac", func(l *LiveState, _ *Dynamic) { l.VNICMAC = "00-15-5D-00-00-77" }},
		{"third-party protocol", "protocols", func(l *LiveState, _ *Dynamic) { l.ProtocolsOn = append(l.ProtocolsOn, "insecure_npcap_proto") }},
		{"protocols unread", "protocols", func(l *LiveState, _ *Dynamic) { l.ProtocolsOn = nil }},
		{"protocols without IPv4", "protocols", func(l *LiveState, _ *Dynamic) { l.ProtocolsOn = []string{"insecure_npcap_proto"} }},
		{"NAT gone", "nat", func(l *LiveState, _ *Dynamic) { l.NATs = l.NATs[1:] }},
		{"NAT prefix changed", "nat", func(l *LiveState, _ *Dynamic) { l.NATs[0].Prefix = pfx("172.20.0.0/24") }},
		{"second NAT of ours", "nat", func(l *LiveState, _ *Dynamic) { l.NATs = append(l.NATs, l.NATs[0]) }},
		{"other NAT on our /29", "nat", func(l *LiveState, _ *Dynamic) {
			l.NATs = append(l.NATs, LiveNAT{Name: "Other", Prefix: pfx("172.20.0.0/16")})
		}},
		{"firewall rule gone", "firewall", func(l *LiveState, _ *Dynamic) { l.Firewall = nil }},
		{"firewall rule off", "firewall", func(l *LiveState, _ *Dynamic) { l.Firewall[0].Enabled = false }},
		{"firewall rule allows", "firewall", func(l *LiveState, _ *Dynamic) { l.Firewall[0].Action = "Allow" }},
		{"firewall rule outbound", "firewall", func(l *LiveState, _ *Dynamic) { l.Firewall[0].Direction = "Outbound" }},
		{"firewall rule on another interface", "firewall", func(l *LiveState, _ *Dynamic) { l.Firewall[0].IfIndex = 10 }},
		{"extra firewall rule", "firewall", func(l *LiveState, _ *Dynamic) {
			l.Firewall = append(l.Firewall, LiveRule{Direction: "Inbound", Action: "Allow", Enabled: true, IfIndex: 40})
		}},
		{"vNIC ifIndex unread", "firewall", func(l *LiveState, _ *Dynamic) { l.VNICIfIndex = 0; l.Firewall[0].IfIndex = 0 }},
		{"host neighbour gone", "hostNeighbor", func(l *LiveState, _ *Dynamic) { l.HostNeighbor = nil }},
		{"host neighbour MAC", "hostNeighbor", func(l *LiveState, _ *Dynamic) { l.HostNeighbor.MAC = "00-15-5D-00-00-66" }},
		{"host neighbour IP", "hostNeighbor", func(l *LiveState, _ *Dynamic) { l.HostNeighbor.IP = addr("172.20.0.11") }},
		{"host neighbour not permanent", "hostNeighbor", func(l *LiveState, _ *Dynamic) { l.HostNeighbor.State = "Reachable" }},
		{"enhanced session on", "enhancedSession", func(l *LiveState, _ *Dynamic) { l.Enhanced = true }},
		{"new hv_sock service", "gcs", func(l *LiveState, _ *Dynamic) {
			l.GCS = append(l.GCS, "0000abcd-facb-11e6-bd58-64006a7986d3")
		}},
		{"hv_sock services unread", "gcs", func(l *LiveState, _ *Dynamic) { l.GCS = nil }},
	}
	// Each BindingsOff component bound again, and each one missing from the
	// reading, is drift.
	for _, c := range BindingsOff() {
		cs = append(cs,
			driftCase{"binding " + c + " on", "bindings", func(l *LiveState, _ *Dynamic) { l.Bindings[c] = true }},
			driftCase{"binding " + c + " unread", "bindings", func(l *LiveState, _ *Dynamic) { delete(l.Bindings, c) }},
		)
	}
	return cs
}

// driftFailures runs every drift case through diff and Decide and returns the
// cases the fence did not catch. The real test wants none; the positive control
// wants all.
func driftFailures(t testing.TB, diff func(Plan, LiveState, Dynamic) []Change) []string {
	var failed []string
	for _, c := range driftCases() {
		p := testPlan(t, nil)
		d := testDynamic()
		live := liveFor(p, d)
		c.mutate(&live, &d)
		changes := diff(p, live, d)
		periodic := Decide(Periodic, okInputs(func(in *Inputs) { in.Changes = changes }))
		boot := Decide(Boot, okInputs(func(in *Inputs) { in.Changes = changes }))
		if !hasField(changes, c.field) || !periodic.Disconnect || periodic.Connect || !boot.Disconnect || boot.Connect || boot.Start {
			failed = append(failed, c.name)
		}
	}
	return failed
}

func hasField(cs []Change, field string) bool {
	for _, c := range cs {
		if c.Field == field {
			return true
		}
	}
	return false
}

func TestEveryComparedFieldDisconnects(t *testing.T) {
	for _, name := range driftFailures(t, Diff) {
		t.Errorf("drift not caught: %s", name)
	}
}

// The positive control: with Diff wired to return nothing, every drift case
// must fail. If this ever passes, the drift tests prove nothing.
func TestDriftTestsFailWithoutDiff(t *testing.T) {
	blind := func(Plan, LiveState, Dynamic) []Change { return nil }
	failed := driftFailures(t, blind)
	if n := len(driftCases()); len(failed) != n {
		t.Errorf("with a blind Diff only %d of %d drift cases failed; the suite cannot go red", len(failed), n)
	}
}

func TestMatchingStateHasNoChangesAndBootConnects(t *testing.T) {
	p := testPlan(t, nil)
	d := testDynamic()
	live := liveFor(p, d)
	if cs := Diff(p, live, d); len(cs) != 0 {
		t.Fatalf("Diff on the matching state = %+v", cs)
	}
	// After the boot routine disconnects, the adapter has no switch and our
	// switch has no VM adapter. That is not drift, or Boot could never
	// reconnect.
	live.Adapters[0].SwitchID = ""
	live.SwitchPorts = 0
	if cs := Diff(p, live, d); len(cs) != 0 {
		t.Errorf("a disconnected adapter is drift: %+v", cs)
	}
	// A removed hv_sock service is not a risk.
	live.GCS = []string{}
	if cs := Diff(p, live, d); len(cs) != 0 {
		t.Errorf("a removed hv_sock service is drift: %+v", cs)
	}
	a := Decide(Boot, okInputs(nil))
	if !a.Disconnect || !a.Connect || !a.Start {
		t.Errorf("Boot on a clean state = %+v, want disconnect, connect and start", a)
	}
}

// Get-VMNetworkAdapterAcl lists each Both rule as an Inbound and an Outbound
// row. Diff folds a matching pair into one Both row, so the real reading is
// not drift, but a row left without its pair still is.
func TestHyperVSplitACLRowsFold(t *testing.T) {
	p := testPlan(t, nil)
	d := testDynamic()
	split := func() []ACL {
		var rows []ACL
		for i, a := range p.WantACLs(d) {
			in, out := a, a
			in.Direction, out.Direction = "Inbound", "Outbound"
			if i%2 == 0 {
				rows = append(rows, out, in) // the order must not matter
			} else {
				rows = append(rows, in, out)
			}
		}
		return rows
	}
	live := liveFor(p, d)
	live.ACLs = split()
	if cs := Diff(p, live, d); len(cs) != 0 {
		t.Fatalf("the Hyper-V split reading is drift: %+v", cs)
	}

	cases := map[string]func([]ACL) []ACL{
		"one half of a pair gone": func(r []ACL) []ACL { return r[1:] },
		"one half turned Allow":   func(r []ACL) []ACL { r[0].Action = "Allow"; return r },
		"a pair plus a stray half": func(r []ACL) []ACL {
			return append(r, ACL{Action: "Deny", Direction: "Inbound", Remote: r[0].Remote})
		},
	}
	for name, mutate := range cases {
		live := liveFor(p, d)
		live.ACLs = mutate(split())
		if cs := Diff(p, live, d); !hasField(cs, "acl") {
			t.Errorf("%s: no acl drift, got %+v", name, cs)
		}
	}
}

func TestEnhancedSessionUnderKeepIsNotDriftButLabelled(t *testing.T) {
	lab, _ := LabNames(1)
	p := testPlan(t, func(p *Plan) { p.Names = lab; p.EnhancedSession = ESKeep })
	d := testDynamic()
	live := liveFor(p, d)
	live.Enhanced = true
	if cs := Diff(p, live, d); len(cs) != 0 {
		t.Errorf("Enhanced Session on under keep is drift: %+v", cs)
	}
	if got := EnhancedSessionLabel(p, true); got != "left on (lab)" {
		t.Errorf("label under keep = %q", got)
	}
	if got := EnhancedSessionLabel(testPlan(t, nil), false); got != "off" {
		t.Errorf("label when off = %q", got)
	}
	if got := EnhancedSessionLabel(testPlan(t, nil), true); got == "off" {
		t.Errorf("Enhanced Session on under off policy labelled off")
	}
}

// okInputs are inputs where nothing is wrong.
func okInputs(edit func(*Inputs)) Inputs {
	in := Inputs{NetworkOK: true, OwnIPsFilled: true}
	if edit != nil {
		edit(&in)
	}
	return in
}

// Every combination of every input: Periodic never connects or starts; Resume
// always does what Boot does; Boot connects only when nothing is wrong, and then
// disconnects first.
func TestDecideOverEveryCombination(t *testing.T) {
	whys := []string{"", NetworkMetered, NetworkUnapproved, NetworkUnknown}
	n := 0
	for mask := 0; mask < 1<<7; mask++ {
		for _, why := range whys {
			bit := func(i int) bool { return mask&(1<<i) != 0 }
			in := Inputs{NetworkOK: bit(0), OwnIPsFilled: bit(1), KeepOff: bit(2), ProofLock: bit(3), NetworkWhy: why}
			if bit(4) {
				in.Changes = []Change{{Field: "acl"}}
			}
			if bit(5) {
				in.FullTunnel = "full"
			}
			if bit(6) {
				in.Collision = "collision"
			}
			n++
			label := fmt.Sprintf("%+v", in)

			if a := Decide(Periodic, in); a.Connect || a.Start {
				t.Errorf("Periodic connected for %s: %+v", label, a)
			}
			boot, resume := Decide(Boot, in), Decide(Resume, in)
			if boot != resume {
				t.Errorf("Resume %+v differs from Boot %+v for %s", resume, boot, label)
			}
			clean := in.NetworkOK && in.OwnIPsFilled && !in.KeepOff && !in.ProofLock && in.Changes == nil && in.FullTunnel == "" && in.Collision == ""
			if boot.Connect != clean || boot.Start != clean {
				t.Errorf("Boot = %+v for %s, want connect=%v", boot, label, clean)
			}
			if boot.Connect && !boot.Disconnect {
				t.Errorf("Boot connected without disconnecting first for %s", label)
			}
			// Under a valid lock, Periodic leaves the proof alone, but Boot and
			// Resume still disconnect: a reboot mid-proof may have left holes open.
			if in.ProofLock && (boot != Action{Disconnect: true, Reason: boot.Reason} || Decide(Periodic, in) != Action{Reason: boot.Reason}) {
				t.Errorf("under a valid proof lock Boot = %+v and Periodic = %+v for %s, want Boot to disconnect only and Periodic to do nothing", boot, Decide(Periodic, in), label)
			}
			if !in.ProofLock && !clean {
				if p := Decide(Periodic, in); !p.Disconnect || p.Reason == "" {
					t.Errorf("Periodic did not disconnect with a reason for %s: %+v", label, p)
				}
			}
		}
	}
	if n != 512 {
		t.Fatalf("ran %d combinations, want 512", n)
	}
}

func TestProofLockExpires(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		taken time.Time
		want  bool
	}{
		{now.Add(-14 * time.Minute), true},
		{now.Add(-15 * time.Minute), false},
		{now.Add(-16 * time.Minute), false},
		{now.Add(time.Minute), false}, // from the future: a clock change
		{time.Time{}, false},
	}
	for _, c := range cases {
		if got := ProofLockValid(c.taken, now); got != c.want {
			t.Errorf("ProofLockValid(%v) = %v, want %v", now.Sub(c.taken), got, c.want)
		}
	}
	// A lock older than 15 minutes no longer holds the guard back.
	drift := []Change{{Field: "acl"}}
	stale := Decide(Periodic, okInputs(func(in *Inputs) {
		in.Changes = drift
		in.ProofLock = ProofLockValid(now.Add(-16*time.Minute), now)
	}))
	if !stale.Disconnect {
		t.Errorf("a 16-minute-old lock still held the guard back: %+v", stale)
	}
	fresh := Decide(Periodic, okInputs(func(in *Inputs) {
		in.Changes = drift
		in.ProofLock = ProofLockValid(now.Add(-time.Minute), now)
	}))
	if fresh.Disconnect {
		t.Errorf("the guard acted during the proof: %+v", fresh)
	}
}

func TestDecideReasonsReadPlainly(t *testing.T) {
	cases := map[string]Inputs{
		"protection changed (acl, bindings)": okInputs(func(in *Inputs) { in.Changes = []Change{{Field: "acl"}, {Field: "bindings"}, {Field: "acl"}} }),
		"did not approve":                    okInputs(func(in *Inputs) { in.NetworkOK = false; in.NetworkWhy = NetworkUnapproved }),
		"metered":                            okInputs(func(in *Inputs) { in.NetworkOK = false; in.NetworkWhy = NetworkMetered }),
		"public address":                     okInputs(func(in *Inputs) { in.OwnIPsFilled = false }),
		"turned the VM off":                  okInputs(func(in *Inputs) { in.KeepOff = true }),
	}
	for want, in := range cases {
		if got := Decide(Periodic, in).Reason; !strings.Contains(got, want) {
			t.Errorf("reason %q does not say %q", got, want)
		}
	}
}

// foldBoth folds an Inbound and an Outbound row into one Both row only when
// their actions match too, whichever comes first: a Deny one way and an
// Allow the other is two rows, never a Both Deny.
func TestFoldBothNeedsTheSameActionInEitherOrder(t *testing.T) {
	q := pfx("198.51.100.0/24")
	in := ACL{Action: "Deny", Direction: "Inbound", Remote: q}
	out := ACL{Action: "Allow", Direction: "Outbound", Remote: q}
	for _, rows := range [][]ACL{{in, out}, {out, in}} {
		got := foldBoth(rows)
		if len(got) != 2 {
			t.Fatalf("%v folded into %v", rows, got)
		}
		for _, a := range got {
			if a.Direction == "Both" {
				t.Fatalf("%v folded into a Both row: %v", rows, got)
			}
		}
	}
	same := ACL{Action: "Deny", Direction: "Outbound", Remote: q}
	for _, rows := range [][]ACL{{in, same}, {same, in}} {
		if got := foldBoth(rows); len(got) != 1 || got[0].Direction != "Both" || got[0].Action != "Deny" {
			t.Fatalf("%v did not fold into one Both Deny: %v", rows, got)
		}
	}
}
