package compose

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// The exported file runs XMRig with the catalog's CPU cap wrapper. Compose turns
// each $$ back into one $, so the escaped parts must give back the catalog's list
// byte for byte; a bare $n or $(nproc) would be filled from the host instead and
// break the cap. CPU_PERCENT stays a variable the file sets and the command reads.
func TestXMRigExportKeepsTheCPUCapWrapper(t *testing.T) {
	cat, err := catalog.LoadEmbedded(os.DirFS("../.."))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	svc, ok := cat.Get("xmrig")
	if !ok || len(svc.Docker.Entrypoint) != 4 {
		t.Fatalf("the xmrig entry with its CPU cap wrapper is not in the catalog: %q", svc.Docker.Entrypoint)
	}
	text, err := Generate(cat, []string{"xmrig"}, Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	block := serviceBlockOf(t, parse(t, text), "cashpilot-xmrig")
	got := listOf(t, block, "entrypoint")
	unescaped := make([]string, len(got))
	for i, part := range got {
		if strings.Count(part, "$") != 2*strings.Count(part, "$$") {
			t.Errorf("entrypoint part %d has a $ Compose would fill from the host: %q", i, part)
		}
		unescaped[i] = strings.ReplaceAll(part, "$$", "$")
	}
	if !slices.Equal(unescaped, svc.Docker.Entrypoint) {
		t.Errorf("entrypoint after Compose = %q\nwant the catalog's %q", unescaped, svc.Docker.Entrypoint)
	}
	// A .env line CPU_PERCENT=17 reaches the container's environment, where the
	// wrapper reads it, and the hint on the command alike.
	if got := mapOf(t, block, "environment")["CPU_PERCENT"]; got != "${CPU_PERCENT:-50}" {
		t.Errorf("CPU_PERCENT = %v, want the catalog default a .env entry can override", got)
	}
	if command, _ := block["command"].(string); !strings.Contains(command, "--cpu-max-threads-hint=${CPU_PERCENT:-50}") {
		t.Errorf("command = %q, want the hint to read CPU_PERCENT", command)
	}
}
