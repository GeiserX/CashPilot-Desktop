package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// stubProvider is a container runtime that does nothing, so GetAppState can be called
// without Docker. keyring.MockInit is installed by TestMain in fleet_server_test.go.
type stubProvider struct{}

func (stubProvider) Status(context.Context) runtime.Status { return runtime.Status{} }
func (stubProvider) Deploy(context.Context, runtime.DeploySpec, func(string)) (runtime.ContainerInfo, error) {
	return runtime.ContainerInfo{}, nil
}
func (stubProvider) Start(context.Context, string) error                         { return nil }
func (stubProvider) Stop(context.Context, string, int) error                     { return nil }
func (stubProvider) Restart(context.Context, string, int) error                  { return nil }
func (stubProvider) Remove(context.Context, string, runtime.RemoveOptions) error { return nil }
func (stubProvider) PlanRemoval(context.Context, string, map[string]string) (runtime.RemovalPlan, error) {
	return runtime.RemovalPlan{}, nil
}
func (stubProvider) Logs(context.Context, string, int) (string, error) {
	return "", nil
}
func (stubProvider) List(context.Context) ([]runtime.ContainerInfo, error) { return nil, nil }

// THE RULE: the credentials a collector needs reach the form the user types into.
//
// The wizard renders docker.env plus whatever collectorFields the app state carries
// for that slug. Repocket is the case that shows why the two are not the same list:
// its container takes RP_EMAIL and an API key from the dashboard, while the collector
// signs in to Firebase with the ACCOUNT password. When the frontend kept this table
// itself, the web catalog's env rename left the form with nothing to collect the
// password with, and Repocket earnings could only ever fail.
//
// This goes through the real binding and the real JSON, because the frontend reads
// the wire format, not the Go struct: a renamed json tag would empty every wizard
// form while every Go test still passed.
func TestGetAppStateCarriesCollectorFieldsToTheWizard(t *testing.T) {
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
	provider := stubProvider{}
	app := &App{
		ctx:      context.Background(),
		cfg:      cfg,
		catalog:  cat,
		store:    st,
		runtime:  provider,
		services: services.NewManager(provider, cat, st),
	}

	state, err := app.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState: %v", err)
	}

	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal app state: %v", err)
	}
	var wire struct {
		CollectorFields map[string][]struct {
			Key    string `json:"key"`
			Label  string `json:"label"`
			Secret bool   `json:"secret"`
		} `json:"collectorFields"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode app state: %v", err)
	}

	repocket, ok := wire.CollectorFields["repocket"]
	if !ok {
		t.Fatalf("the app state reaching the frontend has no collector fields for repocket: %v", wire.CollectorFields)
	}
	var password bool
	for _, field := range repocket {
		if field.Key == "REPOCKET_PASSWORD" {
			password = field.Secret
			if field.Label == "" {
				t.Error("the password field reaches the form with no label")
			}
		}
	}
	if !password {
		t.Errorf("the Repocket account password is not a masked field in the wizard form: %+v", repocket)
	}

	// Every slug the table declares has to survive the trip, not just this one.
	for _, slug := range collectors.FieldSlugs() {
		if len(wire.CollectorFields[slug]) != len(collectors.Fields(slug)) {
			t.Errorf("%s: %d collector fields reached the frontend, %d declared",
				slug, len(wire.CollectorFields[slug]), len(collectors.Fields(slug)))
		}
	}
}
