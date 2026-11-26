package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
)

const gpuMinerYAML = `name: GPU Miner
slug: gpu-miner
category: mining
status: beta
requirements:
  gpu: true
docker:
  image: example/gpu-miner@sha256:2222222222222222222222222222222222222222222222222222222222222222
  runtime: runc
  gpu:
    vendor: nvidia
  env:
    - key: WORKER_NAME
      label: Worker name
      default: rig
`

// gpuApp is a joined app whose catalog holds the vendored entries plus one
// catalog GPU miner, as a later catalog sync will bring.
func gpuApp(t *testing.T) *appUnderTest {
	t.Helper()
	app := joinedApp(t)
	files := fstest.MapFS{}
	err := fs.WalkDir(serviceFiles, "services", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(serviceFiles, path)
		files[path] = &fstest.MapFile{Data: raw}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	files["services/mining/gpu-miner.yml"] = &fstest.MapFile{Data: []byte(gpuMinerYAML)}
	cat, err := catalog.LoadEmbedded(files)
	if err != nil {
		t.Fatal(err)
	}
	if svc, ok := cat.Get("gpu-miner"); !ok || svc.Docker.GPU == nil {
		t.Fatal("the GPU miner did not load with its docker.gpu block")
	}
	app.catalog = cat
	app.services = services.NewManager(app.runtime, cat, app.store)
	return app
}

// Fields the web worker's DeploySpec could carry, and a CashPilot that is not ours
// might send: none of them reaches the container, because the request has no GPU
// field at all.
func withGPUAsk(spec map[string]any) map[string]any {
	spec["device_requests"] = []map[string]any{{"Driver": "nvidia", "Count": -1, "Capabilities": [][]string{{"gpu"}}}}
	spec["gpus"] = "all"
	spec["devices"] = []string{"/dev/dri/renderD128", "/dev/nvidia0"}
	return spec
}

// THE RULE on the #342 path: a joined CashPilot gets a GPU for a service only
// because this computer's catalog declares it, and never asks one into a service
// that does not.
func TestServerDeployGivesTheGPUTheLocalCatalogDeclares(t *testing.T) {
	app := gpuApp(t)
	spec := withGPUAsk(map[string]any{"image": "example/gpu-miner:1.0", "env": map[string]string{"WORKER_NAME": "rig1"}, "instance": 1})
	w := call(t, app, http.MethodPost, "/api/containers/gpu-miner/deploy", testWorkerKey, spec)
	if w.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	c, ok := app.Docker.Container("cashpilot-gpu-miner")
	if !ok {
		t.Fatal("no container")
	}
	if len(c.DeviceRequests) != 1 || c.DeviceRequests[0].Driver != "nvidia" || c.DeviceRequests[0].Count != 1 ||
		len(c.DeviceRequests[0].Capabilities) != 1 || !slices.Equal(c.DeviceRequests[0].Capabilities[0], []string{"gpu"}) {
		t.Fatalf("DeviceRequests = %+v, want the catalog's one nvidia GPU, not the request's -1", c.DeviceRequests)
	}
	if len(c.Devices) != 0 || c.Runtime != "runc" {
		t.Fatalf("devices %v runtime %q: the request's devices must not reach the container", c.Devices, c.Runtime)
	}
	if slices.ContainsFunc(c.Env, func(e string) bool { return strings.HasPrefix(e, "NVIDIA_") }) {
		t.Fatalf("env %v: an NVIDIA grant gets no stamp", c.Env)
	}
}

func TestServerDeployCannotAskAGPUIntoAServiceWithoutOne(t *testing.T) {
	app := gpuApp(t)
	w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, withGPUAsk(honeygainSpec(t, app)))
	if w.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	c, ok := app.Docker.Container("cashpilot-honeygain")
	if !ok {
		t.Fatal("no container")
	}
	if len(c.DeviceRequests) != 0 || len(c.Devices) != 0 {
		t.Fatalf("a service without docker.gpu got %+v / %v from the request", c.DeviceRequests, c.Devices)
	}
	if !slices.Contains(c.Env, "NVIDIA_VISIBLE_DEVICES=void") {
		t.Fatalf("env %v lacks the void stamp", c.Env)
	}
}

func TestServerDeployRefusesNvidiaVariables(t *testing.T) {
	app := gpuApp(t)
	for _, slug := range []string{"gpu-miner", "honeygain"} {
		spec := map[string]any{"image": "example/gpu-miner:1.0", "env": map[string]string{"WORKER_NAME": "rig1", "nvidia_visible_devices": "all"}, "instance": 1}
		if slug == "honeygain" {
			spec = honeygainSpec(t, app)
			spec["env"].(map[string]string)["NVIDIA_VISIBLE_DEVICES"] = "all"
		}
		w := call(t, app, http.MethodPost, "/api/containers/"+slug+"/deploy", testWorkerKey, spec)
		if w.Code != http.StatusForbidden || !strings.Contains(detailText(t, w), "CashPilot sets the NVIDIA_ variables itself") {
			t.Fatalf("%s: %d %s, want 403", slug, w.Code, w.Body.String())
		}
		if _, ok := app.Docker.Container("cashpilot-" + slug); ok {
			t.Fatalf("%s: a refused deploy left a container", slug)
		}
	}
}

func TestServerDeployRefusesASecondCopyOfAGPUMiner(t *testing.T) {
	app := gpuApp(t)
	spec := map[string]any{"image": "example/gpu-miner:1.0", "env": map[string]string{}, "instance": 2}
	if w := call(t, app, http.MethodPost, "/api/containers/gpu-miner/deploy", testWorkerKey, spec); w.Code != http.StatusBadRequest {
		t.Fatalf("copy 2: %d %s, want 400", w.Code, w.Body.String())
	}
	if limit := app.services.ScalingLimit(mustService(t, app, "gpu-miner")); limit.Max != 1 || !strings.Contains(limit.Reason, "GPU") {
		t.Fatalf("ScalingLimit = %+v, want one copy because of the GPU", limit)
	}
}

func mustService(t *testing.T, app *appUnderTest, slug string) catalog.Service {
	t.Helper()
	svc, ok := app.catalog.Get(slug)
	if !ok {
		t.Fatalf("%s not in the catalog", slug)
	}
	return svc
}

// THE RULE the server holds the other end of: a joined CashPilot deploys a GPU
// entry here only when this heartbeat reports the same grant as its own catalog
// (web app/main.py _deploy, gpu_grants).
func TestHeartbeatReportsTheGPUGrantsInTheWebShape(t *testing.T) {
	app := gpuApp(t)
	raw, err := json.Marshal(app.upstreamPayload())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"gpu_grants":{"gpu-miner":{"vendor":"nvidia","count":1,"rocm":false}}`) {
		t.Fatalf("payload %s lacks the GPU miner's grant", raw)
	}
	plain := joinedApp(t)
	raw, _ = json.Marshal(plain.upstreamPayload())
	if !strings.Contains(string(raw), `"gpu_grants":{}`) {
		t.Fatalf("payload %s: a catalog without GPU entries reports {} as the web worker does", raw)
	}
}
