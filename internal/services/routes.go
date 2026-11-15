package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// Route at deploy (web app/route_gate.py, docs/proxies.md "Route a service at
// deploy"). A service can run through one of the owner's proxies from its first
// start, so it never runs once from this machine's own IP. It runs as the proxy's
// copy of it, with the deploy's own device names, and the store keeps its route
// (store.SetRoute) after it is removed. DeployWith is the one way a container of a
// service starts on this machine's own network, and it refuses a routed service
// unless the owner confirms leaving the route.

// ProxiedOnlyEnv set to true makes this machine refuse every service that does not run
// through a proxy. Only whoever starts the app can set it; the app cannot change it.
const ProxiedOnlyEnv = "CASHPILOT_PROXIED_ONLY"

// ProxiedOnly reports whether CASHPILOT_PROXIED_ONLY is set.
func ProxiedOnly() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CASHPILOT_PROXIED_ONLY"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// RouteError is a deploy refused because the service must not run from this machine's
// own IP. Code is "route_locked", "route_unknown" or "proxied_only".
type RouteError struct {
	Code    string
	Message string
}

func (e *RouteError) Error() string { return e.Message }

// IsRouteLocked reports whether err is a refusal the owner can lift by confirming that
// the service leaves its route.
func IsRouteLocked(err error) bool {
	var re *RouteError
	return errors.As(err, &re) && re.Code == "route_locked"
}

// directAllowed refuses a service that would run from this machine's own IP when it
// must not. It returns the proxy the service leaves, when leaveRoute lets it.
func (m *Manager) directAllowed(svc catalog.Service, slug string, leaveRoute bool) (int64, bool, error) {
	name := orText(svc.Name, slug)
	if ProxiedOnly() {
		return 0, false, &RouteError{"proxied_only", fmt.Sprintf(
			"This computer runs services only through a proxy (%s is set), so %s was not started from its own IP. "+
				"Deploy it with a route through one of your proxies.", ProxiedOnlyEnv, name)}
	}
	id, routed, err := m.store.GetRoute(slug)
	if err != nil {
		return 0, false, &RouteError{"route_unknown", fmt.Sprintf(
			"Could not read whether %s runs through a proxy (%v), so it was not started from this computer's own IP.", name, err)}
	}
	if routed && !leaveRoute {
		return 0, false, &RouteError{"route_locked", fmt.Sprintf(
			"%s runs through a proxy on this computer. Running it from this computer's own IP shows that IP to %s. "+
				"Confirm to switch it, or deploy it again with its route. Nothing was deployed.", name, name)}
	}
	return id, routed, nil
}

// startAllowed is the deploy's rule (directAllowed) for a Start or Restart of a
// container that already exists on this machine's own network: one made before
// CASHPILOT_PROXIED_ONLY was set, or before the service was routed, is held to it
// all the same. Leaving a route takes a deploy, so a start never does.
func (m *Manager) startAllowed(slug string) error {
	svc, _ := m.catalog.Get(slug)
	_, _, err := m.directAllowed(svc, slug, false)
	return err
}

// HoldDirect stops every service running from this machine's own IP when
// CASHPILOT_PROXIED_ONLY is set. A deploy, a Start and a Restart refuse one
// already, but one deployed before the setting keeps running, and Docker starts it
// again after a reboot (restart policy unless-stopped). The app runs this at its
// start and every runtime.FenceGuard. A stop counts as the user's for Docker and for
// the LAN fence's guard, so neither brings it back. It returns the slugs it stopped,
// with "-N" for copy N.
func (m *Manager) HoldDirect(ctx context.Context) []string {
	if !ProxiedOnly() {
		return nil
	}
	units, err := m.List(ctx)
	if err != nil {
		return nil
	}
	var stopped []string
	for _, u := range units {
		// A restarting container runs again as soon as Docker's backoff ends.
		if u.Status != "running" && u.Status != "restarting" {
			continue
		}
		why := fmt.Sprintf("%s is set, so nothing runs from this computer's own IP", ProxiedOnlyEnv)
		var err error
		name := u.Slug
		if u.Instance > 1 {
			name = fmt.Sprintf("%s-%d", u.Slug, u.Instance)
			err = m.StopCopy(ctx, u.Slug, u.Instance)
		} else {
			err = m.providerForSlug(u.Slug).Stop(ctx, u.Slug, m.stopTimeout(u.Slug))
			if err == nil {
				if dep, ok, derr := m.store.GetDeployment(u.Slug); derr == nil && ok {
					dep.Status = "stopped"
					_ = m.store.UpsertDeployment(dep)
				}
			}
		}
		if err != nil {
			m.store.RecordEvent(u.Slug, "stop_error", err.Error())
			continue
		}
		m.store.RecordEvent(u.Slug, "stopped", why)
		stopped = append(stopped, name)
	}
	return stopped
}

// Route returns the proxy slug is routed through and whether it runs as that route:
// routed, with a copy through that proxy and no container of its own on this machine.
func (m *Manager) Route(slug string) (int64, bool) {
	id, routed, err := m.store.GetRoute(slug)
	if err != nil || !routed {
		return 0, false
	}
	if _, deployed, err := m.store.GetDeployment(slug); err != nil || deployed {
		return 0, false
	}
	copies, err := m.store.ListProxiedCopies(id)
	if err != nil {
		return 0, false
	}
	for _, c := range copies {
		if c.Slug == slug {
			return id, true
		}
	}
	return 0, false
}

// Routes returns slug -> proxy id of every service that runs as its route.
func (m *Manager) Routes() map[string]int64 {
	out := map[string]int64{}
	all, err := m.store.ListRoutes()
	if err != nil {
		return out
	}
	for slug := range all {
		if id, ok := m.Route(slug); ok {
			out[slug] = id
		}
	}
	return out
}

// DeployRouted deploys slug through proxy id, never from this machine's own IP. The
// route is recorded before the copy exists, so no other deploy can start the service
// from this machine's IP meanwhile; a deploy that fails puts the earlier route back.
// It holds the service's deploy lock throughout, as DeployWith does, so that rollback
// never undoes another deploy's route.
func (m *Manager) DeployRouted(ctx context.Context, slug string, credentials map[string]string, id int64) (ProxiedResult, error) {
	defer m.lockDeploy(slug)()
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return ProxiedResult{}, fmt.Errorf("unknown service: %s", slug)
	}
	if svc.ManualOnly {
		return ProxiedResult{}, fmt.Errorf("%s is tracked manually and has no Docker image", svc.Name)
	}
	if err := refuseRetired(svc); err != nil {
		return ProxiedResult{}, err
	}
	if svc.Custom {
		return ProxiedResult{}, fmt.Errorf("%s is a custom service: deploy it, then add a copy through the proxy on the Proxies page", svc.Name)
	}
	if why := m.ProxyRefusal(slug); why != "" {
		return ProxiedResult{}, errors.New(why)
	}
	if _, deployed, err := m.store.GetDeployment(slug); err != nil {
		return ProxiedResult{}, err
	} else if deployed {
		return ProxiedResult{}, fmt.Errorf("%s already runs from this computer's own IP. Remove it first and deploy it again with the route, or keep it and add a copy through the proxy on the Proxies page", svc.Name)
	}
	before, had, err := m.store.GetRoute(slug)
	if err != nil {
		return ProxiedResult{}, err
	}
	if had && before != id {
		if _, running := m.Route(slug); running {
			return ProxiedResult{}, fmt.Errorf("%s runs through another proxy. Remove it, then deploy it again through this one", svc.Name)
		}
	}
	if _, _, err := m.store.SetRoute(slug, id); err != nil {
		return ProxiedResult{}, fmt.Errorf("could not record the route of %s, so nothing was deployed: %w", svc.Name, err)
	}
	result, err := m.runProxied(ctx, slug, id, svc, credentials, true)
	if err != nil {
		if had {
			_, _, _ = m.store.SetRoute(slug, before)
		} else {
			_ = m.store.DeleteRoute(slug)
		}
		return ProxiedResult{}, err
	}
	return result, nil
}

// RouteLogs is the log tail of a routed service's copy.
func (m *Manager) RouteLogs(ctx context.Context, slug string, id int64, lines int) (string, error) {
	p, err := m.proxier()
	if err != nil {
		return "", err
	}
	logger, ok := p.(interface {
		ProxiedLogs(ctx context.Context, slug string, proxyID int64, lines int) (string, error)
	})
	if !ok {
		return "", errors.New("this runtime cannot read a proxied copy's logs")
	}
	return logger.ProxiedLogs(ctx, slug, id, lines)
}
