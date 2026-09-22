package e2e

import (
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// TestTheRuntimeGoesAwayAndComesBack is the everyday failure on a desktop: the
// user quits Docker Desktop, or the machine sleeps, or Podman's VM stops.
//
// The rule it protects: while the runtime is gone the app says so and refuses the
// work, but it must NOT forget the earners. Deleting the rows because a listing
// came back empty or errored would blank the dashboard and, on the next start,
// leave the user believing they had never deployed anything.
func TestTheRuntimeGoesAwayAndComesBack(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	if _, err := e.Manager.Deploy(ctx, "honeygain", honeygainCreds); err != nil {
		t.Fatalf("deploy while the runtime is up: %v", err)
	}
	before, _ := e.deployment("honeygain")

	provider := runtime.NewDockerProvider()
	if status := provider.Status(ctx); !status.Available {
		t.Fatalf("the runtime reports unavailable while the fake daemon is up: %+v", status)
	}

	e.Docker.SetDown(true)

	t.Run("the app says the runtime is unavailable", func(t *testing.T) {
		status := provider.Status(ctx)
		if status.Available {
			t.Fatal("the runtime still reports available with no daemon behind it")
		}
		if status.Message == "" {
			t.Error("an unavailable runtime gives the user no message to act on")
		}
	})

	t.Run("work is refused rather than half done", func(t *testing.T) {
		if _, err := e.Manager.Deploy(ctx, "earnapp", earnappCreds); err == nil {
			t.Error("a deploy succeeded with no daemon")
		}
		if err := e.Manager.Stop(ctx, "honeygain"); err == nil {
			t.Error("a stop succeeded with no daemon")
		}
		if _, err := e.Manager.List(ctx); err == nil {
			t.Error("listing succeeded with no daemon")
		}
	})

	t.Run("the deployments survive the outage", func(t *testing.T) {
		if _, err := e.Manager.Refresh(ctx); err == nil {
			t.Error("Refresh reported success with no daemon")
		}
		row, ok := e.deployment("honeygain")
		if !ok {
			t.Fatal("the outage wiped the deployment row")
		}
		if row.ContainerID != before.ContainerID {
			t.Errorf("the row changed during the outage: %q -> %q", before.ContainerID, row.ContainerID)
		}
	})

	e.Docker.SetDown(false)

	t.Run("everything works again once the runtime is back", func(t *testing.T) {
		if status := provider.Status(ctx); !status.Available {
			t.Fatalf("the runtime is back but still reported unavailable: %+v", status)
		}
		deployments, err := e.Manager.Refresh(ctx)
		if err != nil {
			t.Fatalf("Refresh after recovery: %v", err)
		}
		found := false
		for _, dep := range deployments {
			if dep.Slug == "honeygain" && dep.ContainerID == before.ContainerID {
				found = true
			}
		}
		if !found {
			t.Errorf("the earner did not come back after recovery: %+v", deployments)
		}
		if err := e.Manager.Stop(ctx, "honeygain"); err != nil {
			t.Errorf("Stop after recovery: %v", err)
		}
	})
}

// TestTheRuntimeDiesMidDeploy cuts the daemon off between creating the container
// and starting it — the one window where the app has made a change it has not
// recorded yet. The deploy must fail loudly and write nothing, and once the
// runtime is back the app must reconcile what is really there instead of leaving
// a container it does not know about.
func TestTheRuntimeDiesMidDeploy(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	e.Docker.SetDownAfter("POST /containers/create")

	_, err := e.Manager.Deploy(ctx, "honeygain", honeygainCreds)
	if err == nil {
		t.Fatal("the deploy reported success although the daemon died before the container started")
	}
	if _, ok := e.deployment("honeygain"); ok {
		t.Error("a deploy that never started the container still wrote a dashboard row")
	}

	e.Docker.SetDown(false)

	// The container exists, created but never started. Refresh is what reconciles
	// it: the user sees the earner with its real state rather than nothing.
	deployments, err := e.Manager.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh after recovery: %v", err)
	}
	var row string
	for _, dep := range deployments {
		if dep.Slug == "honeygain" {
			row = dep.Status
		}
	}
	if row == "" {
		t.Fatalf("the orphaned container was not reconciled: %+v", deployments)
	}
	if row == "running" {
		t.Errorf("the orphan is reported as running; it never started")
	}

	// And a fresh deploy recovers the earner for real.
	if _, err := e.Manager.Deploy(ctx, "honeygain", honeygainCreds); err != nil {
		t.Fatalf("redeploy after recovery: %v", err)
	}
	container, ok := e.Docker.Container("cashpilot-honeygain")
	if !ok || container.State != "running" {
		t.Fatalf("the earner is not running after the recovery deploy: %+v", container)
	}
	if names := e.Docker.Names(); len(names) != 1 {
		t.Errorf("the recovery deploy left duplicate containers: %v", names)
	}
}

// TestAnUnreachableRuntimeExplainsItself checks the wording the user actually
// reads. It is short on purpose: the message is the whole product surface of an
// outage, and "error" on a dashboard tells nobody what to do.
func TestAnUnreachableRuntimeExplainsItself(t *testing.T) {
	e := newEnv(t)
	e.Docker.SetDown(true)

	status := runtime.NewDockerProvider().Status(e.ctx())
	if status.Available {
		t.Fatal("reported available with no daemon")
	}
	lower := strings.ToLower(status.Message)
	if !strings.Contains(lower, "runtime") && !strings.Contains(lower, "docker") {
		t.Errorf("the message does not mention the runtime at all: %q", status.Message)
	}
}
