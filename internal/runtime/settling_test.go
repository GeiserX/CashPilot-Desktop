package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/client"
)

// leaveMidCheck builds what an image update leaves when the app stops in its check
// period: the old container stopped and set aside, the replacement running under
// its name. It returns the old container's ID and the replacement's.
func leaveMidCheck(t *testing.T, p *DockerProvider, node string) (string, string) {
	return leaveMarked(t, node, true)
}

// leaveMarked is leaveMidCheck with the replacement started or only made.
func leaveMarked(t *testing.T, node string, start bool) (string, string) {
	t.Helper()
	ctx := context.Background()
	cli, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	old, err := cli.ContainerInspect(ctx, node, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ContainerStop(ctx, node, client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ContainerRename(ctx, node, client.ContainerRenameOptions{NewName: node + setAsideSuffix}); err != nil {
		t.Fatal(err)
	}
	made, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: old.Container.Config, HostConfig: old.Container.HostConfig, Name: node})
	if err != nil {
		t.Fatal(err)
	}
	if start {
		if _, err := cli.ContainerStart(ctx, made.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return old.Container.ID, made.ID
}

// An app that stops while an update is in its check period leaves a replacement that
// runs and never passed. The next app start undoes it, as a failed check would.
func TestAnUpdateCutOffInItsCheckIsUndoneAtTheNextStart(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	deployed(t, docker, p, svc)
	oldID, newID := leaveMidCheck(t, p, node)
	if err := p.settling.mark(node); err != nil {
		t.Fatal(err)
	}
	p.RecoverSetAside(context.Background())
	sameBox(t, box(docker, svc.Slug), map[string]string{node: oldID + " running"})
	if _, ok := docker.Container(newID); ok {
		t.Fatalf("the replacement that never passed is still there: %v", docker.Names())
	}
	if p.settling.pending(node) {
		t.Fatal("the mark outlived its recovery")
	}

	// Control: with no mark, a replacement that runs is the one kept, as before.
	docker, p, _ = rollbackDaemon(t)
	deployed(t, docker, p, svc)
	_, newID = leaveMidCheck(t, p, node)
	p.RecoverSetAside(context.Background())
	sameBox(t, box(docker, svc.Slug), map[string]string{node: newID + " running"})
}

// A mark with a replacement that was made and never started, or with no
// replacement at all, still undoes the update: the old container runs again.
func TestAMarkedUpdateCutOffBeforeItsStartRunsTheOldContainerAgain(t *testing.T) {
	svc := plainEarner()
	node := containerName(svc.Slug)
	for _, gone := range []bool{false, true} {
		docker, p, _ := rollbackDaemon(t)
		deployed(t, docker, p, svc)
		oldID, newID := leaveMarked(t, node, false)
		if gone {
			docker.Rename(node, "someone-else")
			cli, err := dockerClient()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cli.ContainerRemove(context.Background(), newID, client.ContainerRemoveOptions{Force: true}); err != nil {
				t.Fatal(err)
			}
			cli.Close()
		}
		if err := p.settling.mark(node); err != nil {
			t.Fatal(err)
		}
		p.RecoverSetAside(context.Background())
		sameBox(t, box(docker, svc.Slug), map[string]string{node: oldID + " running"})
	}
}

// An undo that cannot remove the replacement leaves the old container set aside,
// so the mark stays and the next app start finishes the undo.
func TestAnUndoThatCannotRemoveTheReplacementKeepsTheMark(t *testing.T) {
	fastSettle(t)
	docker, p, _ := rollbackDaemon(t)
	svc := pinned()
	name := containerName(svc.Slug)
	docker.SetImage(pinnedRef, oldBuild, "")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	before, _ := docker.Container(name)
	docker.SetImage(tagRef, newBuild, "")
	docker.SetCrashing(newBuild, true)
	docker.FailRemove(name)
	if _, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil); err == nil {
		t.Fatal("a crashing build passed")
	}
	if !p.settling.pending(name) {
		t.Fatal("the mark went while the old container was still set aside")
	}
	docker.ClearFailures()
	docker.SetCrashing(newBuild, false)
	p.RecoverSetAside(context.Background())
	sameBox(t, box(docker, svc.Slug), map[string]string{name: before.ID + " running"})
}

// The mark is there from before the replacement starts until its check is over,
// passed or undone, and a plain redeploy writes none.
func TestAnUpdateMarksItsCheckPeriodFromBeforeTheStart(t *testing.T) {
	fastSettle(t)
	for _, crash := range []bool{false, true} {
		docker, p, _ := rollbackDaemon(t)
		svc := pinned()
		name := containerName(svc.Slug)
		docker.SetImage(pinnedRef, oldBuild, "")
		if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
			t.Fatalf("deploy: %v", err)
		}
		docker.SetImage(tagRef, newBuild, "")
		docker.SetCrashing(newBuild, crash)
		var atStart, atRemoval []bool
		docker.BeforeCall("POST /containers/"+name+"/start", func() { atStart = append(atStart, p.settling.pending(name)) })
		// Once passed, the mark goes before the old container does: from there the
		// replacement is the one to keep.
		old, _ := docker.Container(name)
		docker.BeforeCall("DELETE /containers/"+old.ID, func() { atRemoval = append(atRemoval, p.settling.pending(name)) })
		_, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil)
		if !crash && (len(atRemoval) == 0 || atRemoval[0]) {
			t.Fatalf("the mark was still there when the old container went: %v", atRemoval)
		}
		var undone *ImageUpdateUndone
		if crash != errors.As(err, &undone) {
			t.Fatalf("crash %v: err %v", crash, err)
		}
		if len(atStart) == 0 || !atStart[0] {
			t.Fatalf("crash %v: no mark when the replacement started (%v)", crash, atStart)
		}
		if p.settling.pending(name) {
			t.Fatalf("crash %v: the mark outlived the check", crash)
		}
	}
}

// A plain redeploy has no check period, so it writes no mark.
func TestAPlainRedeployMarksNothing(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	name := containerName(svc.Slug)
	deployed(t, docker, p, svc)
	var atStart []bool
	docker.BeforeCall("POST /containers/"+name+"/start", func() { atStart = append(atStart, p.settling.pending(name)) })
	deployed(t, docker, p, svc)
	if len(atStart) == 0 || atStart[0] {
		t.Fatalf("a plain redeploy marked a check period: %v", atStart)
	}
}

// With no way to write the mark, the update does not start: the old container runs on.
func TestAnUpdateThatCannotMarkItsCheckChangesNothing(t *testing.T) {
	fastSettle(t)
	docker, p, _ := rollbackDaemon(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A folder that cannot be made: a file holds the path above it.
	p.SetSettlingDir(filepath.Join(blocker, "marks"))
	svc := pinned()
	docker.SetImage(pinnedRef, oldBuild, "")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	before, _ := docker.Container(containerName(svc.Slug))
	docker.SetImage(tagRef, newBuild, "")
	if _, err := p.Deploy(context.Background(), updateSpec(svc, newBuild), nil); err == nil {
		t.Fatal("the update ran with no mark of its check period")
	}
	after, _ := docker.Container(containerName(svc.Slug))
	if after.ID != before.ID || after.State != "running" || after.ImageID != oldBuild {
		t.Fatalf("the old container is not back and running: %+v", after)
	}
}

// The marks are files, so a new process (the next app start) reads them.
func TestTheMarksOutliveTheProcess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "settling")
	a, b := NewDockerProvider(), NewDockerProvider()
	a.SetSettlingDir(dir)
	b.SetSettlingDir(dir)
	if b.settling.pending("cashpilot-x") {
		t.Fatal("a mark nobody wrote")
	}
	if err := a.settling.mark("cashpilot-x"); err != nil {
		t.Fatal(err)
	}
	if !b.settling.pending("cashpilot-x") {
		t.Fatal("the next process does not see the mark")
	}
	if err := b.settling.clear("cashpilot-x"); err != nil || a.settling.pending("cashpilot-x") {
		t.Fatalf("clear: %v", err)
	}
	if err := b.settling.clear("cashpilot-x"); err != nil {
		t.Fatalf("a second clear: %v", err)
	}
}
