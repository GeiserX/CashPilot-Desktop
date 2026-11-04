package compose

import (
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// A custom service's variables and command are the user's own and stay encrypted in
// the app's database, so it is never written to a compose file. The catalog service
// beside it is the control: the same call exports it.
func TestGenerateRefusesACustomService(t *testing.T) {
	custom := catalog.Service{Name: "Mine", Slug: "custom-mine", Category: "custom", Status: "custom", Custom: true,
		Docker: catalog.DockerConfig{Image: "org/mine:1"}}
	services := fakeCatalog{"custom-mine": custom, "honeygain": honeygain()}
	if _, err := Generate(services, []string{"honeygain"}, Options{Hostname: "box"}); err != nil {
		t.Fatalf("control: the catalog service did not export: %v", err)
	}
	_, err := Generate(services, []string{"honeygain", "custom-mine"}, Options{Hostname: "box"})
	if err == nil || !strings.Contains(err.Error(), "custom service") {
		t.Fatalf("Generate with a custom service = %v, want a refusal", err)
	}
}
