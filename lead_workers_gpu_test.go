package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
)

// newGPULeadApp is a lead Desktop whose catalog holds one plain service and one
// catalog GPU miner.
func newGPULeadApp(t *testing.T) *App {
	t.Helper()
	app := newLeadApp(t)
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/bandwidth/plain.yml":  {Data: []byte(leadCatalogYAML)},
		"services/mining/gpu-miner.yml": {Data: []byte(gpuMinerYAML)},
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	app.catalog = cat
	app.services = services.NewManager(app.runtime, cat, app.store)
	return app
}

// workerHeartbeatWith sends a heartbeat of an enrolled worker on its own key, with
// systemInfo as its system_info.
func workerHeartbeatWith(t *testing.T, app *App, name, url, key string, systemInfo map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"name": name, "client_id": "client-" + name, "url": url, "containers": []any{}, "system_info": systemInfo})
	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	app.handleWorkerHeartbeat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat %s: %d %s", name, rec.Code, rec.Body.String())
	}
}

// THE RULE end to end: the lead deploys a GPU miner only to a worker whose last
// heartbeat reported the catalog's grant for it, as the web UI does. Every refusal
// leaves the worker uncalled.
func TestALeadDeploysAGPUMinerOnlyWithTheWorkersMatchingGrant(t *testing.T) {
	nvidiaOne := map[string]any{"vendor": "nvidia", "count": 1, "rocm": false}
	const notReported = "This worker did not report the catalog GPU grant for GPU Miner. Update the worker"
	const differs = "This worker reported a different GPU grant for GPU Miner than this computer's catalog. Update whichever is older"
	for name, tc := range map[string]struct {
		grants any // nil: the field is left out, as an older worker does
		deploy bool
		want   string
	}{
		"matching":  {grants: map[string]any{"gpu-miner": nvidiaOne}, deploy: true},
		"missing":   {grants: map[string]any{"other-miner": nvidiaOne}, want: notReported},
		"differing": {grants: map[string]any{"gpu-miner": map[string]any{"vendor": "nvidia", "count": "all", "rocm": false}}, want: differs},
		"older":     {want: notReported},
		"not a map": {grants: []any{"gpu-miner"}, want: notReported},
	} {
		t.Run(name, func(t *testing.T) {
			app := newGPULeadApp(t)
			worker := newFakeDockerWorker(t)
			key := enrolWorker(t, app, "rig", worker.srv.URL, nil)
			info := map[string]any{"os": "Linux 6.1", "arch": "x86_64", "features": []any{"instances", "fence_gated", "gpu_grant"}}
			if tc.grants != nil {
				info["gpu_grants"] = tc.grants
			}
			workerHeartbeatWith(t, app, "rig", worker.srv.URL, key, info)

			res, err := app.DeployToWorker("gpu-miner", WorkerDeployRequest{WorkerID: workerID(t, app, "rig")})
			if tc.deploy {
				if err != nil || res.ContainerID != "c0ffee" {
					t.Fatalf("DeployToWorker = %+v, %v", res, err)
				}
				if call := worker.last(t); call.Path != "/api/containers/gpu-miner/deploy-checked" {
					t.Fatalf("sent %s %s", call.Method, call.Path)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("DeployToWorker = %+v, %v: want %q", res, err, tc.want)
			}
			if n := worker.count(); n != 0 {
				t.Fatalf("the worker got %d calls: a refused deploy must not reach it", n)
			}
		})
	}
}

// A service without docker.gpu deploys to a worker that reports no grants at all.
func TestALeadDeploysAPlainServiceToAWorkerWithoutGPUGrants(t *testing.T) {
	app := newGPULeadApp(t)
	worker := newFakeDockerWorker(t)
	enrolWorker(t, app, "garage", worker.srv.URL, nil)
	res, err := app.DeployToWorker("plain", WorkerDeployRequest{WorkerID: workerID(t, app, "garage"), Values: map[string]string{"TOKEN": "t"}})
	if err != nil || res.ContainerID != "c0ffee" {
		t.Fatalf("DeployToWorker = %+v, %v", res, err)
	}
}
