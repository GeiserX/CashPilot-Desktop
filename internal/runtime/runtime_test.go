package runtime

import (
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/docker/docker/api/types/mount"
)

// TestStreamPullProgressHandlesLongLine pins the bufio.Scanner buffer raise: a
// single progress line larger than Scanner's default 64 KiB token size must not
// stop the scan early with bufio.ErrTooLong (which would fail/truncate the pull).
func TestStreamPullProgressHandlesLongLine(t *testing.T) {
	longStatus := strings.Repeat("x", 200*1024) // 200 KiB, well over the 64 KiB default
	stream := `{"status":"` + longStatus + `","id":"layer1"}` + "\n" +
		`{"status":"Pull complete","id":"layer2"}` + "\n"

	var count int
	var last string
	err := streamPullProgress(strings.NewReader(stream), func(s string) {
		count++
		last = s
	})
	if err != nil {
		t.Fatalf("streamPullProgress returned error on a >64 KiB line: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 progress callbacks (a long line must not stop the scan), got %d", count)
	}
	if last != "layer2: Pull complete" {
		t.Fatalf("final progress = %q, want %q", last, "layer2: Pull complete")
	}
}

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
