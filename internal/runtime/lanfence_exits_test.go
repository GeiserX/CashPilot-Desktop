package runtime

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// envValue is the value of key in env, and whether it is there.
func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// THE RULE (web GeiserX/CashPilot#810): the table drops every public IPv4 of the
// owner, not only this machine's. The helper gets the others as FENCE_EXITS, without
// this machine's own and without anything that is not a public IPv4, and is told
// when that list may be short so it keeps the drops it has.
func TestFenceDropsTheOwnersOtherExits(t *testing.T) {
	others, complete := []string{"198.51.100.4", "203.0.113.7", "192.168.1.9", "198.51.100.4", "2001:db8::1", "nonsense", "192.0.2.10"}, true
	fake := &fakeDocker{}
	fake.start(t)
	p := &DockerProvider{}
	p.SetEgressLookup(func(context.Context) string { return "203.0.113.7" })
	p.SetOtherExits(func() ([]string, bool) { return others, complete })

	p.ReconcileFence(context.Background())
	env := helperEnv(t, fake)
	for key, want := range map[string]string{
		"FENCE_EGRESS":         "203.0.113.7",
		"FENCE_EXITS":          "192.0.2.10,198.51.100.4",
		"FENCE_EXITS_COMPLETE": "1",
		"FENCE_TUNNELS":        "",
	} {
		if got, ok := envValue(env, key); !ok || got != want {
			t.Errorf("%s = %q (set %v), want %q", key, got, ok, want)
		}
	}

	// Paired, and the server has not answered yet: the helper keeps the drops.
	complete = false
	p.ReconcileFence(context.Background())
	if got, _ := envValue(helperEnv(t, fake), "FENCE_EXITS_COMPLETE"); got != "0" {
		t.Errorf("an incomplete list went out as complete: %q", got)
	}
}

// A machine with no fleet sends an empty, complete list: the helper then removes the
// drops of lines that left.
func TestFenceWithNoFleetSendsAnEmptyCompleteList(t *testing.T) {
	fake := &fakeDocker{}
	fake.start(t)
	(&DockerProvider{}).ReconcileFence(context.Background())
	env := helperEnv(t, fake)
	if got, ok := envValue(env, "FENCE_EXITS"); !ok || got != "" {
		t.Errorf("FENCE_EXITS = %q (set %v)", got, ok)
	}
	if got, _ := envValue(env, "FENCE_EXITS_COMPLETE"); got != "1" {
		t.Errorf("FENCE_EXITS_COMPLETE = %q", got)
	}
}

func TestCanceledEgressLookupDoesNotConsumeTheRefresh(t *testing.T) {
	refresh, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fence{lookupEgress: func(ctx context.Context) string {
		if ctx == refresh {
			cancel()
			return ""
		}
		return "203.0.113.7"
	}}
	if got := f.egressIP(refresh); got != "" {
		t.Fatalf("canceled lookup returned %q", got)
	}
	if got := f.egressIP(context.Background()); got != "203.0.113.7" {
		t.Errorf("lookup after cancellation returned %q", got)
	}
}

func TestConcurrentFenceApplyWaitsForTheFirstEgressLookup(t *testing.T) {
	lookupStarted := make(chan struct{})
	lookupCanFinish := make(chan struct{})
	f := &fence{lookupEgress: func(context.Context) string {
		close(lookupStarted)
		<-lookupCanFinish
		return "203.0.113.7"
	}}
	heartbeat := make(chan string, 1)
	go func() { heartbeat <- f.egressIP(context.Background()) }()
	<-lookupStarted

	applyStarted := make(chan struct{})
	apply := make(chan string, 1)
	go func() {
		close(applyStarted)
		apply <- f.egressIP(context.Background())
	}()
	<-applyStarted
	select {
	case early := <-apply:
		close(lookupCanFinish)
		<-heartbeat
		t.Fatalf("the fence applied %q before the first lookup finished", early)
	case <-time.After(50 * time.Millisecond):
	}
	close(lookupCanFinish)
	if reported, dropped := <-heartbeat, <-apply; reported != "203.0.113.7" || dropped != reported {
		t.Errorf("heartbeat reported %q and fence dropped %q", reported, dropped)
	}
}

// The reduced table (isolation off) holds only the masquerade: no drops to name.
func TestTheReducedFenceGetsNoExits(t *testing.T) {
	cfg, _ := fenceHelperSpec("apply", "172.30.0.0/16", fenceExits{Own: "203.0.113.7", Others: []string{"198.51.100.4"}, Complete: true}, false)
	for _, key := range []string{"FENCE_EXITS", "FENCE_EXITS_COMPLETE", "FENCE_TUNNELS"} {
		if _, ok := envValue(cfg.Env, key); ok {
			t.Errorf("the reduced helper got %s: %v", key, cfg.Env)
		}
	}
	if got, _ := envValue(cfg.Env, "FENCE_EGRESS"); got != "203.0.113.7" {
		t.Errorf("FENCE_EGRESS = %q", got)
	}
}

// One table drops at most 64 exits, this machine's own included, as on the web.
func TestOtherExitsAreCapped(t *testing.T) {
	var many []string
	for i := 1; i <= 100; i++ {
		many = append(many, fmt.Sprintf("198.51.%d.%d", i/250, i%250+1))
	}
	got := otherExits(append(many, "203.0.113.7"), "203.0.113.7")
	if len(got) != maxFenceExits-1 || slices.Contains(got, "203.0.113.7") {
		t.Errorf("%d other exits, own included %v", len(got), slices.Contains(got, "203.0.113.7"))
	}
}

func TestExitTargetsSortsNumerically(t *testing.T) {
	got := ExitTargets([]string{" 9.9.9.9 ", "10.0.0.1", "100.64.1.1", "80.1.1.1", "9.9.9.9", "::ffff:8.8.8.8"})
	if !slices.Equal(got, []string{"8.8.8.8", "9.9.9.9", "80.1.1.1"}) {
		t.Errorf("ExitTargets = %v", got)
	}
}

func tunnelSummary(id string, ip string, labels map[string]string) container.Summary {
	addr, _ := netip.ParseAddr(ip)
	return container.Summary{
		ID: id, State: container.StateRunning, Labels: labels,
		NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{FenceNetwork: {IPAddress: addr}}},
	}
}

// THE RULE: a running tunnel gets its own exception, in the helper's FENCE_TUNNELS
// format: "src:udp" for Tailscale, "src:tcp:proxy_ip:port" for SOCKS5 and HTTP. One
// whose settings cannot be read gets none and keeps the drops.
func TestTunnelEntry(t *testing.T) {
	tunnel := map[string]string{LabelManaged: "true", LabelRuntime: desktopRuntime, LabelRole: RoleTunnel}
	files := func(m map[string]string) func(string) string { return func(n string) string { return m[n] } }
	c := tunnelSummary("t", "172.30.0.5", tunnel)
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"tailscale", map[string]string{"kind": "tailscale"}, "172.30.0.5:udp"},
		{"socks5", map[string]string{"kind": "socks5", "proxy_ip": "198.51.100.4", "proxy_port": "1080"}, "172.30.0.5:tcp:198.51.100.4:1080"},
		{"http", map[string]string{"kind": "http", "proxy_ip": "198.51.100.4", "proxy_port": "3128"}, "172.30.0.5:tcp:198.51.100.4:3128"},
		{"no port", map[string]string{"kind": "socks5", "proxy_ip": "198.51.100.4"}, ""},
		{"port out of range", map[string]string{"kind": "socks5", "proxy_ip": "198.51.100.4", "proxy_port": "70000"}, ""},
		{"IPv6 proxy", map[string]string{"kind": "socks5", "proxy_ip": "2001:db8::1", "proxy_port": "1080"}, ""},
		{"unreadable", map[string]string{}, ""},
	} {
		if got := tunnelEntry(c, files(tc.files)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// Not on the fenced network: no exception, it meets no drop there either.
	off := c
	off.NetworkSettings = &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{"bridge": {}}}
	if got := tunnelEntry(off, files(map[string]string{"kind": "tailscale"})); got != "" {
		t.Errorf("a tunnel off the fenced network got %q", got)
	}
}

// The tunnels' exceptions reach the helper, read from each running tunnel's secret
// files through the archive API. An earner carrying the same files, and a stopped
// tunnel, get none: the daemon's label filter is checked again.
func TestFenceHandsTheHelperTheTunnels(t *testing.T) {
	tunnel := map[string]string{LabelManaged: "true", LabelRuntime: desktopRuntime, LabelRole: RoleTunnel, LabelService: "honeygain"}
	asMap := func(c container.Summary, state string) map[string]any {
		return map[string]any{
			"Id": c.ID, "Names": []string{"/" + c.ID}, "State": state, "Labels": c.Labels,
			"HostConfig":      map[string]any{"NetworkMode": FenceNetwork},
			"NetworkSettings": map[string]any{"Networks": map[string]any{FenceNetwork: map[string]any{"NetworkID": "fencenet", "IPAddress": c.NetworkSettings.Networks[FenceNetwork].IPAddress.String()}}},
		}
	}
	socks := map[string]string{"kind": "socks5", "proxy_ip": "198.51.100.4", "proxy_port": "1080"}
	fake := &fakeDocker{
		earners: []map[string]any{
			asMap(tunnelSummary("ts", "172.30.0.6", tunnel), "running"),
			asMap(tunnelSummary("sk", "172.30.0.5", tunnel), "running"),
			asMap(tunnelSummary("gone", "172.30.0.7", tunnel), "exited"),
			asMap(tunnelSummary("earner", "172.30.0.8", map[string]string{LabelManaged: "true", LabelRuntime: desktopRuntime, LabelService: "honeygain"}), "running"),
		},
		tunnelFiles: map[string]map[string]string{
			"ts": {"kind": "tailscale"}, "sk": socks, "gone": {"kind": "tailscale"}, "earner": socks,
		},
	}
	fake.start(t)
	(&DockerProvider{}).ReconcileFence(context.Background())
	got, _ := envValue(helperEnv(t, fake), "FENCE_TUNNELS")
	if got != "172.30.0.5:tcp:198.51.100.4:1080,172.30.0.6:udp" {
		t.Errorf("FENCE_TUNNELS = %q", got)
	}
}

// The helper image is the first worker release whose rule writer reads FENCE_EXITS,
// FENCE_EXITS_COMPLETE and FENCE_TUNNELS (CashPilot v2.10.0). An older one ignores
// them, and the owner's other lines stay reachable.
func TestTheFenceHelperReadsTheOtherExits(t *testing.T) {
	m := regexp.MustCompile(`:(\d+)\.(\d+)\.(\d+)@`).FindStringSubmatch(fenceHelperImage)
	if m == nil {
		t.Fatalf("no version in %q", fenceHelperImage)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 2 || major == 2 && minor < 10 {
		t.Errorf("the fence helper %s predates FENCE_EXITS (needs 2.10.0 or newer)", fenceHelperImage)
	}
}
