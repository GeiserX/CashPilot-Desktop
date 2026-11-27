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
	"html"
	"maps"
	"net/netip"
	"os"
	"regexp"
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
	failAt   []*failAt
	seq      int
	aclSeq   int

	ifaces   []winvm.Interface
	routes   []winvm.Route
	addrs    []netip.Prefix
	network  winvm.CurrentNetwork
	gcs      []string
	gcsName  map[string]string // ElementName of a GCS key, by key
	build    string
	enhanced bool
	guardian string
	tasks    map[string]*fakeTask
	admins   map[string]bool
	isos     map[string][]byte

	// InstalledRAM and FreeRAM are what Inventory reports.
	InstalledRAM, FreeRAM uint64

	// PublicIP is the line's public address, LANIP the host's address on its
	// physical network, Router the LAN's gateway.
	PublicIP, LANIP, Router netip.Addr
	// MagicDNS is what the tailnet resolver answers.
	MagicDNS map[string]netip.Addr
	// GuestAnswers are the canned answers of Guest scripts, by script name.
	GuestAnswers map[string]any

	switches  map[string]*fakeSwitch
	nats      map[string]winvm.LiveNAT
	firewall  map[string][]*fakeRule
	vms       map[string]*fakeVM
	canaries  []*fakeCanary
	capture   *fakeCapture
	records   []winvm.PktRecord
	listeners []*fakeListener
	guestARP  map[netip.Addr]string
}

type fakeSwitch struct {
	id, name, typ     string
	managementID      string
	foreignManagement bool
	owner             string
	vnicIf            int
	vnicMAC           string
	bindings          map[string]bool
	subnet            netip.Prefix
	gateway           netip.Addr
	linkLocal         netip.Addr
	neighbors         map[netip.Addr]string
}

type fakeRule struct {
	name, owner string
	identity    string
	desc        string
	enabled     bool
	ifIndex     int
}

type fakeTask struct {
	exe, owner string
	running    bool
	starts     int
}

type fakeVM struct {
	id, name, state, owner            string
	vhdx                              string
	diskID                            string
	vtpm, keyProtector                bool
	answer                            []byte // the answer file of the ISO it boots with, applied at the first start
	accounts                          map[string]string
	panther                           bool // Windows Setup left its copies of the answer file
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
	portACLs                            []winvm.PortACL
	dhcpGuard, routerGuard, macSpoofing bool
	out, in                             uint64
}

// Interface indexes of the fake host.
const (
	fakeIfLoopback = 1
	fakeIfEthernet = 7
	fakeIfTunnel   = 15
)

// protocols are the NetTrans-class components Windows 11 binds to a new vNIC
// (live run 5).
var protocols = []string{"ms_tcpip", "ms_tcpip6", "ms_lltdio", "ms_rspndr", "ms_lldp", "ms_implat",
	"ms_pppoe", "ms_rdma_ndk", "ms_netbt", "ms_ndisuio"}

// New returns a host on a physical Ethernet network (192.0.2.0/24, its
// router at .1, the host at .10), with a tailnet-style tunnel adapter that
// carries 100.64.0.0/10, an approved-looking unmetered network, and Enhanced
// Session off.
func New() *HyperV {
	f := &HyperV{
		failNext:     map[string]error{},
		build:        "26100.1000",
		enhanced:     false,
		tasks:        map[string]*fakeTask{},
		admins:       map[string]bool{},
		isos:         map[string][]byte{},
		InstalledRAM: 32 << 30,
		FreeRAM:      16 << 30,
		PublicIP:     netip.MustParseAddr("203.0.113.7"),
		LANIP:        netip.MustParseAddr("192.0.2.10"),
		Router:       netip.MustParseAddr("192.0.2.1"),
		MagicDNS:     map[string]netip.Addr{"host.example.ts.net": netip.MustParseAddr("100.64.0.5")},
		switches:     map[string]*fakeSwitch{},
		nats:         map[string]winvm.LiveNAT{},
		firewall:     map[string][]*fakeRule{},
		vms:          map[string]*fakeVM{},
		guestARP:     map[netip.Addr]string{},
		gcs:          []string{"0000AAAA-FACB-11E6-BD58-64006A7986D3"},
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

// FailNth makes the nth call of the named method from now on return err, or
// a generic error when err is nil. Pending reports whether it has not fired.
func (f *HyperV) FailNth(method string, n int, err error) (pending func() bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		err = fmt.Errorf("fake Hyper-V: %s call %d failed", method, n)
	}
	k := &failAt{method: method, n: n, err: err}
	f.failAt = append(f.failAt, k)
	return func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return k.n > 0
	}
}

type failAt struct {
	method string
	n      int
	err    error
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
	var fired error
	for _, k := range f.failAt {
		if k.method == method && k.n > 0 {
			k.n--
			if k.n == 0 && fired == nil {
				fired = k.err
			}
		}
	}
	return fired
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
	const owner = "0123456789abcdef0123456789abcdef"
	p, err := winvm.NewPlan(winvm.Options{Names: n, Subnet: sub,
		HostVNICMAC: "00-15-5D-00-00-02", GuestMAC: "00-15-5D-0A-00-02"}, nil)
	if err != nil {
		return winvm.Plan{}, err
	}
	p.Owner = owner
	if p.SwitchID, p.ManagementAdapterID, p.HostVNICMAC, _, err = f.EnsureSwitch(ctx, n, sub, owner, "", "", ""); err != nil {
		return winvm.Plan{}, err
	}
	if p.NATID, err = f.EnsureNAT(ctx, n, sub, ""); err != nil {
		return winvm.Plan{}, err
	}
	var disk winvm.OwnedDisk
	if p.VMID, p.AdapterID, disk, p.DVDs, err = f.CreateVM(ctx, p, "", ""); err != nil {
		return winvm.Plan{}, err
	}
	p.Disk = &disk
	live, err := f.ReadLive(ctx, n, p.VMID, p.SwitchID)
	if err != nil {
		return winvm.Plan{}, err
	}
	steps := []func() error{
		func() error { return f.SetIntegration(ctx, p.VMID, p.Owner, p.Integration) },
		func() error {
			bindings, err := f.SetBindings(ctx, p, live.VNICIfIndex)
			p.ManagementBindings = bindings
			return err
		},
		func() error {
			id, err := f.EnsureFirewall(ctx, p, live.VNICIfIndex)
			p.FirewallIdentity = id
			return err
		},
		func() error {
			return f.SetHostNeighbor(ctx, live.VNICIfIndex, p.SwitchID, p.Owner, p.ManagementAdapterID, p.GuestIP, p.GuestMAC)
		},
	}
	if staticARP {
		steps = append(steps, func() error { return f.Neighbor(ctx, p.Gateway, p.HostVNICMAC) })
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return winvm.Plan{}, err
		}
	}
	f.SetEnhanced(false)
	h, err := f.ReadHostNet(ctx, winvm.EgressSamples)
	if err != nil {
		return winvm.Plan{}, err
	}
	d := winvm.Dynamic{HostAddrs: winvm.HostDenies(h, p.SwitchID), Tunnels: winvm.TunnelPrefixes(h, p.SwitchID, winvm.StaticDeny())}
	var deny []netip.Prefix
	for _, a := range p.WantACLs(d) {
		deny = append(deny, a.Remote)
	}
	if p.PortACLs, err = f.AddPortACLs(ctx, p, "Deny", deny); err != nil {
		return winvm.Plan{}, err
	}
	p.SetGCSBaseline(live.GCS)
	p.HostBuild = live.HostBuild
	if err := f.Connect(ctx, p.VMID, p.SwitchID, p.Owner, p.AdapterID); err != nil {
		return winvm.Plan{}, err
	}
	return p, f.Start(ctx, p.VMID, p.Owner)
}

// ---- Reader ----

// ReadLive lists what a real host lists, a Both rule as an Inbound and an
// Outbound row included.
func (f *HyperV) ReadLive(_ context.Context, n winvm.Names, vmID, switchID string) (winvm.LiveState, error) {
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
		PortACLs:      append([]winvm.PortACL{}, a.portACLs...),
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
	if s, ok := f.switches[strings.ToLower(switchID)]; ok {
		l.SwitchType = s.typ
		for _, v := range f.vms {
			for _, x := range append([]*fakeAdapter{v.adapter}, v.extra...) {
				if x.switchID == s.id {
					l.SwitchPorts++
				}
			}
		}
		l.VNICIfIndex = s.vnicIf
		l.ManagementAdapterID = s.managementID
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
	for _, nat := range f.nats {
		l.NATs = append(l.NATs, nat)
	}
	for _, r := range f.firewall[n.FirewallGroup] {
		l.Firewall = append(l.Firewall, winvm.LiveRule{Identity: r.identity, Direction: "Inbound", Action: "Block", Enabled: r.enabled, IfIndex: r.ifIndex})
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

// Disconnect takes the recorded adapter off its switch.
func (f *HyperV) Disconnect(_ context.Context, vmID, owner, adapterID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Disconnect"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	if vm.owner != owner || !strings.EqualFold(vm.adapter.id, adapterID) {
		return errors.New("fake Hyper-V: the recorded VM adapter was left in place")
	}
	vm.adapter.switchID = ""
	for _, x := range vm.extra {
		if x.switchID != "" {
			return errors.New("fake Hyper-V: a foreign adapter remains connected")
		}
	}
	return nil
}

// Connect puts the VM's one adapter on the recorded switch. Like the
// real script, it refuses a VM with more than one adapter.
func (f *HyperV) Connect(_ context.Context, vmID, switchID, owner, adapterID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Connect"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	if len(vm.extra) > 0 || !strings.EqualFold(vm.adapter.id, adapterID) {
		return errors.New("fake Hyper-V: the VM must have exactly one network adapter")
	}
	s, ok := f.switches[strings.ToLower(switchID)]
	if !ok || s.typ != "Internal" || owner == "" || s.owner != owner || vm.owner != owner {
		return fmt.Errorf("fake Hyper-V: VM or switch %q not owned", switchID)
	}
	vm.adapter.switchID = s.id
	return nil
}

// Start runs the VM.
func (f *HyperV) Start(_ context.Context, vmID, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Start"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	if vm.answer != nil {
		f.applyAnswer(vm)
	}
	vm.state, vm.booted = "Running", true
	return nil
}

// AddDeny and RemoveDeny model changes an administrator makes in a fixture.
func (f *HyperV) AddDeny(_ context.Context, vmID, owner string, ps []netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if _, err := f.addPortACL(vm.adapter, "Deny", p, "external-"); err != nil {
			return err
		}
	}
	return nil
}

func (f *HyperV) RemoveDeny(_ context.Context, vmID, owner string, ps []netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if err := removeACL(vm.adapter, "Deny", p); err != nil {
			return err
		}
		vm.adapter.portACLs = slices.DeleteFunc(vm.adapter.portACLs, func(a winvm.PortACL) bool {
			return a.Action == 2 && a.Remote == p.Addr().String() && int(a.RemoteBits) == p.Bits()
		})
	}
	return nil
}

func (f *HyperV) addPortACL(a *fakeAdapter, action string, q netip.Prefix, marker string) ([]winvm.PortACL, error) {
	if err := addACL(a, action, q); err != nil {
		return nil, err
	}
	code, family := uint8(2), uint8(2)
	if action == "Allow" {
		code = 1
	}
	if q.Addr().Is6() {
		family = 3
	}
	var rows []winvm.PortACL
	for _, dir := range []uint8{1, 2} {
		f.aclSeq++
		id := fmt.Sprintf("port-acl-%d", f.aclSeq)
		rows = append(rows, winvm.PortACL{ID: id, Name: marker + id, Action: code, Direction: dir,
			Applicability: 2, Type: family, Remote: q.Addr().String(), RemoteBits: uint8(q.Bits())})
	}
	a.portACLs = append(a.portACLs, rows...)
	return rows, nil
}

func (f *HyperV) AddPortACLs(_ context.Context, p winvm.Plan, action string, ps []netip.Prefix) ([]winvm.PortACL, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Add" + action); err != nil {
		return nil, err
	}
	vm, err := f.vm(p.VMID)
	if err != nil {
		return nil, err
	}
	if vm.owner != p.Owner || vm.adapter.id != p.AdapterID || len(vm.extra) != 0 {
		return nil, errors.New("fake Hyper-V: recorded adapter is not the sole adapter")
	}
	var out []winvm.PortACL
	for _, q := range ps {
		rows, err := f.addPortACL(vm.adapter, action, q, p.Owner)
		out = append(out, rows...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func (f *HyperV) RemovePortACLs(_ context.Context, p winvm.Plan, rows []winvm.PortACL) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(rows) == 0 {
		return nil
	}
	name := "Deny"
	if rows[0].Action == 1 {
		name = "Allow"
	}
	if err := f.enter("Remove" + name); err != nil {
		return err
	}
	vm, err := f.vm(p.VMID)
	if err != nil {
		return err
	}
	if vm.owner != p.Owner || vm.adapter.id != p.AdapterID || len(vm.extra) != 0 {
		return errors.New("fake Hyper-V: recorded adapter is not the sole adapter")
	}
	for _, a := range vm.adapter.portACLs {
		for _, r := range rows {
			if a.ID == r.ID && a != r {
				return errors.New("fake Hyper-V: recorded ACL was changed")
			}
		}
	}
	for _, a := range vm.adapter.portACLs {
		if !slices.Contains(rows, a) {
			continue
		}
		ip := netip.MustParseAddr(a.Remote)
		// The fake stores the two directional settings as one packet rule.
		_ = removeACL(vm.adapter, name, netip.PrefixFrom(ip, int(a.RemoteBits)))
	}
	vm.adapter.portACLs = slices.DeleteFunc(vm.adapter.portACLs, func(a winvm.PortACL) bool { return slices.Contains(rows, a) })
	return nil
}

// LeaveOneWay turns the Deny Both row of p into an Inbound row only, as a
// Remove-VMNetworkAdapterAcl that stopped halfway leaves it.
func (f *HyperV) LeaveOneWay(vmID string, p netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	for i, x := range vm.adapter.acls {
		if x.Action == "Deny" && x.Remote == p.Masked() && x.Direction == "Both" {
			vm.adapter.acls[i].Direction = "Inbound"
			vm.adapter.portACLs = slices.DeleteFunc(vm.adapter.portACLs, func(r winvm.PortACL) bool {
				return r.Direction == 2 && r.Remote == p.Addr().String() && int(r.RemoteBits) == p.Bits()
			})
			return nil
		}
	}
	return fmt.Errorf("fake Hyper-V: no Deny Both row for %s", p)
}

// ---- Builder ----

// notOwned refuses an object whose owner token is not this install's.
func (f *HyperV) notOwned(what, name, have, want string) error {
	if have == "" || have != want {
		return fmt.Errorf("fake Hyper-V: a %s named %q exists and does not carry this install's owner token", what, name)
	}
	return nil
}

// Inventory lists every object with one of our names, the VM and switch with
// the known IDs whatever their names, and the host facts.
func (f *HyperV) Inventory(_ context.Context, n winvm.Names, known winvm.KnownIDs, sid string) (winvm.Inventory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Inventory"); err != nil {
		return winvm.Inventory{}, err
	}
	inv := winvm.Inventory{
		NATs:         []winvm.LiveNAT{},
		Guardian:     f.guardian != "",
		GuardianID:   f.guardian,
		HyperVAdmin:  f.admins[sid],
		Enhanced:     f.enhanced,
		GCS:          slices.Clone(f.gcs),
		HostBuild:    f.build,
		InstalledRAM: f.InstalledRAM,
		FreeRAM:      f.FreeRAM,
	}
	for _, vm := range f.sortedVMs() {
		if vm.name == n.VM || strings.EqualFold(vm.id, known.VM) ||
			known.VM == "" && known.Owner != "" && vm.owner == known.Owner {
			inv.VMs = append(inv.VMs, winvm.OwnedObject{ID: vm.id, Owner: vm.owner})
		}
		if vm.vtpm || vm.keyProtector {
			inv.GuardianVMs = append(inv.GuardianVMs, vm.id)
		}
	}
	for _, s := range f.sortedSwitches() {
		if s.name != n.Switch && !strings.EqualFold(s.id, known.Switch) &&
			!(known.Switch == "" && known.Owner != "" && s.owner == known.Owner) {
			continue
		}
		o := winvm.OwnedObject{ID: s.id, Owner: s.owner}
		for _, vm := range f.sortedVMs() {
			for _, a := range append([]*fakeAdapter{vm.adapter}, vm.extra...) {
				if a.switchID == s.id && !slices.Contains(o.Ports, vm.id) {
					o.Ports = append(o.Ports, vm.id)
				}
			}
		}
		inv.Switches = append(inv.Switches, o)
	}
	for _, name := range sortedKeys(f.nats) {
		inv.NATs = append(inv.NATs, f.nats[name])
	}
	for _, r := range f.firewall[n.FirewallGroup] {
		inv.Firewall = append(inv.Firewall, winvm.OwnedObject{ID: r.name, Owner: r.owner})
	}
	if t, ok := f.tasks[n.GuardTask]; ok {
		inv.Task = &winvm.OwnedObject{ID: n.GuardTask, Owner: t.owner}
	}
	return inv, nil
}

func (f *HyperV) sortedVMs() []*fakeVM {
	var out []*fakeVM
	for _, k := range sortedKeys(f.vms) {
		out = append(out, f.vms[k])
	}
	return out
}

func (f *HyperV) sortedSwitches() []*fakeSwitch {
	var out []*fakeSwitch
	for _, k := range sortedKeys(f.switches) {
		out = append(out, f.switches[k])
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// EnsureSwitch creates the Internal switch with its host vNIC, every binding
// on, the gateway address and the on-link route, or returns the one with the
// recorded ID (renamed or not).
func (f *HyperV) EnsureSwitch(_ context.Context, n winvm.Names, sub netip.Prefix, owner, vnicMAC, switchID, managementID string) (string, string, string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("EnsureSwitch"); err != nil {
		return "", "", "", 0, err
	}
	if s, ok := f.switches[strings.ToLower(switchID)]; ok && switchID != "" {
		if err := f.notOwned("switch", n.Switch, s.owner, owner); err != nil {
			return "", "", "", 0, err
		}
		if s.managementID != managementID || s.foreignManagement || s.vnicMAC != vnicMAC || s.subnet != sub {
			return "", "", "", 0, fmt.Errorf("fake Hyper-V: the switch %s has another vNIC MAC or subnet", switchID)
		}
		if s.name != n.Switch {
			if other, ok := f.switchByName(n.Switch); ok && other != s {
				return "", "", "", 0, fmt.Errorf("fake Hyper-V: another switch is named %q", n.Switch)
			}
			s.name = n.Switch
		}
		return s.id, s.managementID, s.vnicMAC, s.vnicIf, nil
	}
	if switchID != "" {
		return "", "", "", 0, errors.New("fake Hyper-V: the recorded switch was not found")
	}
	if _, ok := f.switchByName(n.Switch); ok {
		return "", "", "", 0, errors.New("fake Hyper-V: a switch with this name exists without a recorded ID")
	}
	f.seq++
	s := &fakeSwitch{
		id: f.newID(), name: n.Switch, typ: "Internal", owner: owner,
		vnicIf:       20 + f.seq,
		managementID: "management-" + f.newID(),
		vnicMAC:      fmt.Sprintf("00155D%06X", f.seq),
		bindings:     map[string]bool{"ms_pacer": true},
		subnet:       sub,
		gateway:      sub.Addr().Next(),
		linkLocal:    netip.MustParseAddr(fmt.Sprintf("fe80::%x", 0x1000+f.seq)),
		neighbors:    map[netip.Addr]string{},
	}
	for _, c := range append(slices.Clone(protocols), "ms_server", "ms_msclient") {
		s.bindings[c] = true
	}
	f.switches[s.id] = s
	f.ifaces = append(f.ifaces, winvm.Interface{IfIndex: s.vnicIf, IfType: 6, ComponentID: "vms_mp", Description: "vEthernet", VMSwitchID: s.id, SwitchType: "Internal"})
	f.addrs = append(f.addrs, netip.PrefixFrom(s.gateway, sub.Bits()))
	f.routes = append(f.routes, winvm.Route{Prefix: sub, IfIndex: s.vnicIf, NextHop: netip.IPv4Unspecified(), Metric: 256})
	return s.id, s.managementID, s.vnicMAC, s.vnicIf, nil
}

// EnsureNAT creates the NAT or verifies the identity of the recorded one.
func (f *HyperV) EnsureNAT(_ context.Context, n winvm.Names, sub netip.Prefix, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("EnsureNAT"); err != nil {
		return "", err
	}
	if have, ok := f.nats[n.NAT]; ok {
		if have.Prefix != sub || have.ID != id {
			return "", errors.New("fake Hyper-V: the NAT identity does not match")
		}
		return have.ID, nil
	}
	// Windows keys a NAT's InstanceID by its name and store, never by an ID
	// of its own.
	id = n.NAT + ";0"
	f.nats[n.NAT] = winvm.LiveNAT{ID: id, Name: n.NAT, Prefix: sub}
	return id, nil
}

// CreateVM creates the VM off, with the plan's settings, a vTPM, one
// disconnected adapter with its port guards (only the proof and the guard
// connect it), and Hyper-V's default integration
// services (all on but the Guest Service Interface). Each ISO counts as a DVD
// drive. The answer ISO's file is applied at the first start, as Windows Setup
// does: the two accounts and the guest's static entry for the gateway.
func (f *HyperV) CreateVM(_ context.Context, p winvm.Plan, iso, answerISO string) (string, string, winvm.OwnedDisk, []winvm.OwnedDVD, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateVM"); err != nil {
		return "", "", winvm.OwnedDisk{}, nil, err
	}
	for _, vm := range f.vms {
		if vm.name == p.Names.VM {
			return "", "", winvm.OwnedDisk{}, nil, fmt.Errorf("fake Hyper-V: a VM named %q exists", p.Names.VM)
		}
	}
	if _, ok := f.switches[strings.ToLower(p.SwitchID)]; !ok {
		return "", "", winvm.OwnedDisk{}, nil, fmt.Errorf("fake Hyper-V: no switch %q", p.Names.Switch)
	}
	vm := &fakeVM{
		id: f.newID(), name: p.Names.VM, state: "Off", owner: p.Owner, vtpm: true,
		vhdx: p.Names.DataDir + `\vm-test\` + p.Names.VM + ".vhdx",
		adapter: &fakeAdapter{id: strings.ToUpper(f.newID()), mac: p.GuestMAC,
			dhcpGuard: p.DhcpGuard, routerGuard: p.RouterGuard, macSpoofing: p.MacSpoofing},
		procCount: p.ProcCount, procMax: p.ProcMax, procWeight: p.ProcWeight,
		memory: p.MemoryBytes, checkpoints: p.Checkpoints, autoStart: p.AutoStart,
		integration: map[string]bool{
			winvm.ISHeartbeat: true, winvm.ISShutdown: true, winvm.ISTimeSync: true,
			winvm.ISKVP: true, winvm.ISVSS: true, winvm.ISGuestService: false,
		},
		disks:    1,
		accounts: map[string]string{},
	}
	var dvds []winvm.OwnedDVD
	for _, x := range []string{iso, answerISO} {
		if x != "" {
			vm.dvds++
			dvds = append(dvds, winvm.OwnedDVD{ID: f.newID(), Path: x})
		}
	}
	if answerISO != "" {
		b, ok := f.isos[answerISO]
		if !ok {
			return "", "", winvm.OwnedDisk{}, nil, fmt.Errorf("fake Hyper-V: no ISO at %s", answerISO)
		}
		vm.answer = b
	}
	f.vms[vm.id] = vm
	f.seq++
	vm.diskID = fmt.Sprintf("%048x", f.seq)
	return vm.id, vm.adapter.id, winvm.OwnedDisk{Path: vm.vhdx, ID: vm.diskID}, dvds, nil
}

// SetIntegration sets integration services by ID.
func (f *HyperV) SetIntegration(_ context.Context, vmID, owner string, want map[string]bool) error {
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
func (f *HyperV) SetBindings(_ context.Context, p winvm.Plan, ifIndex int) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetBindings"); err != nil {
		return nil, err
	}
	s, err := f.switchByVNIC(ifIndex)
	if err != nil {
		return nil, err
	}
	if s.managementID != p.ManagementAdapterID || s.foreignManagement {
		return nil, errors.New("fake Hyper-V: the management adapter was left in place")
	}
	if p.ManagementBindings != nil {
		if !maps.Equal(p.ManagementBindings, s.bindings) {
			return nil, errors.New("fake Hyper-V: the management adapter bindings changed")
		}
		return maps.Clone(s.bindings), nil
	}
	for _, c := range p.BindingsOff {
		s.bindings[c] = false
	}
	return maps.Clone(s.bindings), nil
}

// EnsureFirewall creates or verifies the recorded Block rule.
func (f *HyperV) EnsureFirewall(_ context.Context, p winvm.Plan, ifIndex int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("EnsureFirewall"); err != nil {
		return "", err
	}
	rs := f.firewall[p.Names.FirewallGroup]
	if len(rs) == 1 && rs[0].owner == p.Owner && rs[0].enabled && rs[0].ifIndex == ifIndex && rs[0].identity == p.FirewallIdentity {
		return rs[0].identity, nil
	}
	if len(rs) != 0 {
		return "", errors.New("fake Hyper-V: the firewall rule differs from Setup")
	}
	f.seq++
	id := fmt.Sprintf("%064x", f.seq)
	f.firewall[p.Names.FirewallGroup] = []*fakeRule{{
		name:  p.Names.FirewallGroup + "-block-in-" + p.Owner,
		owner: p.Owner, enabled: true, ifIndex: ifIndex, identity: id,
	}}
	return id, nil
}

// SetHostNeighbor adds the host's static entry for the guest, in place of any
// other entry on the vNIC.
func (f *HyperV) SetHostNeighbor(_ context.Context, ifIndex int, switchID, owner, managementID string, ip netip.Addr, mac string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetHostNeighbor"); err != nil {
		return err
	}
	s, err := f.switchByVNIC(ifIndex)
	if err != nil {
		return err
	}
	s.neighbors = map[netip.Addr]string{ip: mac}
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

// SetEnhanced sets Enhanced Session as the user could. It is not a seam call.
func (f *HyperV) SetEnhanced(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enhanced = on
}

// EnsureGuardian creates the vTPM guardian if none exists.
func (f *HyperV) EnsureGuardian(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("EnsureGuardian"); err != nil {
		return "", err
	}
	if f.guardian != "" {
		return "", nil
	}
	f.seq++
	f.guardian = fmt.Sprintf("%064x", f.seq)
	return f.guardian, nil
}

// AddHyperVAdmin adds the SID to Hyper-V Administrators.
func (f *HyperV) AddHyperVAdmin(_ context.Context, sid string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("AddHyperVAdmin"); err != nil {
		return false, err
	}
	if f.admins[sid] {
		return false, nil
	}
	f.admins[sid] = true
	return true, nil
}

// SetHyperVAdmin puts an account in Hyper-V Administrators, or takes it out,
// as the user could. It is not a seam call.
func (f *HyperV) SetHyperVAdmin(sid string, member bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if member {
		f.admins[sid] = true
	} else {
		delete(f.admins, sid)
	}
}

// WriteAnswerISO keeps the answer file and writes it to path, so the caller
// can delete the file.
func (f *HyperV) WriteAnswerISO(_ context.Context, path string, xml []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("WriteAnswerISO"); err != nil {
		return err
	}
	if err := os.WriteFile(path, xml, 0o600); err != nil {
		return err
	}
	f.isos[path] = slices.Clone(xml)
	return nil
}

// BootKeys starts the VM and answers the Gen 2 boot prompt.
func (f *HyperV) BootKeys(_ context.Context, vmID, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("BootKeys"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	if vm.answer != nil {
		f.applyAnswer(vm)
	}
	vm.state, vm.booted = "Running", true
	return nil
}

// EjectDVDs removes the VM's DVD drives.
func (f *HyperV) EjectDVDs(_ context.Context, vmID, owner string, dvds []winvm.OwnedDVD) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("EjectDVDs"); err != nil {
		return err
	}
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	vm.dvds = max(0, vm.dvds-len(dvds))
	if vm.dvds != 0 {
		return errors.New("unverified DVD drives were left on the VM")
	}
	return nil
}

// RegisterGuardTask stops a running guard task, then records the task. It
// refuses a task that does not carry the owner token.
func (f *HyperV) RegisterGuardTask(_ context.Context, n winvm.Names, exe, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RegisterGuardTask"); err != nil {
		return err
	}
	if t, ok := f.tasks[n.GuardTask]; ok {
		if err := f.notOwned("task", n.GuardTask, t.owner, owner); err != nil {
			return err
		}
		t.exe, t.running = exe, false
		return nil
	}
	f.tasks[n.GuardTask] = &fakeTask{exe: exe, owner: owner}
	return nil
}

// StartGuardTask starts the guard task.
func (f *HyperV) StartGuardTask(_ context.Context, n winvm.Names, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("StartGuardTask"); err != nil {
		return err
	}
	t, ok := f.tasks[n.GuardTask]
	if !ok {
		return fmt.Errorf("fake Hyper-V: no task %s", n.GuardTask)
	}
	if err := f.notOwned("task", n.GuardTask, t.owner, owner); err != nil {
		return err
	}
	t.running = true
	t.starts++
	return nil
}

// StopTask stops the guard task, as setup.ps1 and remove.ps1 do before they
// run the exe. It is not a seam call.
func (f *HyperV) StopTask(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tasks[name]; ok {
		t.running = false
	}
}

// TaskRunning is whether the guard task runs. It is not a seam call.
func (f *HyperV) TaskRunning(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[name]
	return ok && t.running
}

var (
	accountRe = regexp.MustCompile(`<LocalAccount[^>]*><Name>([a-z]+)</Name><Group>[^<]*</Group><Password><Value>([^<]*)</Value>`)
	answerGW  = regexp.MustCompile(`<NextHopAddress>([0-9.]+)</NextHopAddress>`)
	answerARP = regexp.MustCompile(`IPAddress='([0-9.]+)'\};Remove-NetNeighbor @p -Confirm:\$false -EA 0;New-NetNeighbor @p -LinkLayerAddress ([0-9A-Fa-f-]{17}) -State Permanent`)
)

// applyAnswer is Windows Setup at the first start: the accounts and the
// static ARP entry the answer file sets.
func (f *HyperV) applyAnswer(vm *fakeVM) {
	x := string(vm.answer)
	vm.answer = nil
	for _, m := range accountRe.FindAllStringSubmatch(x, -1) {
		vm.accounts[m[1]] = html.UnescapeString(m[2])
	}
	if m := answerARP.FindStringSubmatch(x); m != nil {
		if gw := answerGW.FindStringSubmatch(x); gw != nil && gw[1] == m[1] {
			f.guestARP[netip.MustParseAddr(m[1])] = strings.ToUpper(strings.ReplaceAll(m[2], "-", ""))
		}
	}
	vm.panther = true
}

// GuestAccount is a guest account's password, "" when the account does not
// exist. It is not a seam call.
func (f *HyperV) GuestAccount(vmID, user string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vm, err := f.vm(vmID); err == nil {
		return vm.accounts[user]
	}
	return ""
}

// PantherLeft is whether Windows Setup's copies of the answer file are still
// in the guest. It is not a seam call.
func (f *HyperV) PantherLeft(vmID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, err := f.vm(vmID)
	return err == nil && vm.panther
}

// The guest scripts the fake understands besides canned answers.
const (
	guestFirstSession = "first-session"
)

// Guest checks the account like PowerShell Direct does, then answers. The
// first-session script sets both passwords and deletes the Panther copies, as
// the real one does; every other script returns its canned answer, through
// JSON and the 64 KB cap.
func (f *HyperV) Guest(_ context.Context, vmID, owner string, cred winvm.Cred, s winvm.GuestScript, arg, out any) error {
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
	if pw, ok := vm.accounts[cred.User]; !ok || pw != cred.Password {
		return errors.New("fake Hyper-V: the user name or password is incorrect")
	}
	answer, canned := f.GuestAnswers[s.Name]
	if !canned && s.Name == "ping" {
		answer = map[string]any{"ok": true}
	}
	if s.Name == guestFirstSession {
		var a struct {
			Admin  string `json:"admin"`
			Earner string `json:"earner"`
		}
		b, _ := json.Marshal(arg)
		if err := json.Unmarshal(b, &a); err != nil || a.Admin == "" || a.Earner == "" {
			return fmt.Errorf("fake Hyper-V: bad first-session argument: %v", err)
		}
		vm.accounts["cpadmin"], vm.accounts["earner"] = a.Admin, a.Earner
		vm.panther = false
		answer = map[string]any{"ok": true, "pantherLeft": []string{}}
	}
	b, err := json.Marshal(answer)
	if err != nil {
		return err
	}
	if len(b) > 64<<10 {
		return errors.New("fake Hyper-V: guest answer over 64 KB")
	}
	return json.Unmarshal(b, out)
}

// UnregisterGuardTask stops and deletes the guard task when it carries the
// owner token.
func (f *HyperV) UnregisterGuardTask(_ context.Context, n winvm.Names, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("UnregisterGuardTask"); err != nil {
		return err
	}
	t, ok := f.tasks[n.GuardTask]
	if !ok {
		return nil
	}
	if err := f.notOwned("task", n.GuardTask, t.owner, owner); err != nil {
		return err
	}
	delete(f.tasks, n.GuardTask)
	return nil
}

// RemoveVM removes the VM and its disk when its identity matches.
func (f *HyperV) RemoveVM(_ context.Context, vmID string, p winvm.Plan) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RemoveVM"); err != nil {
		return nil, err
	}
	vm, ok := f.vms[strings.ToLower(vmID)]
	if !ok {
		return nil, nil
	}
	if err := f.notOwned("VM", vm.name, vm.owner, p.Owner); err != nil {
		return nil, err
	}
	delete(f.vms, vm.id)
	if vm.vhdx != "" && (p.Disk == nil || !strings.EqualFold(p.Disk.Path, vm.vhdx) || p.Disk.ID != vm.diskID) {
		return []string{vm.vhdx}, nil
	}
	return nil, nil
}

// RemoveSwitch removes a switch by ID, with its host vNIC. Like the real
// script, it refuses while another VM has an adapter on it.
func (f *HyperV) RemoveSwitch(_ context.Context, p winvm.Plan) (string, error) {
	switchID, owner := p.SwitchID, p.Owner
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RemoveSwitch"); err != nil {
		return "", err
	}
	s, ok := f.switches[strings.ToLower(switchID)]
	if !ok {
		return "", nil
	}
	if err := f.notOwned("switch", s.name, s.owner, owner); err != nil {
		return "", err
	}
	if p.ManagementAdapterID != s.managementID || s.foreignManagement || !maps.Equal(p.ManagementBindings, s.bindings) {
		return "the management port inventory differs from Setup", nil
	}
	for _, vm := range f.vms {
		for _, a := range append([]*fakeAdapter{vm.adapter}, vm.extra...) {
			if a.switchID == s.id {
				return "a VM still has an adapter on it", nil
			}
		}
	}
	delete(f.switches, s.id)
	f.ifaces = slices.DeleteFunc(f.ifaces, func(i winvm.Interface) bool { return i.IfIndex == s.vnicIf })
	f.routes = slices.DeleteFunc(f.routes, func(r winvm.Route) bool { return r.IfIndex == s.vnicIf })
	f.addrs = slices.DeleteFunc(f.addrs, func(a netip.Prefix) bool { return a.Addr() == s.gateway })
	return "", nil
}

// RemoveNAT removes our NAT when it has our prefix.
func (f *HyperV) RemoveNAT(_ context.Context, n winvm.Names, sub netip.Prefix, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RemoveNAT"); err != nil {
		return "", err
	}
	if have, ok := f.nats[n.NAT]; ok && have.Prefix == sub && have.ID == id {
		delete(f.nats, n.NAT)
	}
	return "", nil
}

// RemoveFirewall removes the rules of the group that carry the owner token.
func (f *HyperV) RemoveFirewall(_ context.Context, p winvm.Plan) (string, error) {
	n, owner := p.Names, p.Owner
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RemoveFirewall"); err != nil {
		return "", err
	}
	var kept string
	rs := slices.DeleteFunc(f.firewall[n.FirewallGroup], func(r *fakeRule) bool {
		if owner == "" || r.owner != owner {
			return false
		}
		if r.identity != p.FirewallIdentity || !r.enabled {
			kept = "the firewall rule differs from Setup"
			return false
		}
		return true
	})
	if len(rs) == 0 {
		delete(f.firewall, n.FirewallGroup)
	} else {
		f.firewall[n.FirewallGroup] = rs
	}
	return kept, nil
}

// RemoveGuardian deletes the vTPM guardian.
func (f *HyperV) RemoveGuardian(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RemoveGuardian"); err != nil {
		return err
	}
	if f.guardian == id {
		f.guardian = ""
	}
	return nil
}

// RemoveHyperVAdmin takes the SID out of Hyper-V Administrators.
func (f *HyperV) RemoveHyperVAdmin(_ context.Context, sid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RemoveHyperVAdmin"); err != nil {
		return err
	}
	delete(f.admins, sid)
	return nil
}

// AddForeignVM adds a VM that this install did not make, with a name, no
// owner token, and a vTPM when asked. It returns its ID.
func (f *HyperV) AddForeignVM(name string, vtpm bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm := &fakeVM{id: f.newID(), name: name, state: "Off", vtpm: vtpm, vhdx: `D:\VMs\` + name + ".vhdx",
		adapter: &fakeAdapter{id: strings.ToUpper(f.newID()), mac: "00155D0A00EE"}, integration: map[string]bool{}, accounts: map[string]string{}}
	f.vms[vm.id] = vm
	return vm.id
}

// SetVMOwner writes an owner token into a VM's Notes, as a copy or an import
// of the Secure VM would carry. It is not a seam call.
func (f *HyperV) SetVMOwner(vmID, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	vm.owner = owner
	return nil
}

// AddMeterRow adds a Meter row to a VM's adapter, as resource metering does.
// It is not a seam call.
func (f *HyperV) AddMeterRow(vmID string, p netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	return addACL(vm.adapter, "Meter", p)
}

// SetKeyProtector gives a VM a local key protector without a vTPM, as a VM
// with only encrypted state has. It is not a seam call.
func (f *HyperV) SetKeyProtector(vmID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	vm.keyProtector = true
	return nil
}

// RenameVM renames a VM, as a user in Hyper-V Manager could. It is not a seam
// call.
func (f *HyperV) RenameVM(vmID, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, err := f.vm(vmID)
	if err != nil {
		return err
	}
	vm.name = name
	return nil
}

// RenameSwitch renames a switch, as a user in Hyper-V Manager could. It is not
// a seam call.
func (f *HyperV) RenameSwitch(switchID, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.switches[strings.ToLower(switchID)]
	if !ok {
		return fmt.Errorf("fake Hyper-V: no switch %s", switchID)
	}
	s.name = name
	return nil
}

// LeaveProofLeftovers leaves what a proof killed in the middle leaves: a
// canary rule in the instance's proof group, the hv_sock test key, both
// carrying the owner token, and a pktmon session still running. It is not a
// seam call.
func (f *HyperV) LeaveProofLeftovers(n winvm.Names, owner string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := n.FirewallGroup + "-proof"
	f.firewall[g] = append(f.firewall[g], &fakeRule{name: g + "-40001", desc: "CashPilot Secure VM proof canary. cashpilot-owner=" + owner, enabled: true})
	key := "12345678-1234-1234-1234-123456789ABC"
	f.gcs = winvm.GCSBaseline(append(slices.Clone(f.gcs), key))
	if f.gcsName == nil {
		f.gcsName = map[string]string{}
	}
	f.gcsName[key] = winvm.ProofElementName(n, owner)
	f.capture = &fakeCapture{f: f, from: len(f.records), leftover: n.VM, owner: owner}
}

// AddForeignProofObjects adds a rule to the instance's proof group with the
// canary's description but no owner token, and a GCS key with the proof's
// ElementName but no owner token, as another tool or an older build could.
// It returns the key. It is not a seam call.
func (f *HyperV) AddForeignProofObjects(n winvm.Names) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := n.FirewallGroup + "-proof"
	f.firewall[g] = append(f.firewall[g], &fakeRule{name: g + "-40002", desc: "CashPilot Secure VM proof canary", enabled: true})
	key := "87654321-4321-4321-4321-CBA987654321"
	f.gcs = winvm.GCSBaseline(append(slices.Clone(f.gcs), key))
	if f.gcsName == nil {
		f.gcsName = map[string]string{}
	}
	f.gcsName[key] = "CashPilot Secure VM proof " + n.VM
	return key
}

// ClearProof removes what a killed proof of instance n, made by the install
// with the owner token, left. Like the script it matches the rule's
// description and the key's ElementName exactly.
func (f *HyperV) ClearProof(_ context.Context, n winvm.Names, owner string) (winvm.ProofCleanup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ClearProof"); err != nil {
		return winvm.ProofCleanup{}, err
	}
	var res winvm.ProofCleanup
	g := n.FirewallGroup + "-proof"
	keep := f.firewall[g][:0]
	for _, r := range f.firewall[g] {
		if owner != "" && r.desc == "CashPilot Secure VM proof canary. cashpilot-owner="+owner {
			res.Cleared = append(res.Cleared, "the proof firewall rule "+r.name)
			continue
		}
		res.Left = append(res.Left, "the unverified proof firewall rule "+r.name)
		keep = append(keep, r)
	}
	if len(keep) == 0 {
		delete(f.firewall, g)
	} else {
		f.firewall[g] = keep
	}
	f.gcs = slices.DeleteFunc(f.gcs, func(k string) bool {
		if owner != "" && f.gcsName[k] == winvm.ProofElementName(n, owner) {
			res.Cleared = append(res.Cleared, "the proof hv_sock key "+k)
			delete(f.gcsName, k)
			return true
		}
		if strings.HasPrefix(f.gcsName[k], "CashPilot Secure VM proof "+n.VM) {
			res.Left = append(res.Left, "the unverified proof hv_sock key "+k)
		}
		return false
	})
	if f.capture != nil {
		if owner != "" && f.capture.leftover == n.VM && f.capture.owner == owner {
			f.capture = nil
			res.Cleared = append(res.Cleared, "the proof pktmon session")
		} else {
			res.UnverifiedCapture = true
			res.Left = append(res.Left, "the unverified pktmon session")
		}
	}
	return res, nil
}

// AddForeignNAT adds a NetNat this install did not make.
func (f *HyperV) AddForeignNAT(name string, p netip.Prefix) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nats[name] = winvm.LiveNAT{ID: name + ";0", Name: name, Prefix: p}
}

// AddForeignRule adds a firewall rule to a group, with no owner token.
func (f *HyperV) AddForeignRule(group, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.firewall[group] = append(f.firewall[group], &fakeRule{name: name, enabled: true})
}

// AddForeignTask adds a scheduled task this install did not make.
func (f *HyperV) AddForeignTask(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[name] = &fakeTask{exe: `C:\elsewhere\task.exe`}
}

// SetGuardian sets whether the vTPM guardian exists, as another VM's setup
// could. It is not a seam call.
func (f *HyperV) SetGuardian(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guardian = ""
	if on {
		f.seq++
		f.guardian = fmt.Sprintf("%064x", f.seq)
	}
}

// Objects lists every object on the fake host that a build makes or changes,
// in a stable order, for "the host is back as it was" checks: VMs, switches,
// NATs, firewall rules, tasks, Hyper-V Administrators, the guardian, Enhanced
// Session, every ACL row and every vNIC binding and neighbour entry.
func (f *HyperV) Objects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, vm := range f.sortedVMs() {
		out = append(out, fmt.Sprintf("vm %s %s owner=%s state=%s dvds=%d vtpm=%v", vm.id, vm.name, vm.owner, vm.state, vm.dvds, vm.vtpm))
		var acls []string
		for _, a := range vm.adapter.acls {
			acls = append(acls, a.String())
		}
		slices.Sort(acls)
		for _, a := range acls {
			out = append(out, "  acl "+a)
		}
	}
	for _, s := range f.sortedSwitches() {
		out = append(out, fmt.Sprintf("switch %s %s owner=%s", s.id, s.name, s.owner))
		for _, c := range sortedKeys(s.bindings) {
			out = append(out, fmt.Sprintf("  binding %s=%v", c, s.bindings[c]))
		}
		for ip, mac := range s.neighbors {
			out = append(out, fmt.Sprintf("  neighbor %s %s", ip, mac))
		}
	}
	for _, name := range sortedKeys(f.nats) {
		out = append(out, fmt.Sprintf("nat %s %s", name, f.nats[name].Prefix))
	}
	for _, g := range sortedKeys(f.firewall) {
		for _, r := range f.firewall[g] {
			out = append(out, fmt.Sprintf("rule %s %s owner=%s enabled=%v if=%d", g, r.name, r.owner, r.enabled, r.ifIndex))
		}
	}
	for _, name := range sortedKeys(f.tasks) {
		out = append(out, fmt.Sprintf("task %s %s owner=%s", name, f.tasks[name].exe, f.tasks[name].owner))
	}
	for _, sid := range sortedKeys(f.admins) {
		out = append(out, "hyperv-admin "+sid)
	}
	out = append(out, fmt.Sprintf("guardian=%v enhancedSession=%v gcs=%v pktmon=%v", f.guardian != "", f.enhanced, f.gcs, f.capture != nil))
	return out
}

// ---- Prober ----

type fakeCanary struct {
	f       *HyperV
	ip      netip.Addr
	port    uint16
	from    netip.Prefix
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

// Canary listens on a host address and port for a token. The guest is the
// only sender in the fake, so from is kept, not checked.
func (f *HyperV) Canary(_ context.Context, ip netip.Addr, port uint16, from netip.Prefix) (winvm.Canary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Canary"); err != nil {
		return nil, err
	}
	c := &fakeCanary{f: f, ip: ip, port: port, from: from}
	f.canaries = append(f.canaries, c)
	return c, nil
}

type fakeCapture struct {
	f       *HyperV
	filters []winvm.PktFilter
	from    int
	// leftover is the instance whose killed proof left this session running.
	leftover string
	owner    string
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
func (f *HyperV) SetL3(_ context.Context, n winvm.Names, owner string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetL3"); err != nil {
		return err
	}
	var owned []*fakeRule
	for _, r := range f.firewall[n.FirewallGroup] {
		if owner != "" && r.owner == owner {
			owned = append(owned, r)
		}
	}
	if len(owned) != 1 {
		return fmt.Errorf("fake Hyper-V: owned rule missing or ambiguous in %q", n.FirewallGroup)
	}
	owned[0].enabled = on
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
	_, err = f.addPortACL(vm.adapter, "Allow", p, "external-")
	return err
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
	vm.adapter.portACLs = slices.DeleteFunc(vm.adapter.portACLs, func(a winvm.PortACL) bool {
		return a.Action == 1 && a.Remote == p.Addr().String() && int(a.RemoteBits) == p.Bits()
	})
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

// Unregister deletes the registration it made and keeps listening.
func (l *fakeListener) Unregister() error {
	l.f.mu.Lock()
	defer l.f.mu.Unlock()
	if err := l.f.enter("Unregister"); err != nil {
		return err
	}
	if l.registered {
		l.f.gcs = slices.DeleteFunc(l.f.gcs, func(k string) bool { return k == l.svc })
		l.registered = false
	}
	return nil
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

// LinkLocal is the vNIC's link-local address. Like Windows, the vNIC has none
// while ms_tcpip6 is unbound.
func (f *HyperV) LinkLocal(_ context.Context, ifIndex int) (netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("LinkLocal"); err != nil {
		return netip.Addr{}, err
	}
	s, err := f.switchByVNIC(ifIndex)
	if err != nil {
		return netip.Addr{}, err
	}
	if !s.bindings["ms_tcpip6"] {
		return netip.Addr{}, errors.New("fake Hyper-V: the vNIC has no IPv6 address while ms_tcpip6 is unbound")
	}
	return s.linkLocal, nil
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
	for _, rs := range f.firewall {
		for _, r := range rs {
			if r.enabled && r.ifIndex == s.vnicIf {
				return true
			}
		}
	}
	return false
}

func (f *HyperV) arrive(dst netip.Addr, port uint16, token string) {
	for _, c := range f.canaries {
		if c.ip.WithZone("") == dst && c.port == port {
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

// SetDiskPath changes the attachment as Hyper-V Manager can.
func (f *HyperV) SetDiskPath(vmID, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vms[strings.ToLower(vmID)].vhdx = path
}

// ReplaceAdapter models an administrator installing a different vNIC of the same shape.
func (f *HyperV) ReplaceAdapter(vmID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.vms[strings.ToLower(vmID)].adapter.id = strings.ToUpper(f.newID())
}
