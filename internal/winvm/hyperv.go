package winvm

import (
	"context"
	"errors"
	"net/netip"
)

// ErrUnsupported is what every Hyper-V call returns where the Secure VM cannot
// run: any OS but Windows, and the product guest's raw-frame probe.
var ErrUnsupported = errors.New("the Secure VM needs Windows 11 with Hyper-V")

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

// Builder is what the elevated setup, Repair and Remove need.
type Builder interface {
	Guarder
	EnsureSwitch(ctx context.Context, n Names, sub netip.Prefix) (switchID string, vnicMAC string, err error)
	EnsureNAT(ctx context.Context, n Names, sub netip.Prefix) error
	CreateVM(ctx context.Context, p Plan, iso, answerISO string) (vmID, adapterID string, err error)
	// SetIntegration runs before the first Start-VM.
	SetIntegration(ctx context.Context, vmID string, want map[string]bool) error
	SetBindings(ctx context.Context, vnicIfIndex int, off []string) error
	EnsureFirewall(ctx context.Context, n Names, vnicIfIndex int) error
	SetHostNeighbor(ctx context.Context, vnicIfIndex int, ip netip.Addr, mac string) error
	SetEnhancedSession(ctx context.Context, on bool) (was bool, err error)
	EnsureGuardian(ctx context.Context) (created bool, err error)
	// RegisterGuardTask triggers at startup, on resume (Power-Troubleshooter 1)
	// and every 5 minutes, with IgnoreNew.
	RegisterGuardTask(ctx context.Context, n Names, exe string) error
	// Guest runs one fixed script over PowerShell Direct and decodes its answer,
	// capped at 64 KB, into out.
	Guest(ctx context.Context, vmID string, cred Cred, s GuestScript, out any) error
	Remove(ctx context.Context, p Plan) (left []string, err error)
}

// Prober opens the holes the proof needs and watches the host for arrivals.
type Prober interface {
	Canary(ctx context.Context, ip netip.Addr, port uint16, token string) (Canary, error)
	// Pktmon refuses when a pktmon session already runs.
	Pktmon(ctx context.Context, f []PktFilter) (Capture, error)
	SetL3(ctx context.Context, n Names, on bool) error
	SetBinding(ctx context.Context, vnicIfIndex int, component string, on bool) error
	AddAllow(ctx context.Context, vmID string, p netip.Prefix) error
	RemoveAllow(ctx context.Context, vmID string, p netip.Prefix) error
	HVListener(ctx context.Context, bindVMID, service string, register bool) (HVListener, error)
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
// reads, and the disconnect and connect the guard needs. NewHost returns it.
type Host interface {
	Reader
	Disconnect(ctx context.Context, vmID string) error
	Connect(ctx context.Context, vmID, switchName string) error
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
	Close() error
}
