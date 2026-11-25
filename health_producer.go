package main

import (
	"github.com/GeiserX/CashPilot-Desktop/internal/health"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// The windowed health check restarts a service through the manager it reads the
// logs from; if the manager stops satisfying it, a restart: true signal would only
// ever be shown.
var _ health.Restarter = (*services.Manager)(nil)

// The same for every other copy: copies 2 and up, and copies through a proxy.
var _ health.CopyRestarter = (*services.Manager)(nil)

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
	// a.services is nil in the scheduler tests, and a.catalog before startup finishes;
	// health.Collect treats either as "could not look" rather than dereferencing it.
	var lookup health.ServiceLookup
	if a.catalog != nil {
		lookup = a.catalog
	}
	// Before the early return: a routed service has no deployment row, and its copy
	// through the proxy is checked all the same.
	if a.services != nil && lookup != nil {
		health.CheckCopies(a.services, lookup, otherCopies(a.services.Copies(), a.services.ProxiedCopies()))
	}
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
	var logs health.LogReader
	if a.services != nil {
		logs = a.services
	}
	return health.Collect(a.ctx, logs, lookup, deployed)
}

// otherCopies is every copy the windowed check judges besides a service's first:
// copies 2 and up as the last refresh listed them, and every copy through a proxy as
// the last status read saw it, a routed service's included (web CashPilot #809). A
// proxied copy counts as running only while its tunnel runs and has proved its exit:
// an earner in a tunnel that is down or still coming up has nothing to judge, and
// the owner and the cap stop a copy by stopping its tunnel.
func otherCopies(copies map[string][]runtime.ContainerInfo, proxied []runtime.ProxiedCopy) []health.Copy {
	var out []health.Copy
	for slug, infos := range copies {
		for _, info := range infos {
			if info.Instance > 1 {
				out = append(out, health.Copy{Slug: slug, Instance: info.Instance, State: info.Status})
			}
		}
	}
	for _, c := range proxied {
		if c.ProxyID <= 0 {
			continue
		}
		state := c.Status
		if c.TunnelStatus != "running" || c.ExitIP == "" {
			state = "tunnel " + c.TunnelStatus
		}
		out = append(out, health.Copy{Slug: c.Slug, ProxyID: c.ProxyID, State: state})
	}
	return out
}
