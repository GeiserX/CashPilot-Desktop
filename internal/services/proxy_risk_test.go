package services

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/proxies"
)

// A service whose terms forbid a proxy and can suspend the account (Honeygain's
// requirements.proxy_risk) runs through a proxy only after its owner ticks that risk:
// a copy and a routed deploy alike, and nothing is recorded or started without it.

func riskyExample(t *testing.T) *catalog.Catalog {
	t.Helper()
	yml := exampleServiceYAML + "requirements:\n  proxy_risk:\n    source: \"https://support.example.net/terms\"\n"
	cat, err := catalog.LoadEmbedded(fstest.MapFS{"services/bandwidth/example.yml": {Data: []byte(yml)}})
	if err != nil {
		t.Fatalf("LoadEmbedded error: %v", err)
	}
	return cat
}

func TestACopyOfAServiceWhoseTermsForbidAProxyNeedsTheOwnerTick(t *testing.T) {
	m, p, st := newProxyManager(t)
	m.catalog = riskyExample(t)
	ctx := context.Background()
	view, err := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "1080"})
	if err != nil {
		t.Fatal(err)
	}
	deployExample(t, st)
	if got := m.ProxyNeeds("example"); len(got) != 1 || got[0] != proxies.AckAccountRisk {
		t.Fatalf("the copy form must offer the tick: %v", got)
	}
	for _, given := range [][]string{nil, {"net_admin"}} {
		if _, err := m.DeployProxied(ctx, "example", view.ID, given...); err == nil || !strings.Contains(err.Error(), "must acknowledge") {
			t.Fatalf("a copy without the tick (%v) must be refused: %v", given, err)
		}
	}
	if records, _ := st.ListProxiedCopies(view.ID); len(p.deploys) != 0 || len(records) != 0 {
		t.Fatalf("a refused copy starts and records nothing: %d %+v", len(p.deploys), records)
	}
	if _, err := m.DeployProxied(ctx, "example", view.ID, proxies.AckAccountRisk); err != nil {
		t.Fatalf("with the tick the copy runs: %v", err)
	}
	if len(p.deploys) != 1 {
		t.Fatalf("deploys: %d", len(p.deploys))
	}
}

func TestARoutedDeployOfAServiceWhoseTermsForbidAProxyNeedsTheOwnerTick(t *testing.T) {
	m, p, id := routedExample(t)
	m.catalog = riskyExample(t)
	ctx := context.Background()
	creds := map[string]string{"TOKEN": "tok"}
	if _, err := m.DeployRouted(ctx, "example", creds, id); err == nil || !strings.Contains(err.Error(), "must acknowledge") {
		t.Fatalf("a route without the tick must be refused: %v", err)
	}
	if _, ok := m.Route("example"); ok || len(p.deploys) != 0 {
		t.Fatalf("a refused route records and starts nothing: %d", len(p.deploys))
	}
	if _, err := m.DeployRouted(ctx, "example", creds, id, proxies.AckAccountRisk); err != nil {
		t.Fatalf("with the tick the route runs: %v", err)
	}
	if len(p.deploys) != 1 || !p.deploys[0].Routed {
		t.Fatalf("deploys: %+v", p.deploys)
	}
}

func TestAServiceWithNoProxyRiskNeedsNoTick(t *testing.T) {
	m, _, _ := newProxyManager(t)
	if got := m.ProxyNeeds("example"); len(got) != 0 {
		t.Fatalf("no proxy_risk, no tick: %v", got)
	}
	if got := m.ProxyNeeds("not-in-the-catalog"); got == nil || len(got) != 0 {
		t.Fatalf("an unknown service needs no tick, as an empty list: %#v", got)
	}
	if got := proxies.MissingAcknowledgements(catalog.Service{}, nil); len(got) != 0 {
		t.Fatalf("missing: %v", got)
	}
}

// A copy added before the tick existed (the catalog gained Honeygain's proxy_risk
// later) has no tick on record. It keeps running, and is the owner's to remove; but
// it never starts again, by the owner's Start, a cap reset or the cap's retry,
// until the owner adds it again with the tick.
func TestACopyAddedBeforeTheTickNeverStartsAgainWithoutIt(t *testing.T) {
	m, p, st := newProxyManager(t)
	ctx := context.Background()
	deployExample(t, st)
	view, err := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "7000", "cap_gb": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeployProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	m.catalog = riskyExample(t)
	k := key("example", view.ID)

	if err := m.StopProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	p.starts = nil
	if err := m.StartProxied(ctx, "example", view.ID); err == nil || !strings.Contains(err.Error(), "must acknowledge") {
		t.Fatalf("the owner's Start of a copy with no tick must be refused: %v", err)
	}
	if len(p.starts) != 0 {
		t.Fatalf("a refused Start starts nothing: %v", p.starts)
	}

	// Running again (as it would be from before), the cap stops it; neither the
	// reset nor the retry starts it.
	p.running[k] = true
	p.rx[k] = 950_000_000
	if _, err := m.ProxyTick(ctx); err != nil {
		t.Fatal(err)
	}
	if p.running[k] {
		t.Fatal("the cap stops the copy")
	}
	p.starts = nil
	if started, err := m.ResetProxy(ctx, view.ID); err != nil || len(started) != 0 || len(p.starts) != 0 {
		t.Fatalf("a reset must not start a copy with no tick: %v %v %v", started, p.starts, err)
	}
	if m.retryCapStart(ctx, p, view.ID, "example", "192.0.2.1") || len(p.starts) != 0 {
		t.Fatalf("the retry must not start a copy with no tick: %v", p.starts)
	}

	// Added again with the tick, it starts.
	if _, err := m.DeployProxied(ctx, "example", view.ID, proxies.AckAccountRisk); err != nil {
		t.Fatalf("adding it again with the tick: %v", err)
	}
	if err := m.StopProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.StartProxied(ctx, "example", view.ID); err != nil {
		t.Fatalf("a copy added again with the tick starts: %v", err)
	}
}
