package main

import (
	"context"
	"errors"
	"strings"
	"testing"

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
func gateApp(t *testing.T, provider *engineProvider) *App {
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
// says why in plain words. The "Before you deploy" panel says the same and blocks,
// instead of "Nothing stands out — Earn.fm should work normally here".
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
	if !report.Blocking || !strings.HasPrefix(report.Summary, "Docker is not set up on this") || strings.Contains(report.Summary, "should work") {
		t.Fatalf("preflight blocking=%v summary=%q, want the gap", report.Blocking, report.Summary)
	}

	// The engine answers: the same deploy goes through and the panel has no gap.
	provider.status = runtime.Status{Available: true, Reason: runtime.ReasonRunning}
	if _, err := app.DeployService("earnfm", values, nil, nil); err != nil {
		t.Fatalf("DeployService with an engine: %v", err)
	}
	if report, _ := app.PreflightService("earnfm"); report.Blocking || strings.Contains(report.Summary, "Docker is not") {
		t.Fatalf("with an engine: blocking=%v summary=%q", report.Blocking, report.Summary)
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

	// The card saves first, then deploys.
	if err := app.SaveCredentials("mysterium", blanks); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
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

	// Saving every box empty also clears what was stored, as replacing it with the
	// blanks did.
	if err := app.SaveCredentials("mysterium", map[string]string{"MYSTNODES_EMAIL": "a@b.c", "MYSTNODES_PASSWORD": "pw"}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if !configured(t, app, "mysterium") {
		t.Fatal("a saved MystNodes login does not read as Configured")
	}
	if err := app.SaveCredentials("mysterium", blanks); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if got, _ := app.store.GetCredentials("mysterium"); len(got) != 0 {
		t.Fatalf("emptying every box kept %v", got)
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
