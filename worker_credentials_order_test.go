package main

import (
	"context"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
)

// heldDeployProvider reports the token a local deploy starts with, then waits.
type heldDeployProvider struct {
	runtime.Provider
	used    chan string
	release chan struct{}
}

func (p *heldDeployProvider) Deploy(_ context.Context, spec runtime.DeploySpec, _ func(string)) (runtime.ContainerInfo, error) {
	p.used <- spec.Env[deployOrderTokenKey]
	<-p.release
	return runtime.ContainerInfo{Slug: spec.Slug, Name: "cashpilot-" + spec.Slug, Status: "running"}, nil
}

// A worker deploy saves the credentials it sent. When it does that while a local
// deploy of the same service is running, the worker's save is the later one and
// wins, as Save Credentials does: the local deploy then saves nothing. Before, the
// local deploy merged its form into the worker's values, so a blank secret saved
// the worker's token, which the local container never got.
func TestWorkerCredentialWriteWinsOverAnInFlightLocalDeploy(t *testing.T) {
	const slug = "traffmonetizer"
	const local, worker = "1|local-fixture", "2|worker-fixture"
	for _, tc := range []struct {
		name      string
		typed     string
		wantLocal string
	}{
		{name: "blank secret keeps the saved one", typed: "", wantLocal: local},
		{name: "typed secret", typed: "3|typed-local-fixture", wantLocal: "3|typed-local-fixture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newAppUnderTest(t)
			app.collectors = nil
			p := &heldDeployProvider{Provider: app.runtime, used: make(chan string, 1), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(p.release) }) }
			defer release()
			app.services = services.NewManager(p, app.catalog, app.store)
			if err := app.SaveCredentials(slug, map[string]string{deployOrderTokenKey: local, "TRAFFMONETIZER_DEVICE_NAME": "old-local"}); err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			go func() {
				_, err := app.DeployService(slug, map[string]string{deployOrderTokenKey: tc.typed, "TRAFFMONETIZER_DEVICE_NAME": "new-local"}, nil, nil)
				done <- err
			}()
			select {
			case used := <-p.used:
				if used != tc.wantLocal {
					t.Fatalf("local runtime got %q, want %q", used, tc.wantLocal)
				}
			case err := <-done:
				t.Fatalf("local deploy did not reach the runtime: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("local deploy did not reach the runtime")
			}

			if err := app.saveWorkerCredentials(slug, map[string]string{deployOrderTokenKey: worker}); err != nil {
				t.Fatal(err)
			}
			afterWorker, err := app.store.GetCredentials(slug)
			if err != nil {
				t.Fatal(err)
			}
			release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("local deploy did not finish")
			}

			stored, err := app.store.GetCredentials(slug)
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(stored, afterWorker) {
				t.Fatalf("the local deploy changed what the later worker save stored:\n got  %v\n want %v", stored, afterWorker)
			}
			if stored[deployOrderTokenKey] != worker {
				t.Fatalf("stored token = %q, want the worker's %q", stored[deployOrderTokenKey], worker)
			}
		})
	}
}
