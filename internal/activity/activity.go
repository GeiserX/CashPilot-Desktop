// Package activity shows what each earner container does on the network: how
// many connections it opens and how many fail, which ports and networks it talks
// to, and whether it reaches into the owner's own network or sends mail. It is
// the Desktop port of the web repository's Activity page (app/activity.py,
// app/activity_ui.py, docs/activity.md in GeiserX/CashPilot), with one machine
// instead of a fleet.
//
// It is off by default. When the owner switches it on, every 5 minutes one
// short-lived helper container per network namespace reads the kernel's socket
// tables and TCP/UDP counters from /proc/net (or, for an earner on the host
// network, the sockets of the earner's own cgroup through sock_diag). It never
// reads traffic contents. The helper script is the web repository's
// app/procnet.py, vendored byte for byte, so there is one parser and one set of
// classification rules.
package activity

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// WindowSeconds is the length of one window; rounds start on these wall-clock boundaries.
	WindowSeconds = 300
	// ProbeSnapshots and ProbeGap are what the helper is asked for: 5 snapshots 2 s apart.
	ProbeSnapshots = 5
	ProbeGap       = 2
	// ProbeTimeout bounds one helper's whole life.
	ProbeTimeout = 30 * time.Second
	// MaxParallel helpers run at once, and one round has RoundBudget in all.
	MaxParallel = 4
	RoundBudget = 200 * time.Second
	// MaxDeltaSeconds: a counter change over a longer gap is not one window's worth.
	MaxDeltaSeconds = 2 * 300.0

	// ProbeLabel marks every helper, so a sweep can find and remove leftovers.
	ProbeLabel  = "cashpilot.probe"
	ProbeKind   = "activity"
	WorkerLabel = "cashpilot.probe.worker"
	ProbePrefix = "cashpilot-activity-probe-"
)

// PlainRuntimes are the runtime names whose containers keep their sockets in the
// kernel's network namespace. PlainBinaries are the runtime binaries that do so
// under any registered name.
var (
	PlainRuntimes = []string{"", "runc", "io.containerd.runc.v2", "crun"}
	PlainBinaries = []string{"runc", "crun", "nvidia-container-runtime", "youki"}
)

// DeltaKeys are the counters a window reports as changes since the last one.
var DeltaKeys = []string{"opens", "passive_opens", "attempt_fails", "resets", "udp_in", "udp_out"}

// Network is one network a container is attached to, as docker inspect shows it.
type Network struct {
	ID            string
	Gateway       string
	IPAddress     string
	IPPrefixLen   int
	IPv6Gateway   string
	GlobalIPv6    string
	IPv6PrefixLen int
}

// Container is the part of docker inspect a round needs.
type Container struct {
	ID          string
	Name        string
	Labels      map[string]string
	NetworkMode string
	Runtime     string
	DNS         []string
	Networks    []Network
}

// NetworkInfo is a network's driver and the addresses of the earners on it.
type NetworkInfo struct {
	Driver    string
	EarnerIPs []string
}

// Group is one network namespace to probe and the earners that live in it.
// Kind "netns" joins the namespace; kind "cgroup" is an earner on the host
// network, whose sockets are matched by its cgroup instead.
type Group struct {
	Owner       Container
	Members     []string
	Mode        string
	Kind        string
	OwnerEarner bool
}

// Sample is one container's row for one window. Nil numbers are unknown, never zero.
type Sample struct {
	Container    string   `json:"container"`
	Members      []string `json:"members"`
	NetworkMode  string   `json:"network_mode"`
	Status       string   `json:"status"`
	Reason       string   `json:"reason"`
	Opens        *int64   `json:"opens"`
	PassiveOpens *int64   `json:"passive_opens"`
	AttemptFails *int64   `json:"attempt_fails"`
	Resets       *int64   `json:"resets"`
	UDPIn        *int64   `json:"udp_in"`
	UDPOut       *int64   `json:"udp_out"`
	DeltaSeconds *float64 `json:"delta_seconds"`
	EstMax       *int64   `json:"est_max"`
	SynSentMax   *int64   `json:"syn_sent_max"`
	UDPBound     *int64   `json:"udp_bound"`
	ListenPorts  []int    `json:"listen_ports"`
	RemotesTotal *int64   `json:"remotes_total"`
	Truncated    bool     `json:"truncated"`
	Out          []Dest   `json:"out"`
	In           []Dest   `json:"in"`
}

// Dest is one sampled destination. Outbound rows carry Proto and States.
type Dest struct {
	Proto  string         `json:"proto,omitempty"`
	IP     string         `json:"ip"`
	Port   int            `json:"port"`
	Class  string         `json:"class"`
	Seen   int            `json:"seen"`
	States map[string]int `json:"states,omitempty"`
}

// Window is one round's result.
type Window struct {
	WindowEnd      int64    `json:"window_end"`
	WorkerStatus   string   `json:"worker_status"`
	LeftoverProbes []string `json:"leftover_probes"`
	Samples        []Sample `json:"samples"`
}

func baseSample(name string, members []string, mode, status, reason string) Sample {
	m := append([]string(nil), members...)
	sort.Strings(m)
	return Sample{Container: name, Members: m, NetworkMode: mode, Status: status, Reason: reason, ListenPorts: []int{}, Out: []Dest{}, In: []Dest{}}
}

// WindowEnd is the start of the window that contains now.
func WindowEnd(now time.Time) int64 {
	return now.Unix() / WindowSeconds * WindowSeconds
}

// JoinableRuntimes is every runtime whose containers keep their sockets in the
// kernel's network namespace: runc and crun under any name, and gVisor only
// with --network=host. runtimes maps a registered name to (path, runtimeArgs).
func JoinableRuntimes(runtimes map[string]RuntimeSpec) map[string]bool {
	found := map[string]bool{}
	for _, r := range PlainRuntimes {
		found[r] = true
	}
	for name, spec := range runtimes {
		path := spec.Path
		if path == "" {
			path = name
		}
		binary := path[strings.LastIndex(path, "/")+1:]
		hostnet := false
		for _, a := range spec.Args {
			if a == "--network=host" {
				hostnet = true
			}
		}
		for _, b := range PlainBinaries {
			if binary == b {
				found[name] = true
			}
		}
		if binary == "runsc" && hostnet {
			found[name] = true
		}
	}
	return found
}

// RuntimeSpec is one runtime docker info lists.
type RuntimeSpec struct {
	Path string
	Args []string
}

// Lookup finds a container by id or name; ok is false when it is gone.
type Lookup func(idOrName string) (Container, bool)

// Plan groups the running earners by the namespace they live in, and returns
// the samples of earners that cannot be probed.
func Plan(earners []Container, lookup Lookup, joinable map[string]bool) (map[string]*Group, []Sample) {
	groups := map[string]*Group{}
	var fixed []Sample
	names := map[string]bool{}
	for _, c := range earners {
		names[c.Name] = true
	}
	for _, c := range earners {
		if _, probe := c.Labels[ProbeLabel]; probe {
			continue
		}
		mode := c.NetworkMode
		if mode == "" || mode == "default" {
			mode = "bridge"
		}
		if mode == "none" {
			fixed = append(fixed, baseSample(c.Name, []string{c.Name}, mode, "no_network", ""))
			continue
		}
		if !joinable[c.Runtime] {
			fixed = append(fixed, baseSample(c.Name, []string{c.Name}, mode, "sandboxed", ""))
			continue
		}
		if mode == "host" {
			// The host's sockets are everyone's: this earner's are told apart by its cgroup.
			groups[c.ID] = &Group{Owner: c, Members: []string{c.Name}, Mode: mode, Kind: "cgroup", OwnerEarner: true}
			continue
		}
		owner := c
		if strings.HasPrefix(mode, "container:") {
			var ok bool
			owner, ok = lookup(strings.TrimPrefix(mode, "container:"))
			for i := 0; ok && strings.HasPrefix(owner.NetworkMode, "container:"); i++ {
				if i >= 8 {
					ok = false
					break
				}
				owner, ok = lookup(strings.TrimPrefix(owner.NetworkMode, "container:"))
			}
			if !ok {
				fixed = append(fixed, baseSample(c.Name, []string{c.Name}, mode, "probe_failed", "owner_missing"))
				continue
			}
			if owner.NetworkMode == "host" {
				// Joined to a container on the host network: still the host's
				// sockets, matched by this member's own cgroup.
				groups[c.ID] = &Group{Owner: c, Members: []string{c.Name}, Mode: mode, Kind: "cgroup", OwnerEarner: true}
				continue
			}
			if !joinable[owner.Runtime] {
				fixed = append(fixed, baseSample(c.Name, []string{c.Name}, mode, "sandboxed", ""))
				continue
			}
		}
		g, ok := groups[owner.ID]
		if !ok {
			g = &Group{Owner: owner, Mode: mode, Kind: "netns", OwnerEarner: names[owner.Name]}
			groups[owner.ID] = g
		}
		g.Members = append(g.Members, c.Name)
		if owner.ID == c.ID {
			g.Mode = mode
		}
	}
	return groups, fixed
}

// NetworkContext is what the helper's classify needs: gateways, resolvers,
// subnets with their drivers, and the addresses of the earners beside it.
type NetworkContext struct {
	Gateways []string    `json:"gateways"`
	DNS      []string    `json:"dns"`
	Subnets  [][2]string `json:"subnets"`
	Peers    []string    `json:"peers"`
}

// BuildContext is the network context of a namespace owner. networks caches
// one inspect per network for the round. A host-network earner has no bridge:
// no gateways, subnets or peers.
func BuildContext(owner Container, kind string, networks map[string]NetworkInfo, inspect func(id string) NetworkInfo) NetworkContext {
	ctx := NetworkContext{Gateways: []string{}, DNS: append([]string{}, owner.DNS...), Subnets: [][2]string{}, Peers: []string{}}
	if kind == "cgroup" {
		return ctx
	}
	gateways, peers := map[string]bool{}, map[string]bool{}
	for _, n := range owner.Networks {
		info, ok := networks[n.ID]
		if !ok && n.ID != "" {
			info = inspect(n.ID)
			networks[n.ID] = info
		}
		for _, ip := range info.EarnerIPs {
			peers[ip] = true
		}
		for _, f := range []struct {
			gw, ip string
			plen   int
		}{{n.Gateway, n.IPAddress, n.IPPrefixLen}, {n.IPv6Gateway, n.GlobalIPv6, n.IPv6PrefixLen}} {
			if f.gw != "" {
				gateways[f.gw] = true
			}
			if f.ip != "" {
				peers[f.ip] = true
			}
			if f.ip != "" && f.plen > 0 {
				ctx.Subnets = append(ctx.Subnets, [2]string{f.ip + "/" + strconv.Itoa(f.plen), info.Driver})
			}
		}
	}
	ctx.Gateways = sortedKeys(gateways)
	ctx.Peers = sortedKeys(peers)
	return ctx
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// counterState is one namespace's last counters, so the next window can report a change.
type counterState struct {
	netns string
	snmp  [6]int64
	at    float64
}

// Deltas holds the counter state between rounds, one entry per namespace.
type Deltas struct {
	mu   sync.Mutex
	prev map[string]counterState
}

// NewDeltas is an empty state.
func NewDeltas() *Deltas { return &Deltas{prev: map[string]counterState{}} }

// Reset forgets everything, so a switch turned back on never reports a stale delta.
func (d *Deltas) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prev = map[string]counterState{}
}

// Forget drops one namespace.
func (d *Deltas) Forget(owner string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.prev, owner)
}

// Keep drops every namespace not in live.
func (d *Deltas) Keep(live map[string]bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k := range d.prev {
		if !live[k] {
			delete(d.prev, k)
		}
	}
}

// Apply sets the delta fields of s from this reading and stores it for the next.
// A delta exists only for the same namespace, with no counter going backwards,
// and a last reading at most MaxDeltaSeconds old. counters is nil when the
// helper read none (ActiveOpens, PassiveOpens, AttemptFails, EstabResets,
// UDP in, UDP out; -1 for a UDP counter it could not read).
func (d *Deltas) Apply(owner, netns string, counters *[6]int64, now float64, s *Sample) {
	d.mu.Lock()
	defer d.mu.Unlock()
	prev, ok := d.prev[owner]
	if ok && counters != nil && netns != "" && prev.netns == netns && now-prev.at >= 0 && now-prev.at <= MaxDeltaSeconds {
		back := false
		for i := 0; i < 4; i++ {
			if counters[i] < prev.snmp[i] {
				back = true
			}
		}
		if !back {
			fields := []**int64{&s.Opens, &s.PassiveOpens, &s.AttemptFails, &s.Resets, &s.UDPIn, &s.UDPOut}
			for i, f := range fields {
				c, o := counters[i], prev.snmp[i]
				if i >= 4 && (c < 0 || o < 0 || c < o) {
					continue
				}
				v := c - o
				*f = &v
			}
			secs := float64(int64((now-prev.at)*10+0.5)) / 10
			s.DeltaSeconds = &secs
		}
	}
	if counters != nil && netns != "" {
		d.prev[owner] = counterState{netns: netns, snmp: *counters, at: now}
	} else {
		delete(d.prev, owner)
	}
}
