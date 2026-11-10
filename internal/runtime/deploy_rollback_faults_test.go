package runtime

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness"
	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The failure paths of a redeploy that sets the old node aside: each step that
// fails says where the old node is, and leaves it there with its data.

// deployed deploys svc and returns its running container.
func deployed(t *testing.T, docker *harness.Docker, p *DockerProvider, svc catalog.Service) harness.Container {
	t.Helper()
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy %s: %v", svc.Slug, err)
	}
	c, _ := docker.Container(containerName(svc.Slug))
	return c
}

// box is what the fake holds of slug: name to container id and state.
func box(docker *harness.Docker, slug string) map[string]string {
	out := map[string]string{}
	for _, n := range managedNames(docker, slug) {
		c, _ := docker.Container(n)
		out[n] = c.ID + " " + c.State
	}
	return out
}

func sameBox(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("the box holds %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("the box holds %v, want %v", got, want)
		}
	}
}

// Recovery at app start never fails: with no daemon it does nothing, and the next
// start that finds the daemon puts the old node back.
func TestRecoveryAtAppStartWithNoDaemonWaitsForTheNextStart(t *testing.T) {
	svc := plainEarner()
	node := containerName(svc.Slug)
	aside := node + setAsideSuffix

	t.Run("daemon down", func(t *testing.T) {
		docker, p, _ := rollbackDaemon(t)
		old := deployed(t, docker, p, svc)
		docker.SetState(node, "exited")
		docker.Rename(node, aside)
		docker.SetDown(true)
		p.RecoverSetAside(context.Background())
		docker.SetDown(false)
		if now, ok := docker.Container(aside); !ok || now.ID != old.ID {
			t.Fatalf("the leftover moved while the daemon was down: %v", docker.Names())
		}
		p.RecoverSetAside(context.Background())
		if now, ok := docker.Container(node); !ok || now.ID != old.ID || now.State != "exited" {
			t.Fatalf("%s = %+v, want the old node back, stopped", node, now)
		}
	})

	t.Run("no usable DOCKER_HOST", func(t *testing.T) {
		docker, p, _ := rollbackDaemon(t)
		old := deployed(t, docker, p, svc)
		docker.SetState(node, "exited")
		docker.Rename(node, aside)
		t.Setenv("DOCKER_HOST", "not a docker host")
		p.RecoverSetAside(context.Background())
		if now, ok := docker.Container(aside); !ok || now.ID != old.ID {
			t.Fatalf("the leftover moved: %v", docker.Names())
		}
	})
}

// A deploy that cannot settle what an earlier deploy left stops before it touches
// anything: the box is as it was, and the error says what could not be done. Once
// Docker works again the next deploy settles it, and says so.
func TestADeployThatCannotSettleALeftoverTouchesNothing(t *testing.T) {
	svc := plainEarner()
	node := containerName(svc.Slug)
	aside := node + setAsideSuffix

	cases := []struct {
		name  string
		setup func(t *testing.T, docker *harness.Docker, p *DockerProvider)
		want  string
		note  string
	}{
		{
			name: "the set-aside name cannot be checked",
			setup: func(t *testing.T, docker *harness.Docker, p *DockerProvider) {
				deployed(t, docker, p, svc)
				docker.FailInspect(aside)
			},
			want: "could not check " + aside,
		},
		{
			name: "the name cannot be checked",
			setup: func(t *testing.T, docker *harness.Docker, p *DockerProvider) {
				leaveLeftover(t, docker, p, svc)
				docker.FailInspectFrom(node, 1)
			},
			want: "could not check " + node + ":",
			note: "Removed " + aside,
		},
		{
			name: "the leftover beside a running replacement cannot be removed",
			setup: func(t *testing.T, docker *harness.Docker, p *DockerProvider) {
				leaveLeftover(t, docker, p, svc)
				docker.FailRemove(aside)
			},
			want: "could not remove " + aside + ", left by a deploy that did not finish",
			note: "Removed " + aside,
		},
		{
			name: "a replacement that never started cannot be removed",
			setup: func(t *testing.T, docker *harness.Docker, p *DockerProvider) {
				deployed(t, docker, p, svc)
				docker.SetState(node, "exited")
				docker.Rename(node, aside)
				makeReplacement(t, svc, false)
				docker.FailRemove(node)
			},
			want: "could not remove " + node + ", which a deploy that did not finish made and never started",
			note: "Put back " + node,
		},
		{
			name: "the leftover cannot get its name back",
			setup: func(t *testing.T, docker *harness.Docker, p *DockerProvider) {
				deployed(t, docker, p, svc)
				docker.SetState(node, "exited")
				docker.Rename(node, aside)
				docker.FailRename(aside)
			},
			want: "could not put " + aside + " back as " + node,
			note: "Put back " + node,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docker, p, _ := rollbackDaemon(t)
			tc.setup(t, docker, p)
			before := box(docker, svc.Slug)
			_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
			if err == nil {
				t.Fatal("the deploy went on although the leftover could not be settled")
			}
			for _, want := range []string{tc.want, "The running container was not touched"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not say %q", err, want)
				}
			}
			sameBox(t, box(docker, svc.Slug), before)

			docker.ClearFailures()
			var progress []string
			if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, func(m string) { progress = append(progress, m) }); err != nil {
				t.Fatalf("the deploy after Docker recovered: %v", err)
			}
			if got := managedNames(docker, svc.Slug); !slices.Equal(got, []string{node}) {
				t.Fatalf("containers = %v, want only %s", got, node)
			}
			if tc.note != "" && !slices.ContainsFunc(progress, func(m string) bool { return strings.HasPrefix(m, tc.note) }) {
				t.Fatalf("progress %q does not say %q", progress, tc.note)
			}
		})
	}
}

// A redeploy that cannot check the set-aside name after the leftover check leaves
// the node running and replaces nothing.
func TestAFailedCheckOfTheSetAsideNameLeavesTheNodeRunning(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	old := deployed(t, docker, p, svc)
	// The first inspect of the set-aside name is the leftover check; the second,
	// whether another container holds it, fails.
	docker.FailInspectFrom(node+setAsideSuffix, 2)
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil || !strings.Contains(err.Error(), "could not check "+node+setAsideSuffix+", so "+node+" was left as it is") {
		t.Fatalf("Deploy = %v, want it to say the node was left as it is", err)
	}
	sameBox(t, box(docker, svc.Slug), map[string]string{node: old.ID + " running"})
}

// When the old node cannot be checked during the roll-back, the error names both
// places it can be, and the next app start puts it back.
func TestARollBackThatCannotCheckTheOldNodeSaysWhereItIs(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	aside := node + setAsideSuffix
	old := deployed(t, docker, p, svc)
	docker.FailCreate(node, portRefusal)
	// The roll-back inspects the old node by id; nothing before it does.
	docker.FailInspectFrom(old.ID, 1)
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil {
		t.Fatal("the redeploy succeeded although Docker refused the replacement")
	}
	for _, want := range []string{portRefusal, "could not be checked", "kept, stopped, as " + aside + " or " + node} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	sameBox(t, box(docker, svc.Slug), map[string]string{aside: old.ID + " exited"})
	if !slices.Contains(docker.Volumes(), "cashpilot-orderly-data") {
		t.Fatalf("the old node's volume went: %v", docker.Volumes())
	}
	docker.ClearFailures()
	p.RecoverSetAside(context.Background())
	sameBox(t, box(docker, svc.Slug), map[string]string{node: old.ID + " exited"})
}

// An old node that was put back but would not start says so, and is kept, stopped,
// under its own name.
func TestAPutBackNodeThatWillNotStartSaysSo(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	old := deployed(t, docker, p, svc)
	// Every start under the name fails: the replacement's, then the old node's.
	docker.FailStart(node, true)
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil || !strings.Contains(err.Error(), "was put back but could not be started again") {
		t.Fatalf("Deploy = %v, want it to say the old node could not start again", err)
	}
	sameBox(t, box(docker, svc.Slug), map[string]string{node: old.ID + " exited"})
}

// A first deploy Docker refuses has no old node to put back: the error is Docker's
// own, nothing is left on the box, and the refusal is still Docker's kind of error.
func TestARefusedFirstDeployLeavesNothing(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	docker.FailStartOfNext(containerName(svc.Slug), portRefusal)
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil || !strings.Contains(err.Error(), portRefusal) {
		t.Fatalf("Deploy = %v, want Docker's refusal", err)
	}
	if strings.Contains(err.Error(), "previous container") {
		t.Fatalf("a first deploy speaks of a previous container: %v", err)
	}
	if got := managedNames(docker, svc.Slug); len(got) != 0 {
		t.Fatalf("containers = %v, want none", got)
	}
}

// The sentence a roll-back adds does not hide what kind of error Docker gave.
func TestARefusedRedeployKeepsDockersErrorKind(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	deployed(t, docker, p, svc)
	docker.FailCreate(containerName(svc.Slug), portRefusal)
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil || !strings.Contains(err.Error(), "running again") {
		t.Fatalf("Deploy = %v, want the old node running again", err)
	}
	if !cerrdefs.IsInternal(err) {
		t.Fatalf("Deploy = %v (%T), want Docker's internal error still found through it", err, err)
	}
}

// When the old node cannot be removed after its replacement runs, the deploy still
// works, says so, and the leftover is hidden and removed at the next app start.
func TestAnOldNodeThatWouldNotGoIsRemovedLater(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	aside := node + setAsideSuffix
	old := deployed(t, docker, p, svc)
	docker.FailRemove(aside)
	var progress []string
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, func(m string) { progress = append(progress, m) }); err != nil {
		t.Fatalf("Deploy = %v, want the replacement running", err)
	}
	if !slices.ContainsFunc(progress, func(m string) bool { return strings.HasPrefix(m, "Could not remove "+aside) }) {
		t.Fatalf("progress %q does not say the old node could not be removed", progress)
	}
	now, _ := docker.Container(node)
	sameBox(t, box(docker, svc.Slug), map[string]string{node: now.ID + " running", aside: old.ID + " exited"})
	infos, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.Slug == svc.Slug && info.ContainerID != now.ID {
			t.Fatalf("List shows the leftover %s", info.ContainerID)
		}
	}
	docker.ClearFailures()
	p.RecoverSetAside(context.Background())
	sameBox(t, box(docker, svc.Slug), map[string]string{node: now.ID + " running"})
}

// A removal that cannot take the leftover stops with an error, and trying again
// once Docker works removes everything: the service never comes back at app start.
func TestARemoveThatCannotTakeTheLeftoverCanBeRetried(t *testing.T) {
	svc := plainEarner()
	aside := containerName(svc.Slug) + setAsideSuffix
	for name, fail := range map[string]func(*harness.Docker){
		"the leftover cannot be checked": func(d *harness.Docker) { d.FailInspect(aside) },
		"the leftover cannot be removed": func(d *harness.Docker) { d.FailRemove(aside) },
	} {
		t.Run(name, func(t *testing.T) {
			docker, p, _ := rollbackDaemon(t)
			leaveLeftover(t, docker, p, svc)
			fail(docker)
			err := p.Remove(context.Background(), svc.Slug, RemoveOptions{Critical: map[string]string{}})
			if err == nil || !strings.Contains(err.Error(), aside) {
				t.Fatalf("Remove = %v, want an error naming %s", err, aside)
			}
			docker.ClearFailures()
			if err := p.Remove(context.Background(), svc.Slug, RemoveOptions{Critical: map[string]string{}}); err != nil {
				t.Fatalf("Remove again: %v", err)
			}
			p.RecoverSetAside(context.Background())
			if got := managedNames(docker, svc.Slug); len(got) != 0 {
				t.Fatalf("containers after Remove = %v, want none", got)
			}
			if !slices.Contains(docker.Volumes(), "cashpilot-orderly-data") {
				t.Fatalf("a remove that keeps the data took the volume: %v", docker.Volumes())
			}
		})
	}
}

// Removing a service never touches another service's container that happens to
// sit under the name its own would be set aside under.
func TestRemoveLeavesAnotherServiceUnderTheSetAsideName(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	deployed(t, docker, p, svc)
	other := plainEarner()
	other.Slug, other.Docker.Volumes, other.Docker.Ports = svc.Slug+"-replaced", nil, nil
	theirs := deployed(t, docker, p, other)
	if err := p.Remove(context.Background(), svc.Slug, RemoveOptions{Critical: map[string]string{}}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if now, ok := docker.Container(theirs.Name); !ok || now.ID != theirs.ID || now.State != "running" {
		t.Fatalf("the other service's container was touched: %+v (box %v)", now, docker.Names())
	}
}

// A copy whose removal fails, or whose leftover's does, can be removed again once
// Docker works, and nothing of it is left.
func TestARemoveCopyThatFailsCanBeRetried(t *testing.T) {
	svc := plainEarner()
	svc.Docker.Ports, svc.Docker.Volumes = nil, nil
	name := copyName(svc.Slug, 2)
	for what, fail := range map[string]func(*harness.Docker){
		"the copy":            func(d *harness.Docker) { d.FailRemove(name) },
		"the copy's leftover": func(d *harness.Docker) { d.FailRemove(name + setAsideSuffix) },
	} {
		t.Run(what, func(t *testing.T) {
			docker, p, _ := rollbackDaemon(t)
			if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc, Instance: 2}, nil); err != nil {
				t.Fatalf("deploy copy 2: %v", err)
			}
			// A deploy of the copy that died after its replacement started.
			docker.SetState(name, "exited")
			docker.Rename(name, name+setAsideSuffix)
			cli, err := dockerClient()
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			if _, err := cli.ContainerCreate(context.Background(), client.ContainerCreateOptions{
				Name: name, Config: &container.Config{Image: svc.Docker.Image, Labels: containerLabels(svc.Slug, 2)},
			}); err != nil {
				t.Fatal(err)
			}
			fail(docker)
			if err := p.RemoveCopy(context.Background(), svc.Slug, 2, 5); err == nil {
				t.Fatal("RemoveCopy succeeded although Docker refused the removal")
			}
			docker.ClearFailures()
			if err := p.RemoveCopy(context.Background(), svc.Slug, 2, 5); err != nil {
				t.Fatalf("RemoveCopy again: %v", err)
			}
			p.RecoverSetAside(context.Background())
			if got := managedNames(docker, svc.Slug); len(got) != 0 {
				t.Fatalf("containers after RemoveCopy = %v, want none", got)
			}
		})
	}
}

// A deploy cancelled while Docker stops the old node still reads what the stop did,
// with a context of its own: a node the stop did take down runs again, and the
// error does not claim it still runs.
func TestACancelledStopThatStoppedTheNodeStartsItAgain(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	old := deployed(t, docker, p, svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	docker.FailStop(node)
	docker.BeforeCall("POST /containers/"+node+"/stop", func() {
		// The daemon finishes the stop; the client has given up waiting.
		docker.SetState(node, "exited")
		cancel()
	})
	_, err := p.Deploy(ctx, DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil {
		t.Fatal("a cancelled deploy succeeded")
	}
	if strings.Contains(err.Error(), "still running") {
		t.Errorf("error %q says the stopped node still runs", err)
	}
	sameBox(t, box(docker, svc.Slug), map[string]string{node: old.ID + " running"})
}

// leaveOnlySetAside redeploys svc's copy n so that Docker refuses the replacement and
// the old node cannot get its name back: it is left alone, stopped, set aside.
func leaveOnlySetAside(t *testing.T, docker *harness.Docker, p *DockerProvider, svc catalog.Service, n int) harness.Container {
	t.Helper()
	name := copyName(svc.Slug, n)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc, Instance: n}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	old, _ := docker.Container(name)
	docker.FailStartOfNext(name, portRefusal)
	docker.FailRename(name + setAsideSuffix)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc, Instance: n}, nil); err == nil {
		t.Fatal("the redeploy succeeded although Docker refused the replacement")
	}
	docker.ClearFailures()
	sameBox(t, box(docker, svc.Slug), map[string]string{name + setAsideSuffix: old.ID + " exited"})
	return old
}

// A service whose only container is the one a roll-back could not rename back is
// still planned and removed, with the same data choices as any other removal.
func TestAServiceLeftOnlySetAsideCanBeRemoved(t *testing.T) {
	svc := plainEarner()
	node := containerName(svc.Slug)
	critical := map[string]string{"/data": "the node identity"}

	t.Run("plan", func(t *testing.T) {
		docker, p, _ := rollbackDaemon(t)
		leaveOnlySetAside(t, docker, p, svc, 1)
		plan, err := p.PlanRemoval(context.Background(), svc.Slug, critical)
		if err != nil {
			t.Fatalf("PlanRemoval: %v", err)
		}
		if len(plan.Volumes) != 1 || plan.Volumes[0].Source != "cashpilot-orderly-data" || !plan.Volumes[0].Critical {
			t.Fatalf("plan = %+v, want the critical data volume", plan)
		}
	})
	t.Run("keep the data", func(t *testing.T) {
		docker, p, _ := rollbackDaemon(t)
		leaveOnlySetAside(t, docker, p, svc, 1)
		if err := p.Remove(context.Background(), svc.Slug, RemoveOptions{Critical: critical}); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		sameBox(t, box(docker, svc.Slug), map[string]string{})
		if !slices.Contains(docker.Volumes(), "cashpilot-orderly-data") {
			t.Fatalf("a remove that keeps the data took the volume: %v", docker.Volumes())
		}
	})
	t.Run("critical data needs its own yes", func(t *testing.T) {
		docker, p, _ := rollbackDaemon(t)
		old := leaveOnlySetAside(t, docker, p, svc, 1)
		err := p.Remove(context.Background(), svc.Slug, RemoveOptions{DeleteData: true, Critical: critical})
		if err == nil || !strings.Contains(err.Error(), "cannot be recovered") {
			t.Fatalf("Remove = %v, want the critical-data refusal", err)
		}
		sameBox(t, box(docker, svc.Slug), map[string]string{node + setAsideSuffix: old.ID + " exited"})
		if err := p.Remove(context.Background(), svc.Slug, RemoveOptions{DeleteData: true, AllowCritical: true, Critical: critical}); err != nil {
			t.Fatalf("Remove with the yes: %v", err)
		}
		sameBox(t, box(docker, svc.Slug), map[string]string{})
		if slices.Contains(docker.Volumes(), "cashpilot-orderly-data") {
			t.Fatalf("a remove that deletes the data kept the volume: %v", docker.Volumes())
		}
	})
	t.Run("another service under the set-aside name", func(t *testing.T) {
		docker, p, _ := rollbackDaemon(t)
		other := plainEarner()
		other.Slug, other.Docker.Volumes, other.Docker.Ports = svc.Slug+"-replaced", nil, nil
		theirs := deployed(t, docker, p, other)
		if _, err := p.PlanRemoval(context.Background(), svc.Slug, critical); !cerrdefs.IsNotFound(err) {
			t.Fatalf("PlanRemoval = %v, want not found", err)
		}
		if err := p.Remove(context.Background(), svc.Slug, RemoveOptions{DeleteData: true, AllowCritical: true, Critical: critical}); !cerrdefs.IsNotFound(err) {
			t.Fatalf("Remove = %v, want not found", err)
		}
		if now, ok := docker.Container(theirs.Name); !ok || now.ID != theirs.ID || now.State != "running" {
			t.Fatalf("the other service's container was touched: %+v (box %v)", now, docker.Names())
		}
	})
}

// A copy whose only container is set aside is still listed and removed, so the
// service's removal does not stop at it.
func TestACopyLeftOnlySetAsideCanBeRemoved(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	svc.Docker.Ports, svc.Docker.Volumes = nil, nil
	leaveOnlySetAside(t, docker, p, svc, 2)
	copies, err := p.Copies(context.Background(), svc.Slug)
	if err != nil || !slices.Equal(copies, []int{2}) {
		t.Fatalf("Copies = %v, %v; want [2]", copies, err)
	}
	if err := p.RemoveCopy(context.Background(), svc.Slug, 2, 5); err != nil {
		t.Fatalf("RemoveCopy: %v", err)
	}
	sameBox(t, box(docker, svc.Slug), map[string]string{})
}

// Recovery, at app start or at the start of another deploy, never takes the
// replacement a deploy in flight has made and not yet started: it waits for that
// deploy, which then ends with its replacement running.
func TestRecoveryWaitsForADeployInFlight(t *testing.T) {
	svc := plainEarner()
	node := containerName(svc.Slug)
	for what, settle := range map[string]func(p *DockerProvider) error{
		"recovery at app start": func(p *DockerProvider) error { p.RecoverSetAside(context.Background()); return nil },
		"another deploy": func(p *DockerProvider) error {
			_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
			return err
		},
	} {
		t.Run(what, func(t *testing.T) {
			docker, p, _ := rollbackDaemon(t)
			deployed(t, docker, p, svc)
			done := make(chan error, 1)
			docker.BeforeCall("POST /containers/"+node+"/start", func() {
				// The replacement exists, created, and the old node sits set aside.
				go func() { done <- settle(p) }()
				select {
				case err := <-done:
					done <- err
				case <-time.After(300 * time.Millisecond):
				}
			})
			if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
				t.Fatalf("the deploy in flight failed: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", what, err)
			}
			got := box(docker, svc.Slug)
			if len(got) != 1 || !strings.HasSuffix(got[node], " running") {
				t.Fatalf("the box holds %v, want only %s running", got, node)
			}
		})
	}
}
