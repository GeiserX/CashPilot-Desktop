package health

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// deadRelay is half an hour of the ProxyBase client retrying a relay that no longer
// answers, the case its catalog signal restarts.
var deadRelay = strings.Repeat("Failed to connect to 203.0.113.7:2931, retrying in 5 seconds.\n", 120)

// fakeRestarter is a runtime that can read a window and restart a service.
type fakeRestarter struct {
	fakeWindow
	rmu      sync.Mutex
	restarts []string
	listed   []time.Time
	fail     error
}

func (f *fakeRestarter) RestartStuck(_ context.Context, slug string, listedAt time.Time, reason string) error {
	f.rmu.Lock()
	defer f.rmu.Unlock()
	f.restarts = append(f.restarts, slug+": "+reason)
	f.listed = append(f.listed, listedAt)
	return f.fail
}

func (f *fakeRestarter) count() int {
	f.rmu.Lock()
	defer f.rmu.Unlock()
	return len(f.restarts)
}

// check runs one dashboard refresh for slug and waits for the background check.
func check(t *testing.T, logs LogReader, svc catalog.Service, state string) Report {
	t.Helper()
	lookup := fakeCatalog{"proxybase": svc}
	deployed := []Deployed{{Slug: "proxybase", ContainerState: state, Runtime: "docker"}}
	Collect(context.Background(), logs, lookup, deployed)
	windows.wg.Wait()
	return Collect(context.Background(), logs, lookup, deployed)["proxybase"]
}

func TestAProxyBaseCopyOnADeadRelayIsRestartedAtMostEverySixHours(t *testing.T) {
	now := freshWindows(t)
	svc := realService(t, "proxybase")
	r := &fakeRestarter{}
	r.set(deadRelay, false)
	before := time.Now()
	got := check(t, r, svc, "running")
	if got.State != StateFailing {
		t.Fatalf("state = %s, want failing: %s", got.State, reasonText(got))
	}
	if r.count() != 1 {
		t.Fatalf("restarts = %v, want one", r.restarts)
	}
	if !strings.HasPrefix(r.restarts[0], "proxybase: ProxyBase has not reached its relay") {
		t.Errorf("restart reason = %q, want the signal's means", r.restarts[0])
	}
	if r.listed[0].Before(before) {
		t.Errorf("listedAt %v is before the check began (%v)", r.listed[0], before)
	}

	// Still stuck at the next check, inside the wait: no second restart.
	*now = now.Add(windowEvery)
	check(t, r, svc, "running")
	if r.count() != 1 {
		t.Fatalf("restarted again within %s: %v", restartEvery, r.restarts)
	}
	// Still stuck once the wait is over: one more.
	*now = now.Add(restartEvery)
	check(t, r, svc, "running")
	if r.count() != 2 {
		t.Fatalf("restarts after the wait = %d, want 2", r.count())
	}
}

func TestAFailedRestartWaitsAsLongAsAGoodOne(t *testing.T) {
	now := freshWindows(t)
	svc := realService(t, "proxybase")
	r := &fakeRestarter{fail: errors.New("the container runtime is not answering")}
	r.set(deadRelay, false)
	check(t, r, svc, "running")
	*now = now.Add(windowEvery)
	check(t, r, svc, "running")
	if r.count() != 1 {
		t.Fatalf("a failed restart was tried again within %s: %v", restartEvery, r.restarts)
	}
}

func TestACopyThatEarnsIsNeverRestarted(t *testing.T) {
	freshWindows(t)
	svc := realService(t, "proxybase")
	r := &fakeRestarter{}
	r.set(deadRelay+"Balance: $0.42\n", false)
	if got := check(t, r, svc, "running"); got.State == StateFailing {
		t.Fatalf("flagged a copy that logs its balance: %s", reasonText(got))
	}
	if r.count() != 0 {
		t.Fatalf("restarted a copy that earns: %v", r.restarts)
	}
}

func TestOnlyASignalThatSaysRestartRestarts(t *testing.T) {
	freshWindows(t)
	svc := realService(t, "proxybase")
	for i := range svc.Docker.HealthSignals {
		svc.Docker.HealthSignals[i].Restart = false
	}
	r := &fakeRestarter{}
	r.set(deadRelay, false)
	if got := check(t, r, svc, "running"); got.State != StateFailing {
		t.Fatalf("precondition: state = %s", got.State)
	}
	if r.count() != 0 {
		t.Fatalf("restarted on a signal without restart: true: %v", r.restarts)
	}
}

func TestAFailedReadRestartsNothing(t *testing.T) {
	freshWindows(t)
	r := &fakeRestarter{}
	r.set(deadRelay, true)
	check(t, r, realService(t, "proxybase"), "running")
	if r.count() != 0 {
		t.Fatalf("restarted on a failed read: %v", r.restarts)
	}
}

func TestAStoppedCopyIsNeverChecked(t *testing.T) {
	freshWindows(t)
	r := &fakeRestarter{}
	r.set(deadRelay, false)
	check(t, r, realService(t, "proxybase"), "exited")
	if r.count() != 0 {
		t.Fatalf("restarted a stopped copy: %v", r.restarts)
	}
}
