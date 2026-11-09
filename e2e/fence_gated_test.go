package e2e

import (
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// gatedProvider is a Docker provider whose isolation setting the test changes, with
// the catalog's fence_gated services wired in the way app.go does. Mysterium is
// deployed behind the fence and running.
func gatedProvider(t *testing.T, e *env, setting *string) *runtime.DockerProvider {
	t.Helper()
	provider := runtime.NewDockerProvider()
	provider.SetIsolationSetting(func() string { return *setting })
	provider.SetFenceGated(func(slug string) bool {
		svc, ok := e.Catalog.Get(slug)
		return ok && svc.Docker.FenceGated
	})
	svc, _ := e.Catalog.Get("mysterium")
	if _, err := provider.Deploy(e.ctx(), runtime.DeploySpec{Slug: "mysterium", Service: svc}, nil); err != nil {
		t.Fatalf("deploy mysterium: %v", err)
	}
	return provider
}

func state(e *env, name string) string {
	c, _ := e.Docker.Container(name)
	return c.State
}

// A node Docker is about to restart is stopped too: left alone, Docker would bring
// it back after the drops are gone, past the app's Start guard.
func TestTurningIsolationOffStopsARestartingMysterium(t *testing.T) {
	e := newEnv(t)
	setting := "auto"
	provider := gatedProvider(t, e, &setting)
	e.Docker.SetState("cashpilot-mysterium", "restarting")
	setting = "off"
	provider.ReconcileFence(e.ctx())
	if got := state(e, "cashpilot-mysterium"); got != "exited" {
		t.Fatalf("state = %q, want the restarting node stopped", got)
	}
}

// A node that cannot be stopped keeps the fence: the drops stay, and the status
// says why.
func TestTheFenceStaysWhenMysteriumCannotBeStopped(t *testing.T) {
	e := newEnv(t)
	setting := "auto"
	provider := gatedProvider(t, e, &setting)
	e.Docker.FailStop("cashpilot-mysterium")
	setting = "off"
	provider.ReconcileFence(e.ctx())
	st := provider.FenceStatus()
	if st.State != "failed" || !strings.Contains(st.Error, "LAN fence was kept") {
		t.Fatalf("status = %+v, want the fence kept and the failure named", st)
	}
}

// A container whose inspect fails cannot be judged, so Start and Restart refuse it.
func TestStartFailsClosedWhenTheContainerCannotBeInspected(t *testing.T) {
	e := newEnv(t)
	setting := "auto"
	provider := gatedProvider(t, e, &setting)
	if err := provider.Start(e.ctx(), "mysterium"); err != nil {
		t.Fatalf("Start behind the fence: %v", err)
	}
	e.Docker.SetState("cashpilot-mysterium", "exited")
	e.Docker.FailInspect("cashpilot-mysterium")
	for name, err := range map[string]error{
		"Start":   provider.Start(e.ctx(), "mysterium"),
		"Restart": provider.Restart(e.ctx(), "mysterium", 5),
	} {
		if err == nil || !strings.Contains(err.Error(), "could not check") {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	if got := state(e, "cashpilot-mysterium"); got == "running" {
		t.Fatal("a container that could not be inspected was started")
	}
}

// A Mysterium made before the fence-gated label existed is found by its catalog
// entry: stopped when isolation goes off, and not started again.
func TestAnUnlabelledMysteriumIsGatedByItsCatalogEntry(t *testing.T) {
	e := newEnv(t)
	setting := "auto"
	provider := gatedProvider(t, e, &setting)
	e.Docker.SetLabel("cashpilot-mysterium", runtime.LabelFenceGated, "")
	setting = "off"
	provider.ReconcileFence(e.ctx())
	if got := state(e, "cashpilot-mysterium"); got != "exited" {
		t.Fatalf("state = %q, want the unlabelled node stopped", got)
	}
	if err := provider.Start(e.ctx(), "mysterium"); err == nil || !strings.Contains(err.Error(), "only behind the LAN fence") {
		t.Fatalf("Start: err = %v, want a refusal", err)
	}
}

// Behind a working fence, a node that is not on the fenced network alone is still
// refused: a container made on the host's network, or attached elsewhere too.
func TestStartRefusesAMysteriumOffTheFencedNetwork(t *testing.T) {
	e := newEnv(t)
	setting := "auto"
	provider := gatedProvider(t, e, &setting)
	e.Docker.SetState("cashpilot-mysterium", "exited")
	e.Docker.AttachNetwork("cashpilot-mysterium", "bridge")
	if err := provider.Start(e.ctx(), "mysterium"); err == nil || !strings.Contains(err.Error(), "also attached to bridge") {
		t.Fatalf("Start: err = %v, want a refusal naming the other network", err)
	}
	if got := state(e, "cashpilot-mysterium"); got == "running" {
		t.Fatal("a node attached outside the fence was started")
	}
}
