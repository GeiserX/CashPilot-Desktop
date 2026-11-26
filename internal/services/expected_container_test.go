package services

import (
	"context"
	"errors"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// A checked deploy (DeployOptions.ExpectedContainer) replaces only the container
// the caller saw. The runtime compares it while it holds its replace lock, so the
// manager hands the expectation on, and refuses before anything is touched when
// the runtime cannot compare it at all.

// targetProvider is a fakeProvider that can name the container a deploy replaces.
type targetProvider struct {
	*fakeProvider
	target string
}

func (p *targetProvider) DeployTarget(context.Context, string) (string, error) { return p.target, nil }

func TestACheckedDeployHandsTheExpectedContainerToTheRuntime(t *testing.T) {
	fake := &targetProvider{fakeProvider: &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "cid", Status: "running"}}, target: "none"}
	m := NewManager(fake, newTestCatalog(t), newTestStore(t))
	expected := "none"
	if _, err := m.DeployWith(context.Background(), "example", map[string]string{"TOKEN": "abc"}, DeployOptions{ExpectedContainer: &expected}); err != nil {
		t.Fatalf("checked deploy: %v", err)
	}
	if len(fake.deploySpecs) != 1 || fake.deploySpecs[0].ExpectedContainer == nil || *fake.deploySpecs[0].ExpectedContainer != "none" {
		t.Fatalf("the runtime got %+v; want the expected container none", fake.deploySpecs)
	}
	got, err := m.DeployTarget(context.Background(), "example")
	if err != nil || got != "none" {
		t.Fatalf("DeployTarget = %q, %v; want none", got, err)
	}
}

func TestAnUncheckedDeployCarriesNoExpectation(t *testing.T) {
	fake := &targetProvider{fakeProvider: &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "cid", Status: "running"}}, target: "none"}
	m := NewManager(fake, newTestCatalog(t), newTestStore(t))
	if _, err := m.Deploy(context.Background(), "example", map[string]string{"TOKEN": "abc"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(fake.deploySpecs) != 1 || fake.deploySpecs[0].ExpectedContainer != nil {
		t.Fatalf("an ordinary deploy sent an expectation: %+v", fake.deploySpecs)
	}
}

func TestACheckedDeployOnARuntimeThatCannotCompareTouchesNothing(t *testing.T) {
	fake := &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "cid", Status: "running"}}
	m := NewManager(fake, newTestCatalog(t), newTestStore(t))
	expected := "none"
	_, err := m.DeployWith(context.Background(), "example", map[string]string{"TOKEN": "abc"}, DeployOptions{ExpectedContainer: &expected})
	if !errors.Is(err, runtime.ErrNoDeployTarget) {
		t.Fatalf("err = %v; want ErrNoDeployTarget", err)
	}
	if fake.deployCalls != 0 {
		t.Fatalf("the runtime was asked to deploy %d times; want none", fake.deployCalls)
	}
	if _, err := m.DeployTarget(context.Background(), "example"); !errors.Is(err, runtime.ErrNoDeployTarget) {
		t.Fatalf("DeployTarget err = %v; want ErrNoDeployTarget", err)
	}
}

// copyTargetFake runs copies 2..N and names the first copy's container.
type copyTargetFake struct {
	*copyFake
	target string
}

func (f *copyTargetFake) DeployTarget(context.Context, string) (string, error) { return f.target, nil }

// A redeploy also redeploys copies 2..N, and removes the copies above the count.
// A checked deploy names one container, the first copy's, so while other copies
// exist it would replace containers nobody approved: it refuses before anything is
// touched, and so does the lookup.
func TestACheckedDeployRefusesWhileOtherCopiesExist(t *testing.T) {
	ctx := context.Background()
	fake := &copyTargetFake{copyFake: newCopyFake(), target: "none"}
	m := NewManager(fake, newScalingCatalog(t), newTestStore(t))
	if _, err := m.DeployWith(ctx, "scalable", nil, DeployOptions{Instances: intPtr(2)}); err != nil {
		t.Fatalf("two copies: %v", err)
	}
	fake.target = "c1"
	before := fake.deployCalls
	if got, err := m.DeployTarget(ctx, "scalable"); !errors.Is(err, runtime.ErrNoDeployTarget) {
		t.Fatalf("lookup with copy 2 running = %q, %v; want ErrNoDeployTarget", got, err)
	}
	for _, expected := range []string{"c1", "none"} {
		_, err := m.DeployWith(ctx, "scalable", nil, DeployOptions{ExpectedContainer: &expected})
		if !errors.Is(err, runtime.ErrNoDeployTarget) || fake.deployCalls != before || len(fake.removed) != 0 || !fake.running[2] {
			t.Fatalf("checked deploy expecting %s with copy 2 running: err %v, %d deploys, removed %v; want a refusal that touches nothing",
				expected, err, fake.deployCalls-before, fake.removed)
		}
	}

	// Once the extra copy is gone (the owner lowered the count here), the same
	// request replaces the one named container.
	if _, err := m.DeployWith(ctx, "scalable", nil, DeployOptions{Instances: intPtr(1)}); err != nil {
		t.Fatalf("back to one copy: %v", err)
	}
	expected := "c1"
	if got, err := m.DeployTarget(ctx, "scalable"); err != nil || got != "c1" {
		t.Fatalf("lookup with one copy = %q, %v; want c1", got, err)
	}
	if _, err := m.DeployWith(ctx, "scalable", nil, DeployOptions{ExpectedContainer: &expected}); err != nil {
		t.Fatalf("checked deploy with one copy: %v", err)
	}
}
