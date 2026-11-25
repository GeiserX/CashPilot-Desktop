package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
)

// THE RULE: a wallet seed phrase never travels to a worker. Sia's hostd keeps its
// seed in a file in its own data volume (catalog seed_file), and this Desktop has no
// way to hand a worker a wallet choice, so it refuses such a service on a worker. It
// sends nothing, with or without a seed an older Desktop stored, and says to run it
// on this computer instead.
func TestASeedFileServiceNeverTravelsToAWorker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored map[string]string
		typed  map[string]string
	}{
		{name: "no stored seed", stored: siaSettings(), typed: map[string]string{}},
		{name: "a seed an older Desktop stored", stored: withSeed(siaSettings(), seedPhraseA), typed: map[string]string{}},
		{name: "a seed typed by an API client", stored: siaSettings(), typed: map[string]string{siaSeedKey: seedPhraseB}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newLeadApp(t)
			cat, err := catalog.LoadEmbedded(serviceFiles)
			if err != nil {
				t.Fatalf("catalog.LoadEmbedded: %v", err)
			}
			if svc, ok := cat.Get(siaSlug); !ok || svc.Docker.Env == nil {
				t.Fatalf("%s is not in the catalog", siaSlug)
			} else if _, ok := svc.SeedVar(); !ok {
				t.Fatalf("%s keeps no seed_file in the catalog, so this test proves nothing", siaSlug)
			}
			app.catalog = cat
			provider := runtime.NewDockerProvider()
			app.runtime = provider
			app.services = services.NewManager(provider, cat, app.store)
			// Written straight to the store, as a Desktop from before the seed file did.
			if err := app.store.SaveCredentials(siaSlug, tc.stored); err != nil {
				t.Fatal(err)
			}
			worker := newFakeDockerWorker(t)
			enrolWorker(t, app, "garage", worker.srv.URL, nil)

			_, err = app.DeployToWorker(siaSlug, WorkerDeployRequest{WorkerID: workerID(t, app, "garage"), Values: tc.typed})
			worker.mu.Lock()
			calls := append([]workerCall(nil), worker.calls...)
			worker.mu.Unlock()
			for _, call := range calls {
				raw, _ := json.Marshal(call.Body)
				if strings.Contains(string(raw), seedPhraseA) || strings.Contains(string(raw), seedPhraseB) {
					t.Fatalf("the seed phrase went to the worker: %s %s", call.Method, call.Path)
				}
				if env, ok := call.Body["env"].(map[string]any); ok {
					if _, has := env[siaSeedKey]; has {
						t.Fatalf("%s went to the worker in env", siaSeedKey)
					}
				}
			}
			if worker.count() != 0 {
				t.Fatalf("the worker got %d requests; a seed-file service must not reach it", worker.count())
			}
			if err == nil {
				t.Fatal("the deploy went through; it must be refused")
			}
			msg := err.Error()
			if strings.Contains(msg, "missing required fields") || strings.Contains(msg, "Wallet seed phrase") {
				t.Errorf("refusal %q asks for a seed field the form no longer shows", msg)
			}
			if !strings.Contains(msg, "this computer") {
				t.Errorf("refusal %q does not say to run it on this computer", msg)
			}
			if strings.Contains(msg, seedPhraseA) || strings.Contains(msg, seedPhraseB) {
				t.Error("the refusal carries the seed phrase")
			}
		})
	}
}

func withSeed(settings map[string]string, phrase string) map[string]string {
	settings[siaSeedKey] = phrase
	return settings
}
