//go:build !windows

package winvm

import (
	"context"
	"net/netip"
)

// NewHost is a host without Hyper-V: every call returns ErrUnsupported.
func NewHost() Host { return unsupported{} }

// unsupported implements every seam interface with ErrUnsupported.
type unsupported struct{}

var (
	_ Builder    = unsupported{}
	_ Prober     = unsupported{}
	_ GuestProbe = unsupported{}
)

func (unsupported) ReadLive(context.Context, Names, string) (LiveState, error) {
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

func (unsupported) Disconnect(context.Context, string) error              { return ErrUnsupported }
func (unsupported) Connect(context.Context, string, string) error         { return ErrUnsupported }
func (unsupported) Start(context.Context, string) error                   { return ErrUnsupported }
func (unsupported) AddDeny(context.Context, string, []netip.Prefix) error { return ErrUnsupported }
func (unsupported) RemoveDeny(context.Context, string, []netip.Prefix) error {
	return ErrUnsupported
}

func (unsupported) EnsureSwitch(context.Context, Names, netip.Prefix) (string, string, error) {
	return "", "", ErrUnsupported
}
func (unsupported) EnsureNAT(context.Context, Names, netip.Prefix) error { return ErrUnsupported }
func (unsupported) CreateVM(context.Context, Plan, string, string) (string, string, error) {
	return "", "", ErrUnsupported
}
func (unsupported) SetIntegration(context.Context, string, map[string]bool) error {
	return ErrUnsupported
}
func (unsupported) SetBindings(context.Context, int, []string) error { return ErrUnsupported }
func (unsupported) EnsureFirewall(context.Context, Names, int) error { return ErrUnsupported }
func (unsupported) SetHostNeighbor(context.Context, int, netip.Addr, string) error {
	return ErrUnsupported
}
func (unsupported) SetEnhancedSession(context.Context, bool) (bool, error) {
	return false, ErrUnsupported
}
func (unsupported) EnsureGuardian(context.Context) (bool, error) { return false, ErrUnsupported }
func (unsupported) RegisterGuardTask(context.Context, Names, string) error {
	return ErrUnsupported
}
func (unsupported) Guest(context.Context, string, Cred, GuestScript, any) error {
	return ErrUnsupported
}
func (unsupported) Remove(context.Context, Plan) ([]string, error) { return nil, ErrUnsupported }

func (unsupported) Canary(context.Context, netip.Addr, uint16, string) (Canary, error) {
	return nil, ErrUnsupported
}
func (unsupported) Pktmon(context.Context, []PktFilter) (Capture, error) {
	return nil, ErrUnsupported
}
func (unsupported) SetL3(context.Context, Names, bool) error                { return ErrUnsupported }
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
