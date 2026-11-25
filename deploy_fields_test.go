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

// newFormFieldsTestApp runs a catalog with a service whose setup card marks a
// collector login with * (mysterium: MystNodes email and password) and one whose
// container needs a value (req), on a runtime that counts deploys.
func newFormFieldsTestApp(t *testing.T) (*App, *deployCountingProvider) {
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
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/bandwidth/mysterium.yml": {Data: []byte(
			"name: MystNodes\nslug: mysterium\ncategory: bandwidth\nstatus: active\ndocker:\n  image: myst/image:1.0.0\n  env:\n" +
				"    - key: UI_ADDRESS\n      label: WebUI address\n      required: false\n      default: \"127.0.0.1\"\n")},
		"services/bandwidth/req.yml": {Data: []byte(
			"name: Req\nslug: req\ncategory: bandwidth\nstatus: active\ndocker:\n  image: req/image:1.0.0\n  env:\n" +
				"    - key: TOKEN\n      label: Token\n      required: true\n    - key: NAME\n      label: Device name\n      required: true\n      default: desktop\n")},
	})
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

// THE RULE: a deploy from the setup card with a field marked * left empty starts
// nothing (no runtime call, so no admin prompt and no container) and saves
// nothing, and the message names every empty field. It holds for the logins the
// earnings collector needs as well as for the container's own settings, because
// the card marks both the same way.
func TestDeployFromTheCardRefusesEmptyRequiredFields(t *testing.T) {
	app, provider := newFormFieldsTestApp(t)

	empty := map[string]string{"UI_ADDRESS": "127.0.0.1", "MYSTNODES_EMAIL": "", "MYSTNODES_PASSWORD": " "}
	_, err := app.DeployService("mysterium", empty, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "MystNodes email, MystNodes password") || !strings.Contains(err.Error(), "nothing was deployed") {
		t.Fatalf("DeployService with empty logins: err = %v, want both fields named", err)
	}
	if _, err := app.DeployServiceRouted("mysterium", empty, 1); err == nil || !strings.Contains(err.Error(), "MystNodes email") {
		t.Fatalf("DeployServiceRouted with empty logins: err = %v", err)
	}
	if _, err := app.DeployService("req", map[string]string{"TOKEN": "", "NAME": ""}, nil, nil); err == nil ||
		!strings.Contains(err.Error(), ": Token") || strings.Contains(err.Error(), "Device name") {
		t.Fatalf("DeployService with an empty container value: err = %v, want Token only (Device name has a default)", err)
	}
	if len(provider.deployed) != 0 {
		t.Fatalf("the runtime was asked to deploy %v with required fields empty", provider.deployed)
	}
	if got, _ := app.store.GetCredentials("mysterium"); len(got) != 0 {
		t.Fatalf("a refused deploy saved %v", got)
	}

	full := map[string]string{"UI_ADDRESS": "127.0.0.1", "MYSTNODES_EMAIL": "a@example.com", "MYSTNODES_PASSWORD": "pw"}
	if _, err := app.DeployService("mysterium", full, nil, nil); err != nil {
		t.Fatalf("DeployService with every field: %v", err)
	}
	// A redeploy sends no values and uses what is saved: not a form, not checked here.
	if _, err := app.DeployService("mysterium", map[string]string{}, nil, nil); err != nil {
		t.Fatalf("redeploy with no values: %v", err)
	}
	if !slices.Equal(provider.deployed, []string{"mysterium", "mysterium"}) {
		t.Fatalf("deployed %v, want mysterium twice", provider.deployed)
	}
}
