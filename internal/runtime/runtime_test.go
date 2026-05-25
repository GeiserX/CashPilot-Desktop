package runtime

import "testing"

func TestInstallGuidesCoverSupportedRuntimeChoices(t *testing.T) {
	guides := InstallGuides()
	want := map[string]bool{
		"docker-desktop": false,
		"docker-engine":  false,
		"colima":         false,
		"lima":           false,
		"podman":         false,
	}
	for _, guide := range guides {
		if _, ok := want[guide.ID]; ok {
			want[guide.ID] = true
		}
	}
	for id, found := range want {
		if !found {
			t.Fatalf("missing install guide %q", id)
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
