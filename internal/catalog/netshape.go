package catalog

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// The ports a service publishes from its env rather than from static ports entries:
// docker.ui_port and docker.udp_port_range. The web catalog's rules (app/catalog.py
// ui_bindings, expand_udp_range, MAX_UDP_PORTS), so a value one app accepts the
// other publishes the same way.

// MaxUDPPorts is the most UDP ports one udp_port_range may publish, the web catalog's
// ceiling. Each published port is a rule and, with Docker's userland proxy, a process
// on the host, and each one slows the container's start: measured on a live node,
// 300 ports start in about 12 s and 1,000 in about 51 s. An entry's own max_ports
// (UDPPortRange.Cap) is what a deploy enforces; this only bounds it.
const MaxUDPPorts = 300

// UIPort is docker.ui_port: Port is published on each address the Env var lists.
type UIPort struct {
	Env  string `json:"env" yaml:"env"`
	Port int    `json:"port" yaml:"port"`
}

// UDPPortRange is docker.udp_port_range: the start:end range the Env var holds is
// published 1:1, at most MaxPorts ports (0 means MaxUDPPorts).
type UDPPortRange struct {
	Env      string `json:"env" yaml:"env"`
	MaxPorts int    `json:"maxPorts,omitempty" yaml:"max_ports"`
}

// Cap is the most ports this range may publish: MaxPorts within 1..MaxUDPPorts.
func (r UDPPortRange) Cap() int {
	if r.MaxPorts < 1 || r.MaxPorts > MaxUDPPorts {
		return MaxUDPPorts
	}
	return r.MaxPorts
}

// PublishesPorts reports whether the container publishes any port on the host:
// static ports, a WebUI port or a UDP range. Two copies cannot share one, and a
// proxied copy cannot publish one.
func (d DockerConfig) PublishesPorts() bool {
	return len(d.Ports) > 0 || d.UIPort != nil || d.UDPPortRange != nil
}

// UIBindings is each IPv4 address in a comma-separated list, deduplicated, with
// "localhost" read as 127.0.0.1 (Docker binds addresses, not names). An empty list
// is 127.0.0.1, the catalog's default. Anything that is not an IPv4 address is an
// error: published as is, it would fail the container's start.
func UIBindings(value string) ([]netip.Addr, error) {
	var found []netip.Addr
	for _, raw := range strings.Split(value, ",") {
		address := strings.TrimSpace(raw)
		if address == "" {
			continue
		}
		if address == "localhost" {
			address = "127.0.0.1"
		}
		addr, err := netip.ParseAddr(address)
		if err != nil || !addr.Is4() {
			return nil, fmt.Errorf("%q is not an IPv4 address", address)
		}
		dup := false
		for _, f := range found {
			dup = dup || f == addr
		}
		if !dup {
			found = append(found, addr)
		}
	}
	if len(found) == 0 {
		found = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	}
	return found, nil
}

// ExpandUDPRange is the ports of "start:end", in order. A blank value publishes
// nothing. A value that is not a well-ordered range, or one wider than limit, is an
// error: a node can only use a forwarded range when each port reaches the same port
// in the container, so nothing short of the whole range is published.
func ExpandUDPRange(value string, limit int) ([]int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if !IsPortRange(value) {
		return nil, fmt.Errorf("%q is not a port range start:end with start <= end <= 65535", value)
	}
	start, end, _ := strings.Cut(value, ":")
	first, _ := strconv.Atoi(start)
	last, _ := strconv.Atoi(end)
	if n := last - first + 1; n > limit {
		return nil, fmt.Errorf("%q spans %d ports; at most %d can be published, since every published port "+
			"slows the container's start. Choose a range of %d ports or fewer, such as %d:%d, and forward the "+
			"same range on your router", value, n, limit, limit, first, min(first+limit-1, 65535))
	}
	ports := make([]int, 0, last-first+1)
	for p := first; p <= last; p++ {
		ports = append(ports, p)
	}
	return ports, nil
}

// CheckPublishedValues reports whether env (the values the container would get)
// publishes cleanly through ui_port and udp_port_range: the deploy validation asks it
// so a bad address or an oversized range is refused before anything is touched.
func (d DockerConfig) CheckPublishedValues(env map[string]string) error {
	if d.UIPort != nil {
		if _, err := UIBindings(env[d.UIPort.Env]); err != nil {
			return fmt.Errorf("%s: %w", d.UIPort.Env, err)
		}
	}
	if d.UDPPortRange != nil {
		if _, err := ExpandUDPRange(env[d.UDPPortRange.Env], d.UDPPortRange.Cap()); err != nil {
			return fmt.Errorf("%s: %w", d.UDPPortRange.Env, err)
		}
	}
	return nil
}
