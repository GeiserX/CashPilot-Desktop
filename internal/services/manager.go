package services

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// defaultRuntimeKind is the runtime-kind key the Docker provider is registered
// under. It is exactly the value config.RuntimeProvider defaults to and the value
// every existing deployment row carries in its Runtime column, so resolving an
// empty or unknown kind to this provider leaves all of today's data and behavior
// unchanged while giving the app a registry that can hold more than one runtime
// (e.g. a future native process runtime) later.
const defaultRuntimeKind = "existing-docker"

// Manager owns the runtime provider registry and reconciles it against the store.
// providers is keyed by runtime kind; defaultKind is the kind used when a
// deployment or service does not name one — which is every service today, since
// Docker is the only registered runtime.
type Manager struct {
	providers   map[string]runtime.Provider
	defaultKind string
	catalog     *catalog.Catalog
	store       *store.Store
}

// NewManager builds a Manager whose registry holds the given provider under
// defaultRuntimeKind, which also becomes the default kind. The signature is
// unchanged: callers still pass the single Docker provider, so the app stays
// single-runtime in behavior until a second provider is registered.
func NewManager(provider runtime.Provider, cat *catalog.Catalog, st *store.Store) *Manager {
	return &Manager{
		providers:   map[string]runtime.Provider{defaultRuntimeKind: provider},
		defaultKind: defaultRuntimeKind,
		catalog:     cat,
		store:       st,
	}
}

// resolveProvider returns the provider registered for kind together with the kind
// actually used. An empty or unregistered kind falls back to the default provider
// (existing-docker) — which is every deployment today — so callers can persist the
// returned kind instead of a hardcoded literal and always record the runtime that
// really served the request.
func (m *Manager) resolveProvider(kind string) (runtime.Provider, string) {
	if kind != "" {
		if p, ok := m.providers[kind]; ok {
			return p, kind
		}
	}
	return m.providers[m.defaultKind], m.defaultKind
}

// providerForSlug resolves the provider that owns a deployed service from the
// deployment's recorded runtime kind, defaulting to the Docker provider when the
// deployment is absent or its kind is unset/unknown. With only Docker registered
// this always returns the Docker provider, so lifecycle operations are unchanged.
func (m *Manager) providerForSlug(slug string) runtime.Provider {
	kind := ""
	if dep, ok, err := m.store.GetDeployment(slug); err == nil && ok {
		kind = dep.Runtime
	}
	provider, _ := m.resolveProvider(kind)
	return provider
}

func (m *Manager) Deploy(ctx context.Context, slug string, credentials map[string]string) (store.Deployment, error) {
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return store.Deployment{}, fmt.Errorf("unknown service: %s", slug)
	}
	if svc.ManualOnly {
		return store.Deployment{}, fmt.Errorf("%s is tracked manually and has no Docker image", svc.Name)
	}
	if err := validateRequired(svc, credentials); err != nil {
		return store.Deployment{}, err
	}

	// A future phase will pick the kind from the service/config; today every
	// service deploys to the default runtime. Resolve it and record whatever kind
	// actually served the deploy, so the persisted Runtime is derived rather than a
	// hardcoded literal (still "existing-docker" while Docker is the only runtime).
	provider, runtimeKind := m.resolveProvider("")

	m.store.RecordEvent(slug, "pull_start", svc.Docker.Image)
	info, err := provider.Deploy(ctx, runtime.DeploySpec{Slug: slug, Service: svc, Env: credentials}, func(message string) {
		m.store.RecordEvent(slug, "runtime_progress", message)
	})
	if err != nil {
		m.store.RecordEvent(slug, "deploy_error", err.Error())
		return store.Deployment{}, err
	}

	deployment := store.Deployment{
		Slug:        slug,
		ContainerID: info.ContainerID,
		Name:        info.Name,
		Image:       info.Image,
		Status:      info.Status,
		Runtime:     runtimeKind,
		CPUPercent:  info.CPUPercent,
		MemoryMB:    info.MemoryMB,
	}
	if err := m.store.UpsertDeployment(deployment); err != nil {
		return store.Deployment{}, err
	}
	m.store.RecordEvent(slug, "deployed", info.ContainerID)
	return deployment, nil
}

func (m *Manager) Stop(ctx context.Context, slug string) error {
	if err := m.providerForSlug(slug).Stop(ctx, slug); err != nil {
		m.store.RecordEvent(slug, "stop_error", err.Error())
		return err
	}
	if dep, ok, err := m.store.GetDeployment(slug); err == nil && ok {
		dep.Status = "stopped"
		_ = m.store.UpsertDeployment(dep)
	}
	m.store.RecordEvent(slug, "stopped", "")
	return nil
}

func (m *Manager) Start(ctx context.Context, slug string) error {
	if err := m.providerForSlug(slug).Start(ctx, slug); err != nil {
		m.store.RecordEvent(slug, "start_error", err.Error())
		return err
	}
	if dep, ok, err := m.store.GetDeployment(slug); err == nil && ok {
		dep.Status = "running"
		_ = m.store.UpsertDeployment(dep)
	}
	m.store.RecordEvent(slug, "started", "")
	return nil
}

func (m *Manager) Restart(ctx context.Context, slug string) error {
	if err := m.providerForSlug(slug).Restart(ctx, slug); err != nil {
		m.store.RecordEvent(slug, "restart_error", err.Error())
		return err
	}
	if dep, ok, err := m.store.GetDeployment(slug); err == nil && ok {
		dep.Status = "running"
		_ = m.store.UpsertDeployment(dep)
	}
	m.store.RecordEvent(slug, "restarted", "")
	return nil
}

func (m *Manager) Remove(ctx context.Context, slug string) error {
	if err := m.providerForSlug(slug).Remove(ctx, slug); err != nil {
		m.store.RecordEvent(slug, "remove_error", err.Error())
		return err
	}
	if err := m.store.DeleteDeployment(slug); err != nil {
		return err
	}
	m.store.RecordEvent(slug, "removed", "")
	return nil
}

func (m *Manager) Logs(ctx context.Context, slug string, lines int) (string, error) {
	return m.providerForSlug(slug).Logs(ctx, slug, lines)
}

// providerListing pairs a runtime kind with the units its provider reported, so a
// caller (Refresh) can record each unit under the runtime that actually owns it.
type providerListing struct {
	kind  string
	infos []runtime.ContainerInfo
}

// collectListings lists every registered provider in sorted kind order. A provider
// that fails to list is non-fatal: its error is collected and returned, but the
// other providers' units are still gathered, so one backend being down (e.g. Docker
// offline) cannot blank a healthy one. listed reports whether at least one provider
// listed successfully, letting callers surface an error only when ALL providers
// failed — which, with a single provider registered, reproduces the previous
// single-runtime behavior exactly (Docker's list on success, Docker's error on
// failure). Sorted iteration keeps the union order deterministic.
func (m *Manager) collectListings(ctx context.Context) (listings []providerListing, listed bool, err error) {
	kinds := make([]string, 0, len(m.providers))
	for kind := range m.providers {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	var errs []error
	for _, kind := range kinds {
		infos, listErr := m.providers[kind].List(ctx)
		if listErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", kind, listErr))
			continue
		}
		listed = true
		listings = append(listings, providerListing{kind: kind, infos: infos})
	}
	if len(errs) > 0 {
		err = errors.Join(errs...)
	}
	return listings, listed, err
}

// List returns every managed unit across all registered providers as a single flat
// slice — the monitoring/health view used by the app's health sampler. An error is
// returned only when no provider listed successfully; a partial failure yields the
// healthy providers' units (the failing provider's error is non-fatal). With just
// Docker registered this returns Docker's list unchanged, or Docker's error.
func (m *Manager) List(ctx context.Context) ([]runtime.ContainerInfo, error) {
	listings, listed, err := m.collectListings(ctx)
	if !listed && err != nil {
		return nil, err
	}
	var union []runtime.ContainerInfo
	for _, listing := range listings {
		union = append(union, listing.infos...)
	}
	return union, nil
}

func (m *Manager) Refresh(ctx context.Context) ([]store.Deployment, error) {
	listings, listed, err := m.collectListings(ctx)
	if !listed && err != nil {
		return nil, err
	}

	total := 0
	active := make(map[string]bool)
	for _, listing := range listings {
		for _, info := range listing.infos {
			total++
			dep := store.Deployment{
				Slug:        info.Slug,
				ContainerID: info.ContainerID,
				Name:        info.Name,
				Image:       info.Image,
				Status:      info.Status,
				Runtime:     listing.kind,
				CPUPercent:  info.CPUPercent,
				MemoryMB:    info.MemoryMB,
			}
			if dep.Slug != "" {
				active[dep.Slug] = true
				_ = m.store.UpsertDeployment(dep)
			}
		}
	}
	// Only reconcile away stale records when at least one provider actually
	// returned containers. An empty (but error-free) result usually means a
	// different runtime/context is active, not that every managed container
	// vanished — deleting on that would wipe the dashboard and reset CreatedAt.
	if total > 0 {
		for _, dep := range m.store.ListDeployments() {
			if !active[dep.Slug] {
				_ = m.store.DeleteDeployment(dep.Slug)
				m.store.RecordEvent(dep.Slug, "missing_from_runtime", "removed stale deployment record")
			}
		}
	}
	return m.store.ListDeployments(), nil
}

func validateRequired(svc catalog.Service, credentials map[string]string) error {
	for _, item := range svc.Docker.Env {
		if item.Required && credentials[item.Key] == "" && item.Default == "" {
			label := item.Label
			if label == "" {
				label = item.Key
			}
			return fmt.Errorf("missing required field: %s", label)
		}
	}
	return nil
}
