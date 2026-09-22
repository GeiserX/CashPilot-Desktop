// Package e2e drives the real app engine — the services manager, the Docker
// provider, the store, the collectors and the exchange rates — against the fakes
// in e2e/harness.
//
// Nothing here needs a container runtime, a provider account or the internet, so
// the same run proves the app on linux, macOS and windows. What it is for: every
// piece is unit-tested on its own, and the failures that reach users are the ones
// that live BETWEEN the pieces — a deploy that works but persists the wrong row,
// a redeploy that removes an earner and puts nothing back, a runtime that goes
// away and takes the dashboard with it.
package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness"
	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/exchange"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/zalando/go-keyring"
)

// TestMain replaces the OS keychain with an in-memory one for the whole package.
// Without it these tests would read and write the real login keychain — the
// user's own credential master key — which is not something a test may touch.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

// env is one fully wired app engine: a fake daemon, fake earning platforms, a
// real SQLite store in a temp directory and the real manager on top.
type env struct {
	t         *testing.T
	Docker    *harness.Docker
	Providers *harness.Providers
	Store     *store.Store
	Catalog   *catalog.Catalog
	Manager   *services.Manager
	Collector *collectors.Registry
	Exchange  *exchange.Service
	Config    *config.Manager
	DataDir   string
}

// newEnv wires the engine against a temp data directory. The data directory is
// set through the same environment variable a user's install uses, so the path
// handling under test is the production one.
func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvIn(t, t.TempDir())
}

func newEnvIn(t *testing.T, appDir string) *env {
	t.Helper()

	docker := harness.NewDocker()
	t.Cleanup(docker.Close)
	providers := harness.NewProviders()
	t.Cleanup(providers.Close)

	// Point the app's own Docker client at the fake. These are the variables
	// client.FromEnv reads, and clearing the TLS ones matters: a developer machine
	// with a real Docker context exported would otherwise send the tests there.
	t.Setenv("DOCKER_HOST", docker.Host())
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", appDir)

	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cat := loadCatalog(t)
	manager := services.NewManager(runtime.NewDockerProvider(), cat, st)

	rates := exchange.NewService(
		exchange.WithBaseURLs(providers.URL(), providers.URL()),
		exchange.WithHTTPClient(providers.Client()),
	)

	return &env{
		t:         t,
		Docker:    docker,
		Providers: providers,
		Store:     st,
		Catalog:   cat,
		Manager:   manager,
		Collector: collectors.NewRegistry(st, collectors.WithHTTPClient(providers.Client())),
		Exchange:  rates,
		Config:    cfg,
		DataDir:   cfg.DataDir(),
	}
}

// loadCatalog reads the repository's own vendored catalog, addressed from the
// repo root rather than the process working directory. catalog.Load() searches
// relative paths that, from this package's directory, can reach the sibling web
// checkout on a developer machine and nothing at all on a CI runner; pinning the
// root keeps every run reading the same files.
func loadCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	cat, err := catalog.LoadEmbedded(os.DirFS(root))
	if err != nil {
		t.Fatalf("loading the catalog from %s: %v", root, err)
	}
	if len(cat.List()) == 0 {
		t.Fatalf("the catalog at %s is empty", root)
	}
	return cat
}

// ctx is a per-test context with a ceiling, so a fake that stops answering fails
// the test instead of hanging the run.
func (e *env) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	e.t.Cleanup(cancel)
	return ctx
}

// deployment reads a slug's row back from the store.
func (e *env) deployment(slug string) (store.Deployment, bool) {
	e.t.Helper()
	dep, ok, err := e.Store.GetDeployment(slug)
	if err != nil {
		e.t.Fatalf("GetDeployment(%s): %v", slug, err)
	}
	return dep, ok
}

// credentials that satisfy the catalog's required fields for the services these
// tests deploy. They are made up; nothing here ever reaches a real platform.
var (
	honeygainCreds = map[string]string{
		"HONEYGAIN_EMAIL":    "someone@example.test",
		"HONEYGAIN_PASSWORD": "not-a-real-password",
	}
	earnappCreds = map[string]string{"EARNAPP_UUID": "sdk-node-000000000000000000000000000000"}
)

// callIndex is the position of the first recorded call equal to want, or -1.
func callIndex(calls []string, want string) int {
	for i, call := range calls {
		if call == want {
			return i
		}
	}
	return -1
}
