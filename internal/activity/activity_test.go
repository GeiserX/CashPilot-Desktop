package activity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
)

func earner(id, name, mode string) Container {
	return Container{ID: id, Name: name, NetworkMode: mode, Labels: map[string]string{"cashpilot.managed": "true"}}
}

func joinableDefault() map[string]bool { return JoinableRuntimes(nil) }

func TestPlanHostNetworkEarnersAreMatchedByCgroup(t *testing.T) {
	host := earner("h1", "cashpilot-mysterium", "host")
	side := earner("s1", "side", "container:h1")
	bridge := earner("b1", "cashpilot-honeygain", "bridge")
	lookup := func(id string) (Container, bool) {
		for _, c := range []Container{host, side, bridge} {
			if c.ID == id || c.Name == id {
				return c, true
			}
		}
		return Container{}, false
	}
	groups, fixed := Plan([]Container{host, side, bridge}, lookup, joinableDefault())
	if len(fixed) != 0 {
		t.Fatalf("nothing should be fixed, got %+v", fixed)
	}
	if g := groups["h1"]; g == nil || g.Kind != "cgroup" || g.Owner.ID != "h1" {
		t.Fatalf("host earner must be its own cgroup group, got %+v", g)
	}
	if g := groups["s1"]; g == nil || g.Kind != "cgroup" || g.Owner.ID != "s1" {
		t.Fatalf("a member joined to a host-network container is matched by its own cgroup, got %+v", g)
	}
	if g := groups["b1"]; g == nil || g.Kind != "netns" {
		t.Fatalf("bridge earner joins its namespace, got %+v", g)
	}
}

func TestPlanNoneSandboxedAndMissingOwner(t *testing.T) {
	none := earner("n1", "n", "none")
	gv := earner("g1", "g", "bridge")
	gv.Runtime = "runsc"
	lost := earner("l1", "l", "container:gone")
	lookup := func(string) (Container, bool) { return Container{}, false }
	groups, fixed := Plan([]Container{none, gv, lost}, lookup, joinableDefault())
	if len(groups) != 0 {
		t.Fatalf("no group expected, got %v", groups)
	}
	got := map[string]string{}
	for _, s := range fixed {
		got[s.Container] = s.Status + "/" + s.Reason
	}
	want := map[string]string{"n": "no_network/", "g": "sandboxed/", "l": "probe_failed/owner_missing"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}
}

func TestJoinableRuntimes(t *testing.T) {
	j := JoinableRuntimes(map[string]RuntimeSpec{
		"nvidia":        {Path: "/usr/bin/nvidia-container-runtime"},
		"runsc":         {Path: "runsc"},
		"runsc-hostnet": {Path: "/usr/local/bin/runsc", Args: []string{"--network=host"}},
	})
	if !j["nvidia"] || j["runsc"] || !j["runsc-hostnet"] || !j["runc"] {
		t.Fatalf("got %v", j)
	}
}

func TestProbeConfigKeepsTheFloor(t *testing.T) {
	for _, kind := range []string{"netns", "cgroup"} {
		cfg, hc, err := ProbeConfig(ProbeSpec{Name: "x", OwnerID: strings.Repeat("a", 64), Kind: kind, Worker: "desktop-1"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.User != "65534:65534" || hc.Privileged || !hc.ReadonlyRootfs || len(hc.CapDrop) != 1 || hc.CapDrop[0] != "ALL" ||
			len(hc.CapAdd) != 0 || len(hc.SecurityOpt) != 1 || hc.SecurityOpt[0] != "no-new-privileges:true" ||
			hc.Resources.PidsLimit == nil || *hc.Resources.PidsLimit != 32 || hc.Resources.Memory != 48<<20 ||
			hc.Tmpfs["/data"] != "size=64k" || len(hc.Mounts) != 0 || len(hc.Binds) != 0 || len(hc.Devices) != 0 ||
			hc.LogConfig.Type != "json-file" {
			t.Fatalf("%s: the helper lost part of its floor: cfg=%+v hc=%+v", kind, cfg, hc)
		}
		if cfg.Labels[ProbeLabel] != ProbeKind || cfg.Labels[WorkerLabel] != "desktop-1" {
			t.Fatalf("%s: labels %v", kind, cfg.Labels)
		}
		if cfg.Image != HelperImage || !strings.Contains(HelperImage, "@sha256:") {
			t.Fatalf("image must be pinned by digest: %s", cfg.Image)
		}
		env := strings.Join(cfg.Env, "\n")
		if !strings.Contains(env, "PROBE_CTX=") {
			t.Fatalf("%s: no PROBE_CTX", kind)
		}
		switch kind {
		case "netns":
			if string(hc.NetworkMode) != "container:"+strings.Repeat("a", 64) || hc.CgroupnsMode != "" || strings.Contains(env, "PROBE_MODE") {
				t.Fatalf("netns helper: %v %v %s", hc.NetworkMode, hc.CgroupnsMode, env)
			}
		case "cgroup":
			if hc.NetworkMode != "host" || hc.CgroupnsMode != container.CgroupnsModeHost || !strings.Contains(env, "PROBE_MODE=cgroup") ||
				!strings.Contains(env, "PROBE_CONTAINER="+strings.Repeat("a", 64)) {
				t.Fatalf("cgroup helper: %v %v %s", hc.NetworkMode, hc.CgroupnsMode, env)
			}
		}
	}
}

func TestSweepRules(t *testing.T) {
	mine := ProbeRef{Labels: map[string]string{ProbeLabel: ProbeKind, WorkerLabel: "me"}, State: "running"}
	other := ProbeRef{Labels: map[string]string{ProbeLabel: ProbeKind, WorkerLabel: "other"}, State: "running"}
	otherCreated := ProbeRef{Labels: map[string]string{ProbeLabel: ProbeKind, WorkerLabel: "other"}, State: "created"}
	legacyRun := ProbeRef{Labels: map[string]string{ProbeLabel: ProbeKind}, State: "running"}
	legacyDone := ProbeRef{Labels: map[string]string{ProbeLabel: ProbeKind}, State: "exited"}
	isolation := ProbeRef{Labels: map[string]string{ProbeLabel: "isolation"}, State: "exited"}
	if !ShouldSweep(mine, "me") || ShouldSweep(other, "me") || ShouldSweep(otherCreated, "me") || ShouldSweep(legacyRun, "me") || !ShouldSweep(legacyDone, "me") || ShouldSweep(isolation, "me") {
		t.Fatal("sweep rules broken")
	}
	// A finished helper of another app is never mid-round, and its log keeps
	// that round's addresses, so any app removes it.
	for _, state := range []string{"exited", "dead"} {
		done := ProbeRef{Labels: map[string]string{ProbeLabel: ProbeKind, WorkerLabel: "other"}, State: state}
		if !ShouldSweep(done, "me") {
			t.Fatalf("a %s helper of another app must be swept", state)
		}
	}
	if ProbeName("desktop-aaa", "x") == ProbeName("desktop-bbb", "x") {
		t.Fatal("two apps on one host must get different helper names")
	}
}

func probeJSONText(extra string) []byte {
	return []byte(`{"v":1,"netns":"net:[1]","classified":true,` + extra + `"snmp_end":{"tcp":{"ActiveOpens":10,"PassiveOpens":2,"AttemptFails":1,"EstabResets":0,"CurrEstab":3},"udp":{"InDatagrams":5,"OutDatagrams":6}},
	"summary":{"est_max":3,"syn_sent_max":0,"udp_bound":1,"listen_ports":[443,443,99999],"remotes_total":3,"truncated":false,
	"out":[["tcp","::ffff:1.2.3.4",25,"public",2,{"est":1,"bogus":4}],["tcp","1.2.3.4",80,"martian",1,{}],["icmp","1.1.1.1",1,"public",1,{}]],
	"in":[["192.168.1.5",443,"lan",1],["nope",1,"lan",1]]}}`)
}

func TestParseProbeDropsWhatProcnetNeverPrints(t *testing.T) {
	r, err := ParseProbe(probeJSONText(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Out) != 1 || r.Out[0].IP != "1.2.3.4" || r.Out[0].States["est"] != 1 || r.Out[0].States["bogus"] != 0 {
		t.Fatalf("out: %+v", r.Out)
	}
	if len(r.In) != 1 || r.In[0].IP != "192.168.1.5" {
		t.Fatalf("in: %+v", r.In)
	}
	if len(r.ListenPorts) != 1 || r.ListenPorts[0] != 443 {
		t.Fatalf("ports: %v", r.ListenPorts)
	}
	if r.Counters == nil || r.Counters[0] != 10 || r.Counters[5] != 6 {
		t.Fatalf("counters: %v", r.Counters)
	}
	if _, err := ParseProbe([]byte(`{"v":1,"summary":{}}`)); err == nil {
		t.Fatal("an unclassified reading must be refused")
	}
	if _, err := ParseProbe([]byte(`not json`)); err == nil {
		t.Fatal("garbage must be refused")
	}
}

func TestBuildSampleHostCgroup(t *testing.T) {
	r, _ := ParseProbe(probeJSONText(`"attribution":"cgroup",`))
	d := NewDeltas()
	g := &Group{Owner: Container{ID: "h"}, Members: []string{"myst"}, Mode: "host", Kind: "cgroup", OwnerEarner: true}
	s := BuildSample("myst", g, r, d, 1000)
	if s.Status != "ok" || s.Reason != "host_cgroup" || len(s.Out) != 1 {
		t.Fatalf("got %+v", s)
	}
	if s.Opens != nil || s.UDPIn != nil || s.DeltaSeconds != nil {
		t.Fatal("the host's counters are the machine's: a host-network row has no deltas")
	}
	// Even a second reading must not produce a delta from the host's counters.
	s = BuildSample("myst", g, r, d, 1100)
	if s.Opens != nil {
		t.Fatal("second host reading produced a delta")
	}
	failed, _ := ParseProbe(probeJSONText(`"attribution":"cgroup","reason":"cgroup_v1",`))
	s = BuildSample("myst", g, failed, d, 1200)
	if s.Status != "host_network" || s.Reason != "cgroup_v1" || len(s.Out) != 0 {
		t.Fatalf("a helper that could not attribute shows host_network with its reason, got %+v", s)
	}
}

// A host-network reading is trusted only when the helper says it matched by
// cgroup: a netns reading of the host is every program's sockets, not the
// earner's. An unknown reason never reaches the page as-is. The web's
// activity._sample applies the same two rules.
func TestBuildSampleHostNeedsCgroupAttribution(t *testing.T) {
	g := &Group{Owner: Container{ID: "h"}, Members: []string{"myst"}, Mode: "host", Kind: "cgroup", OwnerEarner: true}
	for _, tc := range []struct{ fields, reason string }{
		{``, "cgroup_missing"},
		{`"attribution":"netns",`, "cgroup_missing"},
		{`"attribution":"cgroup","reason":"made_up",`, "cgroup_missing"},
		{`"attribution":"cgroup","reason":"sock_diag_refused",`, "sock_diag_refused"},
	} {
		r, err := ParseProbe(probeJSONText(tc.fields))
		if err != nil {
			t.Fatalf("%s: %v", tc.fields, err)
		}
		s := BuildSample("myst", g, r, NewDeltas(), 1000)
		if s.Status != "host_network" || s.Reason != tc.reason || len(s.Out) != 0 || len(s.In) != 0 {
			t.Fatalf("%s: got status %q reason %q out %d", tc.fields, s.Status, s.Reason, len(s.Out))
		}
	}
}

func TestBuildSampleSharedNamespaceKeepsNoDestinations(t *testing.T) {
	r, _ := ParseProbe(probeJSONText(""))
	s := BuildSample("vpn", &Group{Owner: Container{ID: "v"}, Members: []string{"a"}, Mode: "bridge", Kind: "netns", OwnerEarner: false}, r, NewDeltas(), 1)
	if s.Reason != "shared_namespace" || len(s.Out) != 0 || len(s.In) != 0 {
		t.Fatalf("got %+v", s)
	}
}

func TestDeltas(t *testing.T) {
	d := NewDeltas()
	a := [6]int64{10, 1, 0, 0, 5, 5}
	var s Sample
	d.Apply("o", "net:[1]", &a, 100, &s)
	if s.Opens != nil {
		t.Fatal("first reading has nothing to compare with")
	}
	b := [6]int64{15, 2, 1, 0, 7, -1}
	s = Sample{}
	d.Apply("o", "net:[1]", &b, 400, &s)
	if s.Opens == nil || *s.Opens != 5 || s.UDPIn == nil || *s.UDPIn != 2 || s.UDPOut != nil {
		t.Fatalf("got %+v", s)
	}
	s = Sample{}
	d.Apply("o", "net:[2]", &b, 500, &s)
	if s.Opens != nil {
		t.Fatal("a new namespace must not be compared with the old one")
	}
	s = Sample{}
	d.Apply("o", "net:[2]", &b, 500+MaxDeltaSeconds+1, &s)
	if s.Opens != nil {
		t.Fatal("a long gap gives no delta")
	}
	back := [6]int64{1, 0, 0, 0, 0, 0}
	s = Sample{}
	d.Apply("o", "net:[2]", &back, 1200, &s)
	if s.Opens != nil {
		t.Fatal("a counter going backwards gives no delta")
	}
}

func TestAlerts(t *testing.T) {
	w := Window{Samples: []Sample{{Container: "x", Out: []Dest{
		{Proto: "tcp", IP: "1.2.3.4", Port: 25, Class: "public", Seen: 1, States: map[string]int{"closed": 1}},
		{Proto: "tcp", IP: "192.168.1.1", Port: 80, Class: "lan", Seen: 1, States: map[string]int{"syn_sent": 1}},
		{Proto: "udp", IP: "10.0.0.1", Port: 53, Class: "infra", Seen: 1},
		{Proto: "tcp", IP: "172.17.0.3", Port: 1, Class: "peer", Seen: 1},
	}}}}
	f := AlertFindings(w)
	if len(f) != 2 || f[0].Kind != "activity_mail" || f[1].Kind != "activity_lan" || f[1].Detail != "lan" || !strings.Contains(f[1].Message, "pending") {
		t.Fatalf("got %+v", f)
	}
	for _, x := range f {
		if strings.Contains(x.Message, "1.2.3.4") || strings.Contains(x.Message, "192.168") {
			t.Fatalf("an alert named an address: %s", x.Message)
		}
	}
}

func TestSpike(t *testing.T) {
	one := 1.0
	var hist []*float64
	for i := 0; i < SpikeMinHours-1; i++ {
		hist = append(hist, &one)
	}
	big := 3000.0
	if Spike(hist, &big, SpikeOpensFloor) {
		t.Fatal("needs 72 known hours")
	}
	hist = append(hist, nil, &one)
	if !Spike(hist, &big, SpikeOpensFloor) {
		t.Fatal("72 known hours and 3000 over a median of 1 is a spike")
	}
	small := 1500.0
	if Spike(hist, &small, SpikeOpensFloor) {
		t.Fatal("under the floor is no spike")
	}
}

func TestFit(t *testing.T) {
	w := Window{}
	for i := 0; i < 20; i++ {
		s := Sample{Container: "c"}
		for j := 0; j < 30; j++ {
			s.Out = append(s.Out, Dest{IP: "1.1.1.1"})
		}
		w.Samples = append(w.Samples, s)
	}
	Fit(&w)
	total := 0
	for _, s := range w.Samples {
		total += len(s.Out) + len(s.In)
	}
	if total != MaxDestinations || !w.Samples[19].Truncated || w.Samples[0].Truncated {
		t.Fatalf("total %d", total)
	}
}

// fakeEngine records what a round did.
type fakeEngine struct {
	mu       sync.Mutex
	earners  []Container
	probes   []ProbeRef
	removed  []string
	specs    []ProbeSpec
	output   map[string][]byte
	failRun  map[string]error
	noImage  bool
	leftover bool
	// stallImage blocks EnsureImage until its context ends, as a stalled pull does.
	stallImage bool
}

func (f *fakeEngine) Earners(context.Context) ([]Container, error) { return f.earners, nil }
func (f *fakeEngine) Inspect(_ context.Context, id string) (Container, bool) {
	for _, c := range f.earners {
		if c.ID == id || c.Name == id {
			return c, true
		}
	}
	return Container{}, false
}
func (f *fakeEngine) Runtimes(context.Context) map[string]RuntimeSpec { return nil }
func (f *fakeEngine) Network(context.Context, string, map[string]bool) NetworkInfo {
	return NetworkInfo{Driver: "bridge"}
}
func (f *fakeEngine) Probes(context.Context) ([]ProbeRef, error) { return f.probes, nil }
func (f *fakeEngine) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return nil
}
func (f *fakeEngine) EnsureImage(ctx context.Context, _ string) error {
	if f.stallImage {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.noImage {
		return errors.New("no")
	}
	return nil
}
func (f *fakeEngine) RunProbe(_ context.Context, spec ProbeSpec) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	if err := f.failRun[spec.OwnerID]; err != nil {
		return nil, f.leftover, err
	}
	return f.output[spec.OwnerID], f.leftover, nil
}

func TestRoundProbesHostEarnerByCgroupAndSweepsOnlyOurs(t *testing.T) {
	host := earner(strings.Repeat("a", 64), "cashpilot-mysterium", "host")
	br := earner(strings.Repeat("b", 64), "cashpilot-honeygain", "bridge")
	f := &fakeEngine{
		earners: []Container{host, br},
		probes: []ProbeRef{
			{ID: "p-mine", Labels: map[string]string{ProbeLabel: ProbeKind, WorkerLabel: "me"}, State: "exited"},
			{ID: "p-other", Labels: map[string]string{ProbeLabel: ProbeKind, WorkerLabel: "other"}, State: "running"},
		},
		output: map[string][]byte{
			host.ID: probeJSONText(`"attribution":"cgroup",`),
			br.ID:   probeJSONText(""),
		},
	}
	r := &Runner{Engine: f, Worker: "me", Deltas: NewDeltas()}
	w, err := r.Round(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.removed) != 1 || f.removed[0] != "p-mine" {
		t.Fatalf("the sweep must remove only our helper, removed %v", f.removed)
	}
	kinds := map[string]string{}
	for _, s := range f.specs {
		kinds[s.OwnerID] = s.Kind
		if s.Worker != "me" {
			t.Fatalf("spec without our worker label: %+v", s)
		}
	}
	if kinds[host.ID] != "cgroup" || kinds[br.ID] != "netns" {
		t.Fatalf("kinds %v", kinds)
	}
	if len(w.Samples) != 2 || w.Samples[0].Container != "cashpilot-honeygain" || w.Samples[1].Reason != "host_cgroup" {
		t.Fatalf("samples %+v", w.Samples)
	}
}

// A stalled image pull must end with the round, not hold the sampler loop.
func TestRoundTimeoutEndsAStalledRound(t *testing.T) {
	old := RoundTimeout
	RoundTimeout = 50 * time.Millisecond
	t.Cleanup(func() { RoundTimeout = old })
	f := &fakeEngine{earners: []Container{earner(strings.Repeat("b", 64), "e", "bridge")}, stallImage: true}
	done := make(chan Window, 1)
	go func() {
		w, _ := (&Runner{Engine: f, Worker: "me", Deltas: NewDeltas()}).Round(context.Background())
		done <- w
	}()
	select {
	case w := <-done:
		if w.WorkerStatus != "no_image" || len(f.specs) != 0 {
			t.Fatalf("got %+v", w)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the round outlived RoundTimeout")
	}
}

func TestRoundFailures(t *testing.T) {
	br := earner(strings.Repeat("b", 64), "e", "bridge")
	f := &fakeEngine{earners: []Container{br}, failRun: map[string]error{br.ID: &ProbeError{"timeout"}}, leftover: true}
	w, _ := (&Runner{Engine: f, Worker: "me", Deltas: NewDeltas()}).Round(context.Background())
	if len(w.Samples) != 1 || w.Samples[0].Status != "probe_failed" || w.Samples[0].Reason != "timeout" || len(w.LeftoverProbes) != 1 {
		t.Fatalf("got %+v", w)
	}
	f = &fakeEngine{earners: []Container{br}, noImage: true}
	w, _ = (&Runner{Engine: f, Worker: "me", Deltas: NewDeltas()}).Round(context.Background())
	if w.WorkerStatus != "no_image" || len(f.specs) != 0 {
		t.Fatalf("got %+v", w)
	}
}

func TestBuildContext(t *testing.T) {
	c := Container{DNS: []string{"9.9.9.9"}, Networks: []Network{{ID: "n", Gateway: "172.18.0.1", IPAddress: "172.18.0.2", IPPrefixLen: 16}}}
	ctx := BuildContext(c, "netns", map[string]NetworkInfo{}, func(string) NetworkInfo { return NetworkInfo{Driver: "bridge", EarnerIPs: []string{"172.18.0.3"}} })
	b, _ := json.Marshal(ctx)
	want := `{"gateways":["172.18.0.1"],"dns":["9.9.9.9"],"subnets":[["172.18.0.2/16","bridge"]],"peers":["172.18.0.2","172.18.0.3"]}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
	host := BuildContext(c, "cgroup", map[string]NetworkInfo{}, nil)
	if len(host.Gateways)+len(host.Subnets)+len(host.Peers) != 0 {
		t.Fatalf("a host-network earner has no bridge: %+v", host)
	}
}

// The helper script is the web repository's app/procnet.py, byte for byte. This is
// the local shortcut; catalogsync -check (the weekly drift run) is the CI gate.
func TestProcnetIsTheWebCopy(t *testing.T) {
	web := filepath.Join("..", "..", "..", "CashPilot", "app", "procnet.py")
	if src := os.Getenv("CASHPILOT_WEB"); src != "" {
		web = filepath.Join(src, "app", "procnet.py")
	}
	want, err := os.ReadFile(web)
	if err != nil {
		t.Skip("no web checkout beside this one")
	}
	if sha256.Sum256(want) != sha256.Sum256([]byte(ProcnetScript)) {
		t.Fatalf("internal/activity/procnet.py differs from %s: run make catalog-sync", web)
	}
}
