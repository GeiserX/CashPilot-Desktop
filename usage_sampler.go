package main

import (
	"context"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/GeiserX/CashPilot-Desktop/internal/usage"
)

// usageSampleInterval is how often the usage graphs get a reading of every unit.
// Each reading takes about a second of Docker stats per container (in parallel),
// so five minutes keeps a laptop quiet and still gives twelve readings an hour,
// well inside usage.MaxBaselineAge, so the traffic between two readings counts.
const usageSampleInterval = 5 * time.Minute

// startUsageSampler starts the loop that records CPU, memory and traffic per
// container for the usage graphs. It runs on its own timer, not inside the
// earnings collection, because that one runs hourly by default and an hourly
// graph needs more than one reading an hour.
func (a *App) startUsageSampler(ctx context.Context) {
	if ctx == nil || a.store == nil || a.services == nil {
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	a.usageStop = func() {
		cancel()
		<-done
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(usageSampleInterval)
		defer ticker.Stop()
		for {
			a.sampleUsage(loopCtx, a.services.List)
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// stopUsageSampler stops the loop and waits for it, so Shutdown never closes the
// store under a reading being written.
func (a *App) stopUsageSampler() {
	if a.usageStop != nil {
		a.usageStop()
		a.usageStop = nil
	}
}

// sampleUsage takes one reading of every unit and folds it into this hour's rows.
// A runtime that cannot be listed (Docker not running) records nothing: that hour
// is a gap in the graphs, never a zero.
func (a *App) sampleUsage(ctx context.Context, list func(context.Context) ([]runtime.ContainerInfo, error)) {
	infos, err := list(ctx)
	if err != nil || ctx.Err() != nil {
		return
	}
	now := time.Now()
	if err := a.store.RecordUsage(func(b map[string]store.UsageBaseline) ([]store.UsageHour, map[string]store.UsageBaseline) {
		return usage.Fold(infos, b, now)
	}); err != nil {
		a.emitError("usage", err)
		return
	}
	if err := a.store.PurgeUsage(now); err != nil {
		a.emitError("usage", err)
	}
}

// GetUsage is the usage graphs' data: the window of days (7 draws hourly points,
// 30 six-hourly) for one container by name, or "" for all of them.
func (a *App) GetUsage(days int, container string) (usage.View, error) {
	if err := a.ready(); err != nil {
		return usage.View{}, err
	}
	now := time.Now()
	rows, err := a.store.UsageHours(usage.Since(days, now))
	if err != nil {
		return usage.View{}, err
	}
	view := usage.Build(rows, days, container, now)
	for i, c := range view.Containers {
		view.Containers[i].Service = c.Slug
		if svc, ok := a.catalog.Get(c.Slug); ok && svc.Name != "" {
			view.Containers[i].Service = svc.Name
		}
	}
	return view, nil
}
