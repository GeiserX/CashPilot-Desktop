package services

import (
	"context"
	"fmt"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// stepProvider is a native runtime whose deploy needs the admin prompt.
type stepProvider struct {
	fakeProvider
	asked []string
}

func (s *stepProvider) AdminStep(_ catalog.Service, slug string) (runtime.AdminStep, error) {
	s.asked = append(s.asked, slug)
	return runtime.AdminStep{Needed: true, Kind: runtime.AdminStepFirewallRule}, nil
}

func TestAdminStepAsksTheRuntimeThatWouldDeploy(t *testing.T) {
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/bandwidth/example.yml": {Data: []byte(exampleServiceYAML)},
		"services/bandwidth/nat.yml": {Data: []byte(fmt.Sprintf(
			"name: Nat\nslug: nat\ncategory: bandwidth\nstatus: active\ndocker:\n  image: nat/image:1.0.0\nnative:\n  binaries:\n"+
				"    - os: %s\n      arch: %s\n      url: \"https://example.com/nat.zip\"\n      sha256: \"%064d\"\n      archive: zip\n      bin: nat\n",
			goruntime.GOOS, goruntime.GOARCH, 0))},
	})
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	native := &stepProvider{}
	docker := &fakeProvider{}
	m := NewManager(docker, cat, newTestStore(t))
	m.Register(runtime.NativeRuntimeKind, native)

	if step, err := m.AdminStepFor("nat"); err != nil || !step.Needed {
		t.Fatalf("native service: step = %+v, err = %v, want the runtime's step", step, err)
	}
	if step, err := m.AdminStepFor("example"); err != nil || step.Needed {
		t.Fatalf("Docker service: step = %+v, err = %v, want none", step, err)
	}
	if !slices.Equal(native.asked, []string{"nat"}) {
		t.Fatalf("native runtime asked %v, want nat only", native.asked)
	}

	// A start or restart the runtime refuses for want of approval names the
	// service as Catalog shows it, not by its slug.
	docker.startErr = &runtime.AdminApprovalNeeded{Slug: "nat"}
	docker.restartErr = &runtime.AdminApprovalNeeded{Slug: "nat"}
	if err := m.Start(context.Background(), "nat"); err == nil || !strings.Contains(err.Error(), "open Nat in Catalog") || strings.Contains(err.Error(), "nat ") {
		t.Fatalf("Start refused for approval: %v, want the Catalog name", err)
	}
	if err := m.Restart(context.Background(), "nat"); err == nil || !strings.Contains(err.Error(), "open Nat in Catalog") {
		t.Fatalf("Restart refused for approval: %v, want the Catalog name", err)
	}
	if _, err := m.AdminStepFor("nope"); err == nil {
		t.Fatal("an unknown service was answered")
	}
}
