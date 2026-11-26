package preflight

import (
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// An entry CashPilot passes a GPU into no longer says it will sit idle without one:
// on a Linux host the deploy is refused instead.
func TestAGPUEntrySaysCashPilotPassesTheCardIn(t *testing.T) {
	svc := catalog.Service{Name: "GPU Miner", Slug: "gpu-miner", Requirements: catalog.Requirements{GPU: true},
		Docker: catalog.DockerConfig{Image: "example/gpu@sha256:" + strings.Repeat("a", 64), GPU: &catalog.GPU{Vendor: "nvidia"}}}
	text := ""
	for _, f := range Assess(Input{Service: svc, DaemonArch: "x86_64"}).Findings {
		text += f.Message + " | "
	}
	if !strings.Contains(text, "which CashPilot passes into the container") || strings.Contains(text, "sit idle") {
		t.Fatalf("findings = %s", text)
	}
}
