package runtime

import (
	goruntime "runtime"
	"testing"
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
