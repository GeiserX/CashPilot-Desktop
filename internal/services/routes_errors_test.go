package services

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// Route at deploy (routes.go) when something goes wrong: an unreadable or unwritable
// route, a copy that cannot be stopped or removed, a refused routed deploy. Each must
// leave the service where it was, and never start it from this machine's own IP.

// routeFaults is the proxy fake with the faults the plain one cannot make.
type routeFaults struct {
	*fakeProxyProvider
	removeErr error
	stopErr   error
	logs      string
	logsAsked []string
}

func (f *routeFaults) RemoveProxied(ctx context.Context, slug string, id int64, timeout int) ([]string, error) {
	if f.removeErr != nil {
		return nil, f.removeErr
	}
	return f.fakeProxyProvider.RemoveProxied(ctx, slug, id, timeout)
}

func (f *routeFaults) StopProxied(ctx context.Context, slug string, id int64, timeout int) (bool, error) {
	if f.stopErr != nil {
		return false, f.stopErr
	}
	return f.fakeProxyProvider.StopProxied(ctx, slug, id, timeout)
}

func (f *routeFaults) ProxiedLogs(_ context.Context, slug string, id int64, _ int) (string, error) {
	f.logsAsked = append(f.logsAsked, key(slug, id))
	return f.logs, nil
}

type routeRig struct {
	m  *Manager
	p  *fakeProxyProvider
	f  *routeFaults
	st *store.Store
	db *sql.DB // a second connection to the store's file, for faults and events
	id int64
}

func newRouteRig(t *testing.T) *routeRig {
	t.Helper()
	old := detectOwnIP
	detectOwnIP = func(context.Context) string { return "192.0.2.1" }
	t.Cleanup(func() { detectOwnIP = old })
	dir, st := newTestStoreInDir(t)
	p := newFakeProxyProvider()
	f := &routeFaults{fakeProxyProvider: p}
	m := NewManager(f, newTestCatalog(t), st)
	view, err := m.SaveProxy(context.Background(), 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "203.0.113.50", "port": "1080"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCredentials("example", map[string]string{"TOKEN": "tok"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "cashpilot-desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &routeRig{m: m, p: p, f: f, st: st, db: db, id: view.ID}
}

func (r *routeRig) exec(t *testing.T, query string) {
	t.Helper()
	if _, err := r.db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func (r *routeRig) events(t *testing.T, slug string) []string {
	t.Helper()
	rows, err := r.db.Query(`SELECT event FROM runtime_events WHERE slug = ? ORDER BY id`, slug)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func (r *routeRig) deployRouted(t *testing.T) {
	t.Helper()
	if _, err := r.m.DeployRouted(context.Background(), "example", map[string]string{"TOKEN": "tok"}, r.id); err != nil {
		t.Fatal(err)
	}
}

func tok() map[string]string { return map[string]string{"TOKEN": "tok"} }

// A route row that cannot be read fails closed: the service is neither deployed nor
// started from this machine's own IP, and a routed deploy does not guess either.
func TestAnUnreadableRouteKeepsTheServiceOffThisMachinesIP(t *testing.T) {
	r := newRouteRig(t)
	ctx := context.Background()
	deployExample(t, r.st)
	r.exec(t, `INSERT INTO routes(slug, proxy_id, created_at) VALUES('example', 'garbled', datetime('now'))`)
	r.p.startErr = errors.New("the runtime was asked to start it")

	var re *RouteError
	if _, err := r.m.DeployWith(ctx, "example", tok(), DeployOptions{LeaveRoute: true}); !errors.As(err, &re) || re.Code != "route_unknown" {
		t.Fatalf("deploy: %v", err)
	}
	if err := r.m.Start(ctx, "example"); !errors.As(err, &re) || re.Code != "route_unknown" {
		t.Fatalf("start: %v", err)
	}
	if r.p.deployCalls != 0 {
		t.Fatalf("nothing deployed: %d", r.p.deployCalls)
	}
	if !slices.Contains(r.events(t, "example"), "deploy_refused") {
		t.Fatalf("the refusal is recorded: %v", r.events(t, "example"))
	}
	if got := r.m.Routes(); len(got) != 0 || got == nil {
		t.Fatalf("Routes of an unreadable table: %#v", got)
	}

	if err := r.st.DeleteDeployment("example"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.DeployRouted(ctx, "example", tok(), r.id); err == nil {
		t.Fatal("a routed deploy over an unreadable route must stop")
	}
	if len(r.p.deploys) != 0 {
		t.Fatalf("no copy: %+v", r.p.deploys)
	}
}

// Routes reports only services that run as their route: a copy through the proxy and
// no container of their own. A route left after a removal, or under a direct
// deployment, is not one.
func TestRoutesListsOnlyServicesRunningAsTheirRoute(t *testing.T) {
	r := newRouteRig(t)
	r.deployRouted(t)
	if _, _, err := r.st.SetRoute("removed", r.id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.st.SetRoute("direct", r.id); err != nil {
		t.Fatal(err)
	}
	if err := r.st.UpsertDeployment(store.Deployment{Slug: "direct", Name: "cashpilot-direct", Status: "running", Runtime: "existing-docker"}); err != nil {
		t.Fatal(err)
	}
	if got := r.m.Routes(); !maps.Equal(got, map[string]int64{"example": r.id}) {
		t.Fatalf("Routes = %v", got)
	}

	// A copy table that cannot be read is no route either.
	r.exec(t, `DROP TABLE proxied_copies`)
	if id, ok := r.m.Route("example"); ok {
		t.Fatalf("Route with no readable copies = %d", id)
	}
}

func TestARoutedDeployIsRefusedBeforeItRecordsARoute(t *testing.T) {
	r := newRouteRig(t)
	ctx := context.Background()
	if _, err := r.m.SaveCustom("", CustomDefinition{Name: "My node", Image: "publisher/node:1.4.2"}); err != nil {
		t.Fatal(err)
	}
	if err := r.st.UpsertDeployment(store.Deployment{Slug: "example", Name: "cashpilot-example", Status: "running", Runtime: "native"}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"nope":           "unknown service",
		"custom-my-node": "custom service",
		"example":        "only Docker can run a copy", // deployed on the native runtime
	}
	for slug, want := range cases {
		_, err := r.m.DeployRouted(ctx, slug, tok(), r.id)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", slug, err, want)
		}
		if _, routed, _ := r.st.GetRoute(slug); routed {
			t.Errorf("%s: a refused deploy recorded a route", slug)
		}
	}
	if len(r.p.deploys) != 0 {
		t.Fatalf("nothing deployed: %+v", r.p.deploys)
	}
}

// A service tracked by hand has no Docker image, so there is nothing to run through a
// proxy: refused as DeployWith refuses it, before a route is recorded.
func TestAHandTrackedServiceIsNotDeployedThroughAProxy(t *testing.T) {
	r := newRouteRig(t)
	_, err := r.m.DeployRouted(context.Background(), "manual-svc", tok(), r.id)
	if err == nil || !strings.Contains(err.Error(), "tracked manually and has no Docker image") {
		t.Fatalf("DeployRouted = %v", err)
	}
	var rows int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM routes WHERE slug = 'manual-svc'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("route rows %d, %v", rows, err)
	}
	if len(r.p.deploys) != 0 {
		t.Fatalf("nothing deployed: %+v", r.p.deploys)
	}
}

func TestARetiredServiceIsNotDeployedThroughAProxy(t *testing.T) {
	st := newTestStore(t)
	m := NewManager(newFakeProxyProvider(), newRetiredTestCatalog(t), st)
	if _, err := m.DeployRouted(context.Background(), "gone-dead", nil, 1); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("retired: %v", err)
	}
	if _, routed, _ := st.GetRoute("gone-dead"); routed {
		t.Fatal("a refused deploy recorded a route")
	}
}

// A service running through one proxy is not moved to another by a second routed
// deploy; once removed, its next routed deploy picks the new proxy.
func TestARoutedServiceMovesProxyOnlyAfterItIsRemoved(t *testing.T) {
	r := newRouteRig(t)
	ctx := context.Background()
	other, err := r.m.SaveProxy(ctx, 0, map[string]string{"name": "Other", "kind": "socks5", "host": "203.0.113.60", "port": "1080"})
	if err != nil {
		t.Fatal(err)
	}
	r.deployRouted(t)
	if _, err := r.m.DeployRouted(ctx, "example", tok(), other.ID); err == nil || !strings.Contains(err.Error(), "another proxy") {
		t.Fatalf("a second proxy beside the running one: %v", err)
	}
	if id, ok := r.m.Route("example"); !ok || id != r.id || len(r.p.deploys) != 1 {
		t.Fatalf("the first route stays: %d %v, deploys %d", id, ok, len(r.p.deploys))
	}
	if err := r.m.Remove(ctx, "example", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.DeployRouted(ctx, "example", tok(), other.ID); err != nil {
		t.Fatalf("after a removal: %v", err)
	}
	if id, ok := r.m.Route("example"); !ok || id != other.ID {
		t.Fatalf("it runs through the new proxy: %d %v", id, ok)
	}
}

// A routed deploy whose route cannot be written never starts the copy: the copy
// running unrecorded would let a direct deploy start the same service beside it.
func TestARoutedDeployThatCannotRecordItsRouteStartsNothing(t *testing.T) {
	r := newRouteRig(t)
	r.exec(t, `CREATE TRIGGER no_route BEFORE INSERT ON routes BEGIN SELECT RAISE(ABORT, 'disk is full'); END`)
	_, err := r.m.DeployRouted(context.Background(), "example", tok(), r.id)
	if err == nil || !strings.Contains(err.Error(), "could not record the route") || !strings.Contains(err.Error(), "disk is full") {
		t.Fatalf("DeployRouted = %v", err)
	}
	if len(r.p.deploys) != 0 {
		t.Fatalf("no copy started: %+v", r.p.deploys)
	}
}

// Leaving a route removes the copy first; a copy that will not go stops the switch,
// so the same device never runs from two addresses.
func TestLeavingARouteStopsWhenTheCopyCannotBeRemoved(t *testing.T) {
	r := newRouteRig(t)
	r.deployRouted(t)
	r.f.removeErr = errors.New("daemon gone")
	_, err := r.m.DeployWith(context.Background(), "example", tok(), DeployOptions{LeaveRoute: true})
	if err == nil || !strings.Contains(err.Error(), "could not be removed first") || !strings.Contains(err.Error(), "daemon gone") {
		t.Fatalf("DeployWith = %v", err)
	}
	if r.p.deployCalls != 0 {
		t.Fatalf("nothing started from this machine's own IP: %d", r.p.deployCalls)
	}
	if id, ok := r.m.Route("example"); !ok || id != r.id {
		t.Fatalf("it still runs as its route: %d %v", id, ok)
	}
}

// A route that cannot be forgotten after the switch is recorded, and fails closed:
// the next plain deploy still asks.
func TestARouteThatCannotBeForgottenIsRecordedAndStillAsks(t *testing.T) {
	r := newRouteRig(t)
	ctx := context.Background()
	r.deployRouted(t)
	r.exec(t, `CREATE TRIGGER keep_route BEFORE DELETE ON routes BEGIN SELECT RAISE(ABORT, 'locked'); END`)
	if _, err := r.m.DeployWith(ctx, "example", tok(), DeployOptions{LeaveRoute: true}); err != nil {
		t.Fatalf("the switch itself worked: %v", err)
	}
	events := r.events(t, "example")
	if !slices.Contains(events, "route_error") || !slices.Contains(events, "route_left") {
		t.Fatalf("events %v", events)
	}
	if _, err := r.m.DeployWith(ctx, "example", tok(), DeployOptions{}); !IsRouteLocked(err) {
		t.Fatalf("the route is still there, so a plain deploy asks: %v", err)
	}
}

func TestRestartOfARoutedServiceDoesNotStartACopyThatDidNotStop(t *testing.T) {
	r := newRouteRig(t)
	r.deployRouted(t)
	r.f.stopErr = errors.New("stop timed out")
	if err := r.m.Restart(context.Background(), "example"); err == nil || !strings.Contains(err.Error(), "stop timed out") {
		t.Fatalf("Restart = %v", err)
	}
	if len(r.p.starts) != 0 || len(r.p.restartTimeouts) != 0 {
		t.Fatalf("nothing started: starts %v restarts %v", r.p.starts, r.p.restartTimeouts)
	}
}

func TestARemovalOfARoutedServiceThatFailsKeepsIt(t *testing.T) {
	r := newRouteRig(t)
	r.deployRouted(t)
	r.f.removeErr = errors.New("daemon gone")
	if err := r.m.Remove(context.Background(), "example", true, true); err == nil || !strings.Contains(err.Error(), "daemon gone") {
		t.Fatalf("Remove = %v", err)
	}
	if !slices.Contains(r.events(t, "example"), "remove_error") {
		t.Fatalf("events %v", r.events(t, "example"))
	}
	if id, ok := r.m.Route("example"); !ok || id != r.id {
		t.Fatalf("it still runs as its route: %d %v", id, ok)
	}
	if len(r.p.removeOpts) != 0 {
		t.Fatal("no container of its own was touched")
	}
}

func TestTheLogsOfARoutedServiceAreItsCopys(t *testing.T) {
	r := newRouteRig(t)
	r.deployRouted(t)
	r.f.logs = "copy line\n"
	r.p.logsResult = "own container line\n"
	got, err := r.m.Logs(context.Background(), "example", 50)
	if err != nil || got != "copy line\n" {
		t.Fatalf("Logs = %q, %v", got, err)
	}
	if strings.Join(r.f.logsAsked, ",") != key("example", r.id) {
		t.Fatalf("asked %v", r.f.logsAsked)
	}
}

func TestRouteLogsNeedARuntimeThatCanReadThem(t *testing.T) {
	ctx := context.Background()
	// The plain proxy fake runs copies but has no ProxiedLogs.
	m := NewManager(newFakeProxyProvider(), newTestCatalog(t), newTestStore(t))
	if _, err := m.RouteLogs(ctx, "example", 1, 10); err == nil || !strings.Contains(err.Error(), "cannot read a proxied copy's logs") {
		t.Fatalf("no reader: %v", err)
	}
	// A runtime with no proxies at all.
	m = NewManager(&fakeProvider{}, newTestCatalog(t), newTestStore(t))
	if _, err := m.RouteLogs(ctx, "example", 1, 10); err == nil || !strings.Contains(err.Error(), "needs Docker") {
		t.Fatalf("no proxies: %v", err)
	}
}

func TestHoldDirectStopsNothingWhenTheListFails(t *testing.T) {
	t.Setenv(ProxiedOnlyEnv, "true")
	m, p, st := newProxyManager(t)
	deployExample(t, st)
	p.listErr = errors.New("daemon gone")
	if got := m.HoldDirect(context.Background()); got != nil || len(p.stopTimeouts) != 0 {
		t.Fatalf("HoldDirect = %v, stops %v", got, p.stopTimeouts)
	}
}

// A stop that fails is not reported as stopped, and the record keeps saying running.
func TestHoldDirectReportsOnlyWhatItStopped(t *testing.T) {
	t.Setenv(ProxiedOnlyEnv, "true")
	r := newRouteRig(t)
	deployExample(t, r.st)
	r.p.listResult = []runtime.ContainerInfo{{Slug: "example", Name: "cashpilot-example", Status: "running", Instance: 1}}
	r.p.stopErr = errors.New("stop timed out")
	if got := r.m.HoldDirect(context.Background()); len(got) != 0 {
		t.Fatalf("HoldDirect = %v", got)
	}
	if dep, ok, _ := r.st.GetDeployment("example"); !ok || dep.Status != "running" {
		t.Fatalf("record %+v", dep)
	}
	events := r.events(t, "example")
	if !slices.Contains(events, "stop_error") || slices.Contains(events, "stopped") {
		t.Fatalf("events %v", events)
	}
}
