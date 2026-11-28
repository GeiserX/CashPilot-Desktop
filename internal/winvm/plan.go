package winvm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
)

// PlanVersion is the desired.json format this code writes and reads.
const PlanVersion = 1

// maxPlanBytes caps desired.json. A real plan is a few KB.
const maxPlanBytes = 8 << 20

// GiB is 2^30 bytes.
const GiB = uint64(1) << 30

// DefaultGuestMemory is the guest's static RAM (DESKTOP-VM.md §4).
const DefaultGuestMemory = 6 * GiB

// ESPolicy says what setup does with the host-wide Enhanced Session setting.
type ESPolicy string

const (
	// ESOff requires the host owner to turn Enhanced Session off first.
	ESOff ESPolicy = "off"
	// ESKeep leaves the host setting alone. It is a lab flag only, for a host
	// whose other VMs must keep their Enhanced Session window.
	ESKeep ESPolicy = "keep"
)

// ACL is one port ACL row on the guest's adapter.
type ACL struct {
	// Action is "Deny" in every plan. "Allow" appears only inside a proof step,
	// and "Meter" only in a live reading, where it is drift.
	Action    string       `json:"action"`
	Direction string       `json:"direction"`
	Remote    netip.Prefix `json:"remote"`
}

func (a ACL) String() string { return a.Action + " " + a.Direction + " " + a.Remote.String() }

// Integration service IDs (Get-VMIntegrationService .Id, the GUID after the
// backslash). Windows translates the names, so the plan keys on these.
const (
	ISGuestService = "6C09BB55-D683-4DA0-8931-C9BF705F6480"
	ISHeartbeat    = "84EAAE65-2F2E-45F5-9BB5-0E857DC8EB47"
	ISKVP          = "2A34B1C2-FD73-4043-8A5B-DD2159BC743F"
	ISShutdown     = "9F8233AC-BE49-4C79-8EE3-E7E1985B2077"
	ISTimeSync     = "2497F4DE-E9FA-4204-80E4-4B75C46419C0"
	ISVSS          = "5CED1297-4598-4915-A5FC-AD21BB4D02A4"
)

// Integration is the integration service set every plan wants: Heartbeat,
// Shutdown and Time Sync on; the Guest Service Interface, KVP and VSS off for
// good, because they carry data between guest and host.
func Integration() map[string]bool {
	return map[string]bool{
		ISHeartbeat:    true,
		ISShutdown:     true,
		ISTimeSync:     true,
		ISGuestService: false,
		ISKVP:          false,
		ISVSS:          false,
	}
}

// BindingsOff are the components setup unbinds from the host vNIC of our switch,
// by component ID: IPv6, the file server and client, the LLTD mapper I/O driver,
// the LLTD responder, LLDP, the multiplexor protocol, and the protocols Windows
// 11 also binds to a new vNIC: PPPoE, NDK (RDMA), NetBIOS over TCP/IP and NDIS
// user-mode I/O. Port ACLs filter IP only, so a non-IP frame from the guest can
// reach these. IPv4 must be the only protocol left; the guard compares all
// eleven.
func BindingsOff() []string {
	return []string{"ms_tcpip6", "ms_server", "ms_msclient", "ms_lltdio", "ms_rspndr", "ms_lldp", "ms_implat",
		"ms_pppoe", "ms_rdma_ndk", "ms_netbt", "ms_ndisuio"}
}

// StaticDeny is the fixed L2 Deny list (DESKTOP-VM.md §5): every private,
// shared, link-local, special and multicast IPv4 range, and all of IPv6.
func StaticDeny() []netip.Prefix {
	return []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/0"),
	}
}

// natSpace is where our /29 lives.
var natSpace = netip.MustParsePrefix("172.16.0.0/12")

// firstSubnet is where PickSubnet starts looking: high in 172.16/12, away from
// the 172.17-172.20 ranges container tools take first.
var firstSubnet = netip.MustParsePrefix("172.31.250.0/29")

// PickSubnet returns the first /29 in 172.16/12 that overlaps none of inUse. The
// caller passes the NetNat prefixes and every host address and on-link prefix.
// Only an overlap blocks a candidate (ADR §5 item 5).
func PickSubnet(inUse []netip.Prefix) (netip.Prefix, error) {
	base := natSpace.Addr().As4()
	start := firstSubnet.Addr().As4()
	n := uint32(1) << (32 - natSpace.Bits() - 3) // /29s in the /12
	b := be32(base)
	off := (be32(start) - b) >> 3
	for i := uint32(0); i < n; i++ {
		a := b + ((off+i)%n)<<3
		cand := netip.PrefixFrom(netip.AddrFrom4(fromBE32(a)), 29)
		if !overlapsAny(cand, inUse) {
			return cand, nil
		}
	}
	return netip.Prefix{}, errors.New("no free /29 left in 172.16.0.0/12")
}

func be32(a [4]byte) uint32 {
	return uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
}
func fromBE32(v uint32) [4]byte {
	return [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

func overlapsAny(p netip.Prefix, set []netip.Prefix) bool { return overlapping(p, set).IsValid() }

// Plan is the desired state, written as desired.json by the elevated setup or
// Repair, and compared by the guard on every pass.
// OwnedDisk records the file returned by setup's disk creation.
type OwnedDisk struct {
	Path     string `json:"path"`
	ID       string `json:"id"`
	FolderID string `json:"folderId,omitempty"`
}

var diskIDRE = regexp.MustCompile(`^[0-9a-f]{48}$`)

func (d OwnedDisk) validate() error {
	if err := checkWinPath("disk path", d.Path); err != nil {
		return err
	}
	if !strings.HasSuffix(strings.ToLower(d.Path), ".vhdx") || !diskIDRE.MatchString(d.ID) {
		return errors.New("the VM disk needs a VHDX path and a recorded file ID")
	}
	if d.FolderID != "" && !diskIDRE.MatchString(d.FolderID) {
		return errors.New("the VM disk folder needs a recorded file ID")
	}
	return nil
}

// OwnedDVD identifies an attachment returned by Hyper-V during setup.
type OwnedDVD struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type Plan struct {
	Version             int             `json:"version"`
	Names               Names           `json:"names"`
	VMID                string          `json:"vmId"`
	AdapterID           string          `json:"adapterId"` // the GUID after `\` in Get-VMNetworkAdapter .Id
	PortACLs            []PortACL       `json:"portAcls,omitempty"`
	GuardTaskID         string          `json:"guardTaskId,omitempty"`
	SwitchID            string          `json:"switchId"`
	ManagementAdapterID string          `json:"managementAdapterId,omitempty"`
	ManagementBindings  map[string]bool `json:"managementBindings,omitempty"`
	FirewallIdentity    string          `json:"firewallIdentity,omitempty"`
	NATID               string          `json:"natId,omitempty"`
	Disk                *OwnedDisk      `json:"disk,omitempty"`
	DVDs                []OwnedDVD      `json:"dvds,omitempty"`

	Subnet      netip.Prefix `json:"subnet"` // a free /29 in 172.16/12
	Gateway     netip.Addr   `json:"gateway"`
	GuestIP     netip.Addr   `json:"guestIp"`
	HostVNICMAC string       `json:"hostVnicMac"` // assigned by Hyper-V when the switch is created
	GuestMAC    string       `json:"guestMac"`

	StaticDeny []netip.Prefix `json:"staticDeny"`
	// ExtraExits are the public IPs of the user's other internet lines, each
	// denied as a /32 like the own IPs the guard finds itself.
	ExtraExits []netip.Addr `json:"extraExits"`

	DhcpGuard   bool `json:"dhcpGuard"`
	RouterGuard bool `json:"routerGuard"`
	MacSpoofing bool `json:"macSpoofing"`

	ProcCount   int    `json:"procCount"`
	ProcMax     int    `json:"procMax"`
	ProcWeight  int    `json:"procWeight"`
	MemoryBytes uint64 `json:"memoryBytes"`
	ExposeVirt  bool   `json:"exposeVirt"`
	Checkpoints string `json:"checkpoints"`
	AutoStart   string `json:"autoStart"`

	Integration map[string]bool `json:"integration"`
	BindingsOff []string        `json:"bindingsOff"`

	EnhancedSession ESPolicy `json:"enhancedSession"`
	// CreatedGuardian is whether setup created the vTPM's UntrustedGuardian.
	// Removal deletes it only then: other VMs may share it.
	CreatedGuardian bool   `json:"createdGuardian"`
	GuardianID      string `json:"guardianId,omitempty"`

	// Owner is a random token setup writes into desired.json before it creates
	// anything, and into the Notes of the VM and the switch and the description
	// of the firewall rule and the guard task it creates. A host object is this
	// install's only when it carries this token and matches its recorded ID, if any.
	Owner string `json:"owner"`
	// AdminGrantsTracked distinguishes confirmed grants from older plans,
	// which wrote AddedAdminSIDs before trying the host operation.
	AdminGrantsTracked bool     `json:"adminGrantsTracked,omitempty"`
	AddedAdminSIDs     []string `json:"addedAdminSids"`
	PendingAdminSIDs   []string `json:"pendingAdminSids,omitempty"`

	GCSBaseline []string  `json:"gcsBaseline"` // GuestCommunicationServices keys at setup, upper case
	HostBuild   string    `json:"hostBuild"`   // CurrentBuild.UBR, for the re-proof trigger
	ProvedAt    time.Time `json:"provedAt"`
}

// Options are the inputs setup chooses for a new plan.
type Options struct {
	Names           Names
	GuestMemory     uint64 // 0 means DefaultGuestMemory
	Subnet          netip.Prefix
	HostVNICMAC     string
	GuestMAC        string
	EnhancedSession ESPolicy // "" means ESOff
	ExtraExits      []netip.Addr
}

// LabOverrides lowers resource numbers for the lab, where the test PC has little
// free RAM. Only a lab build (the winvmlab tag) passes one to Apply. It has no
// field that touches the fence: ACLs, bindings, firewall or integration services.
// A test asserts that by reflection.
type LabOverrides struct {
	MinHostRAM          uint64 `json:"minHostRam"`
	GuestMemory         uint64 `json:"guestMemory"`
	KeepEnhancedSession bool   `json:"keepEnhancedSession"`
	Instance            int    `json:"instance"`
}

// Apply returns o with the lab's names and resource numbers.
func (l LabOverrides) Apply(o Options) (Options, error) {
	n, err := LabNames(l.Instance)
	if err != nil {
		return o, err
	}
	o.Names = n
	if l.GuestMemory != 0 {
		o.GuestMemory = l.GuestMemory
	}
	if l.KeepEnhancedSession {
		o.EnhancedSession = ESKeep
	}
	return o, nil
}

var (
	ownerRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	sidRe   = regexp.MustCompile(`^S-1-[0-9]+(-[0-9]+){1,15}$`)
	macHex  = regexp.MustCompile(`^[0-9A-F]{12}$`)
	guidRe  = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
)

// NormalizeMAC turns 00-15-5D-0A-0B-0C, 00:15:5d:0a:0b:0c or 00155D0A0B0C into
// 00155D0A0B0C, and rejects anything else, multicast and all zeros included.
func NormalizeMAC(s string) (string, error) {
	m := strings.ToUpper(strings.NewReplacer("-", "", ":", "", ".", "").Replace(strings.TrimSpace(s)))
	if !macHex.MatchString(m) {
		return "", fmt.Errorf("MAC %q is not 12 hex digits", s)
	}
	if m == "000000000000" {
		return "", fmt.Errorf("MAC %q is all zeros", s)
	}
	if strings.IndexByte("13579BDF", m[1]) >= 0 {
		return "", fmt.Errorf("MAC %q is a multicast address", s)
	}
	return m, nil
}

// NewPlan builds the desired state. prev is the plan of an earlier run (Repair):
// it carries the VM's identity and ownership records,
// which Repair never overwrites.
func NewPlan(o Options, prev *Plan) (Plan, error) {
	if err := o.Names.Validate(); err != nil {
		return Plan{}, err
	}
	if err := checkSubnet(o.Subnet); err != nil {
		return Plan{}, err
	}
	var hostMAC string
	if o.HostVNICMAC != "" {
		var err error
		hostMAC, err = NormalizeMAC(o.HostVNICMAC)
		if err != nil {
			return Plan{}, fmt.Errorf("host vNIC: %w", err)
		}
	} else if prev != nil && (prev.SwitchID != "" || prev.ManagementAdapterID != "" || prev.VMID != "" || !prev.ProvedAt.IsZero()) {
		return Plan{}, errors.New("host vNIC: missing the recorded MAC")
	}
	guestMAC, err := NormalizeMAC(o.GuestMAC)
	if err != nil {
		return Plan{}, fmt.Errorf("guest: %w", err)
	}
	if hostMAC == guestMAC {
		return Plan{}, errors.New("the guest and the host vNIC have the same MAC")
	}
	mem := o.GuestMemory
	if mem == 0 {
		mem = DefaultGuestMemory
	}
	if mem < 2*GiB || mem > 64*GiB || mem%(2<<20) != 0 {
		return Plan{}, fmt.Errorf("guest memory %d is outside 2..64 GiB or not a multiple of 2 MiB", mem)
	}
	es := o.EnhancedSession
	if es == "" {
		es = ESOff
	}
	if es != ESOff && es != ESKeep {
		return Plan{}, fmt.Errorf("enhanced session policy %q is unknown", es)
	}
	// Keep is for the lab only. The product requires Enhanced Session off.
	if es == ESKeep && o.Names == ProductNames() {
		return Plan{}, errors.New("the product VM requires Enhanced Session off; keep is a lab setting")
	}
	var exits []netip.Addr
	for _, a := range o.ExtraExits {
		a = a.Unmap()
		if !a.Is4() || a.IsUnspecified() {
			return Plan{}, fmt.Errorf("other line exit %v is not an IPv4 address", a)
		}
		if !slices.Contains(exits, a) {
			exits = append(exits, a)
		}
	}
	slices.SortFunc(exits, netip.Addr.Compare)

	gw := o.Subnet.Addr().Next()
	p := Plan{
		Version:         PlanVersion,
		Names:           o.Names,
		Subnet:          o.Subnet,
		Gateway:         gw,
		GuestIP:         gw.Next(),
		HostVNICMAC:     hostMAC,
		GuestMAC:        guestMAC,
		StaticDeny:      StaticDeny(),
		ExtraExits:      exits,
		DhcpGuard:       true,
		RouterGuard:     true,
		MacSpoofing:     false,
		ProcCount:       2,
		ProcMax:         75,
		ProcWeight:      50,
		MemoryBytes:     mem,
		ExposeVirt:      false,
		Checkpoints:     "Disabled",
		AutoStart:       "Nothing",
		Integration:     Integration(),
		BindingsOff:     BindingsOff(),
		EnhancedSession: es,
		GCSBaseline:     []string{},
		AddedAdminSIDs:  []string{},
	}
	if p.ExtraExits == nil {
		p.ExtraExits = []netip.Addr{}
	}
	if prev != nil {
		if prev.Names != o.Names {
			return Plan{}, fmt.Errorf("the earlier plan belongs to %q, not %q", prev.Names.VM, o.Names.VM)
		}
		p.VMID, p.AdapterID, p.SwitchID = prev.VMID, prev.AdapterID, prev.SwitchID
		p.DVDs = slices.Clone(prev.DVDs)
		p.ManagementAdapterID = prev.ManagementAdapterID
		p.ManagementBindings = maps.Clone(prev.ManagementBindings)
		p.PortACLs = slices.Clone(prev.PortACLs)
		p.GuardTaskID = prev.GuardTaskID
		if prev.Disk != nil {
			disk := *prev.Disk
			p.Disk = &disk
		}
		p.CreatedGuardian, p.GuardianID = prev.CreatedGuardian, prev.GuardianID
		p.NATID = prev.NATID
		p.FirewallIdentity = prev.FirewallIdentity
		p.Owner = prev.Owner
		p.AdminGrantsTracked = prev.AdminGrantsTracked
		p.AddedAdminSIDs = append([]string{}, prev.AddedAdminSIDs...)
		p.PendingAdminSIDs = slices.Clone(prev.PendingAdminSIDs)

	}
	return p, nil
}

// SetGCSBaseline records the GuestCommunicationServices keys read at setup.
func (p *Plan) SetGCSBaseline(keys []string) { p.GCSBaseline = GCSBaseline(keys) }

// GCSBaseline normalizes GuestCommunicationServices key names: upper case,
// braces stripped, sorted, no duplicates, never nil.
func GCSBaseline(keys []string) []string {
	out := []string{}
	for _, k := range keys {
		k = strings.ToUpper(strings.Trim(strings.TrimSpace(k), "{}"))
		if k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// unmapPrefix turns an IPv4-mapped IPv6 prefix (::ffff:a.b.c.d/n) into its
// IPv4 prefix (a.b.c.d/n-96) and masks it. A mapped prefix shorter than /96
// spans more than the mapped range and has no IPv4 form: it comes back
// invalid, and the caller drops it (::/0 already denies all of IPv6).
func unmapPrefix(q netip.Prefix) netip.Prefix {
	if !q.IsValid() {
		return netip.Prefix{}
	}
	if q.Addr().Is4In6() {
		if q.Bits() < 96 {
			return netip.Prefix{}
		}
		return netip.PrefixFrom(q.Addr().Unmap(), q.Bits()-96).Masked()
	}
	return q.Masked()
}

func checkSubnet(s netip.Prefix) error {
	if !s.IsValid() || !s.Addr().Is4() || s.Bits() != 29 || s.Masked() != s || !natSpace.Contains(s.Addr()) {
		return fmt.Errorf("subnet %v is not a /29 inside %v", s, natSpace)
	}
	return nil
}

// Dynamic are the Denies the guard works out on each pass: the host's own
// addresses, the on-link prefixes of its physical adapters, the user's public
// IPs and every prefix routed through a tunnel.
type Dynamic struct {
	HostAddrs, OnLink, OwnIPs, Tunnels []netip.Prefix
}

// WantACLs is the exact ACL set the guest's adapter must carry: every static
// Deny, each other line's exit that no static row covers, then each dynamic
// prefix that no other row already covers. No two rows share a prefix: Hyper-V
// refuses a second row with the same prefix with 0x800700B7, so a duplicate
// would fail the apply.
//
// An exit's row never gives way to a dynamic row. Setup places it and the
// guard does not manage it, so a tunnel route that covers it for a while must
// not make it look extra, and must not leave it missing once the route goes.
func (p Plan) WantACLs(d Dynamic) []ACL {
	fixed := map[netip.Prefix]bool{}
	var rows []netip.Prefix
	for _, s := range p.StaticDeny {
		s = s.Masked()
		if s.IsValid() && !fixed[s] {
			fixed[s] = true
			rows = append(rows, s)
		}
	}
	staticRows := slices.Clone(rows)
	for _, a := range p.ExtraExits {
		q := netip.PrefixFrom(a.Unmap(), 32)
		if !q.IsValid() || fixed[q] || slices.ContainsFunc(staticRows, func(r netip.Prefix) bool { return r.Contains(q.Addr()) }) {
			continue
		}
		fixed[q] = true
		rows = append(rows, q)
	}
	var dyn []netip.Prefix
	for _, set := range [][]netip.Prefix{d.HostAddrs, d.OnLink, d.OwnIPs, d.Tunnels} {
		for _, q := range set {
			if q = unmapPrefix(q); q.IsValid() {
				dyn = append(dyn, q)
			}
		}
	}
	// A dynamic prefix is dropped when any other row covers it, or when it repeats.
	all := append(slices.Clone(rows), dyn...)
	for i, q := range dyn {
		if fixed[q] || slices.Contains(dyn[:i], q) {
			continue
		}
		covered := false
		for _, r := range all {
			if r != q && r.Bits() < q.Bits() && r.Contains(q.Addr()) {
				covered = true
				break
			}
		}
		if !covered {
			rows = append(rows, q)
		}
	}
	sortPrefixes(rows)
	acls := make([]ACL, len(rows))
	for i, r := range rows {
		acls[i] = ACL{Action: "Deny", Direction: "Both", Remote: r}
	}
	return acls
}

// sortPrefixes orders IPv4 before IPv6, then by address, then by length.
func sortPrefixes(ps []netip.Prefix) {
	slices.SortFunc(ps, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
}

// Marshal writes desired.json.
func (p Plan) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// LoadPlan reads desired.json strictly: capped size, no unknown field, this
// version only, and every fence field exactly as NewPlan writes it. A plan file
// can only describe the one fence this code builds, never a looser one.
func LoadPlan(b []byte) (Plan, error) {
	if len(b) > maxPlanBytes {
		return Plan{}, fmt.Errorf("plan is %d bytes, over the %d byte cap", len(b), maxPlanBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var wire struct {
		Plan
		// Read old plans without restoring retired host settings or using the old storage hint.
		Storage          json.RawMessage `json:"vhdxDir"`
		EnhancedOriginal json.RawMessage `json:"enhancedSessionOriginal"`
		EnhancedChanged  json.RawMessage `json:"enhancedSessionChanged"`
		RevokedGrants    json.RawMessage `json:"revokedAdminSids"`
	}
	if err := dec.Decode(&wire); err != nil {
		return Plan{}, fmt.Errorf("plan: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Plan{}, errors.New("plan: trailing data after the document")
	}
	p := wire.Plan
	if p.Version != PlanVersion {
		return Plan{}, fmt.Errorf("plan version %d, this code reads %d", p.Version, PlanVersion)
	}
	if err := p.validate(); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// validate rebuilds the plan from its own options and checks that every field
// NewPlan derives matches. Only the identity, proof and baseline fields may
// differ.
func (p Plan) validate() error {
	o := Options{
		Names:           p.Names,
		GuestMemory:     p.MemoryBytes,
		Subnet:          p.Subnet,
		HostVNICMAC:     p.HostVNICMAC,
		GuestMAC:        p.GuestMAC,
		EnhancedSession: p.EnhancedSession,
		ExtraExits:      p.ExtraExits,
	}
	want, err := NewPlan(o, &p)
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	want.GCSBaseline = GCSBaseline(p.GCSBaseline)
	want.HostBuild = p.HostBuild
	want.ProvedAt = p.ProvedAt
	for _, id := range []string{p.VMID, p.AdapterID, p.SwitchID} {
		if id != "" && !guidRe.MatchString(id) {
			return fmt.Errorf("plan: ID %q is not a GUID", id)
		}
	}
	if p.GuardTaskID != "" && p.GuardTaskID != p.Names.GuardTask {
		return errors.New("plan: invalid scheduled task identity")
	}
	if p.ManagementAdapterID != "" && !validNATID(p.ManagementAdapterID) {
		return errors.New("plan: invalid management adapter identity")
	}
	if p.NATID != "" && !validNATID(p.NATID) {
		return errors.New("plan: invalid NAT identity")
	}
	if p.FirewallIdentity != "" && !identityDigestRE.MatchString(p.FirewallIdentity) {
		return errors.New("plan: invalid firewall identity")
	}
	if p.GuardianID != "" && !identityDigestRE.MatchString(p.GuardianID) {
		return errors.New("plan: invalid guardian fingerprint")
	}
	if p.Disk != nil {
		if err := p.Disk.validate(); err != nil {
			return err
		}
	}
	for _, a := range p.PortACLs {
		if !a.valid() || a.Action != 2 || !strings.HasPrefix(a.Name, p.Owner) {
			return errors.New("plan: invalid recorded port ACL")
		}
	}
	if len(p.DVDs) > 2 {
		return errors.New("plan: too many recorded DVD drives")
	}
	for _, dvd := range p.DVDs {
		if dvd.ID == "" || len(dvd.ID) > 512 || dvd.Path == "" || len(dvd.Path) > 512 {
			return errors.New("plan: invalid DVD attachment")
		}
	}
	if p.Owner != "" && !ownerRe.MatchString(p.Owner) {
		return fmt.Errorf("plan: owner %q is not 32 lower-case hex digits", p.Owner)
	}
	sids := slices.Concat(p.AddedAdminSIDs, p.PendingAdminSIDs)
	for i, sid := range sids {
		if !sidRe.MatchString(sid) {
			return fmt.Errorf("plan: %q is not a Windows account SID", sid)
		}
		if slices.ContainsFunc(sids[:i], func(s string) bool { return strings.EqualFold(s, sid) }) {
			return fmt.Errorf("plan: %q is recorded twice", sid)
		}
	}
	got := p
	if got.ExtraExits == nil {
		got.ExtraExits = []netip.Addr{}
	}
	if got.GCSBaseline == nil {
		got.GCSBaseline = []string{}
	}
	if got.AddedAdminSIDs == nil {
		got.AddedAdminSIDs = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		return errors.New("plan: a fence field differs from what this code writes")
	}
	return nil
}
