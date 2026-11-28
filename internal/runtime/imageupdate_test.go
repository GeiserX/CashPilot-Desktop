package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness"
	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// pinned is a catalog earner pinned by digest, as image-pins.yml leaves it.
func pinned() catalog.Service {
	svc := plainEarner()
	svc.Docker.Image = "example/orderly:latest@sha256:" + strings.Repeat("a", 64)
	return svc
}

const (
	pinnedRef = "example/orderly:latest@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tagRef    = "example/orderly:latest"
	oldBuild  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newBuild  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func fastSettle(t *testing.T) {
	t.Helper()
	poll, grace := settlePoll, settleGrace
	settlePoll, settleGrace = 10*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { settlePoll, settleGrace = poll, grace })
}

func TestFollowRef(t *testing.T) {
	for _, c := range []struct {
		image, want string
		custom, ok  bool
	}{
		{"example/a:lite@sha256:" + strings.Repeat("b", 64), "example/a:lite", false, true},
		{"example/a@sha256:" + strings.Repeat("b", 64), "example/a:latest", false, true},
		{"example/a@sha256:" + strings.Repeat("b", 64), "", true, false},
		{"example/a", "example/a:latest", false, true},
		{"localhost:5000/a:v1", "localhost:5000/a:v1", true, true},
	} {
		svc := catalog.Service{Custom: c.custom, Docker: catalog.DockerConfig{Image: c.image}}
		got, ok := FollowRef(svc, "amd64")
		if got != c.want || ok != c.ok {
			t.Errorf("FollowRef(%q, custom %v) = %q, %v; want %q, %v", c.image, c.custom, got, ok, c.want, c.ok)
		}
	}
}

// The pin is what a plain deploy runs; the check pulls the tag and compares builds.
func TestCheckImageComparesTheTagWithTheRunningBuild(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := pinned()
	docker.SetImage(pinnedRef, oldBuild, "sha256:d1")
	docker.SetImage(tagRef, oldBuild, "sha256:d1")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	check, err := p.CheckImage(context.Background(), svc.Slug, svc)
	if err != nil || !check.Floating || check.Ref != tagRef || check.LatestID != oldBuild || check.RunningID != oldBuild || check.Status != "running" {
		t.Fatalf("an unchanged tag: %+v, %v", check, err)
	}
	docker.SetImage(tagRef, newBuild, "sha256:d2")
	check, err = p.CheckImage(context.Background(), svc.Slug, svc)
	// The running build is inspected by its ID, and still names its repository's digest.
	if err != nil || check.LatestID != newBuild || check.LatestDigest != "sha256:d2" || check.RunningID != oldBuild || check.RunningDigest != "sha256:d1" {
		t.Fatalf("a moved tag: %+v, %v", check, err)
	}
	if _, err := p.CheckImage(context.Background(), "other", svc); !errors.Is(err, ErrNoContainer) {
		t.Fatalf("no container: %v", err)
	}
}

func updateSpec(svc catalog.Service, expect string) DeploySpec {
	return DeploySpec{Slug: svc.Slug, Service: svc, FollowTag: true, SettleSeconds: 1, ExpectImage: expect}
}

func TestAnUpdateThatPassesReplacesTheOldContainerAndItsImage(t *testing.T) {
	fastSettle(t)
	docker, p, _ := rollbackDaemon(t)
	svc := pinned()
	docker.SetImage(pinnedRef, oldBuild, "")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	before, _ := docker.Container(containerName(svc.Slug))
	docker.SetImage(tagRef, newBuild, "sha256:d2")
	info, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := docker.Container(containerName(svc.Slug))
	if after.ID == before.ID || after.Image != tagRef || after.ImageID != newBuild {
		t.Fatalf("the update did not run the tag's build: %+v", after)
	}
	if info.ImageUpdate == nil || info.ImageUpdate.OldImage != oldBuild || info.ImageUpdate.NewImage != newBuild || info.ImageUpdate.OldKept != "" {
		t.Fatalf("ImageUpdate = %+v", info.ImageUpdate)
	}
	if !slices.Equal(docker.RemovedImages(), []string{oldBuild}) {
		t.Fatalf("removed images %v; want the old build", docker.RemovedImages())
	}
	if names := managedNames(docker, svc.Slug); len(names) != 1 {
		t.Fatalf("containers left: %v", names)
	}
	// The volume the node keeps its identity in is the one it had.
	if !slices.Equal(after.VolumeNames(), before.VolumeNames()) {
		t.Fatalf("volumes %v; want %v", after.VolumeNames(), before.VolumeNames())
	}
}

func TestAnUpdateThatCrashesIsUndone(t *testing.T) {
	fastSettle(t)
	docker, p, _ := rollbackDaemon(t)
	svc := pinned()
	docker.SetImage(pinnedRef, oldBuild, "")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	before, _ := docker.Container(containerName(svc.Slug))
	docker.SetImage(tagRef, newBuild, "")
	docker.SetCrashing(newBuild, true)
	_, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	var undone *ImageUpdateUndone
	if !errors.As(err, &undone) {
		t.Fatalf("a crashing build: err %v; want ImageUpdateUndone", err)
	}
	if !strings.Contains(err.Error(), "restarted by Docker") {
		t.Errorf("the reason is lost: %v", err)
	}
	after, _ := docker.Container(containerName(svc.Slug))
	if after.ID != before.ID || after.State != "running" || after.ImageID != oldBuild {
		t.Fatalf("the old container is not back and running: %+v", after)
	}
	if names := managedNames(docker, svc.Slug); len(names) != 1 {
		t.Fatalf("containers left: %v", names)
	}
	if len(docker.RemovedImages()) != 0 {
		t.Fatalf("an undone update removed images: %v", docker.RemovedImages())
	}
}

func TestAnUnhealthyUpdateIsUndone(t *testing.T) {
	fastSettle(t)
	docker, p, _ := rollbackDaemon(t)
	svc := pinned()
	docker.SetImage(pinnedRef, oldBuild, "")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	docker.SetImage(tagRef, newBuild, "")
	name := containerName(svc.Slug)
	// The new container turns unhealthy on the first look after its start.
	docker.BeforeCall("POST /containers/"+name+"/start", func() {
		docker.BeforeCall("GET /containers/"+name+"/json", func() { docker.SetHealth(name, "unhealthy") })
	})
	_, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	var undone *ImageUpdateUndone
	if !errors.As(err, &undone) || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("an unhealthy build: %v", err)
	}
}

func TestATagThatMovedAfterTheCheckIsRefused(t *testing.T) {
	fastSettle(t)
	docker, p, _ := rollbackDaemon(t)
	svc := pinned()
	docker.SetImage(pinnedRef, oldBuild, "")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	before, _ := docker.Container(containerName(svc.Slug))
	docker.SetImage(tagRef, newBuild, "")
	_, err := p.Deploy(context.Background(), updateSpec(svc, "sha256:"+strings.Repeat("9", 64)), nil)
	if err == nil || !strings.Contains(err.Error(), "moved to another build") {
		t.Fatalf("a moved tag: %v", err)
	}
	var undone *ImageUpdateUndone
	if errors.As(err, &undone) {
		t.Fatal("a refused build must not read as undone: that would hold a build nobody tried")
	}
	after, _ := docker.Container(containerName(svc.Slug))
	if after.ID != before.ID || after.State != "running" {
		t.Fatalf("the running container was touched: %+v", after)
	}
}

func TestACustomImagePinnedByDigestAloneIsNotFollowed(t *testing.T) {
	_, p, _ := rollbackDaemon(t)
	svc := catalog.Service{Name: "Mine", Slug: "custom-mine", Custom: true, Docker: catalog.DockerConfig{Image: "example/mine@sha256:" + strings.Repeat("c", 64)}}
	_, err := p.Deploy(context.Background(), updateSpec(svc, ""), nil)
	if err == nil || !strings.Contains(err.Error(), "no tag to follow") {
		t.Fatalf("a digest-only custom image: %v", err)
	}
}

// Passing needs a look Docker answered: a new container it cannot describe at the
// end of the check period is undone, never taken as healthy.
func TestAnUpdateDockerCannotDescribeAtTheEndIsUndone(t *testing.T) {
	fastSettle(t)
	docker, p, _ := rollbackDaemon(t)
	svc := pinned()
	docker.SetImage(pinnedRef, oldBuild, "")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	before, _ := docker.Container(containerName(svc.Slug))
	docker.SetImage(tagRef, newBuild, "")
	name := containerName(svc.Slug)
	// From the first look after its start, Docker cannot describe the new container.
	docker.BeforeCall("POST /containers/"+name+"/start", func() {
		docker.BeforeCall("GET /containers/"+name+"/json", func() {
			if c, ok := docker.Container(name); ok && c.ImageID == newBuild {
				docker.FailInspect(c.ID)
			}
		})
	})
	_, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	docker.ClearFailures()
	var undone *ImageUpdateUndone
	if !errors.As(err, &undone) || !strings.Contains(err.Error(), "did not describe") {
		t.Fatalf("an unreadable new container: %v", err)
	}
	after, _ := docker.Container(containerName(svc.Slug))
	if after.ID != before.ID || after.State != "running" {
		t.Fatalf("the old container is not back: %+v", after)
	}
}

// withSignal is the pinned earner with one catalog failure signal.
func withSignal(unless string) catalog.Service {
	svc := pinned()
	svc.Docker.HealthSignals = []catalog.HealthSignal{{Pattern: "cannot be reached", Unless: unless, Means: "It cannot start."}}
	return svc
}

// deployPinned deploys svc from its pin and points the tag at newBuild.
func deployPinned(t *testing.T, svc catalog.Service) (*harness.Docker, *DockerProvider, harness.Container) {
	t.Helper()
	docker, p, _ := rollbackDaemon(t)
	docker.SetImage(pinnedRef, oldBuild, "")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	before, _ := docker.Container(containerName(svc.Slug))
	docker.SetImage(tagRef, newBuild, "")
	return docker, p, before
}

// A look that started before the period ended cannot pass it, even when Docker
// answers it after: the container can stop meanwhile, and the next look decides.
func TestOnlyALookThatStartsAfterThePeriodPassesIt(t *testing.T) {
	fastSettle(t)
	// Room for the second look: past the period, well inside the grace.
	settleGrace = time.Minute
	var skew atomic.Int64
	clock := settleNow
	settleNow = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	t.Cleanup(func() { settleNow = clock })
	var second atomic.Bool
	svc := pinned()
	docker, p, before := deployPinned(t, svc)
	name := containerName(svc.Slug)
	docker.BeforeCall("POST /containers/"+name+"/start", func() {
		// The first look: Docker takes past the end of the period to answer it.
		docker.BeforeCall("GET /containers/"+name+"/json", func() {
			skew.Store(int64(2 * time.Second))
			// The second look: the new container has stopped meanwhile.
			docker.BeforeCall("GET /containers/"+name+"/json", func() {
				second.Store(true)
				docker.SetState(name, "exited")
			})
		})
	})
	_, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	var undone *ImageUpdateUndone
	if !errors.As(err, &undone) || !second.Load() || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("a stop seen only after a late look: %v (second look %v); want the stop, undone", err, second.Load())
	}
	if after, _ := docker.Container(name); after.ID != before.ID || after.State != "running" {
		t.Fatalf("the old container is not back: %+v", after)
	}
}

// The whole log of the period is read: a failure line is not lost behind the
// first megabyte, and an unless line later in the period still counts.
func TestAFailureLineAnywhereInTheLogCounts(t *testing.T) {
	fastSettle(t)
	svc := withSignal("")
	docker, p, _ := deployPinned(t, svc)
	docker.SetLogs(strings.Repeat("an ordinary line of a busy earner\n", 60000) + "update server cannot be reached\n")
	_, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	var undone *ImageUpdateUndone
	if !errors.As(err, &undone) || !strings.Contains(err.Error(), "It cannot start") {
		t.Fatalf("a failure line past the first megabyte: %v", err)
	}
}

// A line longer than the scan keeps is judged in parts that overlap, so a
// failure signal cut by a part's end still counts.
func TestAFailureSignalAcrossALongLinesCutCounts(t *testing.T) {
	fastSettle(t)
	svc := withSignal("")
	docker, p, _ := deployPinned(t, svc)
	// The cut falls inside the signal: "canno" ends one part, "t be reached" starts the next.
	docker.SetLogs(strings.Repeat("x", settleLineBytes-5) + "cannot be reached\n")
	_, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	var undone *ImageUpdateUndone
	if !errors.As(err, &undone) || !strings.Contains(err.Error(), "It cannot start") {
		t.Fatalf("a failure signal across the cut of a long line: %v; want undone", err)
	}
}

func TestAnUnlessLineAnywhereInTheLogCancelsTheSignal(t *testing.T) {
	fastSettle(t)
	// The unless line first, the failure line last: a reader that kept only a
	// part of the log would see one without the other.
	log := "connected to server\n" + strings.Repeat("waiting\n", 1000) + "update server cannot be reached\n"
	failing := withSignal("")
	docker, p, _ := deployPinned(t, failing)
	docker.SetLogs(log)
	var undone *ImageUpdateUndone
	if _, err := p.Deploy(context.Background(), updateSpec(failing, newBuild), nil); !errors.As(err, &undone) {
		t.Fatalf("the same log with no unless line: %v; want undone", err)
	}
	svc := withSignal("connected to server")
	docker, p, _ = deployPinned(t, svc)
	docker.SetLogs(log)
	if _, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil); err != nil {
		t.Fatalf("a signal its unless line cancels: %v", err)
	}
}

// The log is judged after the period: an unless line written late in the period
// still cancels a failure line written early.
func TestAnUnlessLineWrittenLateInThePeriodStillCounts(t *testing.T) {
	fastSettle(t)
	svc := withSignal("connected to server")
	docker, p, _ := deployPinned(t, svc)
	name := containerName(svc.Slug)
	docker.SetLogs("update server cannot be reached\n")
	docker.BeforeCall("POST /containers/"+name+"/start", func() {
		docker.BeforeCall("GET /containers/"+name+"/json", func() {
			// After the first look, the node connects.
			docker.BeforeCall("GET /containers/"+name+"/json", func() {
				docker.SetLogs("update server cannot be reached\nconnected to server\n")
			})
		})
	})
	if _, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil); err != nil {
		t.Fatalf("a failure line cancelled later in the period: %v", err)
	}
}

// A service with signals needs its log: one Docker does not return is undone.
func TestALogThatCannotBeReadIsUndone(t *testing.T) {
	fastSettle(t)
	svc := withSignal("")
	docker, p, before := deployPinned(t, svc)
	docker.FailLogs(true)
	_, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	docker.FailLogs(false)
	var undone *ImageUpdateUndone
	if !errors.As(err, &undone) || !strings.Contains(err.Error(), "did not describe") {
		t.Fatalf("an unreadable log: %v", err)
	}
	if after, _ := docker.Container(containerName(svc.Slug)); after.ID != before.ID || after.State != "running" {
		t.Fatalf("the old container is not back: %+v", after)
	}
}

// Docker reads the tag again at create time. A tag another pull moved between the
// check of the image and the create is undone, and not held: nobody tried that build.
func TestATagMovedBeforeTheCreateIsUndoneAndNotHeld(t *testing.T) {
	fastSettle(t)
	svc := pinned()
	docker, p, before := deployPinned(t, svc)
	var moved atomic.Bool
	docker.OnImageInspect(func(ref string) {
		if ref == tagRef && moved.CompareAndSwap(false, true) {
			docker.SetImage(tagRef, "sha256:"+strings.Repeat("3", 64), "")
		}
	})
	_, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	if err == nil || !strings.Contains(err.Error(), "while the update created its container") {
		t.Fatalf("a tag moved before the create: %v", err)
	}
	var undone *ImageUpdateUndone
	if errors.As(err, &undone) {
		t.Fatal("a build nobody tried must not read as undone")
	}
	if after, _ := docker.Container(containerName(svc.Slug)); after.ID != before.ID || after.State != "running" {
		t.Fatalf("the old container is not back: %+v", after)
	}
	if names := managedNames(docker, svc.Slug); len(names) != 1 {
		t.Fatalf("containers left: %v", names)
	}
}

func TestRepoDigestNamesOnlyTheCheckedRepository(t *testing.T) {
	if got := repoDigest([]string{"mirror.example/other@sha256:c"}, tagRef); got != "" {
		t.Fatalf("another repository's digest: %q", got)
	}
	if got := repoDigest([]string{"mirror.example/other@sha256:c", "example/orderly@sha256:d"}, tagRef); got != "sha256:d" {
		t.Fatalf("the checked repository's digest: %q", got)
	}
}

// A look Docker takes and never answers is bounded: the period still ends, and
// a new container nobody could describe is undone.
func TestALookDockerNeverAnswersStillEndsThePeriod(t *testing.T) {
	fastSettle(t)
	look := settleLookTimeout
	settleLookTimeout = 50 * time.Millisecond
	t.Cleanup(func() { settleLookTimeout = look })
	svc := pinned()
	docker, p, before := deployPinned(t, svc)
	name := containerName(svc.Slug)
	until := time.Now().Add(1500 * time.Millisecond)
	var stall func()
	stall = func() {
		if time.Now().After(until) {
			return
		}
		docker.BeforeCall("GET /containers/"+name+"/json", stall)
		time.Sleep(4 * time.Second)
	}
	docker.BeforeCall("POST /containers/"+name+"/start", func() {
		docker.BeforeCall("GET /containers/"+name+"/json", stall)
	})
	began := time.Now()
	_, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	if took := time.Since(began); took > 3*time.Second {
		t.Fatalf("a stalled look held the update for %s", took)
	}
	var undone *ImageUpdateUndone
	if !errors.As(err, &undone) || !strings.Contains(err.Error(), "did not describe") {
		t.Fatalf("a daemon that never answers: %v", err)
	}
	if after, _ := docker.Container(name); after.ID != before.ID || after.State != "running" {
		t.Fatalf("the old container is not back: %+v", after)
	}
}

// The delete of the old image is bounded too. When the daemon does not answer in
// time, nobody knows whether the image went: the passed update stands, and its
// row says the old image may still be on disk.
func TestAnOldImageDeleteDockerNeverAnswersIsBounded(t *testing.T) {
	fastSettle(t)
	remove := imageRemoveTimeout
	imageRemoveTimeout = 50 * time.Millisecond
	t.Cleanup(func() { imageRemoveTimeout = remove })
	svc := pinned()
	docker, p, _ := deployPinned(t, svc)
	docker.BeforeCall("DELETE /images/"+oldBuild, func() { time.Sleep(3 * time.Second) })
	began := time.Now()
	info, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	if took := time.Since(began); took > 2500*time.Millisecond {
		t.Fatalf("a stalled image delete held the update for %s", took)
	}
	if err != nil || info.ImageUpdate == nil || info.ImageUpdate.OldKept == "" {
		t.Fatalf("a stalled image delete: %v, %+v; want a passed update that keeps the old image", err, info.ImageUpdate)
	}
}

// A stop Docker took after the manager's check keeps the container's ID. The
// update still refuses it: replacing it would start the app.
func TestAnUpdateOfACopyThatStoppedAfterTheCheckIsRefused(t *testing.T) {
	fastSettle(t)
	svc := pinned()
	docker, p, before := deployPinned(t, svc)
	name := containerName(svc.Slug)
	docker.SetState(name, "exited")
	_, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("an update of a stopped copy: %v", err)
	}
	var undone *ImageUpdateUndone
	if errors.As(err, &undone) {
		t.Fatal("a refused update must not read as undone")
	}
	if after, _ := docker.Container(name); after.ID != before.ID || after.State != "exited" {
		t.Fatalf("the stopped copy was touched: %+v", after)
	}
}
