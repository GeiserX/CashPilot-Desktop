package main

import (
	"context"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/health"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	_ "modernc.org/sqlite"
)

// producerApp is an App with a real store and the shipped catalog, which is all
// producerStates touches. keyring.MockInit is installed by TestMain in
// fleet_server_test.go, so the store's key stays in memory.
func producerApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", dir)
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager error: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open error: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded error: %v", err)
	}
	return &App{cfg: cfg, store: st, catalog: cat, ctx: context.Background()}
}

func verdictText(r health.Report) string {
	return string(r.State) + ": " + strings.Join(r.Reasons, " | ")
}

// failedActionEvents are every event name production writes that store.HealthScores
// counts as a "crash" (it counts '%_error' and 'missing_from_runtime'). Every one of
// them is a user action that failed or a record being tidied up — Desktop writes no
// event at all when a container exits on its own. They are listed here so that a new
// one cannot quietly start driving an earning verdict.
var failedActionEvents = []string{
	"deploy_error", "stop_error", "start_error", "restart_error", "remove_error",
	"missing_from_runtime",
}

// TestAFailedClickIsNotARestartLoop is the case that put a red "not earning" badge on
// a healthy service: the container runtime is off, the user clicks Deploy three
// times, then starts Docker and the deploy works. Nothing about those three failures
// says the container is looping, and the badge would have stayed up for a day.
func TestAFailedClickIsNotARestartLoop(t *testing.T) {
	for _, event := range failedActionEvents {
		t.Run(event, func(t *testing.T) {
			app := producerApp(t)
			for i := 0; i < 5; i++ {
				app.store.RecordEvent("bitping", event, "Cannot connect to the Docker daemon")
			}

			got := app.producerStates([]store.Deployment{{Slug: "bitping", Status: "running", Runtime: "docker"}})

			if got["bitping"].State.NotEarning() {
				t.Fatalf("a failed user action is not a container exit; got %s", verdictText(got["bitping"]))
			}
		})
	}
}

// TestARestartingServiceIsReportedAsNotEarning is the positive control for the test
// above: the restart-loop signal that IS real must still reach the dashboard, or
// every check here would pass on a verdict that never says anything.
func TestARestartingServiceIsReportedAsNotEarning(t *testing.T) {
	app := producerApp(t)

	got := app.producerStates([]store.Deployment{{Slug: "bitping", Status: "restarting", Runtime: "docker"}})

	if !got["bitping"].State.NotEarning() {
		t.Fatalf("a container the runtime keeps restarting is not earning; got %s", verdictText(got["bitping"]))
	}
}

// TestAStoppedServiceIsNotAccusedOfNotEarning: the status pill already says it is
// stopped, and there is nothing to judge about a container that is not running.
func TestAStoppedServiceIsNotAccusedOfNotEarning(t *testing.T) {
	app := producerApp(t)

	got := app.producerStates([]store.Deployment{{Slug: "bitping", Status: "exited", Runtime: "docker"}})

	if got["bitping"].State.NotEarning() {
		t.Fatalf("a stopped service earns nothing by definition; got %s", verdictText(got["bitping"]))
	}
}

// TestANativeServiceIsNotJudgedByContainerSignals. Mysterium is the one catalogued
// service Desktop can run as a plain process, and its declared signal explains itself
// as a container that was built without two capabilities, ending "redeploy Mysterium
// from the dashboard". Told to a user with no container, that is advice they cannot
// act on.
func TestANativeServiceIsNotJudgedByContainerSignals(t *testing.T) {
	app := producerApp(t)
	running := func(kind string) health.Report {
		return app.producerStates([]store.Deployment{{Slug: "mysterium", Status: "running", Runtime: kind}})["mysterium"]
	}

	native, inContainer := running("native"), running("docker")

	if native.State != health.StateNotChecked {
		t.Fatalf("a native process cannot be judged by container signals; got %s", verdictText(native))
	}
	// The verdict must reach the user as the reason it really is. Which runtime is
	// running this service is the one thing this wiring has to carry across, so the
	// two answers cannot be allowed to read the same.
	if verdictText(native) == verdictText(inContainer) {
		t.Errorf("the runtime must reach the verdict; a native process and a container both said %s", verdictText(native))
	}
	if strings.Contains(strings.Join(native.Reasons, " "), "logs") {
		t.Errorf("and it must not claim to have looked at logs it never read: %s", verdictText(native))
	}
}

// TestNoDeploymentsMeansNoVerdicts, so the dashboard has nothing to render.
func TestNoDeploymentsMeansNoVerdicts(t *testing.T) {
	app := producerApp(t)
	if got := app.producerStates(nil); got != nil {
		t.Fatalf("no deployments means no verdicts; got %v", got)
	}
}
