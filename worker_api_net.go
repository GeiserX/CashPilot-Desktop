package main

import (
	"net"
	"net/netip"
	"slices"
	"strings"
)

// Where the server-commands API listens (worker_api.go). It listens on this
// computer's private LAN and Tailscale addresses only: RFC 1918, the CGNAT block
// Tailscale uses (100.64.0.0/10) and IPv6 ULA (fc00::/7). Never 0.0.0.0, never a
// public address, and never loopback alone: the CashPilot server refuses a
// loopback worker URL (its SSRF guard), and a loopback listener would serve only
// this computer.

// workerAPIPort is the port the API listens on, next to the hub's 8085.
const workerAPIPort = 8086

// privateNets are the source and listen ranges the API accepts.
var privateNets = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fc00::/7"),
}

// privateSubnets is privateNets as text, for the Windows Firewall rule and the UI.
func privateSubnets() []string {
	out := make([]string, 0, len(privateNets))
	for _, p := range privateNets {
		out = append(out, p.String())
	}
	return out
}

// isPrivateAddr reports whether ip is in one of privateNets. An IPv4-mapped IPv6
// address is read as its IPv4 address, so it cannot walk around the check.
func isPrivateAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return false
	}
	for _, p := range privateNets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// skippedInterface reports whether an interface is a bridge the earners' containers
// or local VMs sit on. Their addresses are private, but listening there would put the
// API next to the earners, which are code we do not trust. The key still guards the
// API; this keeps it off their network as well.
//
// A Hyper-V host vNIC is named "vEthernet (<switch>)". Most are not skipped: on a PC
// with an External switch, the host's own LAN address sits on that switch's vNIC,
// and the server reaches this computer there. Only the switches that carry VMs are
// skipped: WSL's, the Default Switch, Docker's and Windows containers' NAT switches,
// and the Secure VM's own switch and its lab copies (internal/winvm/names.go), the
// host side of the fenced earner VM.
func skippedInterface(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "vethernet (") {
		for _, prefix := range []string{"vethernet (wsl", "vethernet (default switch)", "vethernet (dockernat)", "vethernet (nat)", "vethernet (cashpilot-vm)", "vethernet (cpd-svm-"} {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
		return false
	}
	for _, prefix := range []string{"docker", "br-", "veth", "cni", "podman", "virbr", "vmnet", "bridge1"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// ifaceAddr is one address of one interface, the input listenAddrs filters.
type ifaceAddr struct {
	Iface string
	Up    bool
	Loop  bool
	Addr  netip.Addr
}

// listenAddrs picks the addresses the API listens on from the machine's interface
// addresses: up, not loopback, not a container bridge, and private. Sorted, so two
// scans of an unchanged machine compare equal.
func listenAddrs(in []ifaceAddr) []netip.Addr {
	var out []netip.Addr
	for _, a := range in {
		if !a.Up || a.Loop || skippedInterface(a.Iface) {
			continue
		}
		ip := a.Addr.Unmap()
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || !isPrivateAddr(ip) {
			continue
		}
		if !slices.Contains(out, ip) {
			out = append(out, ip)
		}
	}
	slices.SortFunc(out, func(a, b netip.Addr) int { return a.Compare(b) })
	return out
}

// machineAddrs reads every interface address of this machine. A var so tests can
// give the API the addresses they need.
var machineAddrs = func() []ifaceAddr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []ifaceAddr
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipNet.IP)
			if !ok {
				continue
			}
			out = append(out, ifaceAddr{
				Iface: iface.Name,
				Up:    iface.Flags&net.FlagUp != 0,
				Loop:  iface.Flags&net.FlagLoopback != 0,
				Addr:  ip.Unmap(),
			})
		}
	}
	return out
}

// routeAddr is the local address this machine would use to reach host, read without
// sending anything (a UDP "connect" only picks a route). The server most likely
// reaches this computer at that address. A var so tests can pick it.
var routeAddr = func(host string) netip.Addr {
	if host == "" {
		return netip.Addr{}
	}
	conn, err := net.Dial("udp", net.JoinHostPort(host, "80"))
	if err != nil {
		return netip.Addr{}
	}
	defer conn.Close()
	if udp, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		if ip, ok := netip.AddrFromSlice(udp.IP); ok {
			return ip.Unmap()
		}
	}
	return netip.Addr{}
}

// pickURLAddr is the address the heartbeat reports from the ones the API listens on:
// the one this machine reaches the server from, else the first private LAN IPv4, then
// a tailnet address, then any.
func pickURLAddr(listening []netip.Addr, toServer netip.Addr) netip.Addr {
	if len(listening) == 0 {
		return netip.Addr{}
	}
	if toServer.IsValid() && slices.Contains(listening, toServer.Unmap()) {
		return toServer.Unmap()
	}
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	for _, ip := range listening {
		if ip.Is4() && !cgnat.Contains(ip) {
			return ip
		}
	}
	for _, ip := range listening {
		if ip.Is4() {
			return ip
		}
	}
	return listening[0]
}
