package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// Route at deploy (routes.go): a service routed through a proxy from its first start
// never runs from this machine's own IP, until the owner confirms the switch.

func routedExample(t *testing.T) (*Manager, *fakeProxyProvider, int64) {
	t.Helper()
	m, p, st := newProxyManager(t)
	view, err := m.SaveProxy(context.Background(), 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "1080"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCredentials("example", map[string]string{"TOKEN": "tok"}); err != nil {
		t.Fatal(err)
	}
	return m, p, view.ID
}

func TestARoutedDeployRunsOnlyAsItsCopyWithItsOwnDevice(t *testing.T) {
	m, p, id := routedExample(t)
	result, err := m.DeployRouted(context.Background(), "example", map[string]string{"TOKEN": "tok"}, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.deployCalls != 0 {
		t.Fatalf("a routed deploy must start no container on this machine's own network: %d", p.deployCalls)
	}
	if len(p.deploys) != 1 || !p.deploys[0].Routed || p.deploys[0].Env["TOKEN"] != "tok" {
		t.Fatalf("the copy is the routed service, with the deploy's own settings: %+v", p.deploys)
	}
	if result.ExitIP != "203.0.113.7" {
		t.Fatalf("result: %+v", result)
	}
	if got, ok := m.Route("example"); !ok || got != id {
		t.Fatalf("it runs as its route: %d %v", got, ok)
	}
}

func TestADirectDeployOfARoutedServiceIsRefusedUntilConfirmed(t *testing.T) {
	m, p, id := routedExample(t)
	ctx := context.Background()
	if _, err := m.DeployRouted(ctx, "example", map[string]string{"TOKEN": "tok"}, id); err != nil {
		t.Fatal(err)
	}
	_, err := m.DeployWith(ctx, "example", map[string]string{"TOKEN": "tok"}, DeployOptions{})
	if !IsRouteLocked(err) {
		t.Fatalf("a direct deploy of a routed service must be refused: %v", err)
	}
	if p.deployCalls != 0 || len(p.removed) != 0 {
		t.Fatalf("a refusal changes nothing: deploys %d removed %v", p.deployCalls, p.removed)
	}
	// Removing the service does not lift it: the route stays recorded.
	if err := m.Remove(ctx, "example", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeployWith(ctx, "example", map[string]string{"TOKEN": "tok"}, DeployOptions{}); !IsRouteLocked(err) {
		t.Fatalf("a removal and a fresh deploy must still ask: %v", err)
	}
}

func TestLeavingARouteRemovesTheCopyFirstAndForgetsTheRoute(t *testing.T) {
	m, p, id := routedExample(t)
	ctx := context.Background()
	if _, err := m.DeployRouted(ctx, "example", map[string]string{"TOKEN": "tok"}, id); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeployWith(ctx, "example", map[string]string{"TOKEN": "tok"}, DeployOptions{LeaveRoute: true}); err != nil {
		t.Fatal(err)
	}
	if len(p.removed) != 1 || p.removed[0] != key("example", id) || p.deployCalls != 1 {
		t.Fatalf("the routed copy goes before the direct one starts: removed %v deploys %d", p.removed, p.deployCalls)
	}
	if _, routed, _ := m.store.GetRoute("example"); routed {
		t.Fatal("the route is forgotten once the service runs from this machine's IP")
	}
	if _, err := m.DeployWith(ctx, "example", map[string]string{"TOKEN": "tok"}, DeployOptions{}); err != nil {
		t.Fatalf("a later redeploy needs no confirmation: %v", err)
	}
}

func TestProxiedOnlyRefusesEveryDirectDeploy(t *testing.T) {
	t.Setenv(ProxiedOnlyEnv, "true")
	m, p, id := routedExample(t)
	ctx := context.Background()
	for _, leave := range []bool{false, true} {
		var re *RouteError
		if _, err := m.DeployWith(ctx, "example", map[string]string{"TOKEN": "tok"}, DeployOptions{LeaveRoute: leave}); !errors.As(err, &re) || re.Code != "proxied_only" {
			t.Fatalf("leave=%v: %v", leave, err)
		}
	}
	if p.deployCalls != 0 {
		t.Fatalf("nothing starts from this machine's own IP: %d", p.deployCalls)
	}
	if _, err := m.DeployRouted(ctx, "example", map[string]string{"TOKEN": "tok"}, id); err != nil {
		t.Fatalf("a routed deploy still runs: %v", err)
	}
}

func TestProxiedOnlyIsOffUnlessSet(t *testing.T) {
	for _, v := range []string{"", "false", "0", "no"} {
		t.Setenv(ProxiedOnlyEnv, v)
		if ProxiedOnly() {
			t.Fatalf("%q must not turn it on", v)
		}
	}
	for _, v := range []string{"true", "1", "YES", "on"} {
		t.Setenv(ProxiedOnlyEnv, v)
		if !ProxiedOnly() {
			t.Fatalf("%q must turn it on", v)
		}
	}
}

func TestAFailedRoutedDeployLeavesNoRoute(t *testing.T) {
	m, p, id := routedExample(t)
	p.deployFn = func(runtime.ProxiedDeploy) (runtime.Exit, error) {
		return runtime.Exit{}, errors.New("tunnel did not start")
	}
	if _, err := m.DeployRouted(context.Background(), "example", map[string]string{"TOKEN": "tok"}, id); err == nil {
		t.Fatal("the failure reaches the user")
	}
	if _, routed, _ := m.store.GetRoute("example"); routed {
		t.Fatal("a routed deploy that never ran must not lock the service")
	}
}

func TestARunningDirectServiceIsNotRoutedBesideIt(t *testing.T) {
	m, p, st := newProxyManager(t)
	view, err := m.SaveProxy(context.Background(), 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "1080"})
	if err != nil {
		t.Fatal(err)
	}
	deployExample(t, st)
	if _, err := m.DeployRouted(context.Background(), "example", map[string]string{"TOKEN": "tok"}, view.ID); err == nil || !strings.Contains(err.Error(), "own IP") {
		t.Fatalf("refused: %v", err)
	}
	if len(p.deploys) != 0 {
		t.Fatal("nothing deployed")
	}
}

func TestTheButtonsOfARoutedServiceReachItsCopy(t *testing.T) {
	m, p, id := routedExample(t)
	ctx := context.Background()
	if _, err := m.DeployRouted(ctx, "example", map[string]string{"TOKEN": "tok"}, id); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx, "example"); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, "example"); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(ctx, "example"); err != nil {
		t.Fatal(err)
	}
	k := key("example", id)
	if strings.Join(p.stops, ",") != k+","+k || strings.Join(p.starts, ",") != k+","+k {
		t.Fatalf("stops %v starts %v", p.stops, p.starts)
	}
	if len(p.stopTimeouts) != 0 || len(p.restartTimeouts) != 0 {
		t.Fatal("no container of its own is touched")
	}
	if err := m.Remove(ctx, "example", false, false); err != nil {
		t.Fatal(err)
	}
	if len(p.removed) != 1 || p.removed[0] != k || len(p.removeOpts) != 0 {
		t.Fatalf("remove goes to the copy: %v %v", p.removed, p.removeOpts)
	}
	if _, ok := m.Route("example"); ok {
		t.Fatal("a removed routed service no longer runs as its route")
	}
}

// A container that already exists on this machine's own network: Start, Restart and
// CASHPILOT_PROXIED_ONLY set after it was made.

func TestProxiedOnlyRefusesStartAndRestartOfADirectService(t *testing.T) {
	m, p, st := newProxyManager(t)
	deployExample(t, st)
	t.Setenv(ProxiedOnlyEnv, "true")
	p.startErr = errors.New("the runtime was asked to start it")
	ctx := context.Background()
	for name, act := range map[string]func(context.Context, string) error{"start": m.Start, "restart": m.Restart} {
		var re *RouteError
		if err := act(ctx, "example"); !errors.As(err, &re) || re.Code != "proxied_only" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(p.restartTimeouts) != 0 {
		t.Fatal("the runtime was asked to restart it")
	}
}

func TestARoutedServiceIsNotStartedFromThisMachinesOwnIP(t *testing.T) {
	m, p, st := newProxyManager(t)
	deployExample(t, st)
	if _, _, err := st.SetRoute("example", 1); err != nil {
		t.Fatal(err)
	}
	p.startErr = errors.New("the runtime was asked to start it")
	if err := m.Start(context.Background(), "example"); !IsRouteLocked(err) {
		t.Fatalf("start: %v", err)
	}
	if err := m.Restart(context.Background(), "example"); !IsRouteLocked(err) || len(p.restartTimeouts) != 0 {
		t.Fatalf("restart: %v %v", err, p.restartTimeouts)
	}
}

func TestADirectServiceStartsOnAnOrdinaryMachine(t *testing.T) {
	m, p, st := newProxyManager(t)
	deployExample(t, st)
	if err := m.Start(context.Background(), "example"); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(context.Background(), "example"); err != nil || len(p.restartTimeouts) != 1 {
		t.Fatalf("restart: %v %v", err, p.restartTimeouts)
	}
}

func TestProxiedOnlyRefusesStartOfACopy(t *testing.T) {
	t.Setenv(ProxiedOnlyEnv, "true")
	fake := newCopyFake()
	m := NewManager(fake, newScalingCatalog(t), newTestStore(t))
	var re *RouteError
	if err := m.StartCopy(context.Background(), "scalable", 2); !errors.As(err, &re) || re.Code != "proxied_only" {
		t.Fatalf("start copy: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("the runtime was asked: %v", fake.calls)
	}
}

func TestProxiedOnlyStopsWhatAlreadyRuns(t *testing.T) {
	m, p, st := newProxyManager(t)
	deployExample(t, st)
	p.listResult = []runtime.ContainerInfo{
		{Slug: "example", Name: "cashpilot-example", Status: "running", Instance: 1},
		{Slug: "other", Name: "cashpilot-other", Status: "exited", Instance: 1},
	}
	if got := m.HoldDirect(context.Background()); got != nil || len(p.stopTimeouts) != 0 {
		t.Fatalf("without the setting nothing stops: %v %v", got, p.stopTimeouts)
	}
	t.Setenv(ProxiedOnlyEnv, "true")
	if got := m.HoldDirect(context.Background()); strings.Join(got, ",") != "example" || len(p.stopTimeouts) != 1 {
		t.Fatalf("the running direct service stops, the stopped one is left: %v %v", got, p.stopTimeouts)
	}
	if dep, ok, _ := st.GetDeployment("example"); !ok || dep.Status != "stopped" {
		t.Fatalf("its record says stopped: %+v", dep)
	}
}

func TestProxiedOnlyStopsARunningCopy(t *testing.T) {
	t.Setenv(ProxiedOnlyEnv, "true")
	fake := newCopyFake()
	fake.listResult = []runtime.ContainerInfo{{Slug: "scalable", Status: "running", Instance: 2}}
	m := NewManager(fake, newScalingCatalog(t), newTestStore(t))
	if got := m.HoldDirect(context.Background()); strings.Join(got, ",") != "scalable-2" {
		t.Fatalf("stopped %v", got)
	}
	if strings.Join(fake.calls, ",") != "stop 2" {
		t.Fatalf("calls %v", fake.calls)
	}
}

func TestProxiedOnlyStopsARestartingDirectService(t *testing.T) {
	m, p, st := newProxyManager(t)
	deployExample(t, st)
	p.listResult = []runtime.ContainerInfo{{Slug: "example", Name: "cashpilot-example", Status: "restarting", Instance: 1}}
	t.Setenv(ProxiedOnlyEnv, "true")
	if got := m.HoldDirect(context.Background()); strings.Join(got, ",") != "example" || len(p.stopTimeouts) != 1 {
		t.Fatalf("a restarting container runs again after Docker's backoff, so it stops too: %v %v", got, p.stopTimeouts)
	}
}

// Two deploys of one service in flight: each reads the route and the deployment, then
// writes them after a pull or a tunnel start, so they run one after the other.

func TestARoutedDeployWaitsForADirectDeployInFlight(t *testing.T) {
	m, p, id := routedExample(t)
	ctx := context.Background()
	done := make(chan error, 1)
	p.duringDeploy = func() {
		// The direct deploy is pulling: a routed deploy starts now.
		go func() {
			_, err := m.DeployRouted(ctx, "example", map[string]string{"TOKEN": "tok"}, id)
			done <- err
		}()
		select {
		case err := <-done:
			done <- err
		case <-time.After(200 * time.Millisecond):
		}
	}
	if _, err := m.DeployWith(ctx, "example", map[string]string{"TOKEN": "tok"}, DeployOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "own IP") {
		t.Fatalf("the routed deploy sees the direct one and is refused: %v", err)
	}
	if len(p.deploys) != 0 {
		t.Fatalf("no routed copy beside the direct container: %+v", p.deploys)
	}
	if _, routed, _ := m.store.GetRoute("example"); routed {
		t.Fatal("no route recorded for a service that runs from this machine's IP")
	}
}

func TestARoutedDeployThatFailsKeepsTheRouteOfOneInFlight(t *testing.T) {
	m, p, id := routedExample(t)
	ctx := context.Background()
	calls := 0
	done := make(chan error, 1)
	finishedEarly := false
	p.deployFn = func(runtime.ProxiedDeploy) (runtime.Exit, error) {
		calls++
		if calls > 1 {
			return runtime.Exit{}, errors.New("tunnel did not start")
		}
		// The first deploy is starting its tunnel: a second one starts now, and fails.
		go func() {
			_, err := m.DeployRouted(ctx, "example", map[string]string{"TOKEN": "tok"}, id)
			done <- err
		}()
		select {
		case err := <-done:
			finishedEarly = true
			done <- err
		case <-time.After(200 * time.Millisecond):
		}
		return runtime.Exit{IP: "203.0.113.7", Country: "NL"}, nil
	}
	if _, err := m.DeployRouted(ctx, "example", map[string]string{"TOKEN": "tok"}, id); err != nil {
		t.Fatal(err)
	}
	if finishedEarly {
		t.Fatal("the second deploy ran while the first was in flight")
	}
	if err := <-done; err == nil {
		t.Fatal("the second deploy's failure reaches the user")
	}
	if got, ok := m.Route("example"); !ok || got != id {
		t.Fatalf("the failed deploy must not undo the route of the one that runs: %d %v", got, ok)
	}
}

// A Remove while a routed redeploy is starting its copy: the redeploy saved the copy
// record before the runtime call, so a Remove in between would take the old copy and
// the record, and the new copy would run with no record to show or stop it.
func TestARemoveWaitsForARoutedDeployInFlight(t *testing.T) {
	m, p, id := routedExample(t)
	ctx := context.Background()
	if _, err := m.DeployRouted(ctx, "example", map[string]string{"TOKEN": "tok"}, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	finishedEarly := false
	p.deployFn = func(req runtime.ProxiedDeploy) (runtime.Exit, error) {
		// The redeploy is starting its tunnel: the owner removes the service now.
		go func() { done <- m.Remove(ctx, "example", false, false) }()
		select {
		case err := <-done:
			finishedEarly = true
			done <- err
		case <-time.After(200 * time.Millisecond):
		}
		p.running[key(req.Slug, req.ProxyID)] = true
		return runtime.Exit{IP: "203.0.113.7", Country: "NL"}, nil
	}
	if _, err := m.DeployRouted(ctx, "example", map[string]string{"TOKEN": "tok"}, id); err != nil {
		t.Fatal(err)
	}
	if finishedEarly {
		t.Fatal("the removal ran while the routed deploy was in flight")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Route("example"); ok || p.running[key("example", id)] {
		t.Fatalf("the removal after the deploy takes the new copy and its record: running %v", p.running)
	}
}

// The Remove button of a copy row on the Proxies page reaches RemoveProxied, not
// Remove. It waits for a routed deploy in flight the same way, so the new copy never
// runs with its record taken from under it.
func TestARemovedCopyWaitsForARoutedDeployInFlight(t *testing.T) {
	m, p, id := routedExample(t)
	ctx := context.Background()
	if _, err := m.DeployRouted(ctx, "example", map[string]string{"TOKEN": "tok"}, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	finishedEarly := false
	p.deployFn = func(req runtime.ProxiedDeploy) (runtime.Exit, error) {
		go func() { done <- m.RemoveProxied(ctx, "example", id) }()
		select {
		case err := <-done:
			finishedEarly = true
			done <- err
		case <-time.After(200 * time.Millisecond):
		}
		p.running[key(req.Slug, req.ProxyID)] = true
		return runtime.Exit{IP: "203.0.113.7", Country: "NL"}, nil
	}
	if _, err := m.DeployRouted(ctx, "example", map[string]string{"TOKEN": "tok"}, id); err != nil {
		t.Fatal(err)
	}
	if finishedEarly {
		t.Fatal("the copy's removal ran while the routed deploy was in flight")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Route("example"); ok || p.running[key("example", id)] {
		t.Fatalf("the removal after the deploy takes the new copy and its record: running %v", p.running)
	}
}

// A copy added on the Proxies page saves its record before it starts. Removing the
// service in between would drop that record and leave the new copy running with none.
func TestARemoveWaitsForACopyDeployInFlight(t *testing.T) {
	m, p, st := newProxyManager(t)
	ctx := context.Background()
	deployExample(t, st)
	view, err := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "7000"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	finishedEarly := false
	p.deployFn = func(req runtime.ProxiedDeploy) (runtime.Exit, error) {
		go func() { done <- m.Remove(ctx, "example", false, false) }()
		select {
		case err := <-done:
			finishedEarly = true
			done <- err
		case <-time.After(200 * time.Millisecond):
		}
		p.running[key(req.Slug, req.ProxyID)] = true
		return runtime.Exit{IP: "203.0.113.7", Country: "NL"}, nil
	}
	if _, err := m.DeployProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	if finishedEarly {
		t.Fatal("the removal ran while the copy's deploy was in flight")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if records, _ := st.ListProxiedCopies(view.ID); len(records) != 0 || len(p.allGone) != 1 {
		t.Fatalf("the removal after the deploy takes the copy and its record: %+v %v", records, p.allGone)
	}
}
