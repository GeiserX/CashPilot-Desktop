package runtime

import (
	"context"
	"errors"
	"fmt"
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

// A CashPilot worker on the same Docker names its containers cashpilot-<slug> too
// and labels them cashpilot.managed=true, but never cashpilot.runtime. Desktop must
// not list such a container as its own, and no start, stop, restart, remove, log
// read or redeploy by slug may reach it. The fake daemon ignores the runtime
// filter, so the listing also proves the check after the daemon's answer.
func TestAWorkersContainerIsNeverDesktops(t *testing.T) {
	docker := startHarnessDocker(t)
	p := NewDockerProvider()
	ctx := context.Background()
	spec := func(slug string) DeploySpec {
		return DeploySpec{Slug: slug, Service: commandService, Env: map[string]string{"TOKEN": "t"}}
	}
	for _, slug := range []string{"cmd-example", "worker-probe"} {
		if _, err := p.Deploy(ctx, spec(slug), nil); err != nil {
			t.Fatalf("Deploy %s: %v", slug, err)
		}
	}
	worker := containerName("worker-probe")
	docker.SetLabel(worker, LabelRuntime, "")
	docker.SetLabel(worker, labelDeployedBy, "worker")
	c, _ := docker.Container(worker)
	workerID := c.ID

	docker.ResetCalls()
	list, err := p.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, c := range list {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{containerName("cmd-example")}) {
		t.Errorf("List = %v, want only Desktop's own container", names)
	}
	for _, call := range docker.CallsWithQuery() {
		if strings.HasPrefix(call, "GET /containers/json") && !strings.Contains(call, url.QueryEscape(LabelRuntime+"="+desktopRuntime)) {
			t.Errorf("list %q does not ask the daemon for Desktop's runtime label", call)
		}
	}

	// Every refusal names the container, and one that comes from Desktop's own
	// ownership check also says a worker made it.
	docker.ResetCalls()
	const sayWorker, anyWords = "CashPilot worker", ""
	for name, tc := range map[string]struct {
		op   func() error
		says string
	}{
		"Stop":        {func() error { return p.Stop(ctx, "worker-probe", 1) }, sayWorker},
		"Start":       {func() error { return p.Start(ctx, "worker-probe") }, sayWorker},
		"Restart":     {func() error { return p.Restart(ctx, "worker-probe", 1) }, sayWorker},
		"Remove":      {func() error { return p.Remove(ctx, "worker-probe", RemoveOptions{}) }, sayWorker},
		"PlanRemoval": {func() error { _, err := p.PlanRemoval(ctx, "worker-probe", nil); return err }, sayWorker},
		"ReadState":   {func() error { _, _, err := p.ReadState(ctx, "worker-probe", []string{"/data"}, 1<<20); return err }, sayWorker},
		"Logs":        {func() error { _, err := p.Logs(ctx, "worker-probe", 10); return err }, sayWorker},
		"Deploy":      {func() error { _, err := p.Deploy(ctx, spec("worker-probe"), nil); return err }, sayWorker},
		"DeployTarget": {func() error {
			_, err := p.DeployTarget(ctx, "worker-probe")
			var other *OtherCashPilotError
			if err != nil && !errors.As(err, &other) {
				return fmt.Errorf("not an OtherCashPilotError: %w", err)
			}
			return err
		}, sayWorker},
		"ReadWalletSeed": {func() error { _, err := p.ReadWalletSeed(ctx, "worker-probe", seedService()); return err }, anyWords},
	} {
		err := tc.op()
		if err == nil || !strings.Contains(err.Error(), worker) || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s of the worker's container: err = %v, want a refusal naming it that says %q", name, err, tc.says)
		}
	}
	// A partial restore is deleted only when it is stopped; a stopped worker
	// container is still not Desktop's to delete.
	docker.SetState(worker, "exited")
	if _, err := p.DiscardRestore(ctx, "worker-probe", nil); err == nil || !strings.Contains(err.Error(), worker) {
		t.Errorf("DiscardRestore of the worker's container: err = %v, want a refusal naming it", err)
	}
	for _, call := range docker.Calls() {
		for _, ref := range []string{worker, workerID} {
			if strings.Contains(call, "/containers/"+ref+"/") && !strings.HasPrefix(call, "GET /containers/"+ref+"/json") {
				t.Errorf("Desktop acted on the worker's container: %s", call)
			}
		}
		if strings.HasPrefix(call, "DELETE /containers/"+worker) || strings.HasPrefix(call, "DELETE /containers/"+workerID) {
			t.Errorf("Desktop removed the worker's container: %s", call)
		}
	}
	if c, ok := docker.Container(worker); !ok || c.State != "exited" {
		t.Fatalf("the worker's container changed: %+v, present %v", c, ok)
	}
}

// A container started from a compose file Desktop exported carries
// cashpilot.deployed-by=compose and no runtime label. A refusal must say so, not
// that a worker made it.
func TestAComposeContainerIsNeverDesktops(t *testing.T) {
	docker := startHarnessDocker(t)
	p := NewDockerProvider()
	ctx := context.Background()
	spec := DeploySpec{Slug: "cmd-example", Service: commandService, Env: map[string]string{"TOKEN": "t"}}
	if _, err := p.Deploy(ctx, spec, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	name := containerName("cmd-example")
	docker.SetLabel(name, LabelRuntime, "")
	docker.SetLabel(name, labelDeployedBy, "compose")

	for op, run := range map[string]func() error{
		"Stop":         func() error { return p.Stop(ctx, "cmd-example", 1) },
		"Remove":       func() error { return p.Remove(ctx, "cmd-example", RemoveOptions{}) },
		"Deploy":       func() error { _, err := p.Deploy(ctx, spec, nil); return err },
		"DeployTarget": func() error { _, err := p.DeployTarget(ctx, "cmd-example"); return err },
	} {
		err := run()
		if err == nil || !strings.Contains(err.Error(), "compose") || strings.Contains(err.Error(), "worker") {
			t.Errorf("%s of a compose container: err = %v, want a refusal that names the compose file, not a worker", op, err)
		}
	}
	if c, ok := docker.Container(name); !ok || c.State != "running" {
		t.Fatalf("the compose container changed: %+v, present %v", c, ok)
	}
}
