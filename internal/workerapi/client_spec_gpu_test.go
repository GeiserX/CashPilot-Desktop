package workerapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// gpuService is a catalog GPU miner that passes the web catalog's GPU rules.
func gpuService() catalog.Service {
	svc := service()
	svc.Slug, svc.Name = "gpu-miner", "GPU Miner"
	svc.Requirements.GPU = true
	svc.Docker.ImageByArch = nil
	svc.Docker.Runtime = "runc"
	svc.Docker.Devices = nil
	svc.Docker.GPU = &catalog.GPU{Vendor: "nvidia"}
	return svc
}

func grants(raw string) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		panic(err)
	}
	return out
}

// THE RULE the web app's _deploy holds: a GPU service goes only to a worker that
// reported the same GPU grant as this computer's catalog. The spec carries no GPU,
// so a worker without the grant starts the miner without one, and it looks healthy.
func TestAGPUServiceNeedsTheWorkersMatchingGrant(t *testing.T) {
	in := SpecInput{Typed: map[string]string{"TOKEN": "t"}}
	in.WorkerGPUGrants = grants(`{"gpu-miner":{"vendor":"nvidia","count":1,"rocm":false}}`)
	if _, err := BuildDeploySpec(gpuService(), in); err != nil {
		t.Fatalf("a matching grant must deploy: %v", err)
	}
	all := gpuService()
	all.Docker.GPU.Count = "all"
	in.WorkerGPUGrants = grants(`{"gpu-miner":{"vendor":"nvidia","count":"all","rocm":false}}`)
	if _, err := BuildDeploySpec(all, in); err != nil {
		t.Fatalf("a matching count all grant must deploy: %v", err)
	}

	amd := gpuService()
	amd.Docker.GPU = &catalog.GPU{Vendor: "amd", ROCm: true}
	in.WorkerGPUGrants = grants(`{"gpu-miner":{"vendor":"amd","count":1,"rocm":true}}`)
	if _, err := BuildDeploySpec(amd, in); err != nil {
		t.Fatalf("a matching amd rocm grant must deploy: %v", err)
	}

	const notReported = "This worker did not report the catalog GPU grant for GPU Miner. Update the worker"
	const differs = "This worker reported a different GPU grant for GPU Miner than this computer's catalog. Update whichever is older"
	for name, tc := range map[string]struct{ raw, want string }{
		"missing":       {`{"other-miner":{"vendor":"nvidia","count":1,"rocm":false}}`, notReported},
		"empty":         {`{}`, notReported},
		"null":          {`{"gpu-miner":null}`, notReported},
		"vendor":        {`{"gpu-miner":{"vendor":"amd","count":1,"rocm":false}}`, differs},
		"count":         {`{"gpu-miner":{"vendor":"nvidia","count":"all","rocm":false}}`, differs},
		"rocm":          {`{"gpu-miner":{"vendor":"nvidia","count":1,"rocm":true}}`, differs},
		"rocm string":   {`{"gpu-miner":{"vendor":"nvidia","count":1,"rocm":"false"}}`, differs},
		"no rocm key":   {`{"gpu-miner":{"vendor":"nvidia","count":1}}`, differs},
		"extra key":     {`{"gpu-miner":{"vendor":"nvidia","count":1,"rocm":false,"devices":"all"}}`, differs},
		"count string":  {`{"gpu-miner":{"vendor":"nvidia","count":"1","rocm":false}}`, differs},
		"not an object": {`{"gpu-miner":"nvidia"}`, differs},
	} {
		in.WorkerGPUGrants = grants(tc.raw)
		_, err := BuildDeploySpec(gpuService(), in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// A worker older than the GPU grants sends no gpu_grants at all. It cannot give the
// GPU either, so it is refused the same way.
func TestAWorkerWithoutGPUGrantsIsRefusedAGPUService(t *testing.T) {
	_, err := BuildDeploySpec(gpuService(), SpecInput{Typed: map[string]string{"TOKEN": "t"}, WorkerFeatures: []string{"instances", "fence_gated"}})
	if err == nil || !strings.Contains(err.Error(), "Update the worker and wait for its next heartbeat") {
		t.Fatalf("err = %v", err)
	}
}

// A service without docker.gpu never looks at the grants.
func TestAServiceWithoutAGPUIgnoresTheGrants(t *testing.T) {
	for name, g := range map[string]map[string]json.RawMessage{
		"none":  nil,
		"other": grants(`{"gpu-miner":{"vendor":"nvidia","count":1,"rocm":false}}`),
	} {
		if _, err := BuildDeploySpec(service(), SpecInput{Typed: map[string]string{"TOKEN": "t"}, WorkerGPUGrants: g}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// As on the web, a GPU service runs only the catalog image, and a GPU block that
// breaks the catalog's rules is not sent on as if it granted nothing.
func TestAGPUServiceRefusesACustomImageAndABrokenBlock(t *testing.T) {
	in := SpecInput{Typed: map[string]string{"TOKEN": "t"}, WorkerGPUGrants: grants(`{"gpu-miner":{"vendor":"nvidia","count":1,"rocm":false}}`)}
	in.CustomImage = "mine/miner:1"
	if _, err := BuildDeploySpec(gpuService(), in); err == nil || !strings.Contains(err.Error(), "runs only the catalog image") {
		t.Fatalf("custom image: %v", err)
	}
	in.CustomImage = ""
	broken := gpuService()
	broken.Docker.Runtime = "runsc"
	if _, err := BuildDeploySpec(broken, in); err == nil || !strings.Contains(err.Error(), "docker.gpu needs docker.runtime: runc") {
		t.Fatalf("broken block: %v", err)
	}
}
