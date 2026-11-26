package services

import (
	"context"
	"errors"
	"strings"
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
	target    string
	targetErr error
}

func (p *targetProvider) DeployTarget(context.Context, string) (string, error) {
	return p.target, p.targetErr
}

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
	if got, err := m.DeployTarget(ctx, "scalable"); !errors.Is(err, runtime.ErrNoDeployTarget) || !strings.Contains(err.Error(), "has 1 more copy on") {
		t.Fatalf("lookup with copy 2 running = %q, %v; want ErrNoDeployTarget naming 1 more copy", got, err)
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

// The manager compares the expectation itself, under its deploy lock and before
// the runtime pulls anything, so a stale yes is refused at once. The runtime here
// compares nothing, so only the manager's own check can refuse.
func TestACheckedDeployForAnotherContainerIsRefusedBeforeTheRuntime(t *testing.T) {
	fake := &targetProvider{fakeProvider: &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "c2", Status: "running"}}, target: "c1"}
	m := NewManager(fake, newTestCatalog(t), newTestStore(t))
	for _, expected := range []string{"c0", "none"} {
		_, err := m.DeployWith(context.Background(), "example", map[string]string{"TOKEN": "abc"}, DeployOptions{ExpectedContainer: &expected})
		var changed *runtime.DeployTargetChanged
		if !errors.As(err, &changed) || changed.Actual != "c1" {
			t.Fatalf("checked deploy expecting %s over c1: err = %v; want DeployTargetChanged naming c1", expected, err)
		}
	}
	if fake.deployCalls != 0 {
		t.Fatalf("the runtime was asked to deploy %d times; want none", fake.deployCalls)
	}
}

// A lookup that fails refuses the deploy: the manager cannot tell which container
// the deploy would replace, so it replaces none.
func TestACheckedDeployWhoseLookupFailsTouchesNothing(t *testing.T) {
	ctx := context.Background()
	lookup := errors.New("docker is not answering")
	fake := &targetProvider{fakeProvider: &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "c1", Status: "running"}}, targetErr: lookup}
	m := NewManager(fake, newTestCatalog(t), newTestStore(t))
	expected := "none"
	if _, err := m.DeployWith(ctx, "example", map[string]string{"TOKEN": "abc"}, DeployOptions{ExpectedContainer: &expected}); !errors.Is(err, lookup) {
		t.Fatalf("err = %v; want the lookup's own error", err)
	}
	if fake.deployCalls != 0 {
		t.Fatalf("the runtime was asked to deploy %d times; want none", fake.deployCalls)
	}

	// The same holds when the runtime cannot list the other copies.
	copies := &copyTargetFake{copyFake: newCopyFake(), target: "none"}
	copies.copiesErr = errors.New("cannot list copies")
	m = NewManager(copies, newScalingCatalog(t), newTestStore(t))
	if _, err := m.DeployWith(ctx, "scalable", nil, DeployOptions{ExpectedContainer: &expected}); !errors.Is(err, copies.copiesErr) {
		t.Fatalf("checked deploy while copies cannot be listed: err = %v; want the listing error", err)
	}
	if _, err := m.DeployTarget(ctx, "scalable"); !errors.Is(err, copies.copiesErr) {
		t.Fatalf("lookup while copies cannot be listed: err = %v; want the listing error", err)
	}
	if copies.deployCalls != 0 {
		t.Fatalf("the runtime was asked to deploy %d times; want none", copies.deployCalls)
	}
}

// A redeploy starts every copy the stored count names, not just those that exist.
// When copy 2 failed to start (the runtime removes what it created) or was removed
// outside CashPilot, the count still says 2: a checked deploy approved for the first
// copy would start a second container nobody approved. It refuses, and so does the
// lookup, until the owner lowers the count.
func TestACheckedDeployRefusesWhileMoreCopiesAreRecordedThanRun(t *testing.T) {
	ctx := context.Background()
	fake := &copyTargetFake{copyFake: newCopyFake(), target: "none"}
	m := NewManager(fake, newScalingCatalog(t), newTestStore(t))
	fake.copyDeployErr = errors.New("copy start failed")
	if _, err := m.DeployWith(ctx, "scalable", nil, DeployOptions{Instances: intPtr(2)}); err == nil {
		t.Fatal("want copy 2 to fail")
	}
	fake.copyDeployErr = nil
	fake.target = "c1"
	if got, err := m.DeployTarget(ctx, "scalable"); !errors.Is(err, runtime.ErrNoDeployTarget) || !strings.Contains(err.Error(), "is set to run 2 copies") {
		t.Fatalf("lookup with 2 copies recorded and 1 running = %q, %v; want ErrNoDeployTarget naming the recorded count", got, err)
	}
	before := fake.deployCalls
	expected := "c1"
	_, err := m.DeployWith(ctx, "scalable", nil, DeployOptions{ExpectedContainer: &expected})
	var copies *OtherCopiesError
	if !errors.As(err, &copies) || !errors.Is(err, runtime.ErrNoDeployTarget) || fake.deployCalls != before || fake.running[2] {
		t.Fatalf("checked deploy with 2 copies recorded: err %v, %d deploys, copy 2 running %v; want a refusal that touches nothing",
			err, fake.deployCalls-before, fake.running[2])
	}

	// Positive control: once the owner lowers the count to one, the same checked
	// deploy replaces the one named container.
	if _, err := m.DeployWith(ctx, "scalable", nil, DeployOptions{Instances: intPtr(1)}); err != nil {
		t.Fatalf("back to one copy: %v", err)
	}
	if got, err := m.DeployTarget(ctx, "scalable"); err != nil || got != "c1" {
		t.Fatalf("lookup with one copy = %q, %v; want c1", got, err)
	}
	before = fake.deployCalls
	if _, err := m.DeployWith(ctx, "scalable", nil, DeployOptions{ExpectedContainer: &expected}); err != nil || fake.deployCalls != before+1 || fake.running[2] {
		t.Fatalf("checked deploy with one copy: err %v, %d deploys, copy 2 running %v; want one deploy of copy 1", err, fake.deployCalls-before, fake.running[2])
	}
}

// A checked lookup that cannot read the deployment record cannot know how many
// copies are recorded, so it refuses instead of naming the first copy.
func TestACheckedLookupFailsClosedWhenTheRecordCannotBeRead(t *testing.T) {
	fake := &targetProvider{fakeProvider: &fakeProvider{}, target: "c1"}
	st := newTestStore(t)
	m := NewManager(fake, newTestCatalog(t), st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := m.DeployTarget(context.Background(), "example"); err == nil {
		t.Fatalf("DeployTarget with an unreadable store = %q, nil; want the read error", got)
	}
}
