package runtime

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// The uninstaller removes the containers CashPilot Desktop made and leaves every
// other container alone, including one a CashPilot worker made on the same
// Docker: that one carries cashpilot.managed=true too, but no cashpilot.runtime.
func TestRemoveDesktopContainersTakesOnlyDesktopContainers(t *testing.T) {
	docker := startHarnessDocker(t)
	p := NewDockerProvider()
	ctx := context.Background()

	for n := 1; n <= 2; n++ {
		if _, err := p.Deploy(ctx, DeploySpec{Slug: "cmd-example", Service: commandService, Env: map[string]string{"TOKEN": "t"}, Instance: n}, nil); err != nil {
			t.Fatalf("Deploy copy %d: %v", n, err)
		}
	}
	// What a CashPilot worker's container looks like: same managed label, no runtime label.
	worker := copyName("cmd-example", 2)
	docker.SetLabel(worker, LabelRuntime, "")

	docker.ResetCalls()
	removed, err := RemoveDesktopContainers(ctx)
	if err != nil {
		t.Fatalf("RemoveDesktopContainers: %v", err)
	}
	if want := []string{copyName("cmd-example", 1)}; !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
	if _, ok := docker.Container(copyName("cmd-example", 1)); ok {
		t.Errorf("the Desktop container is still there: %v", docker.Names())
	}
	if _, ok := docker.Container(worker); !ok {
		t.Errorf("the worker's container was removed; only Desktop containers may go: %v", docker.Names())
	}

	// The daemon is asked for Desktop containers only, and no remove takes volumes.
	var listed, removes int
	for _, call := range docker.CallsWithQuery() {
		path, rawQuery, _ := strings.Cut(call, "?")
		query, _ := url.ParseQuery(rawQuery)
		switch {
		case strings.HasPrefix(path, "GET /containers/json"):
			listed++
			filters := query.Get("filters")
			if !strings.Contains(filters, LabelManaged+"=true") || !strings.Contains(filters, LabelRuntime+"="+desktopRuntime) {
				t.Errorf("list filter %q does not ask for both Desktop labels", filters)
			}
		case strings.HasPrefix(path, "DELETE /containers/"):
			removes++
			if v := query.Get("v"); v == "1" || v == "true" {
				t.Errorf("remove %q takes the container's volumes; they must stay", call)
			}
		}
	}
	if listed != 1 || removes != 1 {
		t.Errorf("want one list and one remove, got %d and %d: %v", listed, removes, docker.CallsWithQuery())
	}
}

func TestIsDesktopContainer(t *testing.T) {
	for _, tc := range []struct {
		labels map[string]string
		want   bool
	}{
		{map[string]string{LabelManaged: "true", LabelRuntime: "existing-docker"}, true},
		{map[string]string{LabelManaged: "true"}, false},            // a CashPilot worker's
		{map[string]string{LabelRuntime: "existing-docker"}, false}, // not managed
		{map[string]string{LabelManaged: "false", LabelRuntime: "existing-docker"}, false},
		{nil, false},
	} {
		if got := IsDesktopContainer(tc.labels); got != tc.want {
			t.Errorf("IsDesktopContainer(%v) = %v, want %v", tc.labels, got, tc.want)
		}
	}
}

// Docker that does not answer is an error, never "nothing to remove".
func TestRemoveDesktopContainersReportsADownDocker(t *testing.T) {
	docker := startHarnessDocker(t)
	docker.SetDown(true)
	if _, err := RemoveDesktopContainers(context.Background()); err == nil {
		t.Fatal("a Docker that does not answer gave no error")
	}
}
