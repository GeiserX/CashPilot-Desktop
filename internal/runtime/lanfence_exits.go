package runtime

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/proxies"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The owner's other public IPs, dropped like this machine's own (web app/lan_fence.py,
// GeiserX/CashPilot#810).
//
// A port forward on another of the owner's internet lines answers an earner's connect
// like any connect from the internet, and the services behind it may trust the owner's
// own addresses. So the table drops every public IPv4 of the owner, one "egress-<ip>"
// rule each: this machine's own, the exits of the other boxes of the fleet (from the
// CashPilot server's heartbeat reply when this Desktop is paired, and from the workers
// that report to this Desktop when it is a hub).
//
// The helper gets them as FENCE_EXITS. FENCE_EXITS_COMPLETE=0 says the list may be
// short (paired, but the server has not answered since the app started; the hub's
// device list could not be read): the helper then keeps the drops the table already
// has, and a later complete list removes the ones that went.
//
// A proxied copy's tunnel can leave through a proxy or a Tailscale exit node on one of
// those lines. FENCE_TUNNELS gives each running tunnel its own exception, which the
// helper writes before the drops: UDP to every exit for a Tailscale tunnel (its exit
// node's endpoint is one of them, but only tailscaled knows which), TCP to the proxy's
// port for a SOCKS5 or HTTP one. The earner in a proxied copy shares the tunnel's
// namespace, where the tunnel's own firewall lets out only the tunnel's traffic, so the
// exception carries nothing of the earner's.

// maxFenceExits is the most exits one table drops, this machine's own included. The
// web worker's MAX_EXITS.
const maxFenceExits = 64

// fenceExits is what the helper is told about the owner's public IPs and the tunnels.
type fenceExits struct {
	// Own is this machine's public IPv4, "" when unknown.
	Own string
	// Others are the owner's other public IPv4s, Own left out.
	Others []string
	// Complete is false when Others may be short.
	Complete bool
	// Tunnels are the FENCE_TUNNELS entries: "src:udp" or "src:tcp:proxy_ip:port".
	Tunnels []string
}

// env is the helper's environment for these. The exits and tunnels go only to a
// filtered run: a reduced table holds no drops.
func (e fenceExits) env(filtered bool) []string {
	out := []string{"FENCE_EGRESS=" + e.Own}
	if !filtered {
		return out
	}
	complete := "1"
	if !e.Complete {
		complete = "0"
	}
	return append(out,
		"FENCE_EXITS="+strings.Join(e.Others, ","),
		"FENCE_EXITS_COMPLETE="+complete,
		"FENCE_TUNNELS="+strings.Join(e.Tunnels, ","),
	)
}

// ExitTargets keeps the public IPv4 addresses among values, deduplicated and sorted.
// A private, CGNAT, IPv6 or malformed value is left out: the fence drops the private
// ranges already, and all IPv6 from the bridge.
func ExitTargets(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		ip, err := netip.ParseAddr(proxies.PublicIP(v))
		if err != nil || !ip.Is4() || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		out = append(out, ip.String())
	}
	sort.Slice(out, func(i, j int) bool {
		return netip.MustParseAddr(out[i]).Less(netip.MustParseAddr(out[j]))
	})
	return out
}

// otherExits is the owner's other public IPs without own, capped so the whole list
// stays within maxFenceExits.
func otherExits(values []string, own string) []string {
	var out []string
	for _, ip := range ExitTargets(values) {
		if ip != own {
			out = append(out, ip)
		}
	}
	if len(out) > maxFenceExits-1 {
		out = out[:maxFenceExits-1]
	}
	return out
}

// exitsFor is what the next helper run drops, read now. The caller holds f.mu.
func (f *fence) exitsFor(ctx context.Context, cli fenceAPI) fenceExits {
	e := fenceExits{Own: f.egressIP(ctx), Complete: true}
	if f.otherExits != nil {
		var values []string
		values, e.Complete = f.otherExits()
		e.Others = otherExits(values, e.Own)
	}
	e.Tunnels = fenceTunnels(ctx, cli)
	return e
}

// fenceTunnels lists each running tunnel sidecar on the fenced network as a
// FENCE_TUNNELS entry. A tunnel whose settings cannot be read gets no entry, so it
// keeps the drops: an exception the fence is not sure of is never written.
func fenceTunnels(ctx context.Context, cli fenceAPI) []string {
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{
		Filters: make(client.Filters).Add("label", LabelManaged+"=true", LabelRuntime+"="+desktopRuntime, LabelRole+"="+RoleTunnel),
	})
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range list.Items {
		// Filtered by the daemon already; checked again so a runtime that ignores a
		// filter cannot give an earner a tunnel's exception.
		if !IsDesktopContainer(c.Labels) || c.Labels[LabelRole] != RoleTunnel || c.State != container.StateRunning {
			continue
		}
		read := func(name string) string { return readTunnelFile(ctx, cli, c.ID, name) }
		if entry := tunnelEntry(c, read); entry != "" {
			out = append(out, entry)
		}
	}
	sort.Strings(out)
	return out
}

// tunnelEntry is one tunnel's FENCE_TUNNELS entry, "" for none: its address on the
// fenced network, and how it reaches its proxy or exit node.
func tunnelEntry(c container.Summary, read func(name string) string) string {
	if c.NetworkSettings == nil || c.NetworkSettings.Networks[FenceNetwork] == nil {
		return ""
	}
	src := c.NetworkSettings.Networks[FenceNetwork].IPAddress
	if !src.Is4() {
		return ""
	}
	switch read("kind") {
	case "tailscale":
		return src.String() + ":udp"
	case "socks5", "http":
		ip, err := netip.ParseAddr(read("proxy_ip"))
		port, perr := strconv.Atoi(read("proxy_port"))
		if err != nil || !ip.Is4() || perr != nil || port < 1 || port > 65535 {
			return ""
		}
		return fmt.Sprintf("%s:tcp:%s:%d", src, ip, port)
	}
	return ""
}

// readTunnelFile is one file of a tunnel's secret volume, "" when it cannot be read.
// Through the archive API, as the web worker reads it: no process runs in the tunnel.
func readTunnelFile(ctx context.Context, cli fenceAPI, id, name string) string {
	res, err := cli.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: tunnelSecretDir + "/" + name})
	if err != nil {
		return ""
	}
	defer res.Content.Close()
	tr := tar.NewReader(res.Content)
	if _, err := tr.Next(); err != nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(tr, 4<<10))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// SetOtherExits wires in the owner's other public IPs: the exits of the other boxes of
// the fleet. read returns them and whether the list is complete. Call it before the
// first deploy.
func (p *DockerProvider) SetOtherExits(read func() (ips []string, complete bool)) {
	p.fence.otherExits = read
}

// EgressIP is this machine's public IPv4, "" when unknown. It shares the fence's
// cache, so a caller asks the echo service no more often than the fence does.
func (p *DockerProvider) EgressIP(ctx context.Context) string {
	return p.fence.egressIP(ctx)
}
