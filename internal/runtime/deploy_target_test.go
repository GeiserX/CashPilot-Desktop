package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// THE RULE: a checked deploy (DeploySpec.ExpectedContainer) replaces only the
// container its caller saw, and the lookup names that container the way the deploy
// reads it. These run against the fake daemon, so they pin what Docker is asked.

func TestDockerDeployTargetNamesTheContainerUnderTheDeploysName(t *testing.T) {
	cases := []struct {
		name          string
		inspectStatus int
		want          string
		wantErr       string
	}{
		{name: "a container holds the name", want: "live"},
		{name: "the name is free", inspectStatus: http.StatusNotFound, want: NoContainer},
		{name: "the daemon fails the inspect", inspectStatus: http.StatusInternalServerError, wantErr: "could not look up the deploy container in Docker"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDocker{inspectStatus: tc.inspectStatus}
			fake.start(t)
			got, err := (&DockerProvider{}).DeployTarget(context.Background(), "orderly")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("DeployTarget = %q, %v; want an error with %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("DeployTarget = %q, %v; want %q", got, err, tc.want)
			}
			// One inspect of the deploy's own name, and nothing else: no logs.
			if calls := fake.recorded(); !slices.Equal(calls, []string{"GET /containers/" + containerName("orderly") + "/json"}) {
				t.Fatalf("the lookup asked Docker %v; want one inspect of %s", calls, containerName("orderly"))
			}
		})
	}
}

func TestDockerDeployTargetWithNoDockerClientIsAnError(t *testing.T) {
	t.Setenv("DOCKER_HOST", "not a docker host")
	if got, err := (&DockerProvider{}).DeployTarget(context.Background(), "orderly"); err == nil {
		t.Fatalf("DeployTarget = %q with an unusable DOCKER_HOST; want an error", got)
	}
}

// A checked deploy whose container changed touches nothing: no stop, no rename, no
// create, no removal. The positive controls are the same fake with the expectation
// that matches, which goes on to replace or create.
func TestDockerCheckedDeployReplacesOnlyTheExpectedContainer(t *testing.T) {
	cases := []struct {
		name          string
		inspectStatus int
		expected      string
		wantActual    string // "" when the deploy goes through
	}{
		{name: "another container holds the name", expected: "older", wantActual: "live"},
		{name: "a create-only yes finds a container", expected: NoContainer, wantActual: "live"},
		{name: "the approved container is gone", inspectStatus: http.StatusNotFound, expected: "live", wantActual: NoContainer},
		{name: "the approved container is there", expected: "live"},
		{name: "a create-only yes finds the name free", inspectStatus: http.StatusNotFound, expected: NoContainer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDocker{inspectStatus: tc.inspectStatus}
			fake.start(t)
			spec := deploySpec("orderly")
			expected := tc.expected
			spec.ExpectedContainer = &expected
			info, err := (&DockerProvider{}).Deploy(context.Background(), spec, nil)
			created := slices.Contains(fake.recorded(), "POST /containers/create")
			if tc.wantActual == "" {
				if err != nil || info.ContainerID != "deadbeef" || !created {
					t.Fatalf("Deploy = %+v, %v (calls %v); want the deploy to go through", info, err, fake.recorded())
				}
				return
			}
			var changed *DeployTargetChanged
			if !errors.As(err, &changed) || changed.Actual != tc.wantActual {
				t.Fatalf("Deploy err = %v; want DeployTargetChanged naming %q", err, tc.wantActual)
			}
			// The words the worker sends back are the CashPilot worker's own.
			if !strings.Contains(err.Error(), "no longer matches the deploy request") {
				t.Fatalf("refusal %q does not say the container changed", err)
			}
			for _, call := range fake.recorded() {
				if call == "POST /containers/create" || strings.HasPrefix(call, "DELETE ") || strings.HasSuffix(call, "/stop") || strings.HasSuffix(call, "/rename") {
					t.Fatalf("a refused checked deploy still called %q (calls %v)", call, fake.recorded())
				}
			}
		})
	}
}

// A program has no container ID to name: the lookup names none for a first deploy,
// and refuses once the program runs here.
func TestNativeDeployTargetNamesOnlyAFirstDeploy(t *testing.T) {
	p := newTestNativeProvider(t)
	if got, err := p.DeployTarget(context.Background(), "prog"); err != nil || got != NoContainer {
		t.Fatalf("DeployTarget before any deploy = %q, %v; want %q", got, err, NoContainer)
	}
	seedNativeEntry(t, p, "prog")
	if got, err := p.DeployTarget(context.Background(), "prog"); !errors.Is(err, ErrNoDeployTarget) {
		t.Fatalf("DeployTarget while the program runs = %q, %v; want ErrNoDeployTarget", got, err)
	}
}

// A checked deploy of a program only creates one: it refuses an expected container
// (none can exist), and refuses while the program runs, before any download.
func TestNativeCheckedDeployOnlyCreates(t *testing.T) {
	ctx := context.Background()
	p := newTestNativeProvider(t)
	var downloads atomic.Int32
	url := serveCounting(t, p, &downloads)
	spec := DeploySpec{Slug: "prog", Service: nativeService("prog", url, strings.Repeat("0", 64), "")}

	someID := "live"
	spec.ExpectedContainer = &someID
	var changed *DeployTargetChanged
	if _, err := p.Deploy(ctx, spec, nil); !errors.As(err, &changed) || changed.Actual != NoContainer {
		t.Fatalf("checked deploy expecting a container: err = %v; want DeployTargetChanged naming none", err)
	}

	none := NoContainer
	spec.ExpectedContainer = &none
	seedNativeEntry(t, p, "prog")
	if _, err := p.Deploy(ctx, spec, nil); !errors.Is(err, ErrNoDeployTarget) {
		t.Fatalf("checked deploy while the program runs: err = %v; want ErrNoDeployTarget", err)
	}
	if downloads.Load() != 0 {
		t.Fatalf("a refused checked deploy downloaded the binary %d times", downloads.Load())
	}

	// The positive control: with no program here, a create-only yes goes on to the
	// download (which fails on the checksum, past the check).
	if err := p.mutateRegistry(func(reg *nativeRegistry) { delete(reg.Entries, "prog") }); err != nil {
		t.Fatalf("clear registry: %v", err)
	}
	if _, err := p.Deploy(ctx, spec, nil); err == nil || errors.As(err, &changed) || errors.Is(err, ErrNoDeployTarget) || downloads.Load() == 0 {
		t.Fatalf("create-only deploy with no program here: err = %v, %d downloads; want it past the check, at the download", err, downloads.Load())
	}
}

// A program this session launched but could not write to the registry still runs:
// the lookup must not name it as none, and a create-only yes must not stop it.
func TestNativeProgramWithNoRegistryEntryIsNotAFirstDeploy(t *testing.T) {
	ctx := context.Background()
	p := newTestNativeProvider(t)
	var downloads atomic.Int32
	url := serveCounting(t, p, &downloads)
	p.mu.Lock()
	p.procs["prog"] = &managedProcess{slug: "prog"}
	p.mu.Unlock()
	t.Cleanup(func() {
		p.mu.Lock()
		delete(p.procs, "prog")
		p.mu.Unlock()
	})
	if len(p.readRegistry().Entries) != 0 {
		t.Fatalf("registry has entries %v; want none", p.readRegistry().Entries)
	}

	if got, err := p.DeployTarget(ctx, "prog"); !errors.Is(err, ErrNoDeployTarget) {
		t.Fatalf("DeployTarget while an unregistered program runs = %q, %v; want ErrNoDeployTarget", got, err)
	}
	none := NoContainer
	spec := DeploySpec{Slug: "prog", Service: nativeService("prog", url, strings.Repeat("0", 64), ""), ExpectedContainer: &none}
	if _, err := p.Deploy(ctx, spec, nil); !errors.Is(err, ErrNoDeployTarget) {
		t.Fatalf("checked deploy while an unregistered program runs: err = %v; want ErrNoDeployTarget", err)
	}
	if downloads.Load() != 0 {
		t.Fatalf("a refused checked deploy downloaded the binary %d times", downloads.Load())
	}
	p.mu.Lock()
	_, still := p.procs["prog"]
	p.mu.Unlock()
	if !still {
		t.Fatal("a refused checked deploy stopped the running program")
	}
}

func seedNativeEntry(t *testing.T, p *NativeProcessProvider, slug string) {
	t.Helper()
	if err := p.mutateRegistry(func(reg *nativeRegistry) {
		reg.Entries[slug] = &nativeRegistryEntry{Slug: slug, URL: "https://example.test/x", Desired: "stopped"}
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
}

// serveCounting serves a binary that matches no checksum and counts its downloads.
func serveCounting(t *testing.T, p *NativeProcessProvider, downloads *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		_, _ = w.Write([]byte("not the binary"))
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.CheckRedirect = httpsOnlyRedirect
	p.httpClient = client
	return srv.URL + "/stub"
}

// A registry that exists but cannot be read may hold a program from an earlier
// session. A checked lookup and a checked deploy must refuse, not read it as none.
func TestNativeCheckedDeployRefusesAnUnreadableRegistry(t *testing.T) {
	ctx := context.Background()
	p := newTestNativeProvider(t)
	var downloads atomic.Int32
	url := serveCounting(t, p, &downloads)
	if err := os.MkdirAll(filepath.Dir(p.statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := p.DeployTarget(ctx, "prog"); err == nil || got == NoContainer {
		t.Fatalf("DeployTarget with an unreadable registry = %q, %v; want an error", got, err)
	}
	none := NoContainer
	spec := DeploySpec{Slug: "prog", Service: nativeService("prog", url, strings.Repeat("0", 64), ""), ExpectedContainer: &none}
	if _, err := p.Deploy(ctx, spec, nil); err == nil || downloads.Load() != 0 {
		t.Fatalf("checked deploy with an unreadable registry: err = %v, %d downloads; want a refusal before the download", err, downloads.Load())
	}
	// A registry that holds only JSON null is just as unreadable.
	if err := os.WriteFile(p.statePath, []byte("null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := p.DeployTarget(ctx, "prog"); err == nil || got == NoContainer {
		t.Fatalf("DeployTarget with a null registry = %q, %v; want an error", got, err)
	}
	// A missing registry is still a first deploy.
	if err := os.Remove(p.statePath); err != nil {
		t.Fatal(err)
	}
	if got, err := p.DeployTarget(ctx, "prog"); err != nil || got != NoContainer {
		t.Fatalf("DeployTarget with no registry file = %q, %v; want none", got, err)
	}
}
