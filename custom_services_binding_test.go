package main

import (
	"context"
	"encoding/json"
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
