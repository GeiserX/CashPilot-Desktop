package services

import (
	"context"
	"errors"
	"strings"
	"testing"

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
