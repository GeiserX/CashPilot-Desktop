package main

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// recordingProvider is a runtime that runs nothing and remembers what Remove was asked
// to do.
type recordingProvider struct {
	stubProvider
	removeOpts []runtime.RemoveOptions
}

func (r *recordingProvider) Remove(_ context.Context, _ string, opts runtime.RemoveOptions) error {
	r.removeOpts = append(r.removeOpts, opts)
	return nil
}

// THE RULE: the user's two answers reach the runtime unchanged.
//
// RemoveService is the last hop of the only irreversible path in the app. Everything
// below it is covered — the manager passes the catalog's critical list, the runtime
// refuses without the explicit yes — but all of that is reachable only through this
// binding, and a binding that hardcoded true for either flag would delete a Storj
// identity on an ordinary "remove this service" with every other test still green.
func TestRemoveServiceCarriesTheUsersAnswersToTheRuntime(t *testing.T) {
	cases := []struct {
		name          string
		deleteData    bool
		allowCritical bool
	}{
		{"the ordinary remove keeps everything", false, false},
		{"delete the data, but nothing irreplaceable", true, false},
		{"delete the data including what cannot be recovered", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &recordingProvider{}
			app := newRemovalTestApp(t, provider)

			if err := app.RemoveService("keeper", tc.deleteData, tc.allowCritical); err != nil {
				t.Fatalf("RemoveService: %v", err)
			}
			if len(provider.removeOpts) != 1 {
				t.Fatalf("Remove reached the runtime %d times", len(provider.removeOpts))
			}
			opts := provider.removeOpts[0]
			if opts.DeleteData != tc.deleteData {
				t.Fatalf("DeleteData reached the runtime as %v, the user said %v", opts.DeleteData, tc.deleteData)
			}
			if opts.AllowCritical != tc.allowCritical {
				t.Fatalf("AllowCritical reached the runtime as %v, the user said %v", opts.AllowCritical, tc.allowCritical)
			}
		})
	}
}

// newRemovalTestApp builds an App wired to the given runtime, with one catalog entry
// that keeps something irreplaceable.
func newRemovalTestApp(t *testing.T, provider runtime.Provider) *App {
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
		"services/bandwidth/keeper.yml": {Data: []byte(
			"name: Identity Keeper\nslug: keeper\ncategory: bandwidth\nstatus: active\n" +
				"docker:\n  image: keeper/image:1.0.0\n  volumes:\n    - \"keeper-data:/var/lib/keeper\"\n" +
				"  critical_volumes:\n    - target: \"/var/lib/keeper\"\n      holds: \"Node identity keystore.\"\n")},
	})
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded: %v", err)
	}
	return &App{
		cfg:      cfg,
		store:    st,
		catalog:  cat,
		runtime:  provider,
		services: services.NewManager(provider, cat, st),
		ctx:      context.Background(),
	}
}
