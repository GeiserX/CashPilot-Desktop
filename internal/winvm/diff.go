package winvm

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// LiveState is one reading of everything the guard compares. The reader that
// fills it comes later, behind the Hyper-V seam; this type and Diff are pure.
type LiveState struct {
	VMExists            bool            `json:"vmExists"`
	VMState             string          `json:"vmState"`
	Adapters            []LiveAdapter   `json:"adapters"` // every adapter on the VM
	ACLs                []ACL           `json:"acls"`     // as Hyper-V lists them: Meter rows included, a Both rule as an Inbound and an Outbound row
	PortACLs            []PortACL       `json:"portAcls,omitempty"`
	ExtACLs             int             `json:"extAcls"` // must be 0
	DhcpGuard           bool            `json:"dhcpGuard"`
	RouterGuard         bool            `json:"routerGuard"`
	MacSpoofing         bool            `json:"macSpoofing"`
	StaticMAC           string          `json:"staticMac"`
	ProcCount           int             `json:"procCount"`
	ProcMax             int             `json:"procMax"`
	ProcWeight          int             `json:"procWeight"`
	ExposeVirt          bool            `json:"exposeVirt"`
	MemoryBytes         uint64          `json:"memoryBytes"`
	DynamicMemory       bool            `json:"dynamicMemory"`
	Checkpoints         string          `json:"checkpoints"`
	AutoStart           string          `json:"autoStart"`
	Integration         map[string]bool `json:"integration"` // by service ID
	DVDs                int             `json:"dvds"`
	Disks               int             `json:"disks"`
	Passthrough         []string        `json:"passthrough"` // GPU-P, DDA, COM paths, USB: must be empty
	SwitchType          string          `json:"switchType"`
	SwitchPorts         int             `json:"switchPorts"` // VM adapters on our switch: 1 while ours is connected, 0 while it is not
	ManagementDHCP      bool            `json:"managementDhcp,omitempty"`
	ManagementAdapterID string          `json:"managementAdapterId,omitempty"`
	VNICIfIndex         int             `json:"vnicIfIndex"`
	VNICMAC             string          `json:"vnicMac"`
	Bindings            map[string]bool `json:"bindings"`    // component ID -> enabled, on our vNIC
	ProtocolsOn         []string        `json:"protocolsOn"` // enabled NetTrans-class components on our vNIC; ms_tcpip is always among them
	NATs                []LiveNAT       `json:"nats"`        // every NetNat on the host
	Firewall            []LiveRule      `json:"firewall"`    // rules of our group
	HostNeighbor        *Neighbor       `json:"hostNeighbor"`
	Enhanced            bool            `json:"enhancedSession"`
	GCS                 []string        `json:"gcs"` // GuestCommunicationServices key names; nil means not read, empty means none
	HostBuild           string          `json:"hostBuild"`
}

// LiveAdapter is one network adapter on the VM. SwitchID is empty while the
// adapter is disconnected.
type LiveAdapter struct {
	ID       string `json:"id"`
	SwitchID string `json:"switchId"`
	MAC      string `json:"mac"`
}

// LiveNAT is one NetNat.
type LiveNAT struct {
	ID     string       `json:"id,omitempty"`
	Name   string       `json:"name"`
	Prefix netip.Prefix `json:"prefix"`
}

// LiveRule is one firewall rule of our group, with the interface it applies to.
type LiveRule struct {
	Identity  string `json:"identity,omitempty"`
	Direction string `json:"direction"`
	Action    string `json:"action"`
	Enabled   bool   `json:"enabled"`
	IfIndex   int    `json:"ifIndex"`
}

// Neighbor is the host's static neighbour entry for the guest.
type Neighbor struct {
	IP    netip.Addr `json:"ip"`
	MAC   string     `json:"mac"`
	State string     `json:"state"`
}

// Change is one difference between the plan and the live state.
type Change struct {
	Field string `json:"field"`
	Want  string `json:"want"`
	Got   string `json:"got"`
}

// Diff compares the live state with the plan and the Denies of this pass. Any
// change means the fence is not the one that was proven. A part the reader
// could not fill counts as changed, so a broken reader fails closed: a missing
// binding or integration service, an empty static MAC, a vNIC ifIndex of 0, no
// ms_tcpip in ProtocolsOn, and a nil GCS list.
func Diff(p Plan, live LiveState, d Dynamic) []Change {
	var cs []Change
	add := func(field string, want, got any) {
		cs = append(cs, Change{Field: field, Want: fmt.Sprint(want), Got: fmt.Sprint(got)})
	}
	if !live.VMExists {
		add("vm", "present", "absent")
		return cs
	}

	// One adapter, ours, with its static MAC, and never on another switch.
	if len(live.Adapters) != 1 {
		add("adapters", 1, len(live.Adapters))
	} else {
		a := live.Adapters[0]
		if !strings.EqualFold(a.ID, p.AdapterID) {
			add("adapters", p.AdapterID, a.ID)
		}
		if a.SwitchID != "" && !strings.EqualFold(a.SwitchID, p.SwitchID) {
			add("adapterSwitch", p.SwitchID, a.SwitchID)
		}
		if m, _ := NormalizeMAC(a.MAC); m != p.GuestMAC {
			add("adapterMac", p.GuestMAC, a.MAC)
		}
	}

	// The exact ACL set. A Meter row is drift on its own: resource metering
	// adds them, and they can clash with our rows.
	want := p.WantACLs(d)
	wantS := make([]string, len(want))
	for i, a := range want {
		wantS[i] = a.String()
	}
	var rows []ACL
	for _, a := range live.ACLs {
		if strings.EqualFold(a.Action, "Meter") {
			add("meterAcl", "none", a.String())
			continue
		}
		rows = append(rows, ACL{Action: a.Action, Direction: a.Direction, Remote: a.Remote.Masked()})
	}
	var gotS []string
	for _, a := range foldBoth(rows) {
		gotS = append(gotS, a.String())
	}
	slices.Sort(gotS)
	slices.Sort(wantS)
	if !slices.Equal(gotS, wantS) {
		add("acl", diffSets(wantS, gotS, "missing"), diffSets(gotS, wantS, "extra"))
	}
	if live.ExtACLs != 0 {
		add("extAcl", 0, live.ExtACLs)
	}

	cmp := func(field string, want, got any) {
		if want != got {
			add(field, want, got)
		}
	}
	cmp("dhcpGuard", p.DhcpGuard, live.DhcpGuard)
	cmp("routerGuard", p.RouterGuard, live.RouterGuard)
	cmp("macSpoofing", p.MacSpoofing, live.MacSpoofing)
	if m, _ := NormalizeMAC(live.StaticMAC); m != p.GuestMAC {
		add("staticMac", p.GuestMAC, live.StaticMAC)
	}
	cmp("procCount", p.ProcCount, live.ProcCount)
	cmp("procMax", p.ProcMax, live.ProcMax)
	cmp("procWeight", p.ProcWeight, live.ProcWeight)
	cmp("exposeVirt", p.ExposeVirt, live.ExposeVirt)
	cmp("memory", p.MemoryBytes, live.MemoryBytes)
	cmp("dynamicMemory", false, live.DynamicMemory)
	cmp("checkpoints", p.Checkpoints, live.Checkpoints)
	cmp("autoStart", p.AutoStart, live.AutoStart)

	// Integration services by ID: each planned one as planned, and any other
	// one the host has must be off.
	liveIS := map[string]bool{}
	for id, on := range live.Integration {
		liveIS[strings.ToUpper(id)] = on
	}
	for _, id := range sortedKeys(p.Integration) {
		on, ok := liveIS[id]
		if !ok {
			add("integration", id+"="+onOff(p.Integration[id]), id+"=unread")
		} else if on != p.Integration[id] {
			add("integration", id+"="+onOff(p.Integration[id]), id+"="+onOff(on))
		}
	}
	for _, id := range sortedKeys(liveIS) {
		if _, planned := p.Integration[id]; !planned && liveIS[id] {
			add("integration", id+"=off", id+"=on")
		}
	}

	cmp("dvd", 0, live.DVDs)
	cmp("disks", 1, live.Disks)
	if len(live.Passthrough) != 0 {
		add("passthrough", "none", strings.Join(live.Passthrough, ","))
	}
	cmp("switchType", "Internal", live.SwitchType)
	// Our adapter is the only one on our switch. While the boot routine has it
	// disconnected, the switch must have none.
	wantPorts := 0
	if len(live.Adapters) == 1 && strings.EqualFold(live.Adapters[0].SwitchID, p.SwitchID) {
		wantPorts = 1
	}
	cmp("switchPorts", wantPorts, live.SwitchPorts)
	if p.ManagementAdapterID != "" && !strings.EqualFold(live.ManagementAdapterID, p.ManagementAdapterID) {
		add("managementAdapter", p.ManagementAdapterID, live.ManagementAdapterID)
	}
	if p.ManagementAdapterID != "" && live.ManagementDHCP {
		add("managementDHCP", "disabled", "enabled or unread")
	}
	if m, _ := NormalizeMAC(live.VNICMAC); m != p.HostVNICMAC {
		add("vnicMac", p.HostVNICMAC, live.VNICMAC)
	}

	// Every unbound component must read as off. One the reading lacks counts
	// as on.
	for _, c := range p.BindingsOff {
		on, ok := live.Bindings[c]
		if !ok {
			add("bindings", c+"=off", c+"=unread")
		} else if on {
			add("bindings", c+"=off", c+"=on")
		}
	}
	// IPv4 is the only protocol allowed on our vNIC; any other, a third-party
	// protocol driver included, could answer non-IP frames. The host's IPv4
	// stack is always on, so a list without it means the reader failed.
	if !slices.ContainsFunc(live.ProtocolsOn, func(c string) bool { return strings.EqualFold(c, "ms_tcpip") }) {
		add("protocols", "ms_tcpip", "unread")
	}
	for _, c := range live.ProtocolsOn {
		if !strings.EqualFold(c, "ms_tcpip") {
			add("protocols", "ms_tcpip", c)
		}
	}

	// Our NAT exactly, and no other NAT on our /29.
	ours := 0
	for _, n := range live.NATs {
		if strings.EqualFold(n.Name, p.Names.NAT) {
			ours++
			if p.NATID != "" && n.ID != p.NATID {
				add("nat", p.NATID, n.ID)
			}
			if n.Prefix.Masked() != p.Subnet {
				add("nat", p.Subnet, n.Prefix)
			}
		} else if n.Prefix.IsValid() && n.Prefix.Overlaps(p.Subnet) {
			add("nat", "no other NAT on "+p.Subnet.String(), n.Name+" "+n.Prefix.String())
		}
	}
	if ours != 1 {
		add("nat", "1 "+p.Names.NAT, ours)
	}

	// One rule in our group: inbound, block, enabled, on our vNIC.
	wantRule := LiveRule{Direction: "Inbound", Action: "Block", Enabled: true, IfIndex: live.VNICIfIndex, Identity: p.FirewallIdentity}
	if p.FirewallIdentity == "" && len(live.Firewall) == 1 {
		wantRule.Identity = live.Firewall[0].Identity // older plans lack a recorded fingerprint
	}
	if len(live.Firewall) != 1 || live.VNICIfIndex == 0 || live.Firewall[0] != wantRule {
		add("firewall", fmt.Sprintf("%+v", []LiveRule{wantRule}), fmt.Sprintf("%+v", live.Firewall))
	}

	wantN := Neighbor{IP: p.GuestIP, MAC: p.GuestMAC, State: "Permanent"}
	if n := live.HostNeighbor; n == nil {
		add("hostNeighbor", fmt.Sprintf("%+v", wantN), "none")
	} else if m, _ := NormalizeMAC(n.MAC); n.IP != wantN.IP || m != wantN.MAC || n.State != wantN.State {
		add("hostNeighbor", fmt.Sprintf("%+v", wantN), fmt.Sprintf("%+v", *n))
	}

	if p.EnhancedSession == ESOff && live.Enhanced {
		add("enhancedSession", "off", "on")
	}

	// A new hv_sock service is drift until the user reviews it (Repair takes a
	// new baseline). A removed one is not a risk. A list not read could hide a
	// new one.
	if live.GCS == nil {
		add("gcs", "only the setup list", "unread")
	}
	for _, k := range GCSBaseline(live.GCS) {
		if !slices.Contains(p.GCSBaseline, k) {
			add("gcs", "only the setup list", k)
		}
	}
	return cs
}

// foldBoth turns each Inbound and Outbound row with the same action and prefix
// into one Both row. Get-VMNetworkAdapterAcl lists a Both rule that way. A row
// left without its pair stays one-way, so it is still drift.
func foldBoth(rows []ACL) []ACL {
	opposite := map[string]string{"inbound": "Outbound", "outbound": "Inbound"}
	used := make([]bool, len(rows))
	var out []ACL
	for i, a := range rows {
		if used[i] {
			continue
		}
		used[i] = true
		if other, ok := opposite[strings.ToLower(a.Direction)]; ok {
			for j := i + 1; j < len(rows); j++ {
				b := rows[j]
				if !used[j] && strings.EqualFold(b.Direction, other) && b.Action == a.Action && b.Remote == a.Remote {
					used[j] = true
					a.Direction = "Both"
					break
				}
			}
		}
		out = append(out, a)
	}
	return out
}

func diffSets(a, b []string, label string) string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		return label + ": none"
	}
	return label + ": " + strings.Join(out, "; ")
}

func sortedKeys(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// EnhancedSessionLabel is what the status shows for Enhanced Session: "off", or
// "left on (lab)" when a keep policy left it on. It is never drift under keep,
// but it is never shown as off either.
func EnhancedSessionLabel(p Plan, liveOn bool) string {
	if !liveOn {
		return "off"
	}
	if p.EnhancedSession == ESKeep {
		return "left on (lab)"
	}
	return "on"
}

// Mode is why the guard runs.
type Mode int

const (
	// Boot runs at startup: disconnect first, then connect and start only on an
	// exact match.
	Boot Mode = iota
	// Resume runs after sleep or hibernation and behaves exactly like Boot.
	Resume
	// Periodic runs every 60 seconds and never connects.
	Periodic
)

// ProofLockTTL is how long a proof lock holds the guard back. A lock left by a
// crashed setup is ignored after this, and the guard acts again.
const ProofLockTTL = 15 * time.Minute

// ProofLockValid is whether a lock taken at takenAt still holds at now. A lock
// from the future (a clock change) does not hold.
func ProofLockValid(takenAt, now time.Time) bool {
	if takenAt.IsZero() || takenAt.After(now) {
		return false
	}
	return now.Sub(takenAt) < ProofLockTTL
}

// Inputs is everything Decide reads.
type Inputs struct {
	Changes      []Change
	NetworkOK    bool
	NetworkWhy   string
	OwnIPsFilled bool
	FullTunnel   string // "" when none
	Collision    string // "" when none
	KeepOff      bool   // the user's preference
	ProofLock    bool   // a valid lock: see ProofLockValid
}

// Action is what the guard does. Disconnect comes before Connect when both are
// set.
type Action struct {
	Disconnect, Connect, Start bool
	Reason                     string
}

// Decide is the one function allowed to connect the VM. Boot and Resume always
// disconnect first and connect only when nothing at all is wrong. Periodic only
// ever disconnects. While setup's proof holds the lock, Periodic does nothing:
// the proof opens holes on purpose and restores them itself. Boot and Resume
// still disconnect under the lock and never connect: a boot or a wake in the
// middle of a proof means the proof was cut short, maybe with its holes open,
// and a VM started by hand in that window must not reach the network.
func Decide(m Mode, in Inputs) Action {
	if in.ProofLock {
		const why = "The fence proof is running."
		if m == Periodic {
			return Action{Reason: why}
		}
		return Action{Disconnect: true, Reason: why}
	}
	why := blocker(in)
	if m == Periodic {
		if why == "" {
			return Action{}
		}
		return Action{Disconnect: true, Reason: why}
	}
	if why != "" {
		return Action{Disconnect: true, Reason: why}
	}
	return Action{Disconnect: true, Connect: true, Start: true}
}

// blocker is the first reason the VM must stay off the network, or "".
func blocker(in Inputs) string {
	switch {
	case len(in.Changes) > 0:
		fields := make([]string, 0, len(in.Changes))
		for _, c := range in.Changes {
			if !slices.Contains(fields, c.Field) {
				fields = append(fields, c.Field)
			}
		}
		return "Your VM is cut off from the internet because its protection changed (" + strings.Join(fields, ", ") + ")."
	case in.Collision != "":
		return in.Collision
	case in.FullTunnel != "":
		return in.FullTunnel
	case !in.NetworkOK:
		switch in.NetworkWhy {
		case NetworkMetered:
			return "This PC is on a metered network. Earning is paused."
		case NetworkUnapproved:
			return "This PC is on a network you did not approve. Earning is paused."
		}
		return "Could not tell which network this PC is on. Earning is paused."
	case !in.OwnIPsFilled:
		return "Could not learn this line's public address yet. Earning is paused."
	case in.KeepOff:
		return "You turned the VM off."
	}
	return ""
}
