package compose

import (
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

func gpuEntry(vendor string, mutate ...func(*catalog.Service)) catalog.Service {
	svc := catalog.Service{
		Slug:         "gpu-miner",
		Name:         "GPU Miner",
		Category:     "mining",
		Requirements: catalog.Requirements{GPU: true},
		Docker: catalog.DockerConfig{
			Image:   "example/gpu-miner@sha256:" + strings.Repeat("e", 64),
			Runtime: "runc",
			GPU:     &catalog.GPU{Vendor: vendor},
		},
	}
	for _, m := range mutate {
		m(&svc)
	}
	return svc
}

// THE RULE: the exported file passes in the GPU the deploy would.
func TestExportCarriesTheNvidiaGrant(t *testing.T) {
	for count, want := range map[any]any{nil: 1, "all": "all"} {
		svc := gpuEntry("nvidia", func(s *catalog.Service) { s.Docker.GPU.Count = count })
		block := serviceBlockOf(t, parse(t, generate(t, fakeCatalog{"gpu-miner": svc}, []string{"gpu-miner"}, Options{})), "cashpilot-gpu-miner")
		reservations := mapOf(t, mapOf(t, mapOf(t, block, "deploy"), "resources"), "reservations")
		devices, _ := reservations["devices"].([]any)
		if len(devices) != 1 {
			t.Fatalf("count %v: reservations = %v", count, reservations)
		}
		dev := devices[0].(map[string]any)
		caps, _ := dev["capabilities"].([]any)
		if dev["driver"] != "nvidia" || dev["count"] != want || len(caps) != 1 || caps[0] != "gpu" {
			t.Fatalf("count %v: device = %v", count, dev)
		}
		if block["devices"] != nil {
			t.Fatalf("an NVIDIA grant mapped devices: %v", block["devices"])
		}
	}
}

func TestExportMapsTheFirstRenderNodeForAMDAndIntel(t *testing.T) {
	svc := gpuEntry("amd", func(s *catalog.Service) { s.Docker.GPU.ROCm = true })
	block := serviceBlockOf(t, parse(t, generate(t, fakeCatalog{"gpu-miner": svc}, []string{"gpu-miner"}, Options{})), "cashpilot-gpu-miner")
	var got []string
	for _, d := range block["devices"].([]any) {
		got = append(got, d.(string))
	}
	if !slices.Equal(got, []string{"/dev/dri/renderD128:/dev/dri/renderD128", "/dev/kfd:/dev/kfd"}) || block["deploy"] != nil {
		t.Fatalf("devices = %v deploy = %v", got, block["deploy"])
	}
}

func TestExportRefusesABrokenGPUBlock(t *testing.T) {
	svc := gpuEntry("nvidia", func(s *catalog.Service) { s.Docker.Runtime = "" })
	if _, err := Generate(fakeCatalog{"gpu-miner": svc}, []string{"gpu-miner"}, Options{}); err == nil || !strings.Contains(err.Error(), "docker.runtime: runc") {
		t.Fatalf("err = %v, want the broken block refused", err)
	}
	plain := honeygain()
	block := serviceBlockOf(t, parse(t, generate(t, fakeCatalog{"honeygain": plain}, []string{"honeygain"}, Options{})), "cashpilot-honeygain")
	if block["deploy"] != nil {
		t.Fatalf("a service without docker.gpu got %v", block["deploy"])
	}
}
