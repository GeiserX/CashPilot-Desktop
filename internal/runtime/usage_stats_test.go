package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// statsJSONWithNet is a stats body with network interfaces; nets nil means the
// response has no "networks" key, which is what Docker sends on host networking.
func statsJSONWithNet(t *testing.T, mem uint64, nets map[string]container.NetworkStats) string {
	t.Helper()
	var sr container.StatsResponse
	sr.CPUStats.CPUUsage.TotalUsage = 1_000
	sr.CPUStats.SystemUsage = 1_000_000
	sr.CPUStats.OnlineCPUs = 2
	sr.MemoryStats.Usage = mem
	sr.Networks = nets
	b, err := json.Marshal(sr)
	if err != nil {
		t.Fatalf("marshal stats: %v", err)
	}
	return string(b)
}

// Every interface counts: a container on two networks moved the sum of both.
func TestSampleFromResponseSumsEveryNetwork(t *testing.T) {
	var sr container.StatsResponse
	sr.Networks = map[string]container.NetworkStats{
		"eth0": {RxBytes: 1000, TxBytes: 200},
		"eth1": {RxBytes: 50, TxBytes: 7},
	}
	got := sampleFromResponse(sr)
	if !got.hasNet || got.netRx != 1050 || got.netTx != 207 {
		t.Fatalf("sample = %+v, want hasNet with rx 1050 tx 207", got)
	}
}

// Host networking: no networks at all is "no counters", not zero traffic.
func TestSampleFromResponseWithoutNetworksHasNoCounters(t *testing.T) {
	if got := sampleFromResponse(container.StatsResponse{}); got.hasNet {
		t.Fatalf("sample without networks = %+v, want hasNet false", got)
	}
}

// The list the usage sampler reads carries the counters of a bridged container,
// nil for a host-network one, and says which numbers were really read.
func TestStatsForContainersCarriesNetworkCounters(t *testing.T) {
	bridged := statsJSONWithNet(t, 64*mib, map[string]container.NetworkStats{"eth0": {RxBytes: 4096, TxBytes: 512}})
	p := &DockerProvider{}
	out := p.statsForContainers(context.Background(), &fakeStatsClient{seq: []fakeStat{{body: bridged}}}, []container.Summary{
		{ID: "id-a", Names: []string{"/cashpilot-alpha"}, State: "running", Labels: map[string]string{LabelService: "alpha"}},
	})
	a := out[0]
	if a.NetRxBytes == nil || a.NetTxBytes == nil || *a.NetRxBytes != 4096 || *a.NetTxBytes != 512 {
		t.Fatalf("bridged counters = %v/%v, want 4096/512", a.NetRxBytes, a.NetTxBytes)
	}
	if !a.CPUKnown || !a.MemoryKnown {
		t.Fatalf("bridged known = cpu %v mem %v, want both true", a.CPUKnown, a.MemoryKnown)
	}

	host := &fakeStatsClient{seq: []fakeStat{{body: statsJSONWithNet(t, 64*mib, nil)}}}
	out = p.statsForContainers(context.Background(), host, []container.Summary{
		{ID: "id-h", Names: []string{"/cashpilot-myst"}, State: "running", Labels: map[string]string{LabelService: "mysterium"}},
	})
	if out[0].NetRxBytes != nil || out[0].NetTxBytes != nil {
		t.Fatalf("host-network counters = %v/%v, want nil (unknown)", out[0].NetRxBytes, out[0].NetTxBytes)
	}
}

// A failed stats read reports 0 in the fields, so it must say it read nothing.
func TestReadFailureIsNotAReading(t *testing.T) {
	p := &DockerProvider{}
	r := p.read(context.Background(), &fakeStatsClient{seq: []fakeStat{{err: errors.New("boom")}}}, "id", true)
	if r.cpuOK || r.memOK || r.rx != nil {
		t.Fatalf("failed read = %+v, want nothing known", r)
	}
	// Sample B fails: memory and counters from A are real, CPU is not.
	f := &fakeStatsClient{seq: []fakeStat{
		{body: statsJSONWithNet(t, 32*mib, map[string]container.NetworkStats{"eth0": {RxBytes: 9, TxBytes: 3}})},
		{err: errors.New("gone")},
	}}
	r = p.read(context.Background(), f, "id", true)
	if r.cpuOK || !r.memOK || r.rx == nil || *r.rx != 9 {
		t.Fatalf("half read = %+v, want memory and counters only", r)
	}
}
