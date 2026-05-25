package main

import (
	"context"
	"fmt"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx        context.Context
	cfg        *config.Manager
	catalog    *catalog.Catalog
	store      *store.Store
	runtime    runtime.Provider
	services   *services.Manager
	collectors *collectors.Registry
}

func NewApp() *App {
	return &App{}
}

func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx

	cfg, err := config.NewManager()
	if err != nil {
		a.emitError("config", err)
		return
	}
	a.cfg = cfg

	st, err := store.Open(cfg.DataDir())
	if err != nil {
		a.emitError("store", err)
		return
	}
	a.store = st

	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		a.emitError("catalog", err)
		return
	}
	a.catalog = cat

	a.runtime = runtime.NewDockerProvider()
	a.services = services.NewManager(a.runtime, a.catalog, a.store)
	a.collectors = collectors.NewRegistry(a.store)
}

func (a *App) Shutdown(_ context.Context) {
	if a.store != nil {
		_ = a.store.Close()
	}
}

type AppState struct {
	Config      config.AppConfig       `json:"config"`
	Runtime     runtime.Status         `json:"runtime"`
	Services    []catalog.Service      `json:"services"`
	Deployments []store.Deployment     `json:"deployments"`
	Earnings    []store.EarningsRecord `json:"earnings"`
	Guides      []runtime.InstallGuide `json:"guides"`
}

func (a *App) GetAppState() (AppState, error) {
	if err := a.ready(); err != nil {
		return AppState{}, err
	}
	runtimeStatus := a.runtime.Status(a.ctx)
	return AppState{
		Config:      a.cfg.Config(),
		Runtime:     runtimeStatus,
		Services:    a.catalog.ListVisible(),
		Deployments: a.store.ListDeployments(),
		Earnings:    a.store.ListLatestEarnings(),
		Guides:      runtime.InstallGuides(),
	}, nil
}

func (a *App) CompleteOnboarding() error {
	if err := a.ready(); err != nil {
		return err
	}
	cfg := a.cfg.Config()
	cfg.FirstRunComplete = true
	return a.cfg.Save(cfg)
}

func (a *App) CheckRuntime() (runtime.Status, error) {
	if err := a.ready(); err != nil {
		return runtime.Status{}, err
	}
	return a.runtime.Status(a.ctx), nil
}

func (a *App) GetRuntimeGuides() []runtime.InstallGuide {
	return runtime.InstallGuides()
}

func (a *App) ListServices() ([]catalog.Service, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.catalog.ListVisible(), nil
}

func (a *App) GetService(slug string) (catalog.Service, error) {
	if err := a.ready(); err != nil {
		return catalog.Service{}, err
	}
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return catalog.Service{}, fmt.Errorf("unknown service: %s", slug)
	}
	return svc, nil
}

func (a *App) SaveCredentials(slug string, values map[string]string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.store.SaveCredentials(slug, values)
}

func (a *App) GetCredentials(slug string) (map[string]string, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.store.GetCredentials(slug)
}

func (a *App) DeployService(slug string, values map[string]string) (store.Deployment, error) {
	if err := a.ready(); err != nil {
		return store.Deployment{}, err
	}
	if len(values) > 0 {
		if err := a.store.SaveCredentials(slug, values); err != nil {
			return store.Deployment{}, err
		}
	}
	creds, err := a.store.GetCredentials(slug)
	if err != nil {
		return store.Deployment{}, err
	}
	deployment, err := a.services.Deploy(a.ctx, slug, creds)
	if err != nil {
		a.emitError("deploy", err)
		return store.Deployment{}, err
	}
	wailsruntime.EventsEmit(a.ctx, "deployment:changed", deployment)
	return deployment, nil
}

func (a *App) StopService(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.Stop(a.ctx, slug); err != nil {
		a.emitError("stop", err)
		return err
	}
	wailsruntime.EventsEmit(a.ctx, "deployment:changed", slug)
	return nil
}

func (a *App) RestartService(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.Restart(a.ctx, slug); err != nil {
		a.emitError("restart", err)
		return err
	}
	wailsruntime.EventsEmit(a.ctx, "deployment:changed", slug)
	return nil
}

func (a *App) RemoveService(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.Remove(a.ctx, slug); err != nil {
		a.emitError("remove", err)
		return err
	}
	wailsruntime.EventsEmit(a.ctx, "deployment:changed", slug)
	return nil
}

func (a *App) GetLogs(slug string, lines int) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	return a.services.Logs(a.ctx, slug, lines)
}

func (a *App) RefreshDeployments() ([]store.Deployment, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.services.Refresh(a.ctx)
}

func (a *App) CollectService(slug string) (store.EarningsRecord, error) {
	if err := a.ready(); err != nil {
		return store.EarningsRecord{}, err
	}
	creds, err := a.store.GetCredentials(slug)
	if err != nil {
		return store.EarningsRecord{}, err
	}
	record, err := a.collectors.Collect(a.ctx, slug, creds)
	if err != nil {
		a.emitError("collector", err)
		return store.EarningsRecord{}, err
	}
	wailsruntime.EventsEmit(a.ctx, "earnings:changed", record)
	return record, nil
}

func (a *App) ManagedRuntimePlan() runtime.ManagedRuntimePlan {
	return runtime.ManagedRuntimeRoadmap()
}

func (a *App) ready() error {
	if a.cfg == nil || a.catalog == nil || a.store == nil || a.runtime == nil || a.services == nil {
		return fmt.Errorf("app is still starting")
	}
	return nil
}

func (a *App) emitError(scope string, err error) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "app:error", map[string]string{
			"scope": scope,
			"error": err.Error(),
		})
	}
}
