package compose

import (
	"errors"
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

// An AMD or Intel GPU is exported as the vendor's own node the deploy would pick,
// which need not be renderD128: here an Intel iGPU holds renderD128 and the AMD card
// renderD129, as runtime.HostGPUDevices reads them from this computer.
func TestExportMapsTheVendorsOwnRenderNode(t *testing.T) {
	host := map[string][]string{"intel": {"/dev/dri/renderD128"}, "amd": {"/dev/dri/renderD129", "/dev/kfd"}}
	var asked []catalog.GPUGrant
	opts := Options{GPUDevices: func(g catalog.GPUGrant) ([]string, error) {
		asked = append(asked, g)
		return host[g.Vendor], nil
	}}
	for vendor, want := range map[string][]string{
		"amd":   {"/dev/dri/renderD129:/dev/dri/renderD129", "/dev/kfd:/dev/kfd"},
		"intel": {"/dev/dri/renderD128:/dev/dri/renderD128"},
	} {
		svc := gpuEntry(vendor, func(s *catalog.Service) { s.Docker.GPU.ROCm = vendor == "amd" })
		block := serviceBlockOf(t, parse(t, generate(t, fakeCatalog{"gpu-miner": svc}, []string{"gpu-miner"}, opts)), "cashpilot-gpu-miner")
		var got []string
		for _, d := range block["devices"].([]any) {
			got = append(got, d.(string))
		}
		if !slices.Equal(got, want) || block["deploy"] != nil {
			t.Fatalf("%s: devices = %v deploy = %v, want %v", vendor, got, block["deploy"], want)
		}
	}
	if len(asked) != 2 || !asked[0].ROCm && !asked[1].ROCm {
		t.Fatalf("the export asked for %+v, want each grant as declared", asked)
	}
}

// When the node cannot be known, the export refuses instead of guessing renderD128.
func TestExportRefusesARenderNodeItCannotRead(t *testing.T) {
	svc := gpuEntry("amd")
	for name, opts := range map[string]Options{
		"no reader": {},
		"read fails": {GPUDevices: func(catalog.GPUGrant) ([]string, error) {
			return nil, errors.New("the container runtime runs in a virtual machine")
		}},
	} {
		_, err := Generate(fakeCatalog{"gpu-miner": svc}, []string{"gpu-miner"}, opts)
		if err == nil || !strings.Contains(err.Error(), "could not tell which one") || !strings.Contains(err.Error(), "AMD") {
			t.Fatalf("%s: err = %v, want the export refused", name, err)
		}
	}
	// An NVIDIA grant needs no node, so it exports without a reader.
	if _, err := Generate(fakeCatalog{"gpu-miner": gpuEntry("nvidia")}, []string{"gpu-miner"}, Options{}); err != nil {
		t.Fatalf("nvidia: %v", err)
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
