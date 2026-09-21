package main

import (
	"github.com/GeiserX/CashPilot-Desktop/internal/health"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// producerCrashWindowDays is the window the restart-loop signal counts unexpected
// exits over. One day, not the seven the health score uses: the score is a rolling
// reputation, this is "is it looping right now", and a service that crashed three
// times last Tuesday and has run since is not looping today.
const producerCrashWindowDays = 1

// producerStates gathers the per-service "is it actually earning?" verdicts that
// container state cannot give (see internal/health). It is the wiring only: every
// judgement lives in that package, and every log read is bounded there.
func (a *App) producerStates(deployments []store.Deployment) map[string]health.Report {
	if len(deployments) == 0 {
		return nil
	}
	var crashes map[string]store.HealthScore
	if a.store != nil {
		crashes = a.store.HealthScores(producerCrashWindowDays)
	}
	deployed := make([]health.Deployed, 0, len(deployments))
	for _, dep := range deployments {
		deployed = append(deployed, health.Deployed{
			Slug:           dep.Slug,
			ContainerState: dep.Status,
			RecentCrashes:  crashes[dep.Slug].Crashes,
		})
	}
	// a.services is nil in the scheduler tests, and a.catalog before startup finishes;
	// health.Collect treats either as "could not look" rather than dereferencing it.
	var logs health.LogReader
	if a.services != nil {
		logs = a.services
	}
	var lookup health.ServiceLookup
	if a.catalog != nil {
		lookup = a.catalog
	}
	return health.Collect(a.ctx, logs, lookup, deployed)
}
