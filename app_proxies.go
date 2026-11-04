package main

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/proxies"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
)

// Proxied copies (internal/services/proxies.go): the owner adds a proxy once, then
// runs a copy of a deployed service through it, so this machine earns on another
// exit IP. The traffic cap is enforced by a once-a-minute pass while the app runs.

// proxyTickInterval is how often proxied copies are read, traffic is counted and the
// cap is enforced; the same minute the web worker's heartbeat gives it.
const proxyTickInterval = time.Minute

// ProxyCandidate is a deployed service and whether it can run through a proxy.
type ProxyCandidate struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Refusal string `json:"refusal"`
}

// ProxiesState is everything the Proxies section shows.
type ProxiesState struct {
	Proxies []proxies.View `json:"proxies"`
	// Candidates are the deployed services, each with why it cannot go behind a proxy
	// ("" when it can).
	Candidates []ProxyCandidate `json:"candidates"`
	// TunnelImage is the tunnel image this build runs; TunnelImageError says why
	// proxied copies cannot run yet when there is none.
	TunnelImage      string `json:"tunnelImage"`
	TunnelImageError string `json:"tunnelImageError"`
}

func (a *App) GetProxiesState() (ProxiesState, error) {
	if err := a.ready(); err != nil {
		return ProxiesState{}, err
	}
	views, err := a.services.ListProxies()
	if err != nil {
		return ProxiesState{}, err
	}
	out := ProxiesState{Proxies: views, Candidates: []ProxyCandidate{}}
	for _, dep := range a.store.ListDeployments() {
		out.Candidates = append(out.Candidates, ProxyCandidate{Slug: dep.Slug, Name: a.serviceName(dep.Slug), Refusal: a.services.ProxyRefusal(dep.Slug)})
	}
	sort.Slice(out.Candidates, func(i, j int) bool { return out.Candidates[i].Name < out.Candidates[j].Name })
	if image, err := runtime.TunnelImage(); err != nil {
		out.TunnelImageError = err.Error()
	} else {
		out.TunnelImage = image
	}
	return out, nil
}

// SaveProxy creates a proxy (id 0) or edits one. Secret boxes left blank keep what is
// stored; clear_username / clear_password / clear_auth_key set to "true" drop it.
func (a *App) SaveProxy(id int64, values map[string]string) (proxies.View, error) {
	if err := a.ready(); err != nil {
		return proxies.View{}, err
	}
	return a.services.SaveProxy(a.opCtx(), id, values)
}

func (a *App) DeleteProxy(id int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.services.DeleteProxy(id)
}

// TestProxy starts a throwaway tunnel through the proxy on this machine and reports
// the exit IP and country it gets.
func (a *App) TestProxy(id int64) (services.ProxyTestResult, error) {
	if err := a.ready(); err != nil {
		return services.ProxyTestResult{}, err
	}
	ctx, cancel := context.WithTimeout(a.opCtx(), 2*time.Minute)
	defer cancel()
	return a.services.TestProxy(ctx, id)
}

// DeployProxiedCopy runs a copy of a deployed service through a proxy.
func (a *App) DeployProxiedCopy(slug string, id int64) (services.ProxiedResult, error) {
	if err := a.ready(); err != nil {
		return services.ProxiedResult{}, err
	}
	ctx, cancel := context.WithTimeout(a.opCtx(), 4*time.Minute)
	defer cancel()
	result, err := a.services.DeployProxied(ctx, slug, id)
	if err == nil {
		a.proxyTickOnce(ctx)
	}
	return result, err
}

func (a *App) StopProxiedCopy(slug string, id int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.afterProxyChange(a.services.StopProxied(a.opCtx(), slug, id))
}

func (a *App) StartProxiedCopy(slug string, id int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.opCtx(), 2*time.Minute)
	defer cancel()
	return a.afterProxyChange(a.services.StartProxied(ctx, slug, id))
}

func (a *App) RemoveProxiedCopy(slug string, id int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.afterProxyChange(a.services.RemoveProxied(a.opCtx(), slug, id))
}

// ResetProxyUsage starts a new traffic period by hand and starts the copies the cap
// stopped; it returns the slugs it started.
func (a *App) ResetProxyUsage(id int64) ([]string, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.opCtx(), 4*time.Minute)
	defer cancel()
	started, err := a.services.ResetProxy(ctx, id)
	if started == nil {
		started = []string{}
	}
	return started, a.afterProxyChange(err)
}

func (a *App) afterProxyChange(err error) error {
	if err == nil {
		a.proxyTickOnce(a.opCtx())
	}
	return err
}

// opCtx is the app's context for a user action, or a background one in tests.
func (a *App) opCtx() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// proxyTickOnce runs one pass and tells the user about every proxy that hit its cap.
func (a *App) proxyTickOnce(ctx context.Context) {
	if a.services == nil {
		return
	}
	crossed, err := a.services.ProxyTick(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		return
	}
	for _, n := range crossed {
		a.emitNotice("proxy-cap", n.Message)
	}
}

// startProxyTicker runs proxyTickOnce every minute until ctx ends.
func (a *App) startProxyTicker(ctx context.Context) {
	if ctx == nil || a.services == nil {
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	a.proxyStop = func() {
		cancel()
		<-done
	}
	go func() {
		defer close(done)
		// Once at start, so an earner a reboot left stopped behind a ready tunnel
		// starts without waiting a minute.
		a.proxyTickOnce(loopCtx)
		ticker := time.NewTicker(proxyTickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				a.proxyTickOnce(loopCtx)
			}
		}
	}()
}

func (a *App) stopProxyTicker() {
	if a.proxyStop != nil {
		a.proxyStop()
		a.proxyStop = nil
	}
}

// proxyNotifications are the caps that hold right now, read from the store so a
// restart cannot drop one.
func (a *App) proxyNotifications() []Notification {
	if a.services == nil {
		return nil
	}
	var items []Notification
	for _, n := range a.services.CapNotices() {
		items = append(items, Notification{Level: "warning", Title: n.Name + " reached its traffic cap", Message: n.Message})
	}
	return items
}
