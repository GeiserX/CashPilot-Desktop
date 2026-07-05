package services

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/zalando/go-keyring"
)

// TestMain mocks the keyring so store.Open (via config.MasterKey) stays fully
// in-memory and never touches the real OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

// fakeProvider is a controllable runtime.Provider used to drive Manager logic
// without a real container runtime.
type fakeProvider struct {
	listResult   []runtime.ContainerInfo
	listErr      error
	deployResult runtime.ContainerInfo
	deployErr    error
	startErr     error
	stopErr      error
	restartErr   error
	removeErr    error
	logsResult   string
	logsErr      error
}

func (f *fakeProvider) Status(context.Context) runtime.Status { return runtime.Status{} }

func (f *fakeProvider) Deploy(_ context.Context, _ runtime.DeploySpec, progress func(string)) (runtime.ContainerInfo, error) {
	if progress != nil {
		progress("pulling")
	}
	return f.deployResult, f.deployErr
}

func (f *fakeProvider) Start(context.Context, string) error   { return f.startErr }
func (f *fakeProvider) Stop(context.Context, string) error    { return f.stopErr }
func (f *fakeProvider) Restart(context.Context, string) error { return f.restartErr }
func (f *fakeProvider) Remove(context.Context, string) error  { return f.removeErr }

func (f *fakeProvider) Logs(context.Context, string, int) (string, error) {
	return f.logsResult, f.logsErr
}

func (f *fakeProvider) List(context.Context) ([]runtime.ContainerInfo, error) {
	return f.listResult, f.listErr
}

// Flush-left YAML: inside a raw string literal, leading indentation would become
// part of the document, so top-level keys must start at column 0.
const exampleServiceYAML = `name: Example
slug: example
category: bandwidth
status: active
docker:
  image: example/image:1.0.0
  env:
    - key: TOKEN
      label: API Token
      required: true
`

const manualServiceYAML = `name: Manual Service
slug: manual-svc
category: other
status: active
`

func newTestCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	fsys := fstest.MapFS{
		"services/bandwidth/example.yml": {Data: []byte(exampleServiceYAML)},
		"services/manual/manual.yml":     {Data: []byte(manualServiceYAML)},
	}
	cat, err := catalog.LoadEmbedded(fsys)
	if err != nil {
		t.Fatalf("LoadEmbedded error: %v", err)
	}
	return cat
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRefreshReconcilesStaleDeployments(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertDeployment(store.Deployment{Slug: "alive", ContainerID: "old", Status: "running", Name: "n", Image: "i", Runtime: "existing-docker"}); err != nil {
		t.Fatalf("seed alive error: %v", err)
	}
	if err := st.UpsertDeployment(store.Deployment{Slug: "stale", ContainerID: "gone", Status: "running", Name: "n", Image: "i", Runtime: "existing-docker"}); err != nil {
		t.Fatalf("seed stale error: %v", err)
	}

	fake := &fakeProvider{listResult: []runtime.ContainerInfo{
		{Slug: "alive", ContainerID: "new", Status: "running", Image: "i"},
		{Slug: "fresh", ContainerID: "fresh-id", Status: "running", Image: "i"},
	}}
	m := NewManager(fake, newTestCatalog(t), st)

	deps, err := m.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh error: %v", err)
	}

	slugs := map[string]bool{}
	for _, d := range deps {
		slugs[d.Slug] = true
	}
	if !slugs["alive"] || !slugs["fresh"] {
		t.Fatalf("expected alive and fresh deployments, got %v", slugs)
	}
	if slugs["stale"] {
		t.Fatal("expected the stale deployment to be reconciled away")
	}
	if _, ok, _ := st.GetDeployment("stale"); ok {
		t.Fatal("stale deployment is still present in the store")
	}
	if dep, ok, _ := st.GetDeployment("alive"); !ok || dep.ContainerID != "new" {
		t.Fatalf("expected the alive deployment refreshed with the new container id, got %+v", dep)
	}
}

// TestRefreshEmptyListKeepsTracked pins the reconcile guard: an error-free empty
// list from the runtime (a different runtime/context is likely active) must NOT
// delete tracked deployments — they stay in the store and in the returned slice.
func TestRefreshEmptyListKeepsTracked(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertDeployment(store.Deployment{Slug: "storj", ContainerID: "c1", Status: "running", Name: "n1", Image: "i1", Runtime: "existing-docker"}); err != nil {
		t.Fatalf("seed storj error: %v", err)
	}
	if err := st.UpsertDeployment(store.Deployment{Slug: "mysterium", ContainerID: "c2", Status: "running", Name: "n2", Image: "i2", Runtime: "existing-docker"}); err != nil {
		t.Fatalf("seed mysterium error: %v", err)
	}

	fake := &fakeProvider{listResult: nil} // empty, error-free list
	m := NewManager(fake, newTestCatalog(t), st)

	deps, err := m.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh error: %v", err)
	}

	slugs := map[string]bool{}
	for _, d := range deps {
		slugs[d.Slug] = true
	}
	if !slugs["storj"] || !slugs["mysterium"] {
		t.Fatalf("expected both tracked deployments in the returned slice, got %v", slugs)
	}
	if _, ok, _ := st.GetDeployment("storj"); !ok {
		t.Fatal("expected storj to remain in the store")
	}
	if _, ok, _ := st.GetDeployment("mysterium"); !ok {
		t.Fatal("expected mysterium to remain in the store")
	}
}

func TestRefreshReturnsRuntimeError(t *testing.T) {
	st := newTestStore(t)
	fake := &fakeProvider{listErr: errors.New("runtime down")}
	m := NewManager(fake, newTestCatalog(t), st)
	if _, err := m.Refresh(context.Background()); err == nil {
		t.Fatal("expected an error when runtime List fails")
	}
}

func TestDeployValidationErrors(t *testing.T) {
	st := newTestStore(t)
	m := NewManager(&fakeProvider{}, newTestCatalog(t), st)

	cases := []struct {
		name     string
		slug     string
		creds    map[string]string
		contains string
	}{
		{"unknown service", "does-not-exist", nil, "unknown service"},
		{"manual only", "manual-svc", nil, "tracked manually"},
		{"missing required field", "example", nil, "missing required field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.Deploy(context.Background(), tc.slug, tc.creds)
			if err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("expected error containing %q, got %q", tc.contains, err.Error())
			}
		})
	}
}

func TestDeployPersistsDeployment(t *testing.T) {
	st := newTestStore(t)
	fake := &fakeProvider{deployResult: runtime.ContainerInfo{
		ContainerID: "cid-1",
		Name:        "cashpilot-example",
		Image:       "example/image:1.0.0",
		Status:      "running",
	}}
	m := NewManager(fake, newTestCatalog(t), st)

	dep, err := m.Deploy(context.Background(), "example", map[string]string{"TOKEN": "abc"})
	if err != nil {
		t.Fatalf("Deploy error: %v", err)
	}
	if dep.Slug != "example" || dep.ContainerID != "cid-1" || dep.Runtime != "existing-docker" {
		t.Fatalf("unexpected returned deployment: %+v", dep)
	}
	stored, ok, err := st.GetDeployment("example")
	if err != nil || !ok {
		t.Fatalf("expected a stored deployment, ok=%v err=%v", ok, err)
	}
	if stored.ContainerID != "cid-1" || stored.Status != "running" {
		t.Fatalf("unexpected stored deployment: %+v", stored)
	}
}

func TestDeployPropagatesRuntimeError(t *testing.T) {
	st := newTestStore(t)
	fake := &fakeProvider{deployErr: errors.New("pull failed")}
	m := NewManager(fake, newTestCatalog(t), st)
	if _, err := m.Deploy(context.Background(), "example", map[string]string{"TOKEN": "abc"}); err == nil {
		t.Fatal("expected an error when runtime Deploy fails")
	}
}

func TestLifecycleOperationsUpdateStore(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertDeployment(store.Deployment{Slug: "example", ContainerID: "c1", Status: "running", Name: "n", Image: "i", Runtime: "existing-docker"}); err != nil {
		t.Fatalf("seed error: %v", err)
	}
	fake := &fakeProvider{logsResult: "log-line"}
	m := NewManager(fake, newTestCatalog(t), st)
	ctx := context.Background()

	if err := m.Stop(ctx, "example"); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
	if dep, _, _ := st.GetDeployment("example"); dep.Status != "stopped" {
		t.Fatalf("expected status 'stopped', got %q", dep.Status)
	}

	if err := m.Start(ctx, "example"); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if dep, _, _ := st.GetDeployment("example"); dep.Status != "running" {
		t.Fatalf("expected status 'running' after Start, got %q", dep.Status)
	}

	if err := m.Restart(ctx, "example"); err != nil {
		t.Fatalf("Restart error: %v", err)
	}
	if dep, _, _ := st.GetDeployment("example"); dep.Status != "running" {
		t.Fatalf("expected status 'running' after Restart, got %q", dep.Status)
	}

	logs, err := m.Logs(ctx, "example", 100)
	if err != nil {
		t.Fatalf("Logs error: %v", err)
	}
	if logs != "log-line" {
		t.Fatalf("expected delegated logs, got %q", logs)
	}

	if err := m.Remove(ctx, "example"); err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if _, ok, _ := st.GetDeployment("example"); ok {
		t.Fatal("expected the deployment to be removed from the store")
	}
}

func TestLifecycleOperationsPropagateErrors(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	m := NewManager(&fakeProvider{
		startErr:   errors.New("x"),
		stopErr:    errors.New("x"),
		restartErr: errors.New("x"),
		removeErr:  errors.New("x"),
	}, newTestCatalog(t), st)

	if err := m.Start(ctx, "s"); err == nil {
		t.Fatal("expected a Start error")
	}
	if err := m.Stop(ctx, "s"); err == nil {
		t.Fatal("expected a Stop error")
	}
	if err := m.Restart(ctx, "s"); err == nil {
		t.Fatal("expected a Restart error")
	}
	if err := m.Remove(ctx, "s"); err == nil {
		t.Fatal("expected a Remove error")
	}
}
