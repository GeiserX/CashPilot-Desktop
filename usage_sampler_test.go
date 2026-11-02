package main

import (
	"context"
	"errors"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// Two readings of one container through the sampler reach GetUsage as one point
// of the current hour, named by its catalog service, and a failed list records
// nothing (a gap, not a zero).
func TestSampleUsageFeedsGetUsage(t *testing.T) {
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatal(err)
	}
	provider := runtime.NewDockerProvider()
	app := &App{cfg: cfg, store: st, catalog: cat, runtime: provider, services: services.NewManager(provider, cat, st), ctx: context.Background()}

	rx, tx := uint64(1000), uint64(100)
	list := func(context.Context) ([]runtime.ContainerInfo, error) {
		return []runtime.ContainerInfo{{
			Slug: "honeygain", Name: "cashpilot-honeygain", ContainerID: "c1", Status: "running",
			CPUPercent: 12, MemoryMB: 64, CPUKnown: true, MemoryKnown: true, NetRxBytes: &rx, NetTxBytes: &tx,
		}}, nil
	}
	app.sampleUsage(context.Background(), list)
	app.sampleUsage(context.Background(), func(context.Context) ([]runtime.ContainerInfo, error) {
		return nil, errors.New("docker is not running")
	})

	view, err := app.GetUsage(7, "")
	if err != nil {
		t.Fatal(err)
	}
	last := len(view.Hours) - 1
	if view.CPUPercent[last] == nil || *view.CPUPercent[last] != 12 || *view.MemoryMB[last] != 64 {
		t.Fatalf("latest point = cpu %v mem %v, want 12 / 64", view.CPUPercent[last], view.MemoryMB[last])
	}
	if view.CPUPercent[last-1] != nil {
		t.Fatalf("the hour before any reading = %v, want null", *view.CPUPercent[last-1])
	}
	if len(view.Containers) != 1 || view.Containers[0].Service != "Honeygain" {
		t.Fatalf("containers = %+v, want one named Honeygain", view.Containers)
	}
	rows, _ := st.UsageHours(0)
	if len(rows) != 1 || rows[0].Beats != 1 {
		t.Fatalf("rows = %+v, want one reading (the failed list records nothing)", rows)
	}
}
