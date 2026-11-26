//go:build !windows

package winvm

import (
	"context"
	"net/netip"
)

// NewHost is a host without Hyper-V: every call returns ErrUnsupported.
func NewHost(string) Host { return unsupported{} }

// NewBuilder is a host without Hyper-V: every call returns ErrUnsupported.
func NewBuilder(Names) Builder { return unsupported{} }

// NewProber is a host without Hyper-V: every call returns ErrUnsupported.
func NewProber(Names) Prober { return unsupported{} }

// unsupported implements every seam interface with ErrUnsupported.
type unsupported struct{}

var (
	_ Builder    = unsupported{}
	_ Prober     = unsupported{}
	_ GuestProbe = unsupported{}
)

func (unsupported) ReadLive(context.Context, Names, string, string) (LiveState, error) {
	return LiveState{}, ErrUnsupported
}
func (unsupported) ReadHostNet(context.Context, []netip.Addr) (HostNet, error) {
	return HostNet{}, ErrUnsupported
}
func (unsupported) ReadNetwork(context.Context) (CurrentNetwork, error) {
	return CurrentNetwork{}, ErrUnsupported
}
func (unsupported) DropCounters(context.Context, string, string) (DropSample, error) {
	return DropSample{}, ErrUnsupported
}
func (unsupported) Usage(context.Context, string) (Usage, error) { return Usage{}, ErrUnsupported }

func (unsupported) Disconnect(context.Context, string, string, string) error { return ErrUnsupported }
func (unsupported) Connect(context.Context, string, string, string, string) error {
	return ErrUnsupported
}
func (unsupported) Start(context.Context, string, string) error { return ErrUnsupported }
func (unsupported) AddPortACLs(context.Context, Plan, string, []netip.Prefix) ([]PortACL, error) {
	return nil, ErrUnsupported
}
func (unsupported) RemovePortACLs(context.Context, Plan, []PortACL) error { return ErrUnsupported }
func (unsupported) Inventory(context.Context, Names, KnownIDs, string) (Inventory, error) {
	return Inventory{}, ErrUnsupported
}
func (unsupported) EnsureSwitch(context.Context, Names, netip.Prefix, string, string, string, string) (string, string, string, int, error) {
	return "", "", "", 0, ErrUnsupported
}
func (unsupported) EnsureNAT(context.Context, Names, netip.Prefix, string) (string, error) {
	return "", ErrUnsupported
}
func (unsupported) CreateVM(context.Context, Plan, string, string) (string, string, OwnedDisk, []OwnedDVD, error) {
	return "", "", OwnedDisk{}, nil, ErrUnsupported
}
func (unsupported) SetIntegration(context.Context, string, string, map[string]bool) error {
	return ErrUnsupported
}
func (unsupported) SetBindings(context.Context, Plan, int) (map[string]bool, error) {
	return nil, ErrUnsupported
}
func (unsupported) EnsureFirewall(context.Context, Plan, int) (string, error) {
	return "", ErrUnsupported
}
func (unsupported) SetHostNeighbor(context.Context, int, string, string, string, netip.Addr, string) error {
	return ErrUnsupported
}
func (unsupported) EnsureGuardian(context.Context) (string, error) { return "", ErrUnsupported }
func (unsupported) AddHyperVAdmin(context.Context, string) (bool, error) {
	return false, ErrUnsupported
}
func (unsupported) WriteAnswerISO(context.Context, string, []byte) error { return ErrUnsupported }
func (unsupported) BootKeys(context.Context, string, string) error       { return ErrUnsupported }
func (unsupported) EjectDVDs(context.Context, string, string, []OwnedDVD) error {
	return ErrUnsupported
}
func (unsupported) RegisterGuardTask(context.Context, Names, string, string) error {
	return ErrUnsupported
}
func (unsupported) StartGuardTask(context.Context, Names, string) error { return ErrUnsupported }
func (unsupported) Guest(context.Context, string, string, Cred, GuestScript, any, any) error {
	return ErrUnsupported
}
func (unsupported) UnregisterGuardTask(context.Context, Names, string) error { return ErrUnsupported }
func (unsupported) RemoveVM(context.Context, string, Plan) ([]string, error) {
	return nil, ErrUnsupported
}
func (unsupported) RemoveSwitch(context.Context, Plan) (string, error) { return "", ErrUnsupported }
func (unsupported) RemoveNAT(context.Context, Names, netip.Prefix, string) (string, error) {
	return "", ErrUnsupported
}
func (unsupported) RemoveFirewall(context.Context, Plan) (string, error) { return "", ErrUnsupported }
func (unsupported) RemoveGuardian(context.Context, string) error         { return ErrUnsupported }
func (unsupported) LinkLocal(context.Context, int) (netip.Addr, error) {
	return netip.Addr{}, ErrUnsupported
}

func (unsupported) Canary(context.Context, netip.Addr, uint16, netip.Prefix) (Canary, error) {
	return nil, ErrUnsupported
}
func (unsupported) Pktmon(context.Context, []PktFilter) (Capture, error) {
	return nil, ErrUnsupported
}
func (unsupported) SetL3(context.Context, Names, string, bool) error        { return ErrUnsupported }
func (unsupported) SetBinding(context.Context, int, string, bool) error     { return ErrUnsupported }
func (unsupported) AddAllow(context.Context, string, netip.Prefix) error    { return ErrUnsupported }
func (unsupported) RemoveAllow(context.Context, string, netip.Prefix) error { return ErrUnsupported }
func (unsupported) HVListener(context.Context, string, string, bool) (HVListener, error) {
	return nil, ErrUnsupported
}

func (unsupported) TCP(context.Context, netip.AddrPort, string) (bool, error) {
	return false, ErrUnsupported
}
func (unsupported) UDP(context.Context, netip.AddrPort, string, int) error { return ErrUnsupported }
func (unsupported) DNS(context.Context, netip.Addr, string) ([]netip.Addr, error) {
	return nil, ErrUnsupported
}
func (unsupported) Neighbor(context.Context, netip.Addr, string) error { return ErrUnsupported }
func (unsupported) HVSock(context.Context, string, string) (bool, error) {
	return false, ErrUnsupported
}
func (unsupported) Egress(context.Context) (netip.Addr, error) { return netip.Addr{}, ErrUnsupported }
func (unsupported) RawFrame(context.Context, uint16, string, []byte, int) error {
	return ErrUnsupported
}

func (unsupported) ClearProof(context.Context, Names, string) (ProofCleanup, error) {
	return ProofCleanup{}, ErrUnsupported
}
