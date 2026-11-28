package winvm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// A session serializes its own queries, as SWbemServices does. Sharing this
// session between probes also models the old Client.Query process-wide lock.
type wmiFixtureSession struct {
	mu      sync.Mutex
	started chan<- struct{}
	unblock <-chan struct{}
}

func (s *wmiFixtureSession) Query(query string, rows any, _ ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.Contains(query, "Win32_OptionalFeature"):
		close(s.started)
		<-s.unblock
		return nil
	case strings.Contains(query, "Win32_ComputerSystem"):
		return json.Unmarshal([]byte(`[{"HypervisorPresent":true,"Manufacturer":"Microsoft Corporation","Model":"Virtual Machine"}]`), rows)
	case strings.Contains(query, "Win32_OperatingSystem"):
		return json.Unmarshal([]byte(`[{"LastBootUpTime":"2026-11-26T04:21:39+01:00"}]`), rows)
	case strings.Contains(query, "Win32_Processor"):
		return json.Unmarshal([]byte(`[{"Architecture":9}]`), rows)
	default:
		return fmt.Errorf("unexpected WMI query %q", query)
	}
}

func (*wmiFixtureSession) Close() error { return nil }

type stalledFeatureProbes struct {
	fakeProbes
	wmi       *wmiProbes
	started   <-chan struct{}
	readsDone *sync.WaitGroup
}

func (p stalledFeatureProbes) cpu(ctx context.Context) (func(*HostFacts), error) {
	defer p.readsDone.Done()
	// Give the feature provider the first query, so a shared lock blocks CPU.
	select {
	case <-p.started:
		return p.wmi.cpu(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p stalledFeatureProbes) features(ctx context.Context) (func(*HostFacts), error) {
	defer p.readsDone.Done()
	return p.wmi.features(ctx)
}

func TestUnelevatedWMIKeepsCPUAndBootWhenFeaturesStall(t *testing.T) {
	started, unblock := make(chan struct{}), make(chan struct{})
	var readsDone sync.WaitGroup
	readsDone.Add(2)
	defer func() {
		close(unblock)
		readsDone.Wait()
	}()

	wmi := &wmiProbes{open: func() (wmiSession, error) {
		return &wmiFixtureSession{started: started, unblock: unblock}, nil
	}}
	p := stalledFeatureProbes{wmi: wmi, started: started, readsDone: &readsDone}
	f := snapshot(context.Background(), p, "windows", time.Second)
	if f.HypervisorPresent != Yes || f.Arch != "x64" || f.LastBoot != "2026-11-26T03:21:39Z" || f.IsVM != Yes {
		t.Errorf("CPU and boot facts stalled with the feature query: arch=%s hypervisor=%s boot=%q VM=%s problems=%v",
			f.Arch, f.HypervisorPresent, f.LastBoot, f.IsVM, f.Problems)
	}
	if f.VMPlatformFeature != Unknown || f.Problems["features"] != "timed out" {
		t.Errorf("stalled feature query: feature=%s problems=%v", f.VMPlatformFeature, f.Problems)
	}
}
