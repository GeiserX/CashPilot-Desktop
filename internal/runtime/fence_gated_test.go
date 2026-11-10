package runtime

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

// mysteriumShaped is the network shape of the real Mysterium entry since the web
// moved it off host networking: no static ports, the WebUI on each UI_ADDRESS
// address, the UDP range 1:1, runc only, and fence-gated.
func mysteriumShaped() catalog.Service {
	return catalog.Service{
		Name: "MystNodes",
		Slug: "mysterium",
		Docker: catalog.DockerConfig{
			Image: "mysteriumnetwork/myst:1.39.9-alpine@sha256:f89347d244efeeaf29555e876404cf8aebcd41561c5cbed06c365ba64beb8564",
			Env: []catalog.EnvVar{
				{Key: "UI_ADDRESS", Default: "127.0.0.1"},
				{Key: "UDP_PORTS", Default: "23000:23029", PortRange: true},
			},
			Runtime:      "runc",
			UIPort:       &catalog.UIPort{Env: "UI_ADDRESS", Port: 4449},
			UDPPortRange: &catalog.UDPPortRange{Env: "UDP_PORTS", MaxPorts: 1000},
			FenceGated:   true,
			CapAdd:       []string{"NET_ADMIN", "SETUID", "SETGID"},
			Devices:      []string{"/dev/net/tun"},
		},
	}
}

func TestTheWebUIIsPublishedOnEachAddressAndTheUDPRangeOneToOne(t *testing.T) {
	svc := mysteriumShaped()
	env := buildEnv(svc, map[string]string{"UI_ADDRESS": "127.0.0.1,172.18.0.1,localhost", "UDP_PORTS": "23000:23004"})
	ports, bindings, err := buildServicePorts(svc.Docker, env)
	if err != nil {
		t.Fatalf("buildServicePorts: %v", err)
	}
	ui := bindings[network.MustParsePort("4449/tcp")]
	want := []network.PortBinding{
		{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "4449"},
		{HostIP: netip.MustParseAddr("172.18.0.1"), HostPort: "4449"},
	}
	if len(ui) != len(want) || ui[0] != want[0] || ui[1] != want[1] {
		t.Fatalf("4449/tcp bindings = %+v, want %+v (localhost folded into 127.0.0.1)", ui, want)
	}
	for p := 23000; p <= 23004; p++ {
		key := network.MustParsePort(strings.Join([]string{itoa(p), "udp"}, "/"))
		if _, ok := ports[key]; !ok {
			t.Errorf("%s is not exposed", key)
		}
		if b := bindings[key]; len(b) != 1 || b[0].HostPort != itoa(p) {
			t.Errorf("%s bindings = %+v, want host port %d (1:1)", key, b, p)
		}
	}
	if len(bindings) != 6 {
		t.Errorf("published %d ports, want the WebUI and 5 UDP ports", len(bindings))
	}
}

func TestTheCatalogDefaultsPublishLoopbackAndTheDefaultRange(t *testing.T) {
	svc := mysteriumShaped()
	_, bindings, err := buildServicePorts(svc.Docker, buildEnv(svc, nil))
	if err != nil {
		t.Fatalf("buildServicePorts: %v", err)
	}
	if ui := bindings[network.MustParsePort("4449/tcp")]; len(ui) != 1 || ui[0].HostIP != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("4449/tcp bindings = %+v, want loopback only", ui)
	}
	if len(bindings) != 31 {
		t.Fatalf("published %d ports, want the WebUI and the 30 default UDP ports", len(bindings))
	}
}

func TestABadAddressOrRangeIsRefusedBeforeAnythingIsPublished(t *testing.T) {
	svc := mysteriumShaped()
	svc.Docker.UDPPortRange.MaxPorts = 10
	for name, creds := range map[string]map[string]string{
		"not an address": {"UI_ADDRESS": "127.0.0.1,example.com"},
		"IPv6":           {"UI_ADDRESS": "::1"},
		"reversed range": {"UDP_PORTS": "23010:23000"},
		"over the cap":   {"UDP_PORTS": "23000:23010"},
		"not a range":    {"UDP_PORTS": "23000"},
	} {
		if _, _, err := buildServicePorts(svc.Docker, buildEnv(svc, creds)); err == nil {
			t.Errorf("%s: %v was published, want a refusal", name, creds)
		}
	}
	if _, _, err := buildServicePorts(svc.Docker, buildEnv(svc, map[string]string{"UDP_PORTS": "23000:23009"})); err != nil {
		t.Fatalf("a range of exactly the cap was refused: %v", err)
	}
}

func TestTheRuntimeTheEntryNamesIsSetAndChecked(t *testing.T) {
	svc := mysteriumShaped()
	host, err := buildHostConfig(svc, nil, nil, "linux")
	if err != nil {
		t.Fatalf("buildHostConfig: %v", err)
	}
	if host.Runtime != "runc" {
		t.Fatalf("Runtime = %q, want runc whatever the daemon's default", host.Runtime)
	}
	plain := svc
	plain.Docker.Runtime = ""
	if host, _ := buildHostConfig(plain, nil, nil, "linux"); host.Runtime != "" {
		t.Fatalf("an entry naming no runtime got %q, want the daemon's default", host.Runtime)
	}

	if err := checkRuntime(svc, map[string]bool{"runc": true, "runsc": true}); err != nil {
		t.Fatalf("runc is available: %v", err)
	}
	if err := checkRuntime(svc, map[string]bool{"crun": true}); err == nil || !strings.Contains(err.Error(), "crun") {
		t.Fatalf("a daemon without runc: err = %v, want a refusal naming what it has", err)
	}
	if err := checkRuntime(svc, nil); err == nil {
		t.Fatal("a daemon that did not list its runtimes must be refused: the create would fail after the removal")
	}
	if err := checkRuntime(plain, nil); err != nil {
		t.Fatalf("an entry naming no runtime needs no check: %v", err)
	}
}

type runtimesInfo struct {
	runtimes map[string]system.RuntimeWithStatus
}

func (r runtimesInfo) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	return client.SystemInfoResult{Info: system.Info{Architecture: "x86_64", OSType: "linux", Runtimes: r.runtimes}}, nil
}

func TestDaemonFactsListTheRuntimes(t *testing.T) {
	facts := daemonFacts(context.Background(), runtimesInfo{map[string]system.RuntimeWithStatus{"runc": {}, "runsc": {}}})
	if !facts.Runtimes["runc"] || !facts.Runtimes["runsc"] || len(facts.Runtimes) != 2 {
		t.Fatalf("Runtimes = %v, want runc and runsc", facts.Runtimes)
	}
	if facts := daemonFacts(context.Background(), runtimesInfo{}); facts.Runtimes != nil {
		t.Fatalf("a daemon listing none gave %v, want nil (unknown)", facts.Runtimes)
	}
}

func TestAFenceGatedServiceDeploysOnlyOnTheFencedNetwork(t *testing.T) {
	svc := mysteriumShaped()
	on := &container.HostConfig{NetworkMode: FenceNetwork}
	if p := fenceGateProblem(svc, on, FenceNetwork, "linux", FenceModeOn); p != "" {
		t.Fatalf("on the fenced network: %q, want no problem", p)
	}
	bridge := &container.HostConfig{NetworkMode: "bridge"}
	for mode, want := range map[string]string{
		FenceModeOff:         "off in Settings",
		FenceModeLegacy:      "Turn on isolation",
		FenceModeUnsupported: "Podman",
		"":                   "off in Settings",
	} {
		p := fenceGateProblem(svc, bridge, "", "linux", mode)
		if !strings.Contains(p, "only behind the LAN fence") || !strings.Contains(p, want) || !strings.Contains(p, "not touched") {
			t.Errorf("mode %q: %q, want a refusal mentioning %q", mode, p, want)
		}
	}
	if p := fenceGateProblem(svc, &container.HostConfig{NetworkMode: "host"}, "", "linux", FenceModeOn); !strings.Contains(p, "outside the fence") {
		t.Errorf("host networking: %q, want a refusal", p)
	}
	if p := fenceGateProblem(svc, &container.HostConfig{NetworkMode: "nat"}, "", "windows", ""); !strings.Contains(p, "Windows containers") {
		t.Errorf("Windows containers: %q, want a refusal", p)
	}

	plain := svc
	plain.Docker.FenceGated = false
	if p := fenceGateProblem(plain, bridge, "", "linux", FenceModeOff); p != "" {
		t.Fatalf("an ordinary earner on the default bridge was refused: %q", p)
	}
}

func TestAFencedMysteriumMissingItsWebUIPortIsDrift(t *testing.T) {
	svc := mysteriumShaped()
	shape := deployedShape(t, svc, nil)
	if found := ShapeDrift(svc, shape, ""); len(found) != 0 {
		t.Fatalf("a freshly deployed node shows drift: %v", found)
	}
	var kept []string
	for _, p := range shape.Ports {
		if p != "4449/tcp" && !strings.HasSuffix(p, "/udp") {
			kept = append(kept, p)
		}
	}
	shape.Ports = kept
	got := strings.Join(ShapeDrift(svc, shape, ""), "; ")
	if !strings.Contains(got, "4449/tcp") || !strings.Contains(got, "UDP") {
		t.Fatalf("drift = %q, want the missing WebUI port and UDP range named", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// An entry that names the fenced network itself never went through the fence, so
// it is refused like any other network: the fence's rules may not be there.
func TestNamingTheFencedNetworkDoesNotSkipTheFence(t *testing.T) {
	svc := mysteriumShaped()
	named := &container.HostConfig{NetworkMode: FenceNetwork}
	if p := fenceGateProblem(svc, named, "", "linux", FenceModeOff); !strings.Contains(p, "only behind the LAN fence") {
		t.Fatalf("a fenced network the fence did not hand out: %q, want a refusal", p)
	}
}

// A blank (or whitespace-only) UI_ADDRESS or UDP_PORTS keeps the catalog default, as the deploy
// validation assumes; otherwise the node would get no UDP ports and an empty
// --udp.ports.
func TestABlankPublishedSettingKeepsItsDefault(t *testing.T) {
	svc := mysteriumShaped()
	for _, blank := range []string{"", "  "} {
		env := buildEnv(svc, map[string]string{"UI_ADDRESS": blank, "UDP_PORTS": blank})
		if env["UDP_PORTS"] != "23000:23029" || env["UI_ADDRESS"] != "127.0.0.1" {
			t.Fatalf("blank %q: env = %v, want the defaults", blank, env)
		}
		_, bindings, err := buildServicePorts(svc.Docker, env)
		if err != nil || len(bindings) != 31 {
			t.Fatalf("blank %q: published %d ports (%v), want the WebUI and the 30 default UDP ports", blank, len(bindings), err)
		}
	}
	// Any other variable may still be blanked on purpose.
	other := svc
	other.Docker.Env = append(other.Docker.Env, catalog.EnvVar{Key: "EXTRA", Default: "x"})
	if got := buildEnv(other, map[string]string{"EXTRA": ""})["EXTRA"]; got != "" {
		t.Fatalf("EXTRA = %q, want the blank the user chose", got)
	}
}

func TestARuntimeOtherThanTheCatalogsIsDrift(t *testing.T) {
	svc := mysteriumShaped()
	shape := deployedShape(t, svc, nil)
	if shape.Runtime != "runc" {
		t.Fatalf("shape runtime = %q, want runc", shape.Runtime)
	}
	shape.Runtime = "runsc"
	if got := strings.Join(ShapeDrift(svc, shape, ""), "; "); !strings.Contains(got, "runsc") || !strings.Contains(got, "requires runc") {
		t.Fatalf("drift = %q, want the runtime named", got)
	}
	shape.Runtime = ""
	if found := ShapeDrift(svc, shape, ""); len(found) != 0 {
		t.Fatalf("a container reporting no runtime was judged: %v", found)
	}
	plain := svc
	plain.Docker.Runtime = ""
	if ShapeFingerprint(plain) == ShapeFingerprint(svc) {
		t.Fatal("a runtime-only catalog change keeps the fingerprint, so an Ignore would hide it")
	}
}

func TestAFenceGatedContainerIsLabelled(t *testing.T) {
	svc := mysteriumShaped()
	if gatedLabels(containerLabels("mysterium", 1), svc)[LabelFenceGated] != "true" {
		t.Fatal("a fence-gated container must carry the label the fence stops it by")
	}
	svc.Docker.FenceGated = false
	if _, ok := gatedLabels(containerLabels("x", 1), svc)[LabelFenceGated]; ok {
		t.Fatal("an ordinary earner must not carry the fence-gated label")
	}
}

// In on mode a fence-gated node runs only on the fenced network alone. A fence that
// cannot be put back stops every one, since the table may already be gone and the
// fence is the only thing keeping it off the host's own addresses. A fence that
// applies leaves the fenced node alone and stops one outside it, such as a
// host-networked node made before the fence. Other earners keep running either way.
func TestInOnModeAFenceGatedNodeRunsOnlyBehindAnAppliedFence(t *testing.T) {
	earner := func(id, mode string, labels map[string]string) map[string]any {
		return map[string]any{
			"Id": id, "Names": []string{"/cashpilot-" + id}, "State": "running", "Labels": labels,
			"HostConfig":      map[string]any{"NetworkMode": mode},
			"NetworkSettings": map[string]any{"Networks": map[string]any{mode: map[string]any{"NetworkID": mode + "-id"}}},
		}
	}
	gatedLabels := func(slug string) map[string]string {
		return map[string]string{LabelManaged: "true", LabelService: slug, LabelFenceGated: "true"}
	}
	fenced := earner("mysterium", FenceNetwork, gatedLabels("mysterium"))
	hosted := earner("oldmyst", "host", gatedLabels("oldmyst"))
	plain := earner("honeygain", FenceNetwork, map[string]string{LabelManaged: "true", LabelService: "honeygain"})

	for _, tc := range []struct {
		exit                     int
		state                    string
		fencedStops, hostedStops bool
	}{
		{exit: 1, state: "failed", fencedStops: true, hostedStops: true},
		{exit: 0, state: "applied", fencedStops: false, hostedStops: true},
	} {
		fake := &fakeDocker{earners: []map[string]any{fenced, hosted, plain}, fenceExit: tc.exit}
		fake.start(t)
		p := &DockerProvider{}
		p.SetIsolationSetting(func() string { return "on" })
		p.ReconcileFence(context.Background())
		st := p.FenceStatus()
		stopped := map[string]bool{}
		for _, call := range fake.recorded() {
			for _, id := range []string{"mysterium", "oldmyst", "honeygain"} {
				if call == "POST /containers/"+id+"/stop" {
					stopped[id] = true
				}
			}
		}
		if st.Mode != FenceModeOn || st.State != tc.state {
			t.Fatalf("helper exit %d: status %+v, want on and %s", tc.exit, st, tc.state)
		}
		if stopped["honeygain"] {
			t.Errorf("helper exit %d: an earner that is not fence-gated was stopped", tc.exit)
		}
		if stopped["mysterium"] != tc.fencedStops {
			t.Errorf("helper exit %d (%s): the fenced node stopped = %v, want %v (calls %v)", tc.exit, st.Error, stopped["mysterium"], tc.fencedStops, fake.recorded())
		}
		if stopped["oldmyst"] != tc.hostedStops {
			t.Errorf("helper exit %d: the host-networked node stopped = %v, want %v (calls %v)", tc.exit, stopped["oldmyst"], tc.hostedStops, fake.recorded())
		}
	}
}
