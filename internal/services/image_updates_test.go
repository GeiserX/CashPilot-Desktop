package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// The manager's part of an app image update (internal/imageupdates): it asks the
// runtime for the tag's newest build, reads the folders a custom app keeps outside
// a redeploy, and runs the update's deploy only while the app still runs and the
// owner has not stopped it since the check began.

// shapesProvider is a fakeProvider that can say how its containers were created.
type shapesProvider struct {
	*fakeProvider
	shapes    []runtime.ContainerShape
	shapesErr error
}

func (p *shapesProvider) ContainerShapes(context.Context) ([]runtime.ContainerShape, error) {
	return p.shapes, p.shapesErr
}

// checkerProvider is a fakeProvider that can check a service's tag.
type checkerProvider struct {
	*fakeProvider
	check    runtime.ImageCheck
	checkErr error
	checked  []string
}

func (p *checkerProvider) CheckImage(_ context.Context, slug string, _ catalog.Service) (runtime.ImageCheck, error) {
	p.checked = append(p.checked, slug)
	return p.check, p.checkErr
}

func TestLooseMountsFailsWhenTheRuntimeCannotBeRead(t *testing.T) {
	m := NewManager(&fakeProvider{}, newTestCatalog(t), newTestStore(t))
	if got, err := m.LooseMounts(context.Background()); err == nil || got != nil {
		t.Fatalf("a runtime with no shapes gave %v, %v; want an error", got, err)
	}
	broken := &shapesProvider{fakeProvider: &fakeProvider{}, shapesErr: errors.New("daemon gone")}
	m = NewManager(broken, newTestCatalog(t), newTestStore(t))
	if got, err := m.LooseMounts(context.Background()); err == nil || got != nil {
		t.Fatalf("a failed read gave %v, %v; want the error", got, err)
	}
}

func TestLooseMountsHasAKeyForEveryContainerItRead(t *testing.T) {
	p := &shapesProvider{fakeProvider: &fakeProvider{}, shapes: []runtime.ContainerShape{
		{ID: "c-example", Slug: "example", Mounts: []string{"/cache"}},
		{ID: "c-unknown", Slug: "not-in-catalog", Mounts: []string{"/x"}},
	}}
	m := NewManager(p, newTestCatalog(t), newTestStore(t))
	got, err := m.LooseMounts(context.Background())
	if err != nil {
		t.Fatalf("LooseMounts: %v", err)
	}
	folders, read := got["c-example"]
	if !read || len(folders) != 0 {
		t.Fatalf("a catalog app = %v (read %v); want a key with no folders", folders, read)
	}
	if _, read := got["c-unknown"]; read {
		t.Fatalf("an app the catalog does not know got a key: %v", got)
	}
}

func TestCheckImageAsksTheRuntimeThatRunsTheApp(t *testing.T) {
	want := runtime.ImageCheck{Ref: "example/image:1.0.0", Floating: true, LatestID: "sha256:new", RunningID: "sha256:old"}
	p := &checkerProvider{fakeProvider: &fakeProvider{}, check: want}
	m := NewManager(p, newTestCatalog(t), newTestStore(t))
	got, ok, err := m.CheckImage(context.Background(), "example")
	if err != nil || !ok || got != want {
		t.Fatalf("CheckImage = %+v, %v, %v; want %+v from the runtime", got, ok, err, want)
	}
	if len(p.checked) != 1 || p.checked[0] != "example" {
		t.Fatalf("the runtime checked %v; want example once", p.checked)
	}

	p.checkErr = errors.New("registry down")
	if _, ok, err := m.CheckImage(context.Background(), "example"); err == nil || ok {
		t.Fatalf("a failed pull gave ok=%v err=%v; want the error and not ok", ok, err)
	}
}

func TestCheckImageLeavesOutWhatHasNoImageToFollow(t *testing.T) {
	m := NewManager(&fakeProvider{}, newTestCatalog(t), newTestStore(t))
	if _, ok, err := m.CheckImage(context.Background(), "example"); ok || err != nil {
		t.Fatalf("a runtime that cannot check gave ok=%v err=%v; want neither", ok, err)
	}
	if _, ok, err := m.CheckImage(context.Background(), "no-such-app"); ok || err == nil {
		t.Fatalf("an unknown app gave ok=%v err=%v; want an error", ok, err)
	}
}

func updateOptions(checkedAt time.Time, got **runtime.ImageUpdateResult) DeployOptions {
	return DeployOptions{
		FollowTag: true, SettleSeconds: 120, ExpectImage: "sha256:new", CheckedAt: checkedAt,
		ImageUpdate: func(r *runtime.ImageUpdateResult) { *got = r },
	}
}

func TestAnUpdateDeployHandsTheCheckedBuildToTheRuntime(t *testing.T) {
	result := &runtime.ImageUpdateResult{OldImage: "sha256:old", NewImage: "sha256:new"}
	fake := &fakeProvider{
		listResult:   []runtime.ContainerInfo{{Slug: "example", ContainerID: "c1", Status: "running"}},
		deployResult: runtime.ContainerInfo{Slug: "example", ContainerID: "c2", Status: "running", ImageUpdate: result},
	}
	m := NewManager(fake, newTestCatalog(t), newTestStore(t))
	var got *runtime.ImageUpdateResult
	if _, err := m.DeployWith(context.Background(), "example", map[string]string{"TOKEN": "abc"}, updateOptions(time.Now(), &got)); err != nil {
		t.Fatalf("update deploy: %v", err)
	}
	if len(fake.deploySpecs) != 1 {
		t.Fatalf("the runtime got %d deploys; want 1", len(fake.deploySpecs))
	}
	spec := fake.deploySpecs[0]
	if !spec.FollowTag || spec.SettleSeconds != 120 || spec.ExpectImage != "sha256:new" {
		t.Fatalf("the runtime got %+v; want the tag followed, a 120 s check and the checked build", spec)
	}
	if got != result {
		t.Fatalf("the caller got %+v; want what the runtime did with the old image", got)
	}
}

func TestAnUpdateDeployLeavesAnAppTheOwnerStopped(t *testing.T) {
	fake := &fakeProvider{listResult: []runtime.ContainerInfo{{Slug: "example", ContainerID: "c1", Status: "running"}}}
	m := NewManager(fake, newTestCatalog(t), newTestStore(t))
	checkedAt := time.Now()
	if err := m.Stop(context.Background(), "example"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	var got *runtime.ImageUpdateResult
	if _, err := m.DeployWith(context.Background(), "example", map[string]string{"TOKEN": "abc"}, updateOptions(checkedAt, &got)); err == nil {
		t.Fatal("an update deploy ran after the owner stopped the app")
	}
	if fake.deployCalls != 0 {
		t.Fatalf("the runtime was asked to deploy %d times; want none", fake.deployCalls)
	}

	// Control: a Stop before the check began does not hold a later update back.
	if _, err := m.DeployWith(context.Background(), "example", map[string]string{"TOKEN": "abc"}, updateOptions(time.Now().Add(time.Second), &got)); err != nil {
		t.Fatalf("an update checked after the Stop was refused: %v", err)
	}
	if fake.deployCalls != 1 {
		t.Fatalf("the runtime was asked to deploy %d times; want 1", fake.deployCalls)
	}
}

func TestAnUpdateDeployLeavesAnAppThatIsNotRunning(t *testing.T) {
	fake := &fakeProvider{listResult: []runtime.ContainerInfo{{Slug: "example", ContainerID: "c1", Status: "exited"}}}
	m := NewManager(fake, newTestCatalog(t), newTestStore(t))
	var got *runtime.ImageUpdateResult
	if _, err := m.DeployWith(context.Background(), "example", map[string]string{"TOKEN": "abc"}, updateOptions(time.Now(), &got)); err == nil {
		t.Fatal("an update deploy ran on an app that had stopped by itself")
	}
	if fake.deployCalls != 0 {
		t.Fatalf("the runtime was asked to deploy %d times; want none", fake.deployCalls)
	}
}

func TestAnUpdateDeployNamesARuntimeThatCouldNotBeRead(t *testing.T) {
	fake := &fakeProvider{listErr: errors.New("daemon gone")}
	m := NewManager(fake, newTestCatalog(t), newTestStore(t))
	var got *runtime.ImageUpdateResult
	_, err := m.DeployWith(context.Background(), "example", map[string]string{"TOKEN": "abc"}, updateOptions(time.Now(), &got))
	if err == nil || !strings.Contains(err.Error(), "daemon gone") || strings.Contains(err.Error(), "was stopped") {
		t.Fatalf("err = %v; want the runtime's reason, not a stop", err)
	}
	if fake.deployCalls != 0 {
		t.Fatalf("the runtime was asked to deploy %d times; want none", fake.deployCalls)
	}
}
