package winvm

import (
	"context"
	"errors"
	"net/netip"
)

// ErrUnsupported is what every Hyper-V call returns where the Secure VM cannot
// run: any OS but Windows, and the product guest's raw-frame probe.
var ErrUnsupported = errors.New("the Secure VM (Windows 11 Pro) needs Windows 11 with Hyper-V")

// The Hyper-V seam. Every call to the operating system goes through these
// interfaces. The Windows implementation runs one fixed PowerShell script per
// method with -EncodedCommand. Host data reaches a script only as one base64
// JSON argument, which the script reads with ConvertFrom-Json, and Go checks
// every value before it goes in: names against ^[A-Za-z0-9-]{1,32}$, IDs as
// GUIDs, addresses through netip. No guest string ever enters a script. Each
// script answers with one JSON document of at most 1 MB.
//
// LiveState, the reading Diff compares, lives in diff.go.

// Reader reads the host. Nothing it does changes anything.
type Reader interface {
	ReadLive(ctx context.Context, n Names, vmID string) (LiveState, error)
	ReadHostNet(ctx context.Context, samples []netip.Addr) (HostNet, error)
	ReadNetwork(ctx context.Context) (CurrentNetwork, error)
	// DropCounters reads the FailedSecurityPolicy drop counter of one adapter.
	// It returns an error, never a zero sample, when no source answered, so an
	// unknown count is never shown as 0.
	DropCounters(ctx context.Context, vmID, adapterID string) (DropSample, error)
	// Usage reads Get-VM CPUUsage, MemoryAssigned and Uptime, the VHDX file
	// size and the adapter's byte counter. It never turns on resource metering,
	// which can add Meter ACLs.
	Usage(ctx context.Context, vmID string) (Usage, error)
	// Inventory lists every host object that carries one of our names, plus
	// the VM and the switch whose IDs are given even when someone renamed
	// them, with the owner token each carries, and the host facts build and
	// remove check. The guard reads it only to find the VM of an interrupted
	// setup.
	Inventory(ctx context.Context, n Names, known KnownIDs, userSID string) (Inventory, error)
}

// Guarder is what the SYSTEM guard needs: the reads, and the few writes it may
// make on its own.
type Guarder interface {
	Reader
	Disconnect(ctx context.Context, vmID string) error
	// Connect takes the switch by name, because a disconnect clears the
	// adapter's switch (ADR §5 item 11).
	Connect(ctx context.Context, vmID, switchName string) error
	Start(ctx context.Context, vmID string) error
	AddDeny(ctx context.Context, vmID string, p []netip.Prefix) error
	RemoveDeny(ctx context.Context, vmID string, p []netip.Prefix) error
}

// Builder is what the elevated setup, Repair and Remove need. Every write is
// idempotent: a call whose result is already in place changes nothing, so a
// build that stopped half way runs again from the start.
//
// Ownership: a VM or a switch is this install's only when its ID is recorded in
// the plan or its Notes carry the plan's Owner token. A firewall rule or the
// guard task is ours only when its description carries the token. The NAT has
// neither, so it is ours only when it has our name and our recorded /29. Build
// and Remove read Inventory first and refuse, or leave alone, anything else.
type Builder interface {
	Guarder
	// EnsureSwitch creates the Internal switch with the owner token in its
	// Notes, gives its host vNIC vnicMAC and the gateway address of sub, or
	// checks those on the switch already there: the one with the recorded
	// switchID (renamed or not), else the one with our name. It refuses a
	// switch with our name that does not carry the token.
	EnsureSwitch(ctx context.Context, n Names, sub netip.Prefix, owner, vnicMAC, switchID string) (id string, vnicIfIndex int, err error)
	EnsureNAT(ctx context.Context, n Names, sub netip.Prefix) error
	// CreateVM creates the VM off, with p.Owner in its Notes, the plan's
	// shape, one disconnected adapter with its static MAC and port guards
	// (only the proof and the guard ever connect it), and the two ISOs as DVD
	// drives, the Windows ISO first to boot.
	CreateVM(ctx context.Context, p Plan, iso, answerISO string) (vmID, adapterID string, err error)
	// SetIntegration runs before the first Start-VM.
	SetIntegration(ctx context.Context, vmID string, want map[string]bool) error
	SetBindings(ctx context.Context, vnicIfIndex int, off []string) error
	EnsureFirewall(ctx context.Context, n Names, vnicIfIndex int, owner string) error
	SetHostNeighbor(ctx context.Context, vnicIfIndex int, ip netip.Addr, mac string) error
	SetEnhancedSession(ctx context.Context, on bool) (was bool, err error)
	EnsureGuardian(ctx context.Context) (created bool, err error)
	// AddHyperVAdmin adds the account to Hyper-V Administrators (S-1-5-32-578)
	// by SID, and reports whether it was added now.
	AddHyperVAdmin(ctx context.Context, sid string) (added bool, err error)
	// WriteAnswerISO writes the answer file onto a new ISO at path.
	WriteAnswerISO(ctx context.Context, path string, xml []byte) error
	// BootKeys presses a key once a second for 10 seconds, for the Gen 2
	// "press any key to boot from CD" prompt.
	BootKeys(ctx context.Context, vmID string) error
	// EjectDVDs removes every DVD drive of the VM.
	EjectDVDs(ctx context.Context, vmID string) error
	// RegisterGuardTask stops the guard task if it runs, then registers it with
	// the owner token in its description. It triggers at startup, on resume
	// (Power-Troubleshooter 1) and every 5 minutes from ProofLockTTL on, with
	// IgnoreNew, so a fresh guard never starts in the middle of the proof.
	RegisterGuardTask(ctx context.Context, n Names, exe, owner string) error
	StartGuardTask(ctx context.Context, n Names) error
	// Guest runs one fixed script over PowerShell Direct with one JSON
	// argument, and decodes its answer, capped at 64 KB, into out.
	Guest(ctx context.Context, vmID string, cred Cred, s GuestScript, arg, out any) error

	// The removal side. Each one is a no-op when the object is already gone.
	UnregisterGuardTask(ctx context.Context, n Names, owner string) error
	// RemoveVM turns the VM off, removes it, and deletes its disks that lie in
	// vhdxDir. It lists the disks it left because they lie elsewhere.
	RemoveVM(ctx context.Context, vmID, vhdxDir string) (left []string, err error)
	RemoveSwitch(ctx context.Context, switchID string) error
	RemoveNAT(ctx context.Context, n Names, sub netip.Prefix) error
	// RemoveFirewall removes the rules of our group that carry the owner token.
	RemoveFirewall(ctx context.Context, n Names, owner string) error
	RemoveGuardian(ctx context.Context) error
	RemoveHyperVAdmin(ctx context.Context, sid string) error
	// ClearProof removes what a proof that was killed can leave behind: the
	// canary rules of the instance's proof firewall group whose description
	// carries owner, the hv_sock test key named ProofElementName(n, owner),
	// and a pktmon session that writes under DataDir\proof. A rule or key
	// without the token is never touched; with owner "" only the pktmon
	// session goes. It lists what it removed. Setup runs it before it lists
	// the host, so a stale key never enters the GCS baseline; Remove VM runs
	// it too.
	ClearProof(ctx context.Context, n Names, owner string) (cleared []string, err error)
}

// KnownIDs are the VM and switch IDs desired.json records, "" for none.
// Inventory lists those objects by ID too, so a VM or a switch the user renamed
// is still found, kept and removed.
type KnownIDs struct {
	VM, Switch string
}

// Inventory is what Builder.Inventory reads.
type Inventory struct {
	VMs      []OwnedObject `json:"vms"`      // every VM named Names.VM, and the one with KnownIDs.VM
	Switches []OwnedObject `json:"switches"` // every switch named Names.Switch, and the one with KnownIDs.Switch
	NATs     []LiveNAT     `json:"nats"`     // every NetNat on the host
	Firewall []OwnedObject `json:"firewall"` // every rule of Names.FirewallGroup, by rule name
	Task     *OwnedObject  `json:"task"`     // the guard task, nil when absent
	Guardian bool          `json:"guardian"` // the UntrustedGuardian exists
	// GuardianVMs are the IDs of every VM that may need the guardian: a vTPM,
	// a key protector, encrypted state, or one Hyper-V could not read.
	GuardianVMs []string `json:"guardianVms"`
	// HyperVAdmin is whether the user SID is in Hyper-V Administrators.
	HyperVAdmin  bool     `json:"hypervAdmin"`
	Enhanced     bool     `json:"enhancedSession"`
	GCS          []string `json:"gcs"` // nil when not read
	HostBuild    string   `json:"hostBuild"`
	InstalledRAM uint64   `json:"installedRam"`
	FreeRAM      uint64   `json:"freeRam"`
}

// OwnedObject is one host object with our name. Owner is the token its Notes
// or description carry, "" for none. Ports lists the VMs with an adapter on a
// switch.
type OwnedObject struct {
	ID    string   `json:"id"`
	Owner string   `json:"owner"`
	Ports []string `json:"ports,omitempty"`
}

// Prober opens the holes the proof needs and watches the host for arrivals.
type Prober interface {
	// Canary listens on ip and port behind a firewall Allow that takes only
	// connections from the prefix from: the guest's address.
	Canary(ctx context.Context, ip netip.Addr, port uint16, from netip.Prefix) (Canary, error)
	// Pktmon refuses when a pktmon session already runs.
	Pktmon(ctx context.Context, f []PktFilter) (Capture, error)
	SetL3(ctx context.Context, n Names, on bool) error
	SetBinding(ctx context.Context, vnicIfIndex int, component string, on bool) error
	AddAllow(ctx context.Context, vmID string, p netip.Prefix) error
	RemoveAllow(ctx context.Context, vmID string, p netip.Prefix) error
	HVListener(ctx context.Context, bindVMID, service string, register bool) (HVListener, error)
	// LinkLocal is the host vNIC's IPv6 link-local address, without a zone.
	// It exists only while ms_tcpip6 is bound.
	LinkLocal(ctx context.Context, vnicIfIndex int) (netip.Addr, error)
}

// GuestProbe sends probes from inside the guest. Product: PowerShell Direct.
// Lab (build tag winvmlab): the instrument VM's serial agent. What it reports
// only says a probe was sent; the verdict reads host evidence.
type GuestProbe interface {
	TCP(ctx context.Context, to netip.AddrPort, token string) (sent bool, err error)
	UDP(ctx context.Context, to netip.AddrPort, token string, n int) error
	DNS(ctx context.Context, server netip.Addr, name string) ([]netip.Addr, error)
	Neighbor(ctx context.Context, ip netip.Addr, mac string) error
	HVSock(ctx context.Context, service, token string) (connected bool, err error)
	Egress(ctx context.Context) (netip.Addr, error)
	// RawFrame returns ErrUnsupported in the product guest.
	RawFrame(ctx context.Context, ethertype uint16, dst string, payload []byte, n int) error
}

// Host is what this package implements against the real Hyper-V today: the
// reads and the guard's writes. NewHost returns it.
type Host interface {
	Guarder
}

// Usage is what the host measures of the VM, without resource metering.
type Usage struct {
	CPUPercent    int    `json:"cpuPercent"`
	MemoryBytes   uint64 `json:"memoryBytes"`
	UptimeSeconds uint64 `json:"uptimeSeconds"`
	DiskBytes     uint64 `json:"diskBytes"`
	// BytesSent is the raw "Bytes Sent" counter of the VM's adapter. Nil means
	// it was not read, never 0.
	BytesSent *uint64 `json:"bytesSent"`
}

// Cred is a guest account. The password never goes into JSON.
type Cred struct {
	User     string `json:"user"`
	Password string `json:"-"`
}

// GuestScript is one fixed script the host runs in the guest. Only scripts
// this package defines exist; no guest string is ever part of one.
type GuestScript struct {
	Name string
	Text string
}

// Canary is a TCP listener on the host that waits for a token.
type Canary interface {
	// Arrived lists the tokens that arrived so far.
	Arrived() []string
	Close() error
}

// PktFilter is one pktmon filter: a remote address, and a protocol and port
// when set.
type PktFilter struct {
	Addr  netip.Addr `json:"addr"`
	Proto string     `json:"proto"` // "TCP", "UDP" or "" for any
	Port  uint16     `json:"port"`
}

// PktRecord is one packet pktmon saw.
type PktRecord struct {
	Component string     `json:"component"`
	Class     string     `json:"class"` // "physical", "tunnel", "vnic" or "vmport"
	Src       netip.Addr `json:"src"`
	Dst       netip.Addr `json:"dst"`
	Proto     string     `json:"proto"`
	Port      uint16     `json:"port"`
	Dropped   bool       `json:"dropped"`
}

// Capture is a running pktmon session.
type Capture interface {
	Stop(ctx context.Context) ([]PktRecord, error)
}

// HVListener is an hv_sock listener on the host.
type HVListener interface {
	Received() []string
	// Unregister deletes the GuestCommunicationServices key the listener
	// made and keeps the socket bound, so a probe after it shows that the
	// deleted registration alone stops the guest.
	Unregister() error
	Close() error
}
