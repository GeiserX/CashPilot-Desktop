package runtime

import (
	goruntime "runtime"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/docker/docker/api/types/mount"
)

func TestInstallGuidesCoverSupportedRuntimeChoices(t *testing.T) {
	guides := InstallGuides()
	if len(guides) == 0 {
		t.Fatal("expected install guides for current platform")
	}
	for _, guide := range guides {
		if !guide.Supports(goruntime.GOOS) {
			t.Fatalf("guide %q does not support current platform %q", guide.ID, goruntime.GOOS)
		}
	}
}

func TestManagedRuntimeRoadmapNamesDeferredProviders(t *testing.T) {
	plan := ManagedRuntimeRoadmap()
	if plan.Summary == "" {
		t.Fatal("expected roadmap summary")
	}
	if len(plan.Providers) < 2 {
		t.Fatalf("expected deferred providers, got %#v", plan.Providers)
	}
}

func TestBuildMountsSeparatesNamedVolumesFromHostPaths(t *testing.T) {
	svcEnv := map[string]string{
		"HOST_DIR": "/Users/example/cashpilot-data",
	}
	mounts := buildMounts([]string{
		"cashpilot-data:/var/lib/app",
		"${HOST_DIR}:/data:ro",
	}, svcEnv)
	if len(mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(mounts))
	}
	if mounts[0].Type != mount.TypeVolume {
		t.Fatalf("expected named volume, got %s", mounts[0].Type)
	}
	if mounts[1].Type != mount.TypeBind {
		t.Fatalf("expected bind mount, got %s", mounts[1].Type)
	}
	if !mounts[1].ReadOnly {
		t.Fatal("expected read-only bind mount")
	}
}

func TestBuildEnvSubstitutesDefaultsAndOverrides(t *testing.T) {
	env := buildEnv(catalog.Service{Docker: catalog.DockerConfig{Env: []catalog.EnvVar{
		{Key: "DEVICE_ID", Default: "cashpilot-{hostname}"},
		{Key: "DEVICE_NAME", Default: "Device-${DEVICE_ID}"},
	}}}, map[string]string{"DEVICE_ID": "desktop-1"})
	if env["DEVICE_NAME"] != "Device-desktop-1" {
		t.Fatalf("expected substituted default, got %q", env["DEVICE_NAME"])
	}
}
