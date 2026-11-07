package services

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/proxies"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	_ "modernc.org/sqlite"
)

// fakeProxyProvider is a Docker-like provider that also runs proxied copies.
type fakeProxyProvider struct {
	fakeProvider
	deploys  []runtime.ProxiedDeploy
	deployFn func(runtime.ProxiedDeploy) (runtime.Exit, error)
	running  map[string]bool // "slug/id" -> running
	rx       map[string]uint64
	stops    []string
	starts   []string
	specs    []runtime.ProxySpec
	removed  []string
	allGone  []string
}

func newFakeProxyProvider() *fakeProxyProvider {
	return &fakeProxyProvider{running: map[string]bool{}, rx: map[string]uint64{}}
}

func key(slug string, id int64) string { return slug + "/" + string(rune('0'+id)) }

func (f *fakeProxyProvider) DeployProxied(_ context.Context, req runtime.ProxiedDeploy) (runtime.Exit, error) {
	f.deploys = append(f.deploys, req)
	if f.deployFn != nil {
		return f.deployFn(req)
	}
	f.running[key(req.Slug, req.ProxyID)] = true
	return runtime.Exit{IP: "203.0.113.7", Country: "NL"}, nil
}
func (f *fakeProxyProvider) StopProxied(_ context.Context, slug string, id int64, _ int) (bool, error) {
	f.stops = append(f.stops, key(slug, id))
	was := f.running[key(slug, id)]
	f.running[key(slug, id)] = false
	return was, nil
}
func (f *fakeProxyProvider) StartProxied(_ context.Context, slug string, id int64, _ int, spec runtime.ProxySpec, _ string) (runtime.Exit, error) {
	f.starts = append(f.starts, key(slug, id))
	f.specs = append(f.specs, spec)
	f.running[key(slug, id)] = true
	return runtime.Exit{IP: "203.0.113.7"}, nil
}
func (f *fakeProxyProvider) RemoveProxied(_ context.Context, slug string, id int64, _ int) ([]string, error) {
	f.removed = append(f.removed, key(slug, id))
	delete(f.running, key(slug, id))
	return []string{slug}, nil
}
func (f *fakeProxyProvider) RemoveAllProxied(_ context.Context, slug string) ([]string, error) {
	f.allGone = append(f.allGone, slug)
	return nil, nil
}
func (f *fakeProxyProvider) ProxiedStatus(context.Context) ([]runtime.ProxiedCopy, error) {
	var out []runtime.ProxiedCopy
	for k, on := range f.running {
		slug, idText, _ := strings.Cut(k, "/")
		id := int64(idText[0] - '0')
		status := "exited"
		if on {
			status = "running"
		}
		rx, tx := f.rx[k], uint64(0)
		out = append(out, runtime.ProxiedCopy{Slug: slug, ProxyID: id, Status: status, TunnelStatus: status, TunnelContainerID: "t-" + k, TunnelStartedAt: "s", NetRxBytes: &rx, NetTxBytes: &tx, ExitIP: "203.0.113.7"})
	}
	return out, nil
}
func (f *fakeProxyProvider) TestProxy(context.Context, runtime.ProxySpec, string) (runtime.Exit, error) {
	return runtime.Exit{IP: "203.0.113.9", Country: "DE", MS: 5}, nil
}

func newProxyManager(t *testing.T) (*Manager, *fakeProxyProvider, *store.Store) {
	t.Helper()
	old := detectOwnIP
	detectOwnIP = func(context.Context) string { return "192.0.2.1" }
	t.Cleanup(func() { detectOwnIP = old })
	st := newTestStore(t)
	p := newFakeProxyProvider()
	m := NewManager(p, newTestCatalog(t), st)
	return m, p, st
}

func deployExample(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.SaveCredentials("example", map[string]string{"TOKEN": "tok"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertDeployment(store.Deployment{Slug: "example", Name: "cashpilot-example", Status: "running", Runtime: "existing-docker"}); err != nil {
		t.Fatal(err)
	}
}

func TestProxySecretsAreEncryptedAndNeverShown(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(newFakeProxyProvider(), newTestCatalog(t), st)
	view, err := m.SaveProxy(context.Background(), 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "gate.example.net", "port": "7000", "username": "u-SECRET", "password": "p-SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Secrets["password"] || !view.Secrets["username"] {
		t.Fatalf("view: %+v", view)
	}
	// An edit with blank boxes keeps them.
	view, err = m.SaveProxy(context.Background(), view.ID, map[string]string{"name": "Gate 2", "password": ""})
	if err != nil || !view.Secrets["password"] || view.Name != "Gate 2" {
		t.Fatalf("edit: %+v %v", view, err)
	}
	_ = st.Close()
	db, err := sql.Open("sqlite", filepath.Join(dir, "cashpilot-desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value, state string
	if err := db.QueryRow(`SELECT value, state FROM proxies`).Scan(&value, &state); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if strings.Contains(value+state, "SECRET") || strings.Contains(value, "gate.example.net") {
		t.Fatalf("the definition must be encrypted at rest: %q %q", value, state)
	}
}

func TestDeployProxiedUsesTheDeploymentAndNamesItsWarnings(t *testing.T) {
	m, p, st := newProxyManager(t)
	ctx := context.Background()
	view, err := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "http", "host": "203.0.113.50", "port": "8080", "password": "pw-SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeployProxied(ctx, "example", view.ID); err == nil || !strings.Contains(err.Error(), "deploy Example on this machine first") {
		t.Fatalf("an undeployed service is refused: %v", err)
	}
	deployExample(t, st)
	if err := st.SetCustomImage("example", "mirror/example:1.0.1"); err != nil {
		t.Fatal(err)
	}
	result, err := m.DeployProxied(ctx, "example", view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitIP != "203.0.113.7" || len(p.deploys) != 1 {
		t.Fatalf("result %+v deploys %d", result, len(p.deploys))
	}
	req := p.deploys[0]
	if req.Env["TOKEN"] != "tok" || req.Service.Docker.Image != "mirror/example:1.0.1" || req.Proxy.Password != "pw-SECRET" || req.OwnIP != "192.0.2.1" {
		t.Fatalf("request: %+v", req)
	}
	records, _ := st.ListProxiedCopies(view.ID)
	if len(records) != 1 || records[0].Slug != "example" {
		t.Fatalf("records: %+v", records)
	}

	// A runtime error carrying the password reaches the user masked.
	p.deployFn = func(runtime.ProxiedDeploy) (runtime.Exit, error) {
		return runtime.Exit{}, errors.New("proxy said no to pw-SECRET")
	}
	if _, err := m.DeployProxied(ctx, "example", view.ID); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("errors are masked: %v", err)
	}
	if err := m.DeleteProxy(view.ID); err == nil {
		t.Fatal("a proxy with copies cannot be deleted")
	}
}

func TestCapStopsCopiesAndAResetStartsOnlyThoseItStopped(t *testing.T) {
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
	k := key("example", view.ID)
	p.rx[k] = 500_000_000
	if crossed, err := m.ProxyTick(ctx); err != nil || len(crossed) != 0 {
		t.Fatalf("under the cap: %v %v", crossed, err)
	}
	if p.running[k] != true {
		t.Fatal("a copy under its cap keeps running")
	}
	p.rx[k] = 950_000_000
	crossed, err := m.ProxyTick(ctx)
	if err != nil || len(crossed) != 1 || !strings.Contains(crossed[0].Message, "reached 90% of its 1 GB cap") {
		t.Fatalf("crossing the threshold: %+v %v", crossed, err)
	}
	if p.running[k] || !slices.Contains(p.stops, k) {
		t.Fatalf("the cap stops the copy: running=%v stops=%v", p.running[k], p.stops)
	}
	if notices := m.CapNotices(); len(notices) != 1 || !strings.HasPrefix(notices[0].Message, "Stopped: Gate reached 90%") {
		t.Fatalf("notices: %+v", notices)
	}
	if err := m.StartProxied(ctx, "example", view.ID); err == nil {
		t.Fatal("a start is refused while the cap holds")
	}
	if _, err := m.DeployProxied(ctx, "example", view.ID); err == nil {
		t.Fatal("a deploy is refused while the cap holds")
	}
	// A copy started behind the app's back is stopped again on the next pass.
	p.running[k] = true
	p.stops = nil
	if _, err := m.ProxyTick(ctx); err != nil {
		t.Fatal(err)
	}
	if p.running[k] || len(p.stops) != 1 {
		t.Fatalf("the cap holds on every pass: %v", p.stops)
	}
	started, err := m.ResetProxy(ctx, view.ID)
	if err != nil || !slices.Equal(started, []string{"example"}) || !p.running[k] {
		t.Fatalf("a reset starts what the cap stopped: %v %v", started, err)
	}
	if len(m.CapNotices()) != 0 {
		t.Fatal("a reset lifts the notice")
	}
	// A copy the owner stopped stays stopped through a reset.
	if err := m.StopProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	p.starts = nil
	if started, _ := m.ResetProxy(ctx, view.ID); len(started) != 0 || len(p.starts) != 0 {
		t.Fatalf("a manual stop survives a reset: %v", started)
	}
}

func TestRaisingTheCapLiftsTheStop(t *testing.T) {
	m, p, st := newProxyManager(t)
	ctx := context.Background()
	deployExample(t, st)
	view, _ := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "7000", "cap_gb": "1"})
	if _, err := m.DeployProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	k := key("example", view.ID)
	p.rx[k] = 950_000_000
	if _, err := m.ProxyTick(ctx); err != nil {
		t.Fatal(err)
	}
	if p.running[k] {
		t.Fatal("capped")
	}
	if _, err := m.SaveProxy(ctx, view.ID, map[string]string{"cap_gb": "10"}); err != nil {
		t.Fatal(err)
	}
	if !p.running[k] || len(m.CapNotices()) != 0 {
		t.Fatalf("a cap raised above this period's use starts the copies again: %v", p.running)
	}
}

func TestResetDayStartsANewPeriod(t *testing.T) {
	m, p, st := newProxyManager(t)
	ctx := context.Background()
	deployExample(t, st)
	old := proxyNow
	t.Cleanup(func() { proxyNow = old })
	proxyNow = func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) }
	view, _ := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "7000", "cap_gb": "1", "reset_day": "5"})
	if _, err := m.DeployProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	k := key("example", view.ID)
	p.rx[k] = 950_000_000
	if _, err := m.ProxyTick(ctx); err != nil || p.running[k] {
		t.Fatalf("capped on the 4th: %v", err)
	}
	proxyNow = func() time.Time { return time.Date(2026, 11, 5, 0, 1, 0, 0, time.UTC) }
	if _, err := m.ProxyTick(ctx); err != nil {
		t.Fatal(err)
	}
	if !p.running[k] || len(m.CapNotices()) != 0 {
		t.Fatalf("the reset day starts the copies again: running=%v", p.running[k])
	}
}

func TestRemovingAServiceRemovesItsProxiedCopies(t *testing.T) {
	m, p, st := newProxyManager(t)
	ctx := context.Background()
	deployExample(t, st)
	view, _ := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "7000"})
	if _, err := m.DeployProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, "example", false, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.allGone, []string{"example"}) {
		t.Fatalf("proxied copies go with the service: %v", p.allGone)
	}
	if records, _ := st.ListProxiedCopies(0); len(records) != 0 {
		t.Fatalf("their records too: %+v", records)
	}
	if err := m.DeleteProxy(view.ID); err != nil {
		t.Fatalf("with no copies the proxy can go: %v", err)
	}
}

func TestTestProxyKeepsTheOutcomeOnTheProxy(t *testing.T) {
	m, _, _ := newProxyManager(t)
	ctx := context.Background()
	view, _ := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "7000"})
	res, err := m.TestProxy(ctx, view.ID)
	if err != nil || res.ExitIP != "203.0.113.9" {
		t.Fatalf("test: %+v %v", res, err)
	}
	got, _ := m.view(view.ID)
	if got.LastExitIP != "203.0.113.9" || got.LastTest == nil || !got.LastTest.OK {
		t.Fatalf("view: %+v", got)
	}
}

// Docker answers a stop of a stopped container with success, so a crossing marked a
// copy the owner had stopped as stopped by the cap, and the next reset started it.
func TestACopyTheOwnerStoppedStaysStoppedThroughACrossingAndAReset(t *testing.T) {
	m, p, st := newProxyManager(t)
	ctx := context.Background()
	deployExample(t, st)
	view, _ := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "7000", "cap_gb": "1"})
	if _, err := m.DeployProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.StopProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	p.rx[key("example", view.ID)] = 950_000_000
	if crossed, err := m.ProxyTick(ctx); err != nil || len(crossed) != 1 {
		t.Fatalf("crossing: %v %v", crossed, err)
	}
	p.starts = nil
	if started, err := m.ResetProxy(ctx, view.ID); err != nil || len(started) != 0 || len(p.starts) != 0 {
		t.Fatalf("the reset started a copy the owner stopped: %v %v %v", started, p.starts, err)
	}
}

// An edit reached nothing before: a start passed only the host.
func TestAStartUsesTheProxyAsStoredNow(t *testing.T) {
	m, p, st := newProxyManager(t)
	ctx := context.Background()
	deployExample(t, st)
	view, _ := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "7000", "password": "old"})
	if _, err := m.DeployProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SaveProxy(ctx, view.ID, map[string]string{"password": "n3w", "port": "7100"}); err != nil {
		t.Fatal(err)
	}
	if err := m.StartProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	last := p.specs[len(p.specs)-1]
	if last.Password != "n3w" || last.Port != 7100 {
		t.Fatalf("start got %+v", last)
	}
}

func TestAProxyCannotBeDeletedWhileACopyOfItDeploys(t *testing.T) {
	m, p, st := newProxyManager(t)
	ctx := context.Background()
	deployExample(t, st)
	view, _ := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "7000"})
	var deleteErr error
	p.deployFn = func(runtime.ProxiedDeploy) (runtime.Exit, error) {
		deleteErr = m.DeleteProxy(view.ID)
		return runtime.Exit{IP: "203.0.113.7"}, nil
	}
	if _, err := m.DeployProxied(ctx, "example", view.ID); err != nil {
		t.Fatal(err)
	}
	if deleteErr == nil {
		t.Fatal("the proxy was deleted under a copy being deployed")
	}
}

func TestAFailedDeployLeavesNoRecord(t *testing.T) {
	m, p, st := newProxyManager(t)
	ctx := context.Background()
	deployExample(t, st)
	view, _ := m.SaveProxy(ctx, 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "7000"})
	p.deployFn = func(runtime.ProxiedDeploy) (runtime.Exit, error) { return runtime.Exit{}, errors.New("refused") }
	if _, err := m.DeployProxied(ctx, "example", view.ID); err == nil {
		t.Fatal("expected the refusal")
	}
	if records, _ := st.ListProxiedCopies(view.ID); len(records) != 0 {
		t.Fatalf("records after a failed deploy: %+v", records)
	}
}

func TestANewProxyWithAResetDayStartsItsPeriod(t *testing.T) {
	m, _, _ := newProxyManager(t)
	view, err := m.SaveProxy(context.Background(), 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "7000", "cap_gb": "1", "reset_day": "1"})
	if err != nil {
		t.Fatal(err)
	}
	d, s, err := m.loadProxy(view.ID)
	if err != nil || s.PeriodStart == "" || proxies.ResetDue(d, s, proxyNow().UTC()) {
		t.Fatalf("period: %+v %v", s, err)
	}
}

// A delete the critical-data check refuses must not cost the proxied copies: the
// refusal comes before they are removed.
func TestARefusedDeleteKeepsTheProxiedCopies(t *testing.T) {
	m, p, st := newProxyManager(t)
	deployExample(t, st)
	p.planVolumes = []runtime.DataMount{{Target: "/data", Source: "cashpilot-example-data", Volume: true, Critical: true, Holds: "the node identity"}}
	err := m.Remove(context.Background(), "example", true, false)
	if err == nil || !strings.Contains(err.Error(), "cannot be recovered") {
		t.Fatalf("the delete must be refused for the critical volume, got %v", err)
	}
	if len(p.allGone) != 0 {
		t.Fatalf("a refused delete removed the proxied copies: %v", p.allGone)
	}
	if len(p.removeOpts) != 0 {
		t.Fatalf("a refused delete reached provider.Remove: %+v", p.removeOpts)
	}
	// A plan that cannot be read stops the delete before the copies go, too.
	p.planVolumes, p.planErr = nil, errors.New("daemon busy")
	if err := m.Remove(context.Background(), "example", true, false); err == nil || len(p.allGone) != 0 {
		t.Fatalf("an unreadable plan must stop the delete first: %v, copies %v", err, p.allGone)
	}
	if err := m.Remove(context.Background(), "example", true, true); err == nil || len(p.allGone) != 0 {
		t.Fatalf("an unreadable plan must stop an approved delete first too: %v, copies %v", err, p.allGone)
	}
	p.planErr = nil
	// With the separate yes, the copies go first, then the service.
	if err := m.Remove(context.Background(), "example", true, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.allGone, []string{"example"}) || len(p.removeOpts) != 1 {
		t.Fatalf("allowed delete: copies %v, removes %d", p.allGone, len(p.removeOpts))
	}
}

// A service deployed on the native runtime is offered as a proxy candidate only with
// its refusal, so the option is disabled instead of failing on click.
func TestProxyRefusalNamesTheNativeRuntime(t *testing.T) {
	m, _, st := newProxyManager(t)
	deployExample(t, st)
	if got := m.ProxyRefusal("example"); got != "" {
		t.Fatalf("a Docker deployment must be allowed, got %q", got)
	}
	if err := st.UpsertDeployment(store.Deployment{Slug: "example", Name: "example", Status: "running", Runtime: "native"}); err != nil {
		t.Fatal(err)
	}
	got := m.ProxyRefusal("example")
	if !strings.Contains(got, "native runtime") {
		t.Fatalf("the refusal must name the native runtime, got %q", got)
	}
	view, err := m.SaveProxy(context.Background(), 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "gate.example.net", "port": "7000"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.DeployProxied(context.Background(), "example", view.ID)
	if err == nil || !strings.Contains(err.Error(), "native runtime") {
		t.Fatalf("the deploy must refuse with the same reason, got %v", err)
	}
	// A deployment that cannot be read is a refusal too, never a silent yes.
	_ = st.Close()
	if got := m.ProxyRefusal("example"); !strings.Contains(got, "Could not read its deployment") {
		t.Fatalf("a failed lookup must be the refusal, got %q", got)
	}
}
