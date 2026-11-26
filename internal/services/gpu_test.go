package services

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

const gpuServiceYAML = `name: GPU Example
slug: gpu-example
category: mining
status: beta
requirements:
  gpu: true
docker:
  image: example/gpu@sha256:` + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" + `
  runtime: runc
  gpu:
    vendor: nvidia
`

func newGPUCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadEmbedded(fstest.MapFS{"services/mining/gpu.yml": {Data: []byte(gpuServiceYAML)}})
	if err != nil {
		t.Fatalf("LoadEmbedded error: %v", err)
	}
	return cat
}

// The grant trusts the reviewed image the catalog pins, so a chosen image is
// refused before the runtime is reached (web gpu_grant_for_deploy).
func TestAGPUServiceRefusesAChosenImage(t *testing.T) {
	fake := &fakeProvider{}
	m := NewManager(fake, newGPUCatalog(t), newTestStore(t))
	_, err := m.DeployImage(context.Background(), "gpu-example", nil, strPtr("example/other@sha256:"+strings.Repeat("d", 64)))
	if err == nil || !strings.Contains(err.Error(), "uses this computer's GPU and runs only the image the catalog pins") {
		t.Fatalf("err = %v, want a refusal of the chosen image", err)
	}
	if fake.deployCalls != 0 {
		t.Fatalf("the runtime was called %d times for a refused image", fake.deployCalls)
	}
	if _, err := m.DeployImage(context.Background(), "gpu-example", nil, strPtr("")); err != nil {
		t.Fatalf("the catalog image was refused: %v", err)
	}
}

func TestAGPUServiceRunsOnce(t *testing.T) {
	m := NewManager(&fakeProvider{}, newGPUCatalog(t), newTestStore(t))
	svc, _ := m.catalog.Get("gpu-example")
	if limit := m.ScalingLimit(svc); limit.Max != 1 || !strings.Contains(limit.Reason, "GPU") {
		t.Fatalf("ScalingLimit = %+v, want one copy", limit)
	}
	two := 2
	if _, err := m.DeployWith(context.Background(), "gpu-example", nil, DeployOptions{Instances: &two}); err == nil || !strings.Contains(err.Error(), "GPU") {
		t.Fatalf("two copies: err = %v", err)
	}
}

func TestACustomServiceCannotSetAnNvidiaVariable(t *testing.T) {
	for _, key := range []string{"NVIDIA_VISIBLE_DEVICES", "nvidia_driver_capabilities"} {
		_, err := validateCustomEnv([]CustomEnv{{Key: key, Value: "all"}}, nil)
		if err == nil || !strings.Contains(err.Error(), "CashPilot sets the NVIDIA_ variables itself") {
			t.Fatalf("%s: err = %v", key, err)
		}
	}
	if _, err := validateCustomEnv([]CustomEnv{{Key: "NV_MODE", Value: "x"}}, nil); err != nil {
		t.Fatalf("an unrelated key was refused: %v", err)
	}
}
