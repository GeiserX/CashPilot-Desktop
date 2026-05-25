package services

import (
	"context"
	"fmt"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

type Manager struct {
	runtime runtime.Provider
	catalog *catalog.Catalog
	store   *store.Store
}

func NewManager(provider runtime.Provider, cat *catalog.Catalog, st *store.Store) *Manager {
	return &Manager{runtime: provider, catalog: cat, store: st}
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

	m.store.RecordEvent(slug, "pull_start", svc.Docker.Image)
	info, err := m.runtime.Deploy(ctx, runtime.DeploySpec{Slug: slug, Service: svc, Env: credentials}, func(message string) {
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
		Runtime:     "existing-docker",
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
	if err := m.runtime.Stop(ctx, slug); err != nil {
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

func (m *Manager) Restart(ctx context.Context, slug string) error {
	if err := m.runtime.Restart(ctx, slug); err != nil {
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
	if err := m.runtime.Remove(ctx, slug); err != nil {
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
	return m.runtime.Logs(ctx, slug, lines)
}

func (m *Manager) Refresh(ctx context.Context) ([]store.Deployment, error) {
	containers, err := m.runtime.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, info := range containers {
		dep := store.Deployment{
			Slug:        info.Slug,
			ContainerID: info.ContainerID,
			Name:        info.Name,
			Image:       info.Image,
			Status:      info.Status,
			Runtime:     "existing-docker",
			CPUPercent:  info.CPUPercent,
			MemoryMB:    info.MemoryMB,
		}
		if dep.Slug != "" {
			_ = m.store.UpsertDeployment(dep)
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
