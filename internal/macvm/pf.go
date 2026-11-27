package macvm

import (
	"bufio"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FixedDeny is every private, shared, special and multicast IPv4 range. pf
// denies them on top of softnet, which already drops them.
var FixedDeny = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
	"224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
}

// Hole is one gap the proof opens on purpose: the guest may reach Addr (or the
// gateway, when Gateway is set) on Port over TCP and UDP.
type Hole struct {
	Addr    netip.Addr
	Gateway bool
	Port    uint16
}

// target is the hole's address once the gateway is known.
func (h Hole) target(gw netip.Addr) netip.Addr {
	if h.Gateway {
		return gw
	}
	return h.Addr
}

// Rules is the pf anchor for the guest's bridge (L2). deny is the owner's
// public IPs. holes are the proof's gaps; production passes none.
//
// It is the lab's fence-pf.sh, proven red then green: IPv6 off both ways, the
// host's multicast and broadcast kept from the guest, DHCP to the gateway
// only, nothing else to the gateway, nothing to a private or own address, and
// the internet open.
func Rules(bridge string, gw netip.Addr, deny []netip.Addr, holes []Hole) (string, error) {
	if !IsVMNetBridge(bridge) {
		return "", fmt.Errorf("%q is not a vmnet bridge", bridge)
	}
	if !gw.Is4() {
		return "", fmt.Errorf("gateway %v is not IPv4", gw)
	}
	table := append([]string{}, FixedDeny...)
	for _, a := range deny {
		if !a.Is4() {
			return "", fmt.Errorf("deny address %v is not IPv4", a)
		}
		table = append(table, a.String())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table <cp_deny> const { %s }\n", strings.Join(table, " "))
	fmt.Fprintf(&b, "block drop in  quick log on %s inet6 all label \"cp-v6-in\"\n", bridge)
	fmt.Fprintf(&b, "block drop out quick log on %s inet6 all label \"cp-v6-out\"\n", bridge)
	fmt.Fprintf(&b, "block drop out quick on %s inet from any to { 224.0.0.0/4, 255.255.255.255 } label \"cp-mcast-out\"\n", bridge)
	fmt.Fprintf(&b, "pass in quick on %s inet proto udp from any port 68 to { %s, 255.255.255.255 } port 67 keep state label \"cp-dhcp\"\n", bridge, gw)
	for _, h := range holes {
		a := h.target(gw)
		if !a.Is4() || h.Port == 0 {
			return "", fmt.Errorf("bad proof hole %v:%d", a, h.Port)
		}
		fmt.Fprintf(&b, "pass in quick on %s inet proto { tcp, udp } from any to %s port %d keep state label \"cp-hole\"\n", bridge, a, h.Port)
	}
	fmt.Fprintf(&b, "block drop in quick log on %s inet from any to %s label \"cp-host-gw\"\n", bridge, gw)
	fmt.Fprintf(&b, "block drop in quick log on %s inet from any to <cp_deny> label \"cp-deny\"\n", bridge)
	fmt.Fprintf(&b, "pass in quick on %s inet from any to any keep state label \"cp-internet\"\n", bridge)
	return b.String(), nil
}

// CutRules is the anchor while the guest must have no network at all.
func CutRules(bridge string) string {
	return fmt.Sprintf("block drop quick on %s all label \"cp-cut\"\n", bridge)
}

var onBridge = regexp.MustCompile(` on bridge1[0-9][0-9] `)

// NormalizeRules turns `pfctl -a <anchor> -sr` into the form the guard
// compares: the bridge name masked (the daemon checks it apart), blank lines
// dropped.
func NormalizeRules(out string) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		lines = append(lines, onBridge.ReplaceAllString(l, " on BRIDGE "))
	}
	return strings.Join(lines, "\n")
}

// NormalizeTable sorts `pfctl -t cp_deny -T show`.
func NormalizeTable(out string) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// blockLabels are the fence's drop rules, whose packets the page counts.
var blockLabels = []string{"cp-host-gw", "cp-deny", "cp-v6-in", "cp-v6-out", "cp-mcast-out"}

// Blocked sums the fence's drop counters in `pfctl -a <anchor> -vsr` output.
// ok is false when none of the drop rules is loaded.
func Blocked(out string) (n uint64, ok bool) {
	c := RuleCounters(out)
	for _, l := range blockLabels {
		if v, has := c[l]; has {
			n += v
			ok = true
		}
	}
	return n, ok
}

// RuleCounters reads `pfctl -a <anchor> -vsr`: packets per label.
func RuleCounters(out string) map[string]uint64 {
	c := map[string]uint64{}
	label := ""
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		l := sc.Text()
		if i := strings.Index(l, `label "`); i >= 0 && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			rest := l[i+len(`label "`):]
			if j := strings.IndexByte(rest, '"'); j >= 0 {
				label = rest[:j]
			}
			continue
		}
		if label == "" {
			continue
		}
		// "  [ Evaluations: 12  Packets: 3  Bytes: 180  States: 0 ]"
		if i := strings.Index(l, "Packets:"); i >= 0 {
			f := strings.Fields(l[i+len("Packets:"):])
			if len(f) > 0 {
				if n, err := strconv.ParseUint(f[0], 10, 64); err == nil {
					c[label] += n
				}
			}
		}
	}
	return c
}

// SoftnetBlock is softnet's --block list (L1): the gateway (@host) and the
// owner's public IPs. Softnet already drops every private, shared, link-local
// and multicast IPv4 destination and all IPv6.
func SoftnetBlock(deny []netip.Addr) string {
	parts := []string{"@host"}
	for _, a := range deny {
		parts = append(parts, a.String()+"/32")
	}
	return strings.Join(parts, ",")
}

// SoftnetAllow is softnet's --allow list for the proof's holes: one stateful
// rule per hole and protocol, so the guest reaches the canary's port and no
// other.
func SoftnetAllow(holes []Hole) string {
	var parts []string
	for _, h := range holes {
		target := h.Addr.String() + "/32"
		if h.Gateway {
			target = "@host"
		}
		for _, proto := range []string{"tcp", "udp"} {
			parts = append(parts, fmt.Sprintf("out proto %s %s port %d", proto, target, h.Port))
		}
	}
	return strings.Join(parts, ",")
}

// ParseDeny reads deny.list: one IPv4 address per line, # comments.
func ParseDeny(s string) ([]netip.Addr, error) {
	var out []netip.Addr
	seen := map[netip.Addr]bool{}
	for _, l := range strings.Split(s, "\n") {
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = l[:i]
		}
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		a, err := netip.ParseAddr(l)
		if err != nil || !a.Is4() {
			return nil, fmt.Errorf("deny.list: %q is not an IPv4 address", l)
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out, nil
}

// FormatDeny writes deny.list.
func FormatDeny(deny []netip.Addr) string {
	var b strings.Builder
	b.WriteString("# The owner's public IPv4 addresses. The guest may never reach them.\n")
	for _, a := range deny {
		b.WriteString(a.String() + "\n")
	}
	return b.String()
}
