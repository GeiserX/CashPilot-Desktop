package macvm

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// Bridge is one vmnet bridge (bridge1NN) as ifconfig prints it.
type Bridge struct {
	Name    string
	Members []string // the member ports, e.g. vmenet0
	// Cache maps a learned MAC (normalized, see NormalizeMAC) to the member it
	// was learned on.
	Cache map[string]string
	// Addr is the bridge's first IPv4 address (the guest's gateway) and Prefix
	// its network.
	Addr   netip.Addr
	Prefix netip.Prefix
}

// vmnetBridge matches the bridges vmnet makes. bridge0 is Thunderbolt's.
var vmnetBridge = regexp.MustCompile(`^bridge1[0-9][0-9]$`)

// IsVMNetBridge is whether name is a vmnet bridge.
func IsVMNetBridge(name string) bool { return vmnetBridge.MatchString(name) }

// ParseBridge reads `ifconfig <name>`.
func ParseBridge(name, out string) Bridge {
	b := Bridge{Name: name, Cache: map[string]string{}}
	inCache := false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "member:" && len(f) > 1:
			b.Members = append(b.Members, f[1])
			inCache = false
		case f[0] == "Address" && len(f) > 1 && f[1] == "cache:":
			inCache = true
		case f[0] == "inet" && len(f) >= 4 && f[2] == "netmask" && !b.Addr.IsValid():
			a, err := netip.ParseAddr(f[1])
			bits, ok := maskBits(f[3])
			if err == nil && ok {
				b.Addr = a
				b.Prefix = netip.PrefixFrom(a, bits).Masked()
			}
			inCache = false
		case inCache && len(f) >= 3 && strings.Count(f[0], ":") == 5:
			if mac, ok := NormalizeMAC(f[0]); ok {
				b.Cache[mac] = f[2]
			}
		default:
			inCache = false
		}
	}
	return b
}

// HasMember is whether port is a member of the bridge.
func (b Bridge) HasMember(port string) bool {
	for _, m := range b.Members {
		if m == port {
			return true
		}
	}
	return false
}

// maskBits reads ifconfig's 0xffffff00 netmask.
func maskBits(hexMask string) (int, bool) {
	v, err := strconv.ParseUint(strings.TrimPrefix(hexMask, "0x"), 16, 32)
	if err != nil {
		return 0, false
	}
	n := 0
	seenZero := false
	for i := 31; i >= 0; i-- {
		if v&(1<<uint(i)) != 0 {
			if seenZero {
				return 0, false
			}
			n++
		} else {
			seenZero = true
		}
	}
	return n, true
}

// NormalizeMAC turns any MAC spelling (ifconfig drops leading zeros, Tart
// writes two digits) into lower-case two-digit form.
func NormalizeMAC(s string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 6 {
		return "", false
	}
	out := make([]string, 6)
	for i, p := range parts {
		if len(p) == 0 || len(p) > 2 {
			return "", false
		}
		if len(p) == 1 {
			p = "0" + p
		}
		if _, err := hex.DecodeString(p); err != nil {
			return "", false
		}
		out[i] = strings.ToLower(p)
	}
	return strings.Join(out, ":"), true
}

// GuestPort finds the bridge and member that carry mac.
func GuestPort(bridges []Bridge, mac string) (Bridge, string, bool) {
	m, ok := NormalizeMAC(mac)
	if !ok {
		return Bridge{}, "", false
	}
	for _, b := range bridges {
		if p, ok := b.Cache[m]; ok && strings.HasPrefix(p, "vmenet") {
			return b, p, true
		}
	}
	return Bridge{}, "", false
}

// MemberSet is "bridge port" for every vmenet member, to spot the one a new
// start adds.
func MemberSet(bridges []Bridge) map[string]bool {
	s := map[string]bool{}
	for _, b := range bridges {
		for _, m := range b.Members {
			if strings.HasPrefix(m, "vmenet") {
				s[b.Name+" "+m] = true
			}
		}
	}
	return s
}

// NewMember is the one member in after that is not in before.
func NewMember(before, after map[string]bool) (bridge, port string, ok bool) {
	var found []string
	for k := range after {
		if !before[k] {
			found = append(found, k)
		}
	}
	if len(found) != 1 {
		return "", "", false
	}
	b, p, _ := strings.Cut(found[0], " ")
	return b, p, true
}

// DefaultRoute reads `route -n get default`: the router and its interface.
func DefaultRoute(out string) (gateway netip.Addr, iface string, err error) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "gateway":
			if a, e := netip.ParseAddr(v); e == nil {
				gateway = a
			}
		case "interface":
			iface = v
		}
	}
	if iface == "" {
		return netip.Addr{}, "", fmt.Errorf("no default route")
	}
	return gateway, iface, nil
}
