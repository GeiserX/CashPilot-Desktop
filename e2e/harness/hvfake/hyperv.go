// Package hvfake is one stateful fake of the whole Hyper-V seam in
// internal/winvm, for the Secure VM's tests. It sits beside package harness,
// not in it: winvm imports internal/runtime, whose tests import harness, so
// harness cannot import winvm.
package hvfake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// HyperV is one stateful fake of the whole Hyper-V seam: winvm.Builder (and so
// Guarder and Reader), winvm.Prober and winvm.GuestProbe. It holds a host with
// interfaces, routes and addresses, switches with their host vNICs, NATs, the
// firewall group, and VMs with their adapters and port ACLs.
//
// It models the fence, so a probe from the guest goes red or green the way it
// would on a real host:
//   - port ACLs, longest prefix wins; a Deny drops the packet and raises the
//     adapter's drop counter, and pktmon logs the drop at the VM port;
//   - a Deny over the gateway also drops the guest's ARP for it, unless the
//     guest has a static neighbour entry for the gateway;
//   - the host firewall (L3) for packets to the host's vNIC address;
//   - ms_tcpip6 on the vNIC for the host's link-local address;
//   - the vNIC's bindings for raw frames;
//   - GuestCommunicationServices registration for hv_sock.
//
// Every packet that leaves the host after NAT gets a pktmon record on the
// interface the host's routes pick. The mutant switch (SetMutant) makes the
// fence ignore every Deny row while ReadLive still lists them, so only a check
// that sends probes can catch it.
type HyperV struct {
	mu sync.Mutex

	mutant   bool
	calls    []string
	failNext map[string]error
	seq      int

	ifaces   []winvm.Interface
	routes   []winvm.Route
	addrs    []netip.Prefix
	network  winvm.CurrentNetwork
	gcs      []string
	build    string
	enhanced bool
	guardian bool
	tasks    map[string]string

	// PublicIP is the line's public address, LANIP the host's address on its
	// physical network, Router the LAN's gateway.
	PublicIP, LANIP, Router netip.Addr
	// MagicDNS is what the tailnet resolver answers.
	MagicDNS map[string]netip.Addr
	// GuestAnswers are the canned answers of Guest scripts, by script name.
	GuestAnswers map[string]any

	switches  map[string]*fakeSwitch
	nats      map[string]netip.Prefix
	firewall  map[string]*fakeRule
	vms       map[string]*fakeVM
	canaries  []*fakeCanary
	capture   *fakeCapture
	records   []winvm.PktRecord
	listeners []*fakeListener
	guestARP  map[netip.Addr]string
}

type fakeSwitch struct {
	id, name, typ string
	vnicIf        int
	vnicMAC       string
	bindings      map[string]bool
	subnet        netip.Prefix
	gateway       netip.Addr
	linkLocal     netip.Addr
	neighbors     map[netip.Addr]string
}

type fakeRule struct {
	enabled bool
	ifIndex int
}

type fakeVM struct {
	id, name, state                   string
	adapter                           *fakeAdapter
	extra                             []*fakeAdapter // adapters someone added after setup
	procCount, procMax, procWeight    int
	memory                            uint64
	checkpoints, autoStart            string
	integration                       map[string]bool
	dvds, disks                       int
	exposeVirt, dynamicMemory, booted bool
}

type fakeAdapter struct {
	id, switchID, mac                   string
	acls                                []winvm.ACL
	dhcpGuard, routerGuard, macSpoofing bool
	out, in                             uint64
}

// Interface indexes of the fake host.
const (
	fakeIfLoopback = 1
	fakeIfEthernet = 7
	fakeIfTunnel   = 15
)

// protocols are the NetTrans-class components a vNIC can carry.
var protocols = []string{"ms_tcpip", "ms_tcpip6", "ms_lltdio", "ms_rspndr", "ms_lldp", "ms_implat"}

// New returns a host on a physical Ethernet network (192.0.2.0/24, its
// router at .1, the host at .10), with a tailnet-style tunnel adapter that
// carries 100.64.0.0/10, an approved-looking unmetered network, and Enhanced
// Session on.
func New() *HyperV {
	f := &HyperV{
		failNext: map[string]error{},
		build:    "26100.1000",
		enhanced: true,
		tasks:    map[string]string{},
		PublicIP: netip.MustParseAddr("203.0.113.7"),
		LANIP:    netip.MustParseAddr("192.0.2.10"),
		Router:   netip.MustParseAddr("192.0.2.1"),
		MagicDNS: map[string]netip.Addr{"host.example.ts.net": netip.MustParseAddr("100.64.0.5")},
		switches: map[string]*fakeSwitch{},
		nats:     map[string]netip.Prefix{},
		firewall: map[string]*fakeRule{},
		vms:      map[string]*fakeVM{},
		guestARP: map[netip.Addr]string{},
		gcs:      []string{"0000AAAA-FACB-11E6-BD58-64006A7986D3"},
		network: winvm.CurrentNetwork{ID: winvm.NetworkID{
			GatewayMAC: "00-00-5E-00-53-01", ProfileID: "{6A5D2C1E-0B7F-4C3A-9E21-5F8D7C6B4A30}"}},
	}
	f.ifaces = []winvm.Interface{
		{IfIndex: fakeIfLoopback, IfType: 24, Hidden: true, Description: "Loopback"},
		{IfIndex: fakeIfEthernet, IfType: 6, PhysicalMedium: 14, HardwareInterface: true, ConnectorPresent: true, ComponentID: `pci\ven_8086&dev_15f3`, Description: "Ethernet"},
		{IfIndex: fakeIfTunnel, IfType: 53, ComponentID: "wintun", Description: "Tunnel"},
	}
	f.routes = []winvm.Route{
		{Prefix: netip.MustParsePrefix("0.0.0.0/0"), IfIndex: fakeIfEthernet, NextHop: f.Router, Metric: 0},
		{Prefix: netip.MustParsePrefix("192.0.2.0/24"), IfIndex: fakeIfEthernet, NextHop: netip.IPv4Unspecified(), Metric: 256},
		{Prefix: netip.MustParsePrefix("100.64.0.0/10"), IfIndex: fakeIfTunnel, NextHop: netip.IPv4Unspecified(), Metric: 5},
		{Prefix: netip.MustParsePrefix("127.0.0.0/8"), IfIndex: fakeIfLoopback, NextHop: netip.IPv4Unspecified(), Metric: 256},
	}
	f.addrs = []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/8"),
		netip.MustParsePrefix("192.0.2.10/24"),
		netip.MustParsePrefix("100.64.0.5/32"),
	}
	return f
}

// SetMutant turns the broken fence on or off: with it on, Deny rows drop
// nothing, though they are still listed.
func (f *HyperV) SetMutant(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mutant = on
}

// FailNext makes the next call of the named method (for example "ReadLive")
// return err, or a generic error when err is nil.
func (f *HyperV) FailNext(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		err = fmt.Errorf("fake Hyper-V: %s failed", method)
	}
	f.failNext[method] = err
}

// SetRoute adds a route, or replaces the one with the same prefix and
// interface.
func (f *HyperV) SetRoute(r winvm.Route) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.routes {
		if x.Prefix == r.Prefix && x.IfIndex == r.IfIndex {
			f.routes[i] = r
			return
		}
	}
	f.routes = append(f.routes, r)
}

// SetGCS replaces the GuestCommunicationServices keys.
func (f *HyperV) SetGCS(keys []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gcs = winvm.GCSBaseline(keys)
}

// ResetCounters puts every drop counter back to 0, as a VM or host restart
// does.
func (f *HyperV) ResetCounters() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, vm := range f.vms {
		vm.adapter.out, vm.adapter.in = 0, 0
	}
}

// SetNetwork replaces the network the host is on.
func (f *HyperV) SetNetwork(n winvm.CurrentNetwork) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.network = n
}

// SetBuild replaces the host's Windows build, as a feature update does.
func (f *HyperV) SetBuild(build string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.build = build
}

// AddSwitch adds a switch of a type ("Internal", "External", "Private") with
// no host side, and returns its ID.
func (f *HyperV) AddSwitch(name, typ string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	s := &fakeSwitch{id: f.newID(), name: name, typ: typ, bindings: map[string]bool{}, neighbors: map[netip.Addr]string{}}
	f.switches[s.id] = s
	return s.id
}

// AddAdapter adds a second network adapter to a VM, on the named switch, or
// on none when switchName is empty, as another program could.
func (f *HyperV) AddAdapter(vmID, switchName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	a := &fakeAdapter{id: strings.ToUpper(f.newID()), mac: "00155D0A00FF"}
	if switchName != "" {
		s, ok := f.switchByName(switchName)
		if !ok {
			return fmt.Errorf("fake Hyper-V: no switch %q", switchName)
		}
		a.switchID = s.id
	}
	vm.extra = append(vm.extra, a)
	return nil
}

// SetVMState sets a VM's state ("Off", "Running"), as a user or a host
// restart could. It is not a seam call.
func (f *HyperV) SetVMState(vmID, state string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	vm.state = state
	return nil
}

// SetAdapterSwitch moves the VM's adapter to the named switch, as another
// program could. It is not a seam call.
func (f *HyperV) SetAdapterSwitch(vmID, switchName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	s, ok := f.switchByName(switchName)
	if !ok {
		return fmt.Errorf("fake Hyper-V: no switch %q", switchName)
	}
	vm.adapter.switchID = s.id
	return nil
}

// VNICLinkLocal is the IPv6 link-local address of a switch's host vNIC, the
// target of the proof's IPv6 steps.
func (f *HyperV) VNICLinkLocal(switchName string) netip.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.switchByName(switchName); ok {
		return s.linkLocal
	}
	return netip.Addr{}
}

// Calls lists every seam method called so far, in order.
func (f *HyperV) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// enter records a call and returns the error FailNext queued for it. The
// caller holds f.mu.
func (f *HyperV) enter(method string) error {
	f.calls = append(f.calls, method)
	if err, ok := f.failNext[method]; ok {
		delete(f.failNext, method)
		return err
	}
	return nil
}

func (f *HyperV) newID() string {
	f.seq++
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", f.seq, f.seq)
}

func (f *HyperV) vm(id string) (*fakeVM, error) {
	vm, ok := f.vms[strings.ToLower(id)]
	if !ok {
		return nil, fmt.Errorf("fake Hyper-V: no VM %s", id)
	}
	return vm, nil
}

func (f *HyperV) switchByName(name string) (*fakeSwitch, bool) {
	for _, s := range f.switches {
		if s.name == name {
			return s, true
		}
	}
	return nil, false
}

func (f *HyperV) switchByVNIC(ifIndex int) (*fakeSwitch, error) {
	for _, s := range f.switches {
		if s.vnicIf == ifIndex {
			return s, nil
		}
	}
	return nil, fmt.Errorf("fake Hyper-V: no vNIC with ifIndex %d", ifIndex)
}

// errClash is Hyper-V's answer to a second ACL row with the same prefix.
var errClash = errors.New("fake Hyper-V: 0x800700B7, an ACL with this address already exists")

func addACL(a *fakeAdapter, action string, p netip.Prefix) error {
	p = p.Masked()
	for _, x := range a.acls {
		if x.Remote == p {
			return errClash
		}
	}
	a.acls = append(a.acls, winvm.ACL{Action: action, Direction: "Both", Remote: p})
	return nil
}

func removeACL(a *fakeAdapter, action string, p netip.Prefix) error {
	p = p.Masked()
	for i, x := range a.acls {
		if x.Action == action && x.Remote == p {
			a.acls = slices.Delete(a.acls, i, i+1)
			return nil
		}
	}
	return fmt.Errorf("fake Hyper-V: no %s row for %s", action, p)
}

// BuildFenced builds the product Secure VM on the fake the way setup will:
// switch, NAT, VM, integration services, bindings off, firewall, the host's
// neighbour entry and, when staticARP is set, the guest's entry for the
// gateway, Enhanced Session off, the Denies for this host, then connect and
// start. It returns the plan that matches.
func (f *HyperV) BuildFenced(staticARP bool) (winvm.Plan, error) {
	ctx := context.Background()
	n := winvm.ProductNames()
	sub := netip.MustParsePrefix("172.31.250.0/29")
	swID, vnicMAC, err := f.EnsureSwitch(ctx, n, sub)
	if err != nil {
		return winvm.Plan{}, err
	}
	p, err := winvm.NewPlan(winvm.Options{Names: n, VHDXDir: `C:\ProgramData\CashPilot\vm`, Subnet: sub,
		HostVNICMAC: vnicMAC, GuestMAC: "00-15-5D-0A-00-02"}, nil)
	if err != nil {
		return winvm.Plan{}, err
	}
	p.SwitchID = swID
	if err := f.EnsureNAT(ctx, n, sub); err != nil {
		return winvm.Plan{}, err
	}
	if p.VMID, p.AdapterID, err = f.CreateVM(ctx, p, "", ""); err != nil {
		return winvm.Plan{}, err
	}
	live, err := f.ReadLive(ctx, n, p.VMID)
	if err != nil {
		return winvm.Plan{}, err
	}
	steps := []func() error{
		func() error { return f.SetIntegration(ctx, p.VMID, p.Integration) },
		func() error { return f.SetBindings(ctx, live.VNICIfIndex, p.BindingsOff) },
		func() error { return f.EnsureFirewall(ctx, n, live.VNICIfIndex) },
		func() error { return f.SetHostNeighbor(ctx, live.VNICIfIndex, p.GuestIP, p.GuestMAC) },
	}
	if staticARP {
		steps = append(steps, func() error { return f.Neighbor(ctx, p.Gateway, p.HostVNICMAC) })
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return winvm.Plan{}, err
		}
	}
	was, err := f.SetEnhancedSession(ctx, false)
	if err != nil {
		return winvm.Plan{}, err
	}
	p.RecordEnhancedSession(was)
	h, err := f.ReadHostNet(ctx, winvm.EgressSamples)
	if err != nil {
		return winvm.Plan{}, err
	}
	d := winvm.Dynamic{HostAddrs: winvm.HostDenies(h, p.SwitchID), Tunnels: winvm.TunnelPrefixes(h, p.SwitchID, winvm.StaticDeny())}
	var deny []netip.Prefix
	for _, a := range p.WantACLs(d) {
		deny = append(deny, a.Remote)
	}
	if err := f.AddDeny(ctx, p.VMID, deny); err != nil {
		return winvm.Plan{}, err
	}
	p.SetGCSBaseline(live.GCS)
	p.HostBuild = live.HostBuild
	if err := f.Connect(ctx, p.VMID, n.Switch); err != nil {
		return winvm.Plan{}, err
	}
	return p, f.Start(ctx, p.VMID)
}

// ---- Reader ----

// ReadLive lists what a real host lists, a Both rule as an Inbound and an
// Outbound row included.
func (f *HyperV) ReadLive(_ context.Context, n winvm.Names, vmID string) (winvm.LiveState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ReadLive"); err != nil {
		return winvm.LiveState{}, err
	}
	vm, ok := f.vms[strings.ToLower(vmID)]
	if !ok {
		return winvm.LiveState{}, nil
	}
	a := vm.adapter
	l := winvm.LiveState{
		VMExists:      true,
		VMState:       vm.state,
		Adapters:      []winvm.LiveAdapter{{ID: a.id, SwitchID: a.switchID, MAC: a.mac}},
		ACLs:          []winvm.ACL{},
		DhcpGuard:     a.dhcpGuard,
		RouterGuard:   a.routerGuard,
		MacSpoofing:   a.macSpoofing,
		StaticMAC:     a.mac,
		ProcCount:     vm.procCount,
		ProcMax:       vm.procMax,
		ProcWeight:    vm.procWeight,
		ExposeVirt:    vm.exposeVirt,
		MemoryBytes:   vm.memory,
		DynamicMemory: vm.dynamicMemory,
		Checkpoints:   vm.checkpoints,
		AutoStart:     vm.autoStart,
		Integration:   map[string]bool{},
		DVDs:          vm.dvds,
		Disks:         vm.disks,
		Passthrough:   []string{},
		NATs:          []winvm.LiveNAT{},
		Enhanced:      f.enhanced,
		GCS:           slices.Clone(f.gcs),
		HostBuild:     f.build,
	}
	for _, r := range a.acls {
		if r.Direction == "Both" {
			l.ACLs = append(l.ACLs, winvm.ACL{Action: r.Action, Direction: "Inbound", Remote: r.Remote},
				winvm.ACL{Action: r.Action, Direction: "Outbound", Remote: r.Remote})
		} else {
			l.ACLs = append(l.ACLs, r)
		}
	}
	for _, x := range vm.extra {
		l.Adapters = append(l.Adapters, winvm.LiveAdapter{ID: x.id, SwitchID: x.switchID, MAC: x.mac})
	}
	for id, on := range vm.integration {
		l.Integration[id] = on
	}
	if s, ok := f.switchByName(n.Switch); ok {
		l.SwitchType = s.typ
		for _, v := range f.vms {
			for _, x := range append([]*fakeAdapter{v.adapter}, v.extra...) {
				if x.switchID == s.id {
					l.SwitchPorts++
				}
			}
		}
		l.VNICIfIndex = s.vnicIf
		l.VNICMAC = s.vnicMAC
		l.Bindings = map[string]bool{}
		for c, on := range s.bindings {
			l.Bindings[c] = on
			if on && slices.Contains(protocols, c) {
				l.ProtocolsOn = append(l.ProtocolsOn, c)
			}
		}
		slices.Sort(l.ProtocolsOn)
		for ip, mac := range s.neighbors {
			l.HostNeighbor = &winvm.Neighbor{IP: ip, MAC: mac, State: "Permanent"}
		}
	}
	for name, p := range f.nats {
		l.NATs = append(l.NATs, winvm.LiveNAT{Name: name, Prefix: p})
	}
	if r, ok := f.firewall[n.FirewallGroup]; ok {
		l.Firewall = []winvm.LiveRule{{Direction: "Inbound", Action: "Block", Enabled: r.enabled, IfIndex: r.ifIndex}}
	}
	return l, nil
}

// ReadHostNet answers each sample with the interface of its longest-prefix
// route.
func (f *HyperV) ReadHostNet(_ context.Context, samples []netip.Addr) (winvm.HostNet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ReadHostNet"); err != nil {
		return winvm.HostNet{}, err
	}
	h := winvm.HostNet{
		Interfaces: slices.Clone(f.ifaces),
		Routes:     slices.Clone(f.routes),
		Addrs:      slices.Clone(f.addrs),
		Egress:     []winvm.Egress{},
	}
	for _, s := range samples {
		if r, ok := f.lookup(s); ok {
			h.Egress = append(h.Egress, winvm.Egress{Dest: s, IfIndex: r.IfIndex})
		}
	}
	return h, nil
}

// lookup is the host's route lookup: longest prefix, then lowest metric.
func (f *HyperV) lookup(dst netip.Addr) (winvm.Route, bool) {
	var best winvm.Route
	found := false
	for _, r := range f.routes {
		if !r.Prefix.Contains(dst) {
			continue
		}
		if !found || r.Prefix.Bits() > best.Prefix.Bits() || (r.Prefix.Bits() == best.Prefix.Bits() && r.Metric < best.Metric) {
			best, found = r, true
		}
	}
	return best, found
}

// ReadNetwork returns the current network.
func (f *HyperV) ReadNetwork(context.Context) (winvm.CurrentNetwork, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ReadNetwork"); err != nil {
		return winvm.CurrentNetwork{}, err
	}
	return f.network, nil
}

// DropCounters reads the adapter's FailedSecurityPolicy drops, as CIM would.
func (f *HyperV) DropCounters(_ context.Context, vmID, adapterID string) (winvm.DropSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DropCounters"); err != nil {
		return winvm.DropSample{}, err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return winvm.DropSample{}, err
	}
	if !strings.EqualFold(vm.adapter.id, adapterID) {
		return winvm.DropSample{}, winvm.ErrNoDropCounter
	}
	return winvm.DropSample{Out: vm.adapter.out, In: vm.adapter.in, Source: "wmi"}, nil
}

// Usage returns fixed host measurements.
func (f *HyperV) Usage(_ context.Context, vmID string) (winvm.Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Usage"); err != nil {
		return winvm.Usage{}, err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return winvm.Usage{}, err
	}
	return winvm.Usage{CPUPercent: 3, MemoryBytes: vm.memory, UptimeSeconds: 600, DiskBytes: 20 << 30}, nil
}

// ---- Guarder ----

// Disconnect takes every adapter of the VM off its switch.
func (f *HyperV) Disconnect(_ context.Context, vmID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Disconnect"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	vm.adapter.switchID = ""
	for _, x := range vm.extra {
		x.switchID = ""
	}
	return nil
}

// Connect puts the VM's one adapter on the switch with that name. Like the
// real script, it refuses a VM with more than one adapter.
func (f *HyperV) Connect(_ context.Context, vmID, switchName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Connect"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	if len(vm.extra) > 0 {
		return errors.New("fake Hyper-V: the VM must have exactly one network adapter")
	}
	s, ok := f.switchByName(switchName)
	if !ok {
		return fmt.Errorf("fake Hyper-V: no switch %q", switchName)
	}
	vm.adapter.switchID = s.id
	return nil
}

// Start runs the VM.
func (f *HyperV) Start(_ context.Context, vmID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Start"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	vm.state, vm.booted = "Running", true
	return nil
}

// AddDeny adds one Deny Both row per prefix. A prefix that already has a row
// fails as Hyper-V does.
func (f *HyperV) AddDeny(_ context.Context, vmID string, ps []netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("AddDeny"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if err := addACL(vm.adapter, "Deny", p); err != nil {
			return err
		}
	}
	return nil
}

// RemoveDeny removes the Deny row of each prefix.
func (f *HyperV) RemoveDeny(_ context.Context, vmID string, ps []netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RemoveDeny"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if err := removeACL(vm.adapter, "Deny", p); err != nil {
			return err
		}
	}
	return nil
}

// ---- Builder ----

// EnsureSwitch creates the Internal switch with its host vNIC, every binding
// on, the gateway address and the on-link route, or returns the one that
// exists.
func (f *HyperV) EnsureSwitch(_ context.Context, n winvm.Names, sub netip.Prefix) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("EnsureSwitch"); err != nil {
		return "", "", err
	}
	if s, ok := f.switchByName(n.Switch); ok {
		return s.id, s.vnicMAC, nil
	}
	f.seq++
	s := &fakeSwitch{
		id: f.newID(), name: n.Switch, typ: "Internal",
		vnicIf:    20 + f.seq,
		vnicMAC:   fmt.Sprintf("00155D00%04X", f.seq),
		bindings:  map[string]bool{"ms_pacer": true},
		subnet:    sub,
		gateway:   sub.Addr().Next(),
		linkLocal: netip.MustParseAddr(fmt.Sprintf("fe80::%x", 0x1000+f.seq)),
		neighbors: map[netip.Addr]string{},
	}
	for _, c := range append(slices.Clone(protocols), "ms_server", "ms_msclient") {
		s.bindings[c] = true
	}
	f.switches[s.id] = s
	f.ifaces = append(f.ifaces, winvm.Interface{IfIndex: s.vnicIf, IfType: 6, ComponentID: "vms_mp", Description: "vEthernet", VMSwitchID: s.id, SwitchType: "Internal"})
	f.addrs = append(f.addrs, netip.PrefixFrom(s.gateway, sub.Bits()))
	f.routes = append(f.routes, winvm.Route{Prefix: sub, IfIndex: s.vnicIf, NextHop: netip.IPv4Unspecified(), Metric: 256})
	return s.id, s.vnicMAC, nil
}

// EnsureNAT records the NetNat.
func (f *HyperV) EnsureNAT(_ context.Context, n winvm.Names, sub netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("EnsureNAT"); err != nil {
		return err
	}
	f.nats[n.NAT] = sub
	return nil
}

// CreateVM creates the VM off, with the plan's settings, one adapter on our
// switch with its port guards, and Hyper-V's default integration services
// (all on but the Guest Service Interface). Each ISO counts as a DVD drive.
func (f *HyperV) CreateVM(_ context.Context, p winvm.Plan, iso, answerISO string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateVM"); err != nil {
		return "", "", err
	}
	s, ok := f.switchByName(p.Names.Switch)
	if !ok {
		return "", "", fmt.Errorf("fake Hyper-V: no switch %q", p.Names.Switch)
	}
	vm := &fakeVM{
		id: f.newID(), name: p.Names.VM, state: "Off",
		adapter: &fakeAdapter{id: strings.ToUpper(f.newID()), switchID: s.id, mac: p.GuestMAC,
			dhcpGuard: p.DhcpGuard, routerGuard: p.RouterGuard, macSpoofing: p.MacSpoofing},
		procCount: p.ProcCount, procMax: p.ProcMax, procWeight: p.ProcWeight,
		memory: p.MemoryBytes, checkpoints: p.Checkpoints, autoStart: p.AutoStart,
		integration: map[string]bool{
			winvm.ISHeartbeat: true, winvm.ISShutdown: true, winvm.ISTimeSync: true,
			winvm.ISKVP: true, winvm.ISVSS: true, winvm.ISGuestService: false,
		},
		disks: 1,
	}
	for _, x := range []string{iso, answerISO} {
		if x != "" {
			vm.dvds++
		}
	}
	f.vms[vm.id] = vm
	return vm.id, vm.adapter.id, nil
}

// SetIntegration sets integration services by ID.
func (f *HyperV) SetIntegration(_ context.Context, vmID string, want map[string]bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetIntegration"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	for id, on := range want {
		vm.integration[strings.ToUpper(id)] = on
	}
	return nil
}

// SetBindings turns components off on a vNIC.
func (f *HyperV) SetBindings(_ context.Context, ifIndex int, off []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetBindings"); err != nil {
		return err
	}
	s, err := f.switchByVNIC(ifIndex)
	if err != nil {
		return err
	}
	for _, c := range off {
		s.bindings[c] = false
	}
	return nil
}

// EnsureFirewall creates the group's one inbound Block rule on the vNIC.
func (f *HyperV) EnsureFirewall(_ context.Context, n winvm.Names, ifIndex int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("EnsureFirewall"); err != nil {
		return err
	}
	f.firewall[n.FirewallGroup] = &fakeRule{enabled: true, ifIndex: ifIndex}
	return nil
}

// SetHostNeighbor adds the host's static entry for the guest.
func (f *HyperV) SetHostNeighbor(_ context.Context, ifIndex int, ip netip.Addr, mac string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetHostNeighbor"); err != nil {
		return err
	}
	s, err := f.switchByVNIC(ifIndex)
	if err != nil {
		return err
	}
	s.neighbors[ip] = mac
	return nil
}

// SetEnhancedSession sets the host-wide setting and returns the old value.
func (f *HyperV) SetEnhancedSession(_ context.Context, on bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetEnhancedSession"); err != nil {
		return false, err
	}
	was := f.enhanced
	f.enhanced = on
	return was, nil
}

// EnsureGuardian creates the vTPM guardian if none exists.
func (f *HyperV) EnsureGuardian(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("EnsureGuardian"); err != nil {
		return false, err
	}
	if f.guardian {
		return false, nil
	}
	f.guardian = true
	return true, nil
}

// RegisterGuardTask records the guard task.
func (f *HyperV) RegisterGuardTask(_ context.Context, n winvm.Names, exe string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RegisterGuardTask"); err != nil {
		return err
	}
	f.tasks[n.GuardTask] = exe
	return nil
}

// Guest returns the canned answer for the script, through JSON and the 64 KB
// cap.
func (f *HyperV) Guest(_ context.Context, vmID string, _ winvm.Cred, s winvm.GuestScript, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Guest"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	if vm.state != "Running" {
		return errors.New("fake Hyper-V: PowerShell Direct needs a running VM")
	}
	b, err := json.Marshal(f.GuestAnswers[s.Name])
	if err != nil {
		return err
	}
	if len(b) > 64<<10 {
		return errors.New("fake Hyper-V: guest answer over 64 KB")
	}
	return json.Unmarshal(b, out)
}

// Remove deletes what the plan names, by ID, and lists what it could not.
func (f *HyperV) Remove(_ context.Context, p winvm.Plan) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Remove"); err != nil {
		return nil, err
	}
	var left []string
	if _, ok := f.vms[strings.ToLower(p.VMID)]; ok {
		delete(f.vms, strings.ToLower(p.VMID))
	}
	if s, ok := f.switches[strings.ToLower(p.SwitchID)]; ok {
		delete(f.switches, s.id)
		f.ifaces = slices.DeleteFunc(f.ifaces, func(i winvm.Interface) bool { return i.IfIndex == s.vnicIf })
		f.routes = slices.DeleteFunc(f.routes, func(r winvm.Route) bool { return r.IfIndex == s.vnicIf })
		f.addrs = slices.DeleteFunc(f.addrs, func(a netip.Prefix) bool { return a.Addr() == s.gateway })
	}
	delete(f.nats, p.Names.NAT)
	delete(f.firewall, p.Names.FirewallGroup)
	delete(f.tasks, p.Names.GuardTask)
	if p.EnhancedSession == winvm.ESOff && p.EnhancedSessionOriginal != nil {
		f.enhanced = *p.EnhancedSessionOriginal
	}
	if p.CreatedGuardian {
		f.guardian = false
	}
	return left, nil
}

// ---- Prober ----

type fakeCanary struct {
	f       *HyperV
	ip      netip.Addr
	port    uint16
	token   string
	arrived []string
}

func (c *fakeCanary) Arrived() []string {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return slices.Clone(c.arrived)
}

func (c *fakeCanary) Close() error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.canaries = slices.DeleteFunc(c.f.canaries, func(x *fakeCanary) bool { return x == c })
	return nil
}

// Canary listens on a host address and port for a token.
func (f *HyperV) Canary(_ context.Context, ip netip.Addr, port uint16, token string) (winvm.Canary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Canary"); err != nil {
		return nil, err
	}
	c := &fakeCanary{f: f, ip: ip, port: port, token: token}
	f.canaries = append(f.canaries, c)
	return c, nil
}

type fakeCapture struct {
	f       *HyperV
	filters []winvm.PktFilter
	from    int
}

// Stop ends the session and returns the records that match its filters.
func (c *fakeCapture) Stop(context.Context) ([]winvm.PktRecord, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if c.f.capture != c {
		return nil, errors.New("fake Hyper-V: this pktmon session is not running")
	}
	c.f.capture = nil
	var out []winvm.PktRecord
	for _, r := range c.f.records[c.from:] {
		for _, fl := range c.filters {
			if fl.Addr == r.Dst && (fl.Proto == "" || fl.Proto == r.Proto) && (fl.Port == 0 || fl.Port == r.Port) {
				out = append(out, r)
				break
			}
		}
	}
	return out, nil
}

// Pktmon starts a capture on every NIC. It refuses while one runs.
func (f *HyperV) Pktmon(_ context.Context, fl []winvm.PktFilter) (winvm.Capture, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Pktmon"); err != nil {
		return nil, err
	}
	if f.capture != nil {
		return nil, errors.New("fake Hyper-V: a pktmon session already runs")
	}
	f.capture = &fakeCapture{f: f, filters: slices.Clone(fl), from: len(f.records)}
	return f.capture, nil
}

// SetL3 turns the group's firewall rule on or off.
func (f *HyperV) SetL3(_ context.Context, n winvm.Names, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetL3"); err != nil {
		return err
	}
	r, ok := f.firewall[n.FirewallGroup]
	if !ok {
		return fmt.Errorf("fake Hyper-V: no firewall group %q", n.FirewallGroup)
	}
	r.enabled = on
	return nil
}

// SetBinding binds or unbinds one component on a vNIC. Tests also use it as
// the fault hook for a binding that comes back.
func (f *HyperV) SetBinding(_ context.Context, ifIndex int, component string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetBinding"); err != nil {
		return err
	}
	s, err := f.switchByVNIC(ifIndex)
	if err != nil {
		return err
	}
	s.bindings[component] = on
	return nil
}

// AddAllow adds an Allow Both row. A prefix with a row already fails.
func (f *HyperV) AddAllow(_ context.Context, vmID string, p netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("AddAllow"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	return addACL(vm.adapter, "Allow", p)
}

// RemoveAllow removes an Allow row.
func (f *HyperV) RemoveAllow(_ context.Context, vmID string, p netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RemoveAllow"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	return removeACL(vm.adapter, "Allow", p)
}

type fakeListener struct {
	f          *HyperV
	bind, svc  string
	registered bool
	received   []string
}

func (l *fakeListener) Received() []string {
	l.f.mu.Lock()
	defer l.f.mu.Unlock()
	return slices.Clone(l.received)
}

// Close stops listening and deletes the registration it made.
func (l *fakeListener) Close() error {
	l.f.mu.Lock()
	defer l.f.mu.Unlock()
	l.f.listeners = slices.DeleteFunc(l.f.listeners, func(x *fakeListener) bool { return x == l })
	if l.registered {
		l.f.gcs = slices.DeleteFunc(l.f.gcs, func(k string) bool { return k == l.svc })
	}
	return nil
}

// HV_GUID_WILDCARD and HV_GUID_CHILDREN.
const (
	hvWildcard = "00000000-0000-0000-0000-000000000000"
	hvChildren = "90DB8B89-0D35-4F79-8CE9-49EA0AC8B7CD"
)

// HVListener binds an hv_sock listener for (bindVMID, service), and registers
// the service when asked.
func (f *HyperV) HVListener(_ context.Context, bindVMID, service string, register bool) (winvm.HVListener, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("HVListener"); err != nil {
		return nil, err
	}
	l := &fakeListener{f: f, bind: strings.ToUpper(bindVMID), svc: strings.ToUpper(service), registered: register}
	if register && !slices.Contains(f.gcs, l.svc) {
		f.gcs = append(f.gcs, l.svc)
	}
	f.listeners = append(f.listeners, l)
	return l, nil
}

// ---- GuestProbe ----

// guest is the one running VM, the one the probes come from.
func (f *HyperV) guest() (*fakeVM, *fakeSwitch, bool) {
	for _, vm := range f.vms {
		if vm.state != "Running" {
			continue
		}
		s := f.switches[vm.adapter.switchID]
		return vm, s, s != nil
	}
	return nil, nil, false
}

// denied is the port ACL verdict for dst: the longest matching row decides.
func (f *HyperV) denied(a *fakeAdapter, dst netip.Addr) bool {
	if f.mutant {
		return false
	}
	best := -1
	deny := false
	for _, r := range a.acls {
		if !r.Remote.IsValid() || !r.Remote.Contains(dst) || r.Action == "Meter" {
			continue
		}
		if r.Remote.Bits() > best {
			best, deny = r.Remote.Bits(), r.Action == "Deny"
		}
	}
	return deny
}

// l3Blocks is whether the host firewall drops a packet to our vNIC.
func (f *HyperV) l3Blocks(s *fakeSwitch) bool {
	for _, r := range f.firewall {
		if r.enabled && r.ifIndex == s.vnicIf {
			return true
		}
	}
	return false
}

func (f *HyperV) arrive(dst netip.Addr, port uint16, token string) {
	for _, c := range f.canaries {
		if c.ip == dst && c.port == port {
			c.arrived = append(c.arrived, token)
		}
	}
}

func (f *HyperV) record(r winvm.PktRecord, n int) {
	for range n {
		f.records = append(f.records, r)
	}
}

// send moves n packets from the guest to dst through the fence. It reports
// whether they got past it: to the host, or out of a host interface after NAT.
func (f *HyperV) send(proto string, to netip.AddrPort, token string, n int) (passed bool, out winvm.Route) {
	vm, s, ok := f.guest()
	if !ok {
		return false, out
	}
	a := vm.adapter
	dst := to.Addr()
	src := s.gateway.Next()
	port := winvm.PktRecord{Component: vm.name, Class: "vmport", Src: src, Dst: dst, Proto: proto, Port: to.Port()}
	drop := func() (bool, winvm.Route) {
		a.out += uint64(n)
		d := port
		d.Dropped = true
		f.record(d, n)
		return false, winvm.Route{}
	}
	if f.denied(a, dst) {
		return drop()
	}
	// Anything not on the guest's own link goes to the gateway's MAC. Without
	// a static entry the guest ARPs for it, and a Deny over the gateway drops
	// that ARP.
	if dst.Is4() && (dst == s.gateway || !s.subnet.Contains(dst)) {
		if _, static := f.guestARP[s.gateway]; !static && f.denied(a, s.gateway) {
			return drop()
		}
	}
	f.record(port, n)
	if dst.Is6() {
		if dst != s.linkLocal {
			return false, out // WinNAT forwards IPv4 only
		}
		f.record(winvm.PktRecord{Component: "vEthernet", Class: "vnic", Src: src, Dst: dst, Proto: proto, Port: to.Port()}, n)
		if !s.bindings["ms_tcpip6"] || f.l3Blocks(s) {
			return false, out
		}
		f.arrive(dst, to.Port(), token)
		return true, out
	}
	if dst == s.gateway {
		if f.l3Blocks(s) {
			return false, out
		}
		f.arrive(dst, to.Port(), token)
		return true, out
	}
	r, ok := f.lookup(dst)
	if !ok {
		return false, out
	}
	class := "tunnel"
	for _, i := range f.ifaces {
		if i.IfIndex == r.IfIndex && winvm.Classify(i, "") == winvm.IfPhysical {
			class = "physical"
		}
	}
	f.record(winvm.PktRecord{Component: fmt.Sprintf("if%d", r.IfIndex), Class: class, Src: f.LANIP, Dst: dst, Proto: proto, Port: to.Port()}, n)
	for _, h := range f.addrs {
		if h.Addr() == dst {
			f.arrive(dst, to.Port(), token)
		}
	}
	return true, r
}

// TCP sends one connect attempt carrying a token.
func (f *HyperV) TCP(_ context.Context, to netip.AddrPort, token string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("TCP"); err != nil {
		return false, err
	}
	if _, _, ok := f.guest(); !ok {
		return false, nil
	}
	f.send("TCP", to, token, 1)
	return true, nil
}

// UDP sends n datagrams carrying a token.
func (f *HyperV) UDP(_ context.Context, to netip.AddrPort, token string, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("UDP"); err != nil {
		return err
	}
	f.send("UDP", to, token, n)
	return nil
}

// DNS asks a server. Only the tailnet resolver, reached through the tunnel,
// answers.
func (f *HyperV) DNS(_ context.Context, server netip.Addr, name string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DNS"); err != nil {
		return nil, err
	}
	passed, r := f.send("UDP", netip.AddrPortFrom(server, 53), name, 1)
	if a, ok := f.MagicDNS[name]; passed && ok && r.IfIndex == fakeIfTunnel {
		return []netip.Addr{a}, nil
	}
	return nil, errors.New("fake Hyper-V: DNS timed out")
}

// Neighbor adds a static neighbour entry in the guest.
func (f *HyperV) Neighbor(_ context.Context, ip netip.Addr, mac string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Neighbor"); err != nil {
		return err
	}
	f.guestARP[ip] = mac
	return nil
}

// HVSock connects to HV_GUID_PARENT:service. It connects only when the service
// is registered and a listener is bound for every VM, for child VMs, or for
// this VM.
func (f *HyperV) HVSock(_ context.Context, service, token string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("HVSock"); err != nil {
		return false, err
	}
	vm, _, ok := f.guest()
	if !ok {
		return false, nil
	}
	svc := strings.ToUpper(service)
	if !slices.Contains(f.gcs, svc) {
		return false, nil
	}
	for _, l := range f.listeners {
		if l.svc == svc && (l.bind == hvWildcard || l.bind == hvChildren || l.bind == strings.ToUpper(vm.id)) {
			l.received = append(l.received, token)
			return true, nil
		}
	}
	return false, nil
}

// Egress asks an echo service for the guest's public address.
func (f *HyperV) Egress(context.Context) (netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Egress"); err != nil {
		return netip.Addr{}, err
	}
	if passed, _ := f.send("TCP", netip.MustParseAddrPort("198.51.100.80:443"), "", 1); !passed {
		return netip.Addr{}, errors.New("fake Hyper-V: the echo service did not answer")
	}
	return f.PublicIP, nil
}

// RawFrame sends n non-IP frames. Port ACLs filter IP only, so they always
// reach the host vNIC. An LLTD frame (0x88D9) gets an answer from the host
// while ms_rspndr is bound.
func (f *HyperV) RawFrame(_ context.Context, ethertype uint16, dst string, _ []byte, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RawFrame"); err != nil {
		return err
	}
	_, s, ok := f.guest()
	if !ok {
		return errors.New("fake Hyper-V: no running guest")
	}
	f.record(winvm.PktRecord{Component: "vEthernet", Class: "vnic", Proto: fmt.Sprintf("0x%04X", ethertype)}, n)
	if ethertype == 0x88D9 && s.bindings["ms_rspndr"] {
		f.record(winvm.PktRecord{Component: "vEthernet", Class: "vnic", Proto: "0x88D9 answer"}, 1)
	}
	return nil
}

var (
	_ winvm.Builder    = (*HyperV)(nil)
	_ winvm.Prober     = (*HyperV)(nil)
	_ winvm.GuestProbe = (*HyperV)(nil)
)
