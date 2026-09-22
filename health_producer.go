package main

import (
	"github.com/GeiserX/CashPilot-Desktop/internal/health"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// producerStates gathers the per-service "is it actually earning?" verdicts that
// container state cannot give (see internal/health). It is the wiring only: every
// judgement lives in that package, and every log read is bounded there.
//
// Note what is deliberately NOT wired in: store.HealthScores' crash count. That
// counts rows matching '%_error', and every such row production writes is a user
// action that failed — deploy_error, stop_error, start_error, restart_error,
// remove_error (internal/services/manager.go). Three failed Deploy clicks while the
// container runtime was off would have put a red "it keeps stopping unexpectedly"
// badge on a service that is up and earning, for the rest of the day. Desktop
// records no event at all for a container that exits on its own, so that half of the
// restart-loop signal had no real input; the runtime's own "restarting" state, which
// internal/health reads from the deployment status, is the one that works.
func (a *App) producerStates(deployments []store.Deployment) map[string]health.Report {
	if len(deployments) == 0 {
		return nil
	}
	deployed := make([]health.Deployed, 0, len(deployments))
	for _, dep := range deployments {
		deployed = append(deployed, health.Deployed{
			Slug:           dep.Slug,
			ContainerState: dep.Status,
			Runtime:        dep.Runtime,
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
