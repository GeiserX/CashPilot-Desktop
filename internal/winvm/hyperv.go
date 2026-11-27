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
// method from a SHA-256-verified file. Host data reaches a script as one base64
// JSON argument, which the script reads with ConvertFrom-Json, and Go checks
// every value before it goes in: names against ^[A-Za-z0-9-]{1,32}$, IDs as
// GUIDs, addresses through netip. No guest string ever enters a script. Each
// script answers with one JSON document of at most 1 MB.
//
// LiveState, the reading Diff compares, lives in diff.go.

// Reader reads the host. Nothing it does changes anything.
type Reader interface {
	ReadLive(ctx context.Context, n Names, vmID, switchID string) (LiveState, error)
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
	Disconnect(ctx context.Context, vmID, owner, adapterID string) error
	// Connect matches both recorded IDs and both live owner tags.
	Connect(ctx context.Context, vmID, switchID, owner, adapterID string) error
	Start(ctx context.Context, vmID, owner string) error
	AddPortACLs(ctx context.Context, p Plan, action string, prefixes []netip.Prefix) ([]PortACL, error)
	RemovePortACLs(ctx context.Context, p Plan, rows []PortACL) error
}

// Builder is what the elevated setup, Repair and Remove need. Every write is
// idempotent: a call whose result is already in place changes nothing, so a
// build that stopped half way runs again from the start.
//
// Ownership: a VM or switch must carry the plan's Owner token and match its
// recorded ID when present. A firewall rule or the
// guard task is ours only when its description carries the token. A NAT
// needs an independent provider identity before it can be deleted. Build
// and Remove read Inventory first and refuse, or leave alone, anything else.
type Builder interface {
	Guarder
	// EnsureSwitch creates the Internal switch with the owner token in its
	// Notes and configures the gateway. It returns the assigned host vNIC MAC.
	// On a recorded switch it verifies the adapter ID and MAC before changes.
	EnsureSwitch(ctx context.Context, n Names, sub netip.Prefix, owner, vnicMAC, switchID, managementID string) (id, managementAdapterID, assignedMAC string, vnicIfIndex int, err error)
	EnsureNAT(ctx context.Context, n Names, sub netip.Prefix, knownID string) (id string, err error)
	// CreateVM creates the VM off, with p.Owner in its Notes, the plan's
	// shape, one disconnected adapter with its static MAC and port guards
	// (only the proof and the guard ever connect it), and the two ISOs as DVD
	// drives, the Windows ISO first to boot.
	CreateVM(ctx context.Context, p Plan, iso, answerISO string) (vmID, adapterID string, disk OwnedDisk, dvds []OwnedDVD, err error)
	// SetIntegration runs before the first Start-VM.
	SetIntegration(ctx context.Context, vmID, owner string, want map[string]bool) error
	SetBindings(ctx context.Context, p Plan, vnicIfIndex int) (map[string]bool, error)
	EnsureFirewall(ctx context.Context, p Plan, vnicIfIndex int) (string, error)
	SetHostNeighbor(ctx context.Context, vnicIfIndex int, switchID, owner, managementID string, ip netip.Addr, mac string) error

	EnsureGuardian(ctx context.Context) (createdID string, err error)
	// AddHyperVAdmin adds the account to Hyper-V Administrators (S-1-5-32-578)
	// by SID, and reports whether it was added now.
	AddHyperVAdmin(ctx context.Context, sid string) (added bool, err error)
	// WriteAnswerISO writes the answer file onto a new ISO at path.
	WriteAnswerISO(ctx context.Context, path string, xml []byte) error
	// BootKeys starts the VM and presses a key every half second for 20
	// seconds, for the Gen 2 "press any key to boot from CD" prompt. The prompt
	// shows for about 5 seconds, so the keys start in the same process as the
	// start.
	BootKeys(ctx context.Context, vmID, owner string) error
	// EjectDVDs removes the attachments setup recorded.
	EjectDVDs(ctx context.Context, vmID, owner string, dvds []OwnedDVD) error
	// RegisterGuardTask stops the guard task if it runs, then registers it with
	// the owner token in its description. It triggers at startup, on resume
	// (Power-Troubleshooter 1) and every 5 minutes from ProofLockTTL on, with
	// IgnoreNew, so a fresh guard never starts in the middle of the proof.
	RegisterGuardTask(ctx context.Context, n Names, exe, owner string) error
	StartGuardTask(ctx context.Context, n Names, owner string) error
	// Guest runs one fixed script over PowerShell Direct with one JSON
	// argument, and decodes its answer, capped at 64 KB, into out.
	Guest(ctx context.Context, vmID, owner string, cred Cred, s GuestScript, arg, out any) error

	// The removal side. Each one is a no-op when the object is already gone.
	UnregisterGuardTask(ctx context.Context, n Names, owner string) error
	// RemoveVM turns off and removes the owned VM, then deletes the
	// recorded disk if its full path and file ID still match.
	RemoveVM(ctx context.Context, vmID string, p Plan) (left []string, err error)
	RemoveSwitch(ctx context.Context, p Plan) (kept string, err error)
	RemoveNAT(ctx context.Context, n Names, sub netip.Prefix, id string) (kept string, err error)
	// RemoveFirewall removes the rules of our group that carry the owner token.
	RemoveFirewall(ctx context.Context, p Plan) (kept string, err error)
	RemoveGuardian(ctx context.Context, id string) error

	// ClearProof removes owned artifacts from earlier proofs and lists
	// candidates whose ownership cannot be verified.
	ClearProof(ctx context.Context, n Names, owner string) (ProofCleanup, error)
}

type ProofCleanup struct {
	Cleared           []string `json:"cleared"`
	Left              []string `json:"left"`
	UnverifiedCapture bool     `json:"unverifiedCapture"`
}

// KnownIDs lets Inventory find renamed objects by recorded ID. In recovery
// without an ID, it lists every object of that kind carrying the owner tag.
type KnownIDs struct {
	VM, Switch string
	Owner      string
}

// Inventory is what Builder.Inventory reads.
type Inventory struct {
	VMs        []OwnedObject `json:"vms"`        // every VM named Names.VM, and the one with KnownIDs.VM
	Switches   []OwnedObject `json:"switches"`   // every switch named Names.Switch, and the one with KnownIDs.Switch
	NATs       []LiveNAT     `json:"nats"`       // every NetNat on the host
	Firewall   []OwnedObject `json:"firewall"`   // every rule of Names.FirewallGroup, by rule name
	Task       *OwnedObject  `json:"task"`       // the guard task, nil when absent
	Guardian   bool          `json:"guardian"`   // the UntrustedGuardian exists
	GuardianID string        `json:"guardianId"` // fingerprint of both certificates
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
	SetL3(ctx context.Context, n Names, owner string, on bool) error
	SetBinding(ctx context.Context, vnicIfIndex int, component string, on bool) error
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
	// HVSock returns false without an error only for an explicit refusal.
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
	// Received includes every accepted connection, even one without a token.
	Received() []string
	// Unregister deletes the GuestCommunicationServices key the listener
	// made and keeps the socket bound, so a probe after it shows that the
	// deleted registration alone stops the guest.
	Unregister() error
	Close() error
}
