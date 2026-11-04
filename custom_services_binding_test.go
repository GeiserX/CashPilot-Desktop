package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

type deployRecorder struct {
	stubProvider
	specs []runtime.DeploySpec
}

func (d *deployRecorder) Deploy(_ context.Context, spec runtime.DeploySpec, _ func(string)) (runtime.ContainerInfo, error) {
	d.specs = append(d.specs, spec)
	return runtime.ContainerInfo{Slug: spec.Slug, ContainerID: "c1", Name: "cashpilot-" + spec.Slug, Status: "running", Image: spec.Service.Docker.Image}, nil
}

// THE RULE: a custom service's secret and command never reach the UI, through any
// binding, and what the deploy form sends for it is neither stored nor run.
//
// This goes through the real bindings and the real JSON the frontend reads. The
// control is the deploy itself: the secret does reach the runtime, so its absence
// from the app state is the masking and not a definition that was never saved.
func TestCustomServiceBindingsNeverSendTheSecret(t *testing.T) {
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatal(err)
	}
	provider := &deployRecorder{}
	app := &App{ctx: context.Background(), cfg: cfg, catalog: cat, store: st, runtime: provider,
		services: services.NewManager(provider, cat, st)}

	view, err := app.SaveCustomService("", services.CustomDefinition{
		Name: "Mine", Image: "org/mine:1.2.3", Command: "run --key cmd-s3cret",
		Env: []services.CustomEnv{{Key: "TOKEN", Value: "env-s3cret", Secret: true}},
	})
	if err != nil {
		t.Fatalf("SaveCustomService: %v", err)
	}

	listed, err := app.ListCustomServices()
	if err != nil {
		t.Fatal(err)
	}
	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	svc, err := app.GetService(view.Slug)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{"ListCustomServices": listed, "GetAppState": state, "GetService": svc, "SaveCustomService": view} {
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), "env-s3cret") {
			t.Errorf("%s sends the secret value to the UI", name)
		}
		if name != "ListCustomServices" && name != "SaveCustomService" && strings.Contains(string(raw), "cmd-s3cret") {
			t.Errorf("%s sends the command to the UI", name)
		}
	}
	raw, _ := json.Marshal(state)
	if !strings.Contains(string(raw), `"slug":"custom-mine"`) || !strings.Contains(string(raw), `"custom":true`) {
		t.Fatal("the app state does not list the custom service as custom")
	}

	if _, err := app.DeployService(view.Slug, map[string]string{"TOKEN": "typed-in-form"}, nil, nil); err != nil {
		t.Fatalf("DeployService: %v", err)
	}
	if len(provider.specs) != 1 {
		t.Fatalf("deploys reaching the runtime: %d", len(provider.specs))
	}
	got := provider.specs[0]
	if len(got.Service.Docker.Env) != 1 || got.Service.Docker.Env[0].Default != "env-s3cret" || len(got.Env) != 0 {
		t.Fatalf("runtime got env %+v and form values %v, want only the saved secret", got.Service.Docker.Env, got.Env)
	}
	if creds, _ := st.GetCredentials(view.Slug); len(creds) != 0 {
		t.Fatalf("the form's values were stored as credentials: %v", creds)
	}

	if err := app.DeleteCustomService(view.Slug); err == nil {
		t.Fatal("deleted while deployed")
	}
}

// customBindingApp is an App over a real store in a temp data dir, with the stub
// runtime, for the custom-service binding tests. It returns the data dir too, so a
// test can reach the database file underneath.
func customBindingApp(t *testing.T) (*App, *store.Store, *deployRecorder, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", dir)
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatal(err)
	}
	provider := &deployRecorder{}
	app := &App{ctx: context.Background(), cfg: cfg, catalog: cat, store: st, runtime: provider,
		services: services.NewManager(provider, cat, st)}
	return app, st, provider, cfg.DataDir()
}

// rawDB opens the store's database file directly, to put it in a state the store's
// own API refuses to write.
func rawDB(t *testing.T, dataDir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "cashpilot-desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// THE RULE: a custom service deploys from its saved definition alone, so an
// unreadable credentials blob under its slug cannot stop the deploy. The second
// deploy, a catalog service over the same kind of blob, is the control: the blob
// really is unreadable, and a service that needs it still fails.
func TestDeployCustomServiceIgnoresAnUnreadableCredentialsBlob(t *testing.T) {
	app, _, provider, dir := customBindingApp(t)
	view, err := app.SaveCustomService("", services.CustomDefinition{Name: "Mine", Image: "org/mine:1.2.3"})
	if err != nil {
		t.Fatalf("SaveCustomService: %v", err)
	}
	db := rawDB(t, dir)
	for _, slug := range []string{view.Slug, "honeygain"} {
		if _, err := db.Exec(`INSERT INTO credentials(slug, value, updated_at) VALUES(?, 'not-a-blob', datetime('now'))`, slug); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.DeployService(view.Slug, nil, nil, nil); err != nil {
		t.Fatalf("custom deploy stopped on the credentials blob: %v", err)
	}
	if len(provider.specs) != 1 {
		t.Fatalf("deploys reaching the runtime: %d", len(provider.specs))
	}
	if _, err := app.DeployService("honeygain", nil, nil, nil); err == nil {
		t.Fatal("control: a catalog deploy read the unreadable blob without error")
	}
}

// THE RULE: custom services that could not be loaded at startup are loaded by a
// later call once they can be, without a save, a delete or a restart. The state
// before the repair is the control: the service is really missing while the load
// keeps failing.
func TestCustomServicesLoadAgainAfterAFailedStartupLoad(t *testing.T) {
	app, st, _, dir := customBindingApp(t)
	if err := st.InsertCustomService("custom-good", []byte(`{"name":"Good","image":"org/good:1.0.0"}`)); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	if _, err := db.Exec(`INSERT INTO custom_services(slug, value, deleted, created_at, updated_at) VALUES('custom-bad', 'not-a-blob', 0, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	app.loadCustom()
	listsGood := func() bool {
		t.Helper()
		state, err := app.GetAppState()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(state)
		return strings.Contains(string(raw), `"slug":"custom-good"`)
	}
	if listsGood() {
		t.Fatal("control: custom-good listed although the load fails")
	}
	if _, err := db.Exec(`UPDATE custom_services SET deleted = 1 WHERE slug = 'custom-bad'`); err != nil {
		t.Fatal(err)
	}
	if !listsGood() {
		t.Fatal("custom-good is still missing after the load could succeed")
	}
	if _, err := app.DeployService("custom-good", nil, nil, nil); err != nil {
		t.Fatalf("deploy after the retried load: %v", err)
	}
}
