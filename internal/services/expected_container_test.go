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
