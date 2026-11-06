package runtime

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

func earner(name string, running bool, mode string, nets map[string]string) fenceEarner {
	return fenceEarner{ID: name, Name: "cashpilot-" + name, Slug: name, Running: running, NetworkMode: mode, Networks: nets}
}

// THE RULE: a fresh install is fenced, an upgraded one is left as it is until the
// user switches, and the switch, once made, survives anything short of the user
// turning it off. The state lives in Docker and in the config's memory of "on".
func TestResolveFenceMode(t *testing.T) {
	onBridge := earner("honeygain", true, "bridge", map[string]string{"bridge": "b1"})
	onFence := earner("honeygain", true, FenceNetwork, map[string]string{FenceNetwork: "n1"})
	host := earner("mysterium", true, "host", map[string]string{"host": "h"})
	cases := []struct {
		name                       string
		setting                    string
		managed, bound, remembered bool
		earners                    []fenceEarner
		want                       string
	}{
		{"fresh install, nothing deployed", "auto", false, false, false, nil, FenceModeOn},
		{"fresh install with only a host-networked earner", "auto", false, false, false, []fenceEarner{host}, FenceModeOn},
		{"upgraded install, earners on the default bridge", "auto", false, false, false, []fenceEarner{onBridge}, FenceModeLegacy},
		{"our network exists", "auto", true, false, false, []fenceEarner{onBridge}, FenceModeOn},
		{"our network was removed but an earner still names it", "auto", false, true, false, []fenceEarner{onBridge}, FenceModeOn},
		{"earners already fenced", "auto", false, false, false, []fenceEarner{onFence}, FenceModeOn},
		{"setting on beats an upgraded install", "on", false, false, false, []fenceEarner{onBridge}, FenceModeOn},
		{"setting off beats our network", "off", true, true, false, []fenceEarner{onFence}, FenceModeOff},
		{"a container in default network mode is on the default bridge", "auto", false, false, false, []fenceEarner{earner("x", true, "default", nil)}, FenceModeLegacy},
		{"isolation was on, and the network and every fenced earner are gone", "auto", false, false, true, []fenceEarner{earner("storj", false, "bridge", map[string]string{"bridge": "b1"})}, FenceModeOn},
		{"setting off beats the memory", "off", false, false, true, nil, FenceModeOff},
	}
	for _, tc := range cases {
		if got := resolveFenceMode(tc.setting, tc.managed, tc.bound, tc.remembered, tc.earners); got != tc.want {
			t.Errorf("%s: mode %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFenceSettingSpellings(t *testing.T) {
	for raw, want := range map[string]string{"": "auto", "auto": "auto", "ON": "on", "true": "on", "off": "off", " no ": "off", "maybe": "auto"} {
		if got := FenceSetting(raw); got != want {
			t.Errorf("FenceSetting(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestBoundToRemoved(t *testing.T) {
	bound := []fenceEarner{earner("a", false, FenceNetwork, map[string]string{FenceNetwork: "old"})}
	if !boundToRemoved(false, bound) {
		t.Error("an earner naming our removed network does not keep isolation on")
	}
	if boundToRemoved(true, bound) {
		t.Error("a network by our name that still exists counted as removed")
	}
	if boundToRemoved(false, []fenceEarner{earner("a", true, "bridge", map[string]string{"bridge": "b"})}) {
		t.Error("an earner on the default bridge counted as bound to our network")
	}
}

func TestOutsideFence(t *testing.T) {
	earners := []fenceEarner{
		earner("honeygain", true, "bridge", map[string]string{"bridge": "b"}),
		earner("repocket", false, "bridge", map[string]string{"bridge": "b"}),
		earner("earnapp", true, FenceNetwork, map[string]string{FenceNetwork: "n"}),
		earner("mysterium", true, "host", map[string]string{"host": "h"}),
		earner("sidecar", true, "container:abc", nil),
		{ID: "c", Name: "cashpilot-nolabel", Running: true, NetworkMode: "bridge", Networks: map[string]string{"bridge": "b"}},
	}
	running, stopped := outsideFence(earners, FenceNetwork, false)
	if !slices.Equal(running, []string{"cashpilot-nolabel", "honeygain"}) {
		t.Errorf("running outside = %v", running)
	}
	if !slices.Equal(stopped, []string{"repocket"}) {
		t.Errorf("stopped outside = %v", stopped)
	}
	running, _ = outsideFence(earners, "", true)
	if slices.Contains(running, "earnapp") {
		t.Errorf("legacy mode named a fenced earner as exposed: %v", running)
	}
	// A slug with one copy running and one stopped is a running earner: the redeploy
	// moves both.
	copies := []fenceEarner{
		earner("honeygain", true, "bridge", map[string]string{"bridge": "b"}),
		earner("honeygain", false, "bridge", map[string]string{"bridge": "b"}),
	}
	running, stopped = outsideFence(copies, FenceNetwork, false)
	if !slices.Equal(running, []string{"honeygain"}) || len(stopped) != 0 {
		t.Errorf("copies: running %v, stopped %v", running, stopped)
	}
	// On the fenced network and another one: whatever the other reaches, it reaches.
	both := []fenceEarner{earner("repocket", true, FenceNetwork, map[string]string{FenceNetwork: "n", "lan": "l"})}
	if running, _ = outsideFence(both, FenceNetwork, false); !slices.Equal(running, []string{"repocket"}) {
		t.Errorf("an earner on the fence and a second network counted as fenced: %v", running)
	}
}

// THE RULE: an earner the removed network stopped is started only after the table is
// written, and not at all when the apply fails.
func TestReattachRunsOnlyAfterTheFence(t *testing.T) {
	orphan := map[string]any{
		"Id": "orphan", "Names": []string{"/cashpilot-honeygain"}, "State": "exited",
		"Labels":          map[string]string{LabelManaged: "true", LabelService: "honeygain"},
		"HostConfig":      map[string]any{"NetworkMode": FenceNetwork},
		"NetworkSettings": map[string]any{"Networks": map[string]any{FenceNetwork: map[string]any{"NetworkID": "gone"}}},
	}
	for _, tc := range []struct {
		name   string
		output string
		moved  bool
	}{
		{"applied", "", true},
		{"failed", `{"backend":"nft","ok":false,"error":"no"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDocker{earners: []map[string]any{orphan}, fenceOutput: tc.output}
			fake.start(t)
			(&DockerProvider{}).ReconcileFence(context.Background())
			calls := fake.recorded()
			fence, connect := -1, -1
			for i, c := range calls {
				if strings.HasPrefix(c, "FENCE POST /containers/fence/start") {
					fence = i
				}
				if strings.Contains(c, "/networks/fencenet/connect") && connect < 0 {
					connect = i
				}
			}
			if !tc.moved {
				if connect >= 0 {
					t.Fatalf("an earner was reattached after a failed apply: %v", calls)
				}
				return
			}
			if fence < 0 || connect < 0 || connect < fence {
				t.Fatalf("want fence -> reattach, got %v", calls)
			}
		})
	}
}

func TestStaleEndpoint(t *testing.T) {
	if !staleEndpoint(earner("a", false, FenceNetwork, map[string]string{FenceNetwork: "old"}), "new") {
		t.Error("an endpoint on a removed copy of our network is not stale")
	}
	if staleEndpoint(earner("a", false, FenceNetwork, map[string]string{FenceNetwork: "new"}), "new") {
		t.Error("an endpoint on the current network is stale")
	}
	if staleEndpoint(earner("a", false, "bridge", map[string]string{"bridge": "b"}), "new") {
		t.Error("an earner never on our network is stale")
	}
}

// THE RULE: Docker's own masquerade is off on our network, so the only NAT is the one
// in the fence's table. If the table goes, the earners go offline, not onto the LAN.
func TestFenceNetworkOptions(t *testing.T) {
	opts := fenceNetworkOptions()
	if opts.Options[optMasquerade] != "false" {
		t.Errorf("masquerade = %q; with Docker's own NAT on, a missing table puts earners back on the LAN", opts.Options[optMasquerade])
	}
	if opts.Options[optICC] != "false" {
		t.Errorf("inter-container traffic = %q", opts.Options[optICC])
	}
	if opts.Options[optBridgeName] != fenceBridge || len(fenceBridge) > 15 {
		t.Errorf("bridge name = %q", opts.Options[optBridgeName])
	}
	if opts.Labels[fenceLabel] != fenceLabelValue || opts.Driver != "bridge" {
		t.Errorf("labels %v, driver %q", opts.Labels, opts.Driver)
	}
	if opts.EnableIPv6 == nil || *opts.EnableIPv6 {
		t.Error("IPv6 is not explicitly off")
	}
}

func TestCheckFenceNetworkRefusesAnotherInterfaceName(t *testing.T) {
	good := &network.Inspect{Network: network.Network{Driver: "bridge", Options: fenceNetworkOptions().Options}}
	if err := checkFenceNetwork(good); err != nil {
		t.Fatalf("our own network refused: %v", err)
	}
	// Docker's masquerade and ICC default to on, so a missing option counts as on.
	for _, opt := range []string{optMasquerade, optICC} {
		for _, value := range []string{"true", ""} {
			opts := map[string]string{}
			for k, v := range good.Options {
				opts[k] = v
			}
			if value == "" {
				delete(opts, opt)
			} else {
				opts[opt] = value
			}
			err := checkFenceNetwork(&network.Inspect{Network: network.Network{Driver: "bridge", Options: opts}})
			if err == nil || !strings.Contains(err.Error(), opt) {
				t.Errorf("%s=%q accepted (err %v)", opt, value, err)
			}
		}
	}
	ignored := &network.Inspect{Network: network.Network{Driver: "bridge", Options: map[string]string{}}}
	if err := checkFenceNetwork(ignored); err == nil {
		t.Error("a network whose interface no rule matches was accepted")
	}
	if err := checkFenceNetwork(&network.Inspect{Network: network.Network{Driver: "macvlan", Options: good.Options}}); err == nil {
		t.Error("a non-bridge network was accepted")
	}
}

// THE RULE: the helper gets NET_ADMIN in the host's namespace and nothing else.
func TestFenceHelperSpecIsConfined(t *testing.T) {
	cfg, host := fenceHelperSpec("apply", "172.30.0.0/16", "", true)
	if host.NetworkMode != "host" {
		t.Errorf("network mode %q: the table must land in the daemon's namespace", host.NetworkMode)
	}
	if !slices.Equal(host.CapDrop, []string{"ALL"}) || !slices.Equal(host.CapAdd, []string{"NET_ADMIN", "NET_RAW"}) {
		t.Errorf("capabilities drop %v add %v", host.CapDrop, host.CapAdd)
	}
	if host.Privileged || !host.ReadonlyRootfs || !slices.Contains(host.SecurityOpt, noNewPrivileges) {
		t.Errorf("privileged %v, read-only %v, security %v", host.Privileged, host.ReadonlyRootfs, host.SecurityOpt)
	}
	if host.PidsLimit == nil || *host.PidsLimit > 64 || host.Memory == 0 || host.Memory > 64<<20 {
		t.Errorf("pids %v, memory %d", host.PidsLimit, host.Memory)
	}
	if !strings.Contains(cfg.Image, ":") || strings.HasSuffix(cfg.Image, ":latest") {
		t.Errorf("helper image %q is not pinned", cfg.Image)
	}
	if !slices.Equal(cfg.Cmd, []string{"apply"}) || !slices.Contains(cfg.Env, "FENCE_IFACE="+fenceBridge) ||
		!slices.Contains(cfg.Env, "FENCE_SUBNET=172.30.0.0/16") || !slices.Contains(cfg.Env, "FENCE_FILTER=1") {
		t.Errorf("cmd %v env %v", cfg.Cmd, cfg.Env)
	}
	_, reduced := fenceHelperSpec("apply", "172.30.0.0/16", "", false)
	if reduced.NetworkMode != "host" {
		t.Error("the reduced helper is not on the host namespace")
	}
	cfgReduced, _ := fenceHelperSpec("apply", "172.30.0.0/16", "", false)
	if !slices.Contains(cfgReduced.Env, "FENCE_FILTER=0") {
		t.Errorf("reduce does not ask for the masquerade only: %v", cfgReduced.Env)
	}
}

func TestParseFenceResult(t *testing.T) {
	r, err := parseFenceResult("warning from nft\n" + fenceAppliedOutput + "\n")
	if err != nil || !r.OK || r.Backend != "nft" {
		t.Fatalf("result %+v, err %v", r, err)
	}
	if _, err := parseFenceResult(""); err == nil {
		t.Error("empty output was accepted as a result")
	}
	if _, err := parseFenceResult("Traceback (most recent call last)"); err == nil {
		t.Error("a traceback was accepted as a result")
	}
}

func TestIsolateOnlyMovesBridgeEarners(t *testing.T) {
	bridge := &container.HostConfig{NetworkMode: "bridge"}
	isolate(bridge, FenceNetwork)
	if bridge.NetworkMode != FenceNetwork {
		t.Errorf("network mode %q", bridge.NetworkMode)
	}
	if len(bridge.DNS) != 2 || bridge.DNS[0].String() != "1.1.1.1" {
		t.Errorf("DNS %v: the daemon's LAN resolver would be dropped by the fence", bridge.DNS)
	}
	hostNet := &container.HostConfig{NetworkMode: "host"}
	isolate(hostNet, FenceNetwork)
	if hostNet.NetworkMode != "host" || hostNet.DNS != nil {
		t.Errorf("a host-networked earner was moved: %+v", hostNet)
	}
	legacy := &container.HostConfig{NetworkMode: "bridge"}
	isolate(legacy, "")
	if legacy.NetworkMode != "bridge" || legacy.DNS != nil {
		t.Errorf("legacy mode moved the earner: %+v", legacy)
	}
}

func createdHostConfig(t *testing.T, fake *fakeDocker) container.HostConfig {
	t.Helper()
	var body struct {
		HostConfig container.HostConfig
	}
	if err := json.Unmarshal(fake.createBody, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	return body.HostConfig
}

func ranFence(fake *fakeDocker) bool {
	for _, call := range fake.recorded() {
		if strings.HasPrefix(call, "FENCE POST /containers/fence/start") {
			return true
		}
	}
	return false
}

// THE RULE: a fresh install deploys onto the fenced network, after the fence is in
// place, with resolvers the fence does not drop.
func TestDeployOnAFreshInstallJoinsTheFence(t *testing.T) {
	fake := &fakeDocker{}
	fake.start(t)
	if _, err := (&DockerProvider{}).Deploy(context.Background(), deploySpec("orderly"), nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	host := createdHostConfig(t, fake)
	if string(host.NetworkMode) != FenceNetwork {
		t.Fatalf("the earner joined %q, not the fenced network (calls %v)", host.NetworkMode, fake.recorded())
	}
	if len(host.DNS) == 0 {
		t.Error("the earner kept the daemon's resolvers")
	}
	calls := fake.recorded()
	netCreate, fence, create := slices.Index(calls, "POST /networks/create"), -1, slices.Index(calls, "POST /containers/create")
	for i, c := range calls {
		if strings.HasPrefix(c, "FENCE POST /containers/fence/start") {
			fence = i
		}
	}
	if netCreate < 0 || fence < 0 || create < 0 || !(netCreate < fence && fence < create) {
		t.Fatalf("want network -> fence -> earner, got %v", calls)
	}
}

// "default" is the same default bridge, so a service that spells it that way is fenced
// like one that says bridge or nothing.
func TestDeployInDefaultNetworkModeJoinsTheFence(t *testing.T) {
	fake := &fakeDocker{}
	fake.start(t)
	spec := deploySpec("orderly")
	spec.Service.Docker.NetworkMode = "default"
	if _, err := (&DockerProvider{}).Deploy(context.Background(), spec, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if host := createdHostConfig(t, fake); string(host.NetworkMode) != FenceNetwork {
		t.Fatalf("a default-mode earner joined %q, not the fenced network", host.NetworkMode)
	}
}

// THE RULE: a reconcile that started under one setting does not publish its mode once
// the setting has changed; the reconcile the change triggers does.
func TestAStaleReconcileDoesNotPublishItsMode(t *testing.T) {
	fake := &fakeDocker{}
	fake.start(t)
	p := &DockerProvider{}
	calls := 0
	p.SetIsolationSetting(func() string {
		// Off for the reconcile's own reads (its start and the mode), on by the end.
		calls++
		if calls <= 2 {
			return "off"
		}
		return "on"
	})
	p.ReconcileFence(context.Background())
	if st := p.FenceStatus(); st.Mode == FenceModeOff {
		t.Fatalf("a reconcile started with isolation off published off after the switch to on: %+v", st)
	}
}

// THE RULE: a fence that cannot be applied refuses the deploy before the running
// container is touched, and says how to opt out. The positive control is the test
// above: the same fake with a working helper deploys.
func TestDeployIsRefusedWhenTheFenceCannotBeApplied(t *testing.T) {
	cases := []struct {
		name string
		fake *fakeDocker
		want string
	}{
		{"the helper says the table was not written", &fakeDocker{liveLabels: map[string]string{LabelService: "orderly"},
			fenceOutput: `{"backend":"nft","ok":false,"error":"Operation not permitted"}`}, "Operation not permitted"},
		{"the helper crashes", &fakeDocker{liveLabels: map[string]string{LabelService: "orderly"}, fenceExit: 1}, "status 1"},
		{"the runtime ignores the interface name", &fakeDocker{liveLabels: map[string]string{LabelService: "orderly"},
			netOptions: map[string]string{}}, "would not match"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.fake.start(t)
			p := &DockerProvider{}
			_, err := p.Deploy(context.Background(), deploySpec("orderly"), nil)
			if err == nil {
				t.Fatal("the deploy went ahead without the fence")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "off in Settings") ||
				!strings.Contains(err.Error(), "not touched") {
				t.Errorf("error %q lacks the reason, the opt-out or the reassurance", err)
			}
			if tc.fake.removedAContainer() {
				t.Errorf("the running container was removed (calls %v)", tc.fake.recorded())
			}
			for _, call := range tc.fake.recorded() {
				if call == "POST /containers/create" || strings.HasPrefix(call, "POST /containers/cashpilot-orderly/stop") {
					t.Errorf("the deploy touched the earner: %v", tc.fake.recorded())
				}
			}
			if st := p.FenceStatus(); st.State != "failed" || st.Error == "" {
				t.Errorf("status after a failed apply: %+v", st)
			}
		})
	}
}

// THE RULE: an upgraded install, whose earners sit on the default bridge, deploys as
// before until the user switches; and with isolation off nothing is fenced at all.
func TestDeployStaysOnTheDefaultBridgeWhenNotSwitched(t *testing.T) {
	onBridge := map[string]any{
		"Id": "old", "Names": []string{"/cashpilot-other"}, "State": "running",
		"Labels":          map[string]string{LabelManaged: "true", LabelService: "other"},
		"HostConfig":      map[string]any{"NetworkMode": "bridge"},
		"NetworkSettings": map[string]any{"Networks": map[string]any{"bridge": map[string]any{"NetworkID": "b"}}},
	}
	cases := []struct {
		name    string
		setting string
		earners []map[string]any
	}{
		{"upgraded install", "auto", []map[string]any{onBridge}},
		{"isolation off", "off", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDocker{earners: tc.earners}
			fake.start(t)
			p := &DockerProvider{}
			p.SetIsolationSetting(func() string { return tc.setting })
			if _, err := p.Deploy(context.Background(), deploySpec("orderly"), nil); err != nil {
				t.Fatalf("Deploy: %v", err)
			}
			if host := createdHostConfig(t, fake); string(host.NetworkMode) != "bridge" {
				t.Errorf("network mode %q, want the default bridge", host.NetworkMode)
			}
			if ranFence(fake) || slices.Contains(fake.recorded(), "POST /networks/create") {
				t.Errorf("the fence was written without the switch (calls %v)", fake.recorded())
			}
		})
	}
}

// Turning isolation on for an upgraded install applies the fence and names the
// running earners to move, with the stopped ones apart.
func TestEnableFenceListsTheEarnersToMove(t *testing.T) {
	ep := func(id, state string) map[string]any {
		return map[string]any{
			"Id": id, "Names": []string{"/cashpilot-" + id}, "State": state,
			"Labels":          map[string]string{LabelManaged: "true", LabelService: id},
			"HostConfig":      map[string]any{"NetworkMode": "bridge"},
			"NetworkSettings": map[string]any{"Networks": map[string]any{"bridge": map[string]any{"NetworkID": "b"}}},
		}
	}
	fake := &fakeDocker{earners: []map[string]any{ep("honeygain", "running"), ep("repocket", "exited")}}
	fake.start(t)
	p := &DockerProvider{}
	p.ReconcileFence(context.Background())
	if st := p.FenceStatus(); st.Mode != FenceModeLegacy || !slices.Equal(st.Outside, []string{"honeygain"}) {
		t.Fatalf("before the switch: %+v", st)
	}
	if ranFence(fake) {
		t.Fatal("the legacy check wrote the fence")
	}
	st, err := p.EnableFence(context.Background())
	if err != nil {
		t.Fatalf("EnableFence: %v", err)
	}
	if st.Mode != FenceModeOn || st.State != "applied" {
		t.Errorf("after the switch: %+v", st)
	}
	if !slices.Equal(st.Outside, []string{"honeygain"}) || !slices.Equal(st.OutsideStopped, []string{"repocket"}) {
		t.Errorf("outside %v, stopped %v", st.Outside, st.OutsideStopped)
	}
	off := &DockerProvider{}
	off.SetIsolationSetting(func() string { return "off" })
	if _, err := off.EnableFence(context.Background()); err == nil {
		t.Error("the switch went ahead with isolation off in Settings")
	}
}

// With isolation turned off and our network still there, the table keeps only its
// masquerade, so earners still on the network keep their internet.
func TestReconcileReducesTheFenceWhenTurnedOff(t *testing.T) {
	fake := &fakeDocker{}
	fake.fenceNet = fake.fenceNetwork(true, map[string]string{optBridgeName: fenceBridge})
	fake.start(t)
	p := &DockerProvider{}
	p.SetIsolationSetting(func() string { return "off" })
	p.ReconcileFence(context.Background())
	if st := p.FenceStatus(); st.Mode != FenceModeOff || st.State != "reduced" {
		t.Fatalf("status %+v (calls %v)", st, fake.recorded())
	}
	found := false
	for _, call := range fake.recorded() {
		if call == "FENCE POST /containers/create" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no helper ran (calls %v)", fake.recorded())
	}
	var body struct{ Env []string }
	_ = json.Unmarshal(fake.fenceCreate, &body)
	if !slices.Contains(body.Env, "FENCE_FILTER=0") {
		t.Errorf("the helper was asked for the full fence with isolation off: %v", body.Env)
	}
}

// THE RULE: once isolation is on it stays on after Docker loses every trace of it.
// Every fenced earner was removed, a prune or a runtime reset took the network, and a
// stopped earner is left on the default bridge: without the remembered state that
// reads as an upgraded install, and the next deploy lands on the default bridge.
func TestIsolationOnSurvivesLosingTheNetwork(t *testing.T) {
	stopped := map[string]any{
		"Id": "storj", "Names": []string{"/cashpilot-storj"}, "State": "exited",
		"Labels":          map[string]string{LabelManaged: "true", LabelService: "storj"},
		"HostConfig":      map[string]any{"NetworkMode": "bridge"},
		"NetworkSettings": map[string]any{"Networks": map[string]any{"bridge": map[string]any{"NetworkID": "b"}}},
	}
	remembered, writes := false, 0
	wire := func(p *DockerProvider, setting string) {
		p.SetIsolationSetting(func() string { return setting })
		p.SetIsolationMemory(func() bool { return remembered }, func(on bool) { remembered = on; writes++ })
	}

	fresh := &fakeDocker{}
	fresh.start(t)
	p := &DockerProvider{}
	wire(p, "auto")
	p.ReconcileFence(context.Background())
	if st := p.FenceStatus(); st.Mode != FenceModeOn || !remembered {
		t.Fatalf("a fresh install: mode %q, remembered %v", st.Mode, remembered)
	}
	p.ReconcileFence(context.Background())
	if writes != 1 {
		t.Errorf("the memory was written %d times, want once", writes)
	}

	after := &fakeDocker{earners: []map[string]any{stopped}}
	after.start(t)
	p = &DockerProvider{}
	wire(p, "auto")
	p.ReconcileFence(context.Background())
	if st := p.FenceStatus(); st.Mode != FenceModeOn {
		t.Fatalf("after the network went: mode %q, want on", st.Mode)
	}

	// The way out: off forgets it, and a daemon with only that stopped earner reads as
	// legacy again. A clean daemon, because the on pass above recreated our network.
	off := &DockerProvider{}
	wire(off, "off")
	off.ReconcileFence(context.Background())
	if remembered {
		t.Fatal("off did not clear the remembered state")
	}
	clean := &fakeDocker{earners: []map[string]any{stopped}}
	clean.start(t)
	p = &DockerProvider{}
	wire(p, "auto")
	p.ReconcileFence(context.Background())
	if st := p.FenceStatus(); st.Mode != FenceModeLegacy {
		t.Errorf("after off and back to auto: mode %q, want legacy", st.Mode)
	}
}

// Turning isolation on from the dashboard records it.
func TestEnableFenceRemembersIt(t *testing.T) {
	fake := &fakeDocker{}
	fake.start(t)
	p := &DockerProvider{}
	remembered := false
	p.SetIsolationMemory(func() bool { return remembered }, func(on bool) { remembered = on })
	if _, err := p.EnableFence(context.Background()); err != nil {
		t.Fatalf("EnableFence: %v", err)
	}
	if !remembered {
		t.Error("the switch did not record isolation on")
	}
}

// helperEnv is the environment of the last helper the fake was asked to create.
func helperEnv(t *testing.T, fake *fakeDocker) []string {
	t.Helper()
	fake.mu.Lock()
	body := fake.fenceCreate
	fake.mu.Unlock()
	var req struct{ Env []string }
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("helper create body: %v", err)
	}
	return req.Env
}

// THE RULE: most home routers loop a connection to their own public address back
// into the LAN (NAT loopback), so the table drops this machine's public IPv4 too.
// The lookup is cached, a failed one keeps the last address, and an address that is
// not a public IPv4 is never passed on.
func TestFenceDropsTheHomePublicIP(t *testing.T) {
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	old := fenceNow
	fenceNow = func() time.Time { return now }
	t.Cleanup(func() { fenceNow = old })

	answer, lookups := "203.0.113.7\n", 0
	fake := &fakeDocker{}
	fake.start(t)
	p := &DockerProvider{}
	p.SetEgressLookup(func(context.Context) string { lookups++; return answer })

	p.ReconcileFence(context.Background())
	if env := helperEnv(t, fake); !slices.Contains(env, "FENCE_EGRESS=203.0.113.7") {
		t.Fatalf("the home public IP is not dropped: %v", env)
	}

	// Within the hour the table is rewritten without asking again.
	now = now.Add(30 * time.Minute)
	p.ReconcileFence(context.Background())
	if lookups != 1 {
		t.Errorf("%d lookups within the hour, want 1", lookups)
	}

	// A failed lookup keeps the last address rather than dropping the drop.
	now = now.Add(time.Hour)
	answer = ""
	p.ReconcileFence(context.Background())
	if env := helperEnv(t, fake); !slices.Contains(env, "FENCE_EGRESS=203.0.113.7") || lookups != 2 {
		t.Errorf("after a failed lookup (%d lookups): %v", lookups, helperEnv(t, fake))
	}

	// A private, CGNAT or IPv6 answer is never passed to the helper.
	for _, bad := range []string{"192.168.1.5", "100.64.0.9", "2001:db8::1"} {
		fresh := &fakeDocker{}
		fresh.start(t)
		q := &DockerProvider{}
		q.SetEgressLookup(func(context.Context) string { return bad })
		q.ReconcileFence(context.Background())
		if env := helperEnv(t, fresh); !slices.Contains(env, "FENCE_EGRESS=") {
			t.Errorf("%s: want an empty FENCE_EGRESS, got %v", bad, env)
		}
	}
}
