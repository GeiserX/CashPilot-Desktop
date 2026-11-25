package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// newFormFieldsTestApp runs a catalog with a service whose card asks for the
// earnings collector's login but whose container needs nothing typed (mysterium:
// MystNodes email and password), and one whose container needs a value (req), on a
// runtime that counts deploys.
func newFormFieldsTestApp(t *testing.T, files fstest.MapFS) (*App, *deployCountingProvider) {
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
	cat, err := catalog.LoadEmbedded(files)
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded: %v", err)
	}
	provider := &deployCountingProvider{}
	return &App{
		cfg:      cfg,
		store:    st,
		catalog:  cat,
		runtime:  provider,
		services: services.NewManager(provider, cat, st),
		ctx:      context.Background(),
	}, provider
}

var formFieldsCatalog = fstest.MapFS{
	"services/bandwidth/mysterium.yml": {Data: []byte(
		"name: MystNodes\nslug: mysterium\ncategory: bandwidth\nstatus: active\ndocker:\n  image: myst/image:1.0.0\n  env:\n" +
			"    - key: UI_ADDRESS\n      label: WebUI address\n      required: false\n      default: \"127.0.0.1\"\n")},
	"services/bandwidth/req.yml": {Data: []byte(
		"name: Req\nslug: req\ncategory: bandwidth\nstatus: active\ndocker:\n  image: req/image:1.0.0\n  env:\n" +
			"    - key: TOKEN\n      label: Token\n      required: true\n    - key: NAME\n      label: Device name\n      required: true\n      default: desktop\n")},
}

// THE RULE: the app refuses a deploy only for what the deploy itself needs, the
// container's required settings, and it refuses before any runtime call (so no
// admin prompt and no container) and before anything is saved. The earnings
// collector's login is not part of the deploy: leaving it empty still deploys,
// from the card and from any other caller (a command from a joined CashPilot sends
// only the container's settings).
func TestDeployChecksOnlyWhatTheDeployNeeds(t *testing.T) {
	app, provider := newFormFieldsTestApp(t, formFieldsCatalog)

	if _, err := app.DeployService("req", map[string]string{"TOKEN": "", "NAME": ""}, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "Token") || strings.Contains(err.Error(), "Device name") {
		t.Fatalf("DeployService with an empty container value: err = %v, want Token named (Device name has a default)", err)
	}
	if len(provider.deployed) != 0 {
		t.Fatalf("the runtime was asked to deploy %v with a required container value empty", provider.deployed)
	}
	if got, _ := app.store.GetCredentials("req"); len(got) != 0 {
		t.Fatalf("a refused deploy saved %v", got)
	}

	noLogin := map[string]string{"UI_ADDRESS": "127.0.0.1", "MYSTNODES_EMAIL": "", "MYSTNODES_PASSWORD": ""}
	if _, err := app.DeployService("mysterium", noLogin, nil, nil); err != nil {
		t.Fatalf("DeployService with the collector login empty: %v, want it deployed", err)
	}
	if !slices.Equal(provider.deployed, []string{"mysterium"}) {
		t.Fatalf("deployed %v, want mysterium", provider.deployed)
	}
}

func TestDeployRejectsWhitespaceOnlyRequiredValues(t *testing.T) {
	app, provider := newFormFieldsTestApp(t, formFieldsCatalog)
	_, err := app.DeployService("req", map[string]string{"TOKEN": " \t\n", "NAME": ""}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "Token") || len(provider.deployed) != 0 {
		t.Fatalf("blank required value: error = %v, runtime calls = %v", err, provider.deployed)
	}
}

// THE RULE: a wallet seed is never a field (it lives in its file), so a Sia deploy
// with every other field filled and no seed value passes the deploy check.
func TestASiaDeployWithoutASeedValuePassesTheFieldCheck(t *testing.T) {
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded: %v", err)
	}
	svc, ok := cat.Get("sia-hostd")
	if !ok {
		t.Fatal("sia-hostd is not in the catalog")
	}
	values := map[string]string{}
	seeds := 0
	for _, item := range svc.Docker.Env {
		if item.SeedFile != nil {
			seeds++
			continue
		}
		values[item.Key] = "x"
		if item.Default != "" {
			values[item.Key] = item.Default
		}
	}
	if seeds == 0 {
		t.Fatal("sia-hostd declares no seed field; this test no longer covers one")
	}
	m := services.NewManager(&deployCountingProvider{}, cat, newFormFieldsStore(t))
	if err := m.ValidateDeploy("sia-hostd", values); err != nil {
		t.Fatalf("ValidateDeploy(sia-hostd) with no seed value: %v", err)
	}
}

func newFormFieldsStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
