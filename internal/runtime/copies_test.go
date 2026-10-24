package runtime

import (
	"context"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/container"
)

// Copy 1 keeps the name and labels every container had before copies existed, so an
// install that never scales sees no change; copy N is cashpilot-<slug>-N and says so.
func TestCopyNamesAndLabels(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{{0, "cashpilot-earnfm"}, {1, "cashpilot-earnfm"}, {2, "cashpilot-earnfm-2"}, {10, "cashpilot-earnfm-10"}} {
		if got := copyName("earnfm", tc.n); got != tc.want {
			t.Errorf("copyName(earnfm, %d) = %q, want %q", tc.n, got, tc.want)
		}
	}
	if labels := containerLabels("earnfm", 1); len(labels) != 4 || labels[LabelInstance] != "" {
		t.Fatalf("copy 1 labels = %v, want the four it always had and no instance label", labels)
	}
	if labels := containerLabels("earnfm", 3); labels[LabelInstance] != "3" || labels[LabelService] != "earnfm" {
		t.Fatalf("copy 3 labels = %v, want instance 3 of earnfm", labels)
	}
	for _, tc := range []struct {
		label string
		want  int
	}{{"3", 3}, {"", 1}, {"x", 1}, {"-2", 1}, {"0", 1}, {"10", 10}} {
		if got := instanceOf(map[string]string{LabelInstance: tc.label}); got != tc.want {
			t.Errorf("instanceOf(%q) = %d, want %d", tc.label, got, tc.want)
		}
	}
	info := toContainerInfo(container.Summary{Labels: containerLabels("earnfm", 4)}, 0, 0)
	if info.Instance != 4 || info.Slug != "earnfm" {
		t.Fatalf("listed copy = %+v, want instance 4 of earnfm", info)
	}
}

// Every device name the catalog derives from {hostname} gets "-N" on copy N, or the
// provider counts two copies as one device. Other values stay as they are.
func TestCopyEnvSuffixesDeviceNames(t *testing.T) {
	svc := catalog.Service{Docker: catalog.DockerConfig{Env: []catalog.EnvVar{
		{Key: "DEVICE", Default: "cashpilot-{hostname}"},
		{Key: "TOKEN"},
		{Key: "NODE", Default: "{hostname}"},
	}}}
	first := copyEnv(svc, map[string]string{"DEVICE": "cashpilot-box", "TOKEN": "t", "NODE": "box"}, 1)
	if first["DEVICE"] != "cashpilot-box" || first["NODE"] != "box" {
		t.Fatalf("copy 1 env = %v, want the device names unchanged", first)
	}
	env := copyEnv(svc, map[string]string{"DEVICE": "cashpilot-box", "TOKEN": "t", "NODE": ""}, 2)
	if env["DEVICE"] != "cashpilot-box-2" || env["TOKEN"] != "t" || env["NODE"] != "" {
		t.Fatalf("copy 2 env = %v, want only the non-empty {hostname} value suffixed", env)
	}
}

// A copy is only acted on when the container really is that copy of that service and
// CashPilot made it. Anything else with the name is refused like a missing one.
func TestManagedCopyChecksTheContainerIsThatCopy(t *testing.T) {
	ctx := context.Background()
	labelled := func(labels map[string]string) fakeInspectClient {
		return fakeInspectClient{response: container.InspectResponse{Config: &container.Config{Labels: labels}}}
	}
	if name, err := managedCopy(ctx, labelled(containerLabels("earnfm", 2)), "earnfm", 2); err != nil || name != "cashpilot-earnfm-2" {
		t.Fatalf("managedCopy(copy 2) = %q, %v", name, err)
	}
	unmanaged := containerLabels("earnfm", 2)
	delete(unmanaged, LabelManaged)
	for what, client := range map[string]fakeInspectClient{
		"unmanaged":     labelled(unmanaged),
		"other copy":    labelled(containerLabels("earnfm", 3)),
		"first copy":    labelled(containerLabels("earnfm", 1)),
		"other service": labelled(containerLabels("honeygain", 2)),
		"no config":     {response: container.InspectResponse{}},
	} {
		if _, err := managedCopy(ctx, client, "earnfm", 2); err == nil {
			t.Errorf("%s: managedCopy accepted it", what)
		}
	}
	if _, err := managedCopy(ctx, labelled(containerLabels("earnfm", 1)), "earnfm", 1); err == nil {
		t.Error("managedCopy accepted copy 1, which is the service itself")
	}
}
