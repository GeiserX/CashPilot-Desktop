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
	asked    []string
	approved []string
	revoked  []string
}

func (s *stepProvider) AdminStep(_ catalog.Service, slug string) (runtime.AdminStep, error) {
	s.asked = append(s.asked, slug)
	return runtime.AdminStep{Needed: true, Kind: runtime.AdminStepFirewallRule}, nil
}

func (s *stepProvider) ApproveAdminStep(slug string) { s.approved = append(s.approved, slug) }

func (s *stepProvider) RevokeAdminStep(slug string) { s.revoked = append(s.revoked, slug) }

// THE RULE: the admin step is asked of, and approved on, the runtime that would
// deploy the service, and only that one. A Docker service needs none.
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
	if err := m.ApproveAdminStep("nat"); err != nil {
		t.Fatal(err)
	}
	if step, err := m.AdminStepFor("example"); err != nil || step.Needed {
		t.Fatalf("Docker service: step = %+v, err = %v, want none", step, err)
	}
	if err := m.ApproveAdminStep("example"); err != nil {
		t.Fatal(err)
	}
	if err := m.RevokeAdminStep("nat"); err != nil {
		t.Fatal(err)
	}
	if err := m.RevokeAdminStep("example"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(native.asked, []string{"nat"}) || !slices.Equal(native.approved, []string{"nat"}) || !slices.Equal(native.revoked, []string{"nat"}) {
		t.Fatalf("native runtime asked %v, approved %v, revoked %v, want nat only", native.asked, native.approved, native.revoked)
	}
	if err := m.RevokeAdminStep("nope"); err == nil {
		t.Fatal("an unknown service was revoked")
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
