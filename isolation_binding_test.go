package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// deployCountingProvider runs nothing and counts deploys per slug.
type deployCountingProvider struct {
	stubProvider
	deployed []string
	fail     map[string]error
}

func (d *deployCountingProvider) Deploy(_ context.Context, spec runtime.DeploySpec, _ func(string)) (runtime.ContainerInfo, error) {
	if err := d.fail[spec.Slug]; err != nil {
		return runtime.ContainerInfo{}, err
	}
	d.deployed = append(d.deployed, spec.Slug)
	return runtime.ContainerInfo{Slug: spec.Slug, Name: "cashpilot-" + spec.Slug, Status: "running"}, nil
}

// fakeFence answers like the Docker provider's LAN fence.
type fakeFence struct {
	enableErr  error
	outside    []string
	reconciled int
}

func (f *fakeFence) FenceStatus() runtime.FenceStatus {
	return runtime.FenceStatus{Mode: runtime.FenceModeOn, State: "applied"}
}
func (f *fakeFence) ReconcileFence(context.Context) { f.reconciled++ }
func (f *fakeFence) EnableFence(context.Context) (runtime.FenceStatus, error) {
	return runtime.FenceStatus{Mode: runtime.FenceModeOn, Outside: f.outside}, f.enableErr
}

// THE RULE: turning isolation on moves every running earner the fence names, one
// failure does not stop the rest, and each failure is named. A fence that cannot be
// applied moves nothing.
func TestEnableIsolationRedeploysTheEarnersOutside(t *testing.T) {
	provider := &deployCountingProvider{}
	app := newRemovalTestApp(t, provider)
	fence := &fakeFence{outside: []string{"ghost", "keeper"}}
	app.fence = fence

	result, err := app.EnableIsolation()
	if err != nil {
		t.Fatalf("EnableIsolation: %v", err)
	}
	if !slices.Equal(provider.deployed, []string{"keeper"}) {
		t.Errorf("redeployed %v, want keeper only", provider.deployed)
	}
	if !slices.Equal(result.Moved, []string{"keeper"}) {
		t.Errorf("moved %v", result.Moved)
	}
	if result.Failed["ghost"] == "" {
		t.Errorf("a container that is not a known service was not named: %v", result.Failed)
	}
	if fence.reconciled == 0 {
		t.Error("the status was not refreshed after the moves")
	}

	refused := &deployCountingProvider{}
	app = newRemovalTestApp(t, refused)
	app.fence = &fakeFence{outside: []string{"keeper"}, enableErr: errors.New("helper refused")}
	if _, err := app.EnableIsolation(); err == nil {
		t.Fatal("EnableIsolation succeeded although the fence could not be applied")
	}
	if len(refused.deployed) != 0 {
		t.Errorf("earners were redeployed without a fence: %v", refused.deployed)
	}

	failing := &deployCountingProvider{fail: map[string]error{"keeper": errors.New("pull refused")}}
	app = newRemovalTestApp(t, failing)
	app.fence = &fakeFence{outside: []string{"keeper"}}
	result, err = app.EnableIsolation()
	if err != nil {
		t.Fatalf("EnableIsolation: %v", err)
	}
	if result.Failed["keeper"] == "" || len(result.Moved) != 0 {
		t.Errorf("a failed redeploy: moved %v, failed %v", result.Moved, result.Failed)
	}
}

func TestSaveSettingsIsolation(t *testing.T) {
	app := newRemovalTestApp(t, &deployCountingProvider{})
	for _, value := range []string{"off", "on", "auto"} {
		if _, err := app.SaveSettings(map[string]string{"isolation": value}); err != nil {
			t.Fatalf("SaveSettings(%q): %v", value, err)
		}
		want := value
		if value == "auto" {
			want = ""
		}
		if got := app.cfg.Config().Isolation; got != want {
			t.Errorf("isolation %q stored as %q", value, got)
		}
	}
	if _, err := app.SaveSettings(map[string]string{"isolation": "sometimes"}); err == nil {
		t.Error("an unknown isolation value was stored")
	}
}

// Saving off forgets that isolation was on in the same save, with no Docker involved,
// so going back to auto while Docker is unreachable cannot bring a stale "on" back.
// Saving auto or on leaves the memory alone.
func TestSaveSettingsOffClearsTheIsolationMemory(t *testing.T) {
	app := newRemovalTestApp(t, &deployCountingProvider{})
	for _, value := range []string{"auto", "on"} {
		if err := app.cfg.Update(func(c *config.AppConfig) { c.IsolationOn = true }); err != nil {
			t.Fatal(err)
		}
		if _, err := app.SaveSettings(map[string]string{"isolation": value}); err != nil {
			t.Fatalf("SaveSettings(%q): %v", value, err)
		}
		if !app.cfg.Config().IsolationOn {
			t.Errorf("saving %q cleared the memory", value)
		}
	}
	if _, err := app.SaveSettings(map[string]string{"isolation": "off"}); err != nil {
		t.Fatalf("SaveSettings(off): %v", err)
	}
	if app.cfg.Config().IsolationOn {
		t.Error("saving off kept the memory that isolation was on")
	}
}
