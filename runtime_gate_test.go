package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// engineProvider is a runtime whose Status is fixed and whose Deploy records the
// call and answers deployErr.
type engineProvider struct {
	stubProvider
	status    runtime.Status
	deployErr error
	deployed  []string
}

func (p *engineProvider) Status(context.Context) runtime.Status { return p.status }

func (p *engineProvider) Deploy(_ context.Context, spec runtime.DeploySpec, _ func(string)) (runtime.ContainerInfo, error) {
	p.deployed = append(p.deployed, spec.Slug)
	if p.deployErr != nil {
		return runtime.ContainerInfo{}, p.deployErr
	}
	return runtime.ContainerInfo{Slug: spec.Slug, Name: "cashpilot-" + spec.Slug, Status: "running"}, nil
}

// gateApp is an App on the real catalog with provider as its only runtime.
func gateApp(t *testing.T, provider runtime.Provider) *App {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded: %v", err)
	}
	return &App{ctx: context.Background(), cfg: cfg, catalog: cat, store: st, runtime: provider,
		services: services.NewManager(provider, cat, st)}
}

func TestDockerGapSaysWhyADockerAppCannotRunHere(t *testing.T) {
	docker := catalog.Service{Slug: "earnfm", Name: "Earn.fm"}
	docker.Docker.Image = "earnfm/earnfm-client:1.0.0"
	noImage := catalog.Service{Slug: "wipter", Name: "Wipter"}
	missing := runtime.Status{Reason: runtime.ReasonNotInstalled}
	stopped := runtime.Status{Reason: runtime.ReasonNotRunning}
	cases := []struct {
		name   string
		svc    catalog.Service
		native bool
		status runtime.Status
		goos   string
		want   string
	}{
		{"Docker ready", docker, false, runtime.Status{Available: true, Reason: runtime.ReasonRunning}, "windows", ""},
		{"a native app runs without Docker", docker, true, missing, "windows", ""},
		{"an app with no image needs no Docker", noImage, false, missing, "windows", ""},
		{"not installed on Windows", docker, false, missing, "windows", "Docker is not set up on this PC yet, so Earn.fm cannot run here."},
		{"an unread state counts as not set up", docker, false, runtime.Status{}, "linux", "Docker is not set up on this computer yet, so Earn.fm cannot run here."},
		{"installed, not running, on macOS", docker, false, stopped, "darwin", "Docker is not ready on this computer yet, so Earn.fm cannot run here."},
		{"Windows containers", docker, false, runtime.Status{Reason: runtime.ReasonWindowsContainers}, "windows", "Docker is not ready on this PC yet"},
	}
	for _, c := range cases {
		got := dockerGap(c.svc, c.native, c.status, c.goos)
		if c.want == "" {
			if got != "" {
				t.Errorf("%s: gap %q, want none", c.name, got)
			}
			continue
		}
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: gap %q, want it to start %q", c.name, got, c.want)
		}
		if !strings.Contains(got, "Set up Docker") {
			t.Errorf("%s: gap %q does not say where to go", c.name, got)
		}
	}
}

// With no engine, a Docker app's deploy stops before it saves or runs anything, and
// says why in plain words. The frontend adds the shared Docker reading to the
// preflight; the deploy still checks the engine before it changes anything.
func TestADockerDeployWithoutAnEngineIsRefusedBeforeAnythingChanges(t *testing.T) {
	provider := &engineProvider{status: runtime.Status{Reason: runtime.ReasonNotInstalled}}
	app := gateApp(t, provider)
	values := map[string]string{"EARNFM_TOKEN": "tok", "EARNFM_EMAIL": "a@b.c", "EARNFM_PASSWORD": "pw"}

	_, err := app.DeployService("earnfm", values, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "Docker is not set up") || !strings.Contains(err.Error(), "Nothing was deployed") {
		t.Fatalf("DeployService error = %v, want the plain refusal", err)
	}
	if _, err := app.DeployServiceRouted("earnfm", values, 1); err == nil || !strings.Contains(err.Error(), "Docker is not set up") {
		t.Fatalf("DeployServiceRouted error = %v, want the plain refusal", err)
	}
	if len(provider.deployed) != 0 {
		t.Fatalf("the runtime was asked to deploy %v", provider.deployed)
	}
	if got, _ := app.store.GetCredentials("earnfm"); len(got) != 0 {
		t.Fatalf("a refused deploy saved %v", got)
	}

	report, err := app.PreflightService("earnfm")
	if err != nil {
		t.Fatalf("PreflightService: %v", err)
	}
	if report.Blocking || strings.Contains(report.Summary, "Docker is not") {
		t.Fatalf("preflight assessed Docker readiness separately: %+v", report)
	}

	// The engine answers: the same deploy goes through.
	provider.status = runtime.Status{Available: true, Reason: runtime.ReasonRunning}
	if _, err := app.DeployService("earnfm", values, nil, nil); err != nil {
		t.Fatalf("DeployService with an engine: %v", err)
	}
}

// Alerts: with only native apps able to run, the list says Docker is not ready and
// which apps run without it. Nothing runs: "Runtime offline", as before. Docker
// ready: neither.
func TestAlertsSayDockerIsNotReadyWhenOnlyNativeAppsRun(t *testing.T) {
	app := gateApp(t, &engineProvider{})
	titles := func(status runtime.Status) []string {
		var out []string
		for _, n := range app.notifications(status, nil, []store.Deployment{{Slug: "x"}}) {
			out = append(out, n.Title+": "+n.Message)
		}
		return out
	}
	has := func(list []string, prefix string) bool {
		for _, item := range list {
			if strings.HasPrefix(item, prefix) {
				return true
			}
		}
		return false
	}
	native := titles(runtime.Status{NativeAvailable: true, NativeServices: []string{"MystNodes"}, Reason: runtime.ReasonNotInstalled})
	if !has(native, "Docker is not ready: Without Docker only MystNodes can run") || has(native, "Runtime offline") {
		t.Fatalf("native only: %v", native)
	}
	none := titles(runtime.Status{Reason: runtime.ReasonNotInstalled})
	if !has(none, "Runtime offline") || has(none, "Docker is not ready") {
		t.Fatalf("nothing runs: %v", none)
	}
	ready := titles(runtime.Status{Available: true, NativeAvailable: true, NativeServices: []string{"MystNodes"}})
	if has(ready, "Docker is not ready") || has(ready, "Runtime offline") {
		t.Fatalf("Docker ready: %v", ready)
	}
}

// A deploy sent with every box empty, which then fails, must leave nothing stored
// and must not mark the service "Configured" in Settings. The form sends every box,
// so before this the blanks were saved and counted as configured.
func TestAFailedDeployWithEmptyBoxesStoresNothingAndIsNotConfigured(t *testing.T) {
	provider := &engineProvider{status: runtime.Status{Available: true, Reason: runtime.ReasonRunning}, deployErr: errors.New("the admin prompt was declined")}
	app := gateApp(t, provider)
	blanks := map[string]string{"UI_ADDRESS": "", "UDP_PORTS": "", "MYSTNODES_EMAIL": "", "MYSTNODES_PASSWORD": ""}

	if _, err := app.DeployService("mysterium", blanks, nil, nil); err == nil {
		t.Fatal("the deploy was meant to fail")
	}
	if got, _ := app.store.GetCredentials("mysterium"); len(got) != 0 {
		t.Fatalf("stored after a failed deploy with empty boxes: %v", got)
	}
	for _, slug := range app.store.ListCredentialSlugs() {
		if slug == "mysterium" {
			t.Fatal("mysterium is queued for collection with no login")
		}
	}
	if configured(t, app, "mysterium") {
		t.Fatal("Settings shows MystNodes Configured after a failed deploy with empty boxes")
	}

	// A blank password keeps the saved secret. Clearing the public login
	// still leaves the collector unconfigured.
	if err := app.SaveCredentials("mysterium", map[string]string{"MYSTNODES_EMAIL": "a@b.c", "MYSTNODES_PASSWORD": "pw"}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if !configured(t, app, "mysterium") {
		t.Fatal("a saved MystNodes login does not read as Configured")
	}
	if err := app.SaveCredentials("mysterium", blanks); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if got, err := app.store.GetCredentials("mysterium"); err != nil || got["MYSTNODES_PASSWORD"] != "pw" || got["MYSTNODES_EMAIL"] != "" {
		t.Fatalf("blank save did not preserve the password and clear the email: %v", err)
	}
	if configured(t, app, "mysterium") {
		t.Fatal("Settings shows MystNodes Configured without an email")
	}
}

func TestFailedDeployKeepsPreviouslySavedCredentials(t *testing.T) {
	provider := &engineProvider{status: runtime.Status{Available: true}, deployErr: errors.New("the runtime refused to start")}
	app := gateApp(t, provider)
	app.services.Register(runtime.NativeRuntimeKind, provider)
	values := map[string]string{"MYSTNODES_EMAIL": "", "MYSTNODES_PASSWORD": "", "UDP_PORTS": "23000:23029"}

	if _, err := app.DeployService("mysterium", values, nil, nil); err == nil {
		t.Fatal("the deploy was meant to fail")
	}
	if got, _ := app.store.GetCredentials("mysterium"); len(got) != 0 {
		t.Errorf("failed deploy stored defaults without a login: %v", got)
	}
	if slugs := app.store.ListCredentialSlugs(); len(slugs) != 0 {
		t.Errorf("failed deploy queued collection: %v", slugs)
	}

	if err := app.SaveCredentials("earnfm", map[string]string{"EARNFM_TOKEN": "saved"}); err != nil {
		t.Fatal(err)
	}
	typed := map[string]string{"EARNFM_TOKEN": "submitted"}
	if _, err := app.DeployService("earnfm", typed, nil, nil); err == nil {
		t.Fatal("the direct deploy was meant to fail")
	}
	if got, _ := app.store.GetCredentials("earnfm"); got["EARNFM_TOKEN"] != "saved" {
		t.Errorf("failed direct deploy replaced saved credentials: %v", got)
	}

	if err := app.SaveCredentials("earnfm", map[string]string{"EARNFM_TOKEN": "saved"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DeployServiceRouted("earnfm", typed, -1); err == nil {
		t.Fatal("the routed deploy was meant to fail")
	}
	if got, _ := app.store.GetCredentials("earnfm"); got["EARNFM_TOKEN"] != "saved" {
		t.Errorf("failed routed deploy replaced saved credentials: %v", got)
	}

	provider.deployErr = nil
	if _, err := app.DeployService("earnfm", typed, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := app.store.GetCredentials("earnfm"); got["EARNFM_TOKEN"] != "submitted" {
		t.Errorf("successful deploy did not store the submitted values: %v", got)
	}
}

// "Configured" needs the earnings collector's own login, not just any value: a
// UDP range alone leaves MystNodes with nothing to sign in with.
func TestConfiguredNeedsTheCollectorsRequiredLogin(t *testing.T) {
	app := gateApp(t, &engineProvider{})
	cases := []struct {
		creds map[string]string
		want  bool
	}{
		{map[string]string{"MYSTNODES_EMAIL": "", "MYSTNODES_PASSWORD": ""}, false},
		{map[string]string{"UDP_PORTS": "23000:23029"}, false},
		{map[string]string{"MYSTNODES_EMAIL": "a@b.c"}, false},
		{map[string]string{"MYSTNODES_EMAIL": "a@b.c", "MYSTNODES_PASSWORD": "pw"}, true},
	}
	for _, c := range cases {
		if got := app.credsConfigured("mysterium", c.creds); got != c.want {
			t.Errorf("credsConfigured(%v) = %v, want %v", c.creds, got, c.want)
		}
	}
}

func configured(t *testing.T, app *App, slug string) bool {
	t.Helper()
	settings, err := app.GetSettingsState()
	if err != nil {
		t.Fatalf("GetSettingsState: %v", err)
	}
	for _, c := range settings.Collectors {
		if c.Slug == slug {
			return c.Configured
		}
	}
	t.Fatalf("%s is not in Settings' collector list", slug)
	return false
}

type heldEngineProvider struct {
	engineProvider
	entered, release chan struct{}
	firstEntered     atomic.Bool
}

func (p *heldEngineProvider) Deploy(ctx context.Context, spec runtime.DeploySpec, progress func(string)) (runtime.ContainerInfo, error) {
	if p.firstEntered.CompareAndSwap(false, true) {
		close(p.entered)
		<-p.release
	}
	return p.engineProvider.Deploy(ctx, spec, progress)
}

func TestExplicitSaveSurvivesADeployAlreadyInProgress(t *testing.T) {
	for _, tc := range []struct {
		name, saved, want string
	}{
		{name: "replace", saved: "explicit-save", want: "explicit-save"},
		{name: "keep saved secret", want: "original"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &heldEngineProvider{
				engineProvider: engineProvider{status: runtime.Status{Available: true, Reason: runtime.ReasonRunning}},
				entered:        make(chan struct{}), release: make(chan struct{}),
			}
			app := gateApp(t, provider)
			if err := app.SaveCredentials("earnfm", map[string]string{"EARNFM_TOKEN": "original"}); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := app.DeployService("earnfm", map[string]string{"EARNFM_TOKEN": "deploy"}, nil, nil)
				done <- err
			}()
			select {
			case <-provider.entered:
			case err := <-done:
				t.Fatalf("deploy finished before it reached the runtime: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("deploy did not reach the runtime")
			}
			saved := make(chan error, 1)
			go func() { saved <- app.SaveCredentials("earnfm", map[string]string{"EARNFM_TOKEN": tc.saved}) }()
			var saveErr error
			select {
			case saveErr = <-saved:
			case <-time.After(5 * time.Second):
				close(provider.release)
				<-done
				<-saved
				t.Fatal("Save Credentials waited for the deploy")
			}
			close(provider.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if saveErr != nil {
				t.Fatal(saveErr)
			}
			got, err := app.store.GetCredentials("earnfm")
			if err != nil {
				t.Fatal(err)
			}
			if got["EARNFM_TOKEN"] != tc.want {
				t.Error("the deploy overwrote the later Save Credentials")
			}
		})
	}
}
