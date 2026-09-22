package services

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	_ "modernc.org/sqlite"
)

// slowShutdownServiceYAML is the Storj shape: a service whose entry says it needs far
// longer than the default to shut down cleanly.
const slowShutdownServiceYAML = `name: Slow Node
slug: slownode
category: storage
status: active
docker:
  image: slownode/image:1.0.0
  stop_timeout: 300
`

func newStopTimeoutCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/bandwidth/example.yml": {Data: []byte(exampleServiceYAML)},
		"services/storage/slownode.yml":  {Data: []byte(slowShutdownServiceYAML)},
	})
	if err != nil {
		t.Fatalf("LoadEmbedded error: %v", err)
	}
	return cat
}

// THE RULE: every path that stops a container gives it the grace period its own
// catalog entry asks for.
//
// Storj asks for 300 seconds because a storage node has to flush before it goes, and a
// node killed early pays for it with an unclean-shutdown recovery on the next start.
// Deploy records that number on the container it creates, but Stop, Restart and Remove
// are the paths a user reaches for on a node that is ALREADY running — one deployed
// before the number existed, carrying nothing. If the manager does not resolve it from
// the catalog on every call, those nodes stay on the shared 30-second default until
// somebody redeploys them, which is the one thing a user stopping a node is not doing.
func TestLifecyclePathsCarryTheCatalogsGracePeriod(t *testing.T) {
	st := newTestStore(t)
	fake := &fakeProvider{}
	m := NewManager(fake, newStopTimeoutCatalog(t), st)
	ctx := context.Background()

	if err := m.Stop(ctx, "slownode"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(fake.stopTimeouts) != 1 || fake.stopTimeouts[0] != 300 {
		t.Fatalf("Stop was given %v seconds, want [300]", fake.stopTimeouts)
	}

	if err := m.Restart(ctx, "slownode"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if len(fake.restartTimeouts) != 1 || fake.restartTimeouts[0] != 300 {
		t.Fatalf("Restart was given %v seconds, want [300]", fake.restartTimeouts)
	}

	// Remove stops before it deletes, and the data it keeps has to be left consistent.
	if err := m.Remove(ctx, "slownode", false, false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(fake.removeOpts) != 1 || fake.removeOpts[0].StopTimeout != 300 {
		t.Fatalf("Remove was given %+v, want StopTimeout 300", fake.removeOpts)
	}
}

// A service the catalog has dropped can still be stopped. Nothing can say what it
// needs, so the manager says nothing and the runtime falls back to what the container
// itself carries — never to a number this layer invented.
func TestLifecyclePathsSayNothingWhenTheCatalogCannot(t *testing.T) {
	st := newTestStore(t)
	fake := &fakeProvider{}
	m := NewManager(fake, newStopTimeoutCatalog(t), st)

	if err := m.Stop(context.Background(), "vanished"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(fake.stopTimeouts) != 1 || fake.stopTimeouts[0] != 0 {
		t.Fatalf("a service with no catalog entry was given %v seconds, want [0]", fake.stopTimeouts)
	}
}

// resolvingProvider is a provider that knows the image a deploy would really pull,
// the way the Docker provider does once it has asked the daemon its architecture.
type resolvingProvider struct {
	fakeProvider
	resolved string
}

func (r *resolvingProvider) ResolveImage(context.Context, catalog.Service) string { return r.resolved }

// THE RULE: the deploy history records the image that was actually pulled.
//
// A catalog entry can name a different image per architecture — traffmonetizer
// publishes its ARM builds as separate tags, because Docker cannot pick them out of a
// manifest. On an ARM machine the pull is for the override while docker.image still
// reads the x86 digest, so a history that quotes the entry's default is telling the
// user a deploy pulled something it never pulled. That line is exactly what somebody
// reads when the image turns out to be the problem.
func TestPullStartRecordsTheImageActuallyPulled(t *testing.T) {
	dir, st := newTestStoreInDir(t)
	fake := &resolvingProvider{resolved: "example/image:arm64v8"}
	fake.deployResult = runtime.ContainerInfo{ContainerID: "cid", Status: "running", Image: "example/image:arm64v8"}
	m := NewManager(fake, newStopTimeoutCatalog(t), st)

	if _, err := m.Deploy(context.Background(), "example", map[string]string{"TOKEN": "t"}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if got := pullStartDetail(t, dir, "example"); got != "example/image:arm64v8" {
		t.Fatalf("pull_start recorded %q, want the image the deploy really pulled", got)
	}
}

// A provider that cannot resolve an image leaves the catalog's own value in the
// history rather than an empty line.
func TestPullStartFallsBackToTheCatalogImage(t *testing.T) {
	dir, st := newTestStoreInDir(t)
	fake := &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "cid", Status: "running"}}
	m := NewManager(fake, newStopTimeoutCatalog(t), st)

	if _, err := m.Deploy(context.Background(), "example", map[string]string{"TOKEN": "t"}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if got := pullStartDetail(t, dir, "example"); got != "example/image:1.0.0" {
		t.Fatalf("pull_start recorded %q, want the catalog's image", got)
	}
}

// pullStartDetail reads the detail of the most recent pull_start event straight out of
// the store's database. The store has no event-reading API, and what this has to prove
// is what a user would actually see in the deploy history.
func pullStartDetail(t *testing.T, dir, slug string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "cashpilot-desktop.db"))
	if err != nil {
		t.Fatalf("open store database: %v", err)
	}
	defer db.Close()
	var detail string
	err = db.QueryRow(
		`SELECT detail FROM runtime_events WHERE slug = ? AND event = 'pull_start' ORDER BY id DESC LIMIT 1`,
		slug,
	).Scan(&detail)
	if err != nil {
		t.Fatalf("no pull_start event for %s: %v", slug, err)
	}
	return detail
}

// newTestStoreInDir is newTestStore with the directory handed back, so a test can read
// the database the store wrote.
func newTestStoreInDir(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return dir, s
}
