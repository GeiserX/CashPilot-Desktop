package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	_ "modernc.org/sqlite"
)

// producerApp is an App with a real store and the shipped catalog, which is all
// producerStates touches. keyring.MockInit is installed by TestMain in
// fleet_server_test.go, so the store's key stays in memory.
func producerApp(t *testing.T) (*App, string) {
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
	return &App{cfg: cfg, store: st, catalog: cat, ctx: context.Background()}, cfg.DataDir()
}

// recordCrashes writes the unexpected-exit events HealthScores counts as crashes.
func recordCrashes(app *App, slug string, n int) {
	for i := 0; i < n; i++ {
		app.store.RecordEvent(slug, "process_error", "exit status 1")
	}
}

// backdateEvents moves every recorded event into the past, which is the only way to
// test the window: the store always stamps an event with the time it was written.
func backdateEvents(t *testing.T, dataDir string, days int) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "cashpilot-desktop.db"))
	if err != nil {
		t.Fatalf("open the store file: %v", err)
	}
	defer db.Close()
	res, err := db.Exec(`UPDATE runtime_events SET created_at = datetime('now', ?)`, "-"+strconv.Itoa(days)+" days")
	if err != nil {
		t.Fatalf("backdate events: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		t.Fatal("no events were backdated, so this test would prove nothing")
	}
}

// TestARunningServiceThatKeepsCrashingIsReportedAsNotEarning walks the whole wiring:
// a deployment the runtime calls "running", crashes recorded in the store, and a
// verdict that contradicts the green status pill.
func TestARunningServiceThatKeepsCrashingIsReportedAsNotEarning(t *testing.T) {
	app, _ := producerApp(t)
	recordCrashes(app, "bitping", 3)

	got := app.producerStates([]store.Deployment{{Slug: "bitping", Status: "running"}})

	verdict, ok := got["bitping"]
	if !ok {
		t.Fatal("a deployed service must get a verdict")
	}
	if !verdict.State.NotEarning() {
		t.Fatalf("a service crashing repeatedly today is not earning; got %q %v", verdict.State, verdict.Reasons)
	}
}

// TestCrashesFromDaysAgoAreNotTodaysRestartLoop. The health score reads a rolling
// week, which is right for a reputation and wrong for "is it looping now": a service
// that crashed three times last week and has run since would otherwise wear a red
// "not earning" badge for the rest of the week.
func TestCrashesFromDaysAgoAreNotTodaysRestartLoop(t *testing.T) {
	app, dataDir := producerApp(t)
	recordCrashes(app, "bitping", 5)
	backdateEvents(t, dataDir, 3)

	got := app.producerStates([]store.Deployment{{Slug: "bitping", Status: "running"}})

	if got["bitping"].State.NotEarning() {
		t.Fatalf("crashes from three days ago are not a loop today; got %q %v",
			got["bitping"].State, got["bitping"].Reasons)
	}
}

// TestAStoppedServiceIsNotAccusedOfNotEarning: the status pill already says it is
// stopped, and there is nothing to judge about a container that is not running.
func TestAStoppedServiceIsNotAccusedOfNotEarning(t *testing.T) {
	app, _ := producerApp(t)
	recordCrashes(app, "bitping", 5)

	got := app.producerStates([]store.Deployment{{Slug: "bitping", Status: "exited"}})

	if got["bitping"].State.NotEarning() {
		t.Fatalf("a stopped service earns nothing by definition; got %q %v",
			got["bitping"].State, got["bitping"].Reasons)
	}
}

// TestNoDeploymentsMeansNoVerdicts, so the dashboard has nothing to render and the
// store is not queried for a window nobody will read.
func TestNoDeploymentsMeansNoVerdicts(t *testing.T) {
	app, _ := producerApp(t)
	if got := app.producerStates(nil); got != nil {
		t.Fatalf("no deployments means no verdicts; got %v", got)
	}
}
