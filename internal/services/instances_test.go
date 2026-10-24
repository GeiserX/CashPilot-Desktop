package services

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// copyFake is a fakeProvider that can also run copies 2..N, keeping the set of copies
// that exist the way Docker would.
type copyFake struct {
	*fakeProvider
	running map[int]bool
	// removed records the copy numbers RemoveCopy took, in order.
	removed []int
}

func newCopyFake() *copyFake {
	return &copyFake{
		fakeProvider: &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "c1", Name: "cashpilot-scalable", Status: "running"}},
		running:      map[int]bool{},
	}
}

func (f *copyFake) Deploy(ctx context.Context, spec runtime.DeploySpec, progress func(string)) (runtime.ContainerInfo, error) {
	info, err := f.fakeProvider.Deploy(ctx, spec, progress)
	if err == nil && spec.Instance > 1 {
		f.running[spec.Instance] = true
		info.Instance = spec.Instance
	}
	return info, err
}

func (f *copyFake) Copies(context.Context, string) ([]int, error) {
	var out []int
	for n := range f.running {
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

func (f *copyFake) StopCopy(context.Context, string, int, int) error           { return nil }
func (f *copyFake) StartCopy(context.Context, string, int) error               { return nil }
func (f *copyFake) LogsCopy(context.Context, string, int, int) (string, error) { return "", nil }

func (f *copyFake) RemoveCopy(_ context.Context, _ string, n, _ int) error {
	delete(f.running, n)
	f.removed = append(f.removed, n)
	return nil
}

// One entry per reason the Instances box can be locked, and two that scale.
const scalingCatalogYAML = `name: Scalable
slug: scalable
category: bandwidth
status: active
requirements:
  devices_per_ip: 0
docker:
  image: example/scalable:1.0
  env:
    - key: DEVICE_NAME
      default: "cashpilot-{hostname}"
`

const undocumentedYAML = `name: Undocumented
slug: undocumented
category: bandwidth
status: active
docker:
  image: example/undocumented:1.0
`

const onePerIPYAML = `name: One Per IP
slug: one-per-ip
category: bandwidth
status: active
requirements:
  devices_per_ip: 1
docker:
  image: example/one:1.0
`

const volumeYAML = `name: Volume
slug: volume
category: storage
status: active
docker:
  image: example/volume:1.0
  volumes: ["volume-data:/data"]
`

const portYAML = `name: Port
slug: port
category: bandwidth
status: active
docker:
  image: example/port:1.0
  ports: ["8080:8080"]
`

func newScalingCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/bandwidth/scalable.yml":     {Data: []byte(scalingCatalogYAML)},
		"services/bandwidth/undocumented.yml": {Data: []byte(undocumentedYAML)},
		"services/bandwidth/one.yml":          {Data: []byte(onePerIPYAML)},
		"services/storage/volume.yml":         {Data: []byte(volumeYAML)},
		"services/bandwidth/port.yml":         {Data: []byte(portYAML)},
	})
	if err != nil {
		t.Fatalf("LoadEmbedded error: %v", err)
	}
	return cat
}

func intPtr(n int) *int { return &n }

func TestScalingLimit(t *testing.T) {
	m := NewManager(newCopyFake(), newScalingCatalog(t), newTestStore(t))
	for slug, want := range map[string]struct {
		max    int
		reason string
	}{
		"scalable":     {MaxInstances, ""},
		"undocumented": {MaxInstances, ""},
		"one-per-ip":   {1, "One Per IP allows one device per IP address"},
		"volume":       {1, "Volume keeps data in a host directory"},
		"port":         {1, "Port publishes a port on the host"},
	} {
		svc, _ := m.catalog.Get(slug)
		got := m.ScalingLimit(svc)
		if got.Max != want.max || !strings.HasPrefix(got.Reason, want.reason) || (want.reason == "") != (got.Reason == "") {
			t.Errorf("ScalingLimit(%s) = %+v, want max %d with reason %q", slug, got, want.max, want.reason)
		}
	}

	native := NewManager(newCopyFake(), newNativeTestCatalog(t), newTestStore(t))
	native.Register(nativeRuntimeKind, &fakeProvider{})
	svc, _ := native.catalog.Get("native-example")
	if got := native.ScalingLimit(svc); got.Max != 1 || !strings.Contains(got.Reason, "runs here as a program") {
		t.Fatalf("a native service = %+v, want one copy with the reason", got)
	}
}

// Copies 2..N are deployed as their own units, the count is kept by a redeploy that
// does not pass one, and lowering it removes the highest copies first.
func TestDeployWithRunsKeepsAndLowersCopies(t *testing.T) {
	st := newTestStore(t)
	fake := newCopyFake()
	m := NewManager(fake, newScalingCatalog(t), st)
	ctx := context.Background()

	dep, err := m.DeployWith(ctx, "scalable", nil, DeployOptions{Instances: intPtr(3)})
	if err != nil {
		t.Fatalf("DeployWith(3) error: %v", err)
	}
	if dep.Instances != 3 {
		t.Fatalf("returned Instances = %d, want 3", dep.Instances)
	}
	if got := instancesOf(fake.deploySpecs); got != "1,2,3" {
		t.Fatalf("deployed copies %s, want 1,2,3", got)
	}
	if copies := m.Copies()["scalable"]; len(copies) != 2 || copies[0].Instance != 2 || copies[1].Instance != 3 {
		t.Fatalf("Copies() = %+v, want copies 2 and 3", copies)
	}
	if stored, _, _ := st.GetDeployment("scalable"); stored.Instances != 3 {
		t.Fatalf("stored Instances = %d, want 3", stored.Instances)
	}

	// The runtime refresh rewrites the row on a timer; the count must survive it.
	fake.listResult = []runtime.ContainerInfo{{Slug: "scalable", ContainerID: "c1", Status: "running", Instance: 1}}
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatalf("Refresh error: %v", err)
	}
	fake.deploySpecs = nil
	if _, err := m.Deploy(ctx, "scalable", nil); err != nil {
		t.Fatalf("redeploy error: %v", err)
	}
	if got := instancesOf(fake.deploySpecs); got != "1,2,3" {
		t.Fatalf("a redeploy without a count deployed %s, want the recorded 1,2,3", got)
	}

	fake.deploySpecs = nil
	dep, err = m.DeployWith(ctx, "scalable", nil, DeployOptions{Instances: intPtr(1)})
	if err != nil {
		t.Fatalf("DeployWith(1) error: %v", err)
	}
	if got := instancesOf(fake.deploySpecs); got != "1" {
		t.Fatalf("scaling down deployed %s, want only copy 1", got)
	}
	if len(fake.removed) != 2 || fake.removed[0] != 3 || fake.removed[1] != 2 {
		t.Fatalf("scaling down removed %v, want [3 2]", fake.removed)
	}
	if stored, _, _ := st.GetDeployment("scalable"); stored.Instances != 1 || dep.Instances != 1 {
		t.Fatalf("after scaling down Instances = %d / %d, want 1", stored.Instances, dep.Instances)
	}
	if copies := m.Copies(); len(copies) != 0 {
		t.Fatalf("Copies() after scaling down = %+v, want none", copies)
	}
}

// Each copy of the same deploy carries its number, which is what names it and
// suffixes its device names in the runtime.
func instancesOf(specs []runtime.DeploySpec) string {
	parts := make([]string, 0, len(specs))
	for _, spec := range specs {
		parts = append(parts, strconv.Itoa(max(spec.Instance, 1)))
	}
	return strings.Join(parts, ",")
}

// A count the service cannot run is refused before the runtime is touched.
func TestDeployWithRefusesACountItCannotRun(t *testing.T) {
	ctx := context.Background()
	fake := newCopyFake()
	m := NewManager(fake, newScalingCatalog(t), newTestStore(t))
	for _, tc := range []struct {
		slug, want string
		n          int
	}{
		{"one-per-ip", "allows one device per IP", 2},
		{"volume", "keeps data in a host directory", 2},
		{"port", "publishes a port on the host", 2},
		{"scalable", "between 1 and 10", MaxInstances + 1},
		{"scalable", "between 1 and 10", 0},
	} {
		if _, err := m.DeployWith(ctx, tc.slug, nil, DeployOptions{Instances: intPtr(tc.n)}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("DeployWith(%s, %d) error = %v, want %q", tc.slug, tc.n, err, tc.want)
		}
	}
	if fake.deployCalls != 0 {
		t.Fatalf("the runtime was called %d times for refused counts", fake.deployCalls)
	}

	// A runtime with no copies cannot run a second one either.
	plain := &fakeProvider{}
	single := NewManager(plain, newScalingCatalog(t), newTestStore(t))
	if _, err := single.DeployWith(ctx, "scalable", nil, DeployOptions{Instances: intPtr(2)}); err == nil {
		t.Fatal("a runtime without copies accepted two")
	}
	if plain.deployCalls != 0 {
		t.Fatalf("the runtime was called %d times", plain.deployCalls)
	}
}

// Removing the service removes every copy, not only the first.
func TestRemoveClearsEveryCopy(t *testing.T) {
	st := newTestStore(t)
	fake := newCopyFake()
	m := NewManager(fake, newScalingCatalog(t), st)
	ctx := context.Background()
	if _, err := m.DeployWith(ctx, "scalable", nil, DeployOptions{Instances: intPtr(3)}); err != nil {
		t.Fatalf("DeployWith error: %v", err)
	}
	if err := m.Remove(ctx, "scalable", false, false); err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if len(fake.running) != 0 || len(fake.removed) != 2 {
		t.Fatalf("after Remove copies running %v, removed %v; want none running, 2 removed", fake.running, fake.removed)
	}
	if _, ok, _ := st.GetDeployment("scalable"); ok {
		t.Fatal("the deployment row survived Remove")
	}
	if copies := m.Copies(); len(copies) != 0 {
		t.Fatalf("Copies() after Remove = %+v", copies)
	}
}

// The refresh keeps one row per service, the first copy's; the others are listed as
// copies and never overwrite it.
func TestRefreshKeepsCopiesOutOfTheRow(t *testing.T) {
	st := newTestStore(t)
	fake := newCopyFake()
	fake.listResult = []runtime.ContainerInfo{
		{Slug: "scalable", ContainerID: "first", Name: "cashpilot-scalable", Status: "running", Instance: 1},
		{Slug: "scalable", ContainerID: "third", Name: "cashpilot-scalable-3", Status: "exited", Instance: 3},
		{Slug: "scalable", ContainerID: "second", Name: "cashpilot-scalable-2", Status: "running", Instance: 2},
	}
	m := NewManager(fake, newScalingCatalog(t), st)
	deps, err := m.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh error: %v", err)
	}
	if len(deps) != 1 || deps[0].ContainerID != "first" || deps[0].Status != "running" {
		t.Fatalf("rows = %+v, want one row for the first copy", deps)
	}
	copies := m.Copies()["scalable"]
	if len(copies) != 2 || copies[0].ContainerID != "second" || copies[1].ContainerID != "third" {
		t.Fatalf("Copies() = %+v, want copies 2 and 3 in order", copies)
	}
}
