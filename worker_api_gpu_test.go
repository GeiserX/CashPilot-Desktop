package main

import (
	"encoding/json"
	"io/fs"
	"maps"
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
	var payload struct {
		SystemInfo struct {
			GPUGrants map[string]map[string]any `json:"gpu_grants"`
		} `json:"system_info"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	// The test entry and the two vendored GPU miners, each in the web shape.
	nvidiaOne := map[string]any{"vendor": "nvidia", "count": float64(1), "rocm": false}
	grants := payload.SystemInfo.GPUGrants
	if len(grants) != 3 {
		t.Fatalf("payload %s: want the grants of gpu-miner, kryptex-pearl and srbminer-multi", raw)
	}
	for _, slug := range []string{"gpu-miner", "kryptex-pearl", "srbminer-multi"} {
		if !maps.Equal(grants[slug], nvidiaOne) {
			t.Errorf("gpu_grants[%s] = %v, want %v", slug, grants[slug], nvidiaOne)
		}
	}
	// The vendored catalog now carries GPU miners, so the control is a catalog of
	// one entry without a GPU.
	plain := joinedApp(t)
	honeygain, err := fs.ReadFile(serviceFiles, "services/bandwidth/honeygain.yml")
	if err != nil {
		t.Fatal(err)
	}
	if plain.catalog, err = catalog.LoadEmbedded(fstest.MapFS{"services/bandwidth/honeygain.yml": {Data: honeygain}}); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(plain.upstreamPayload())
	if !strings.Contains(string(raw), `"gpu_grants":{}`) {
		t.Fatalf("payload %s: a catalog without GPU entries reports {} as the web worker does", raw)
	}
}

// The two vendored GPU miners deploy over the #342 path with their one NVIDIA GPU
// and the command the catalog builds from the deploy's values.
func TestServerDeploysTheVendoredGPUMinersWithTheirGPU(t *testing.T) {
	for slug, tc := range map[string]struct {
		env  map[string]string
		want []string
	}{
		"kryptex-pearl": {
			env:  map[string]string{"KRYPTEX_EMAIL": "miner@example.com", "WORKER_NAME": "rig-1"},
			want: []string{"--url", "stratum+ssl://prl.kryptex.network:8048", "--user", "miner@example.com/rig-1", "--no-rocm"},
		},
		"srbminer-multi": {
			env: map[string]string{"WALLET": "prl1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq", "WORKER_NAME": "rig-1",
				"ALGO": "pearlhash", "POOL_URL": "pearl.herominers.com:1200", "POOL_TLS": "true"},
			want: []string{"--disable-cpu", "--algorithm", "pearlhash", "--pool", "pearl.herominers.com:1200",
				"--wallet", "prl1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq.rig-1", "--tls", "true", "--log-file", "/dev/stdout"},
		},
	} {
		app := gpuApp(t)
		svc := mustService(t, app, slug)
		spec := withGPUAsk(map[string]any{"image": svc.Docker.Image, "env": tc.env, "instance": 1})
		if w := call(t, app, http.MethodPost, "/api/containers/"+slug+"/deploy", testWorkerKey, spec); w.Code != http.StatusOK {
			t.Fatalf("%s deploy: %d %s", slug, w.Code, w.Body.String())
		}
		c, ok := app.Docker.Container("cashpilot-" + slug)
		if !ok {
			t.Fatalf("%s: no container", slug)
		}
		if len(c.DeviceRequests) != 1 || c.DeviceRequests[0].Driver != "nvidia" || c.DeviceRequests[0].Count != 1 {
			t.Errorf("%s: DeviceRequests = %+v, want one nvidia GPU", slug, c.DeviceRequests)
		}
		if !slices.Equal(c.Cmd, tc.want) {
			t.Errorf("%s: Cmd = %q\nwant %q", slug, c.Cmd, tc.want)
		}
		if limit := app.services.ScalingLimit(svc); limit.Max != 1 {
			t.Errorf("%s: ScalingLimit = %+v, want one copy", slug, limit)
		}
	}
}
