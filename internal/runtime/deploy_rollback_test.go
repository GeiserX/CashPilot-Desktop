package runtime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness"
	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// portRefusal is what Docker answers when a host process holds a published port.
const portRefusal = "failed to set up container networking: driver failed programming external connectivity on endpoint cashpilot-x: Bind for 0.0.0.0:23001 failed: port is already allocated"

// rollbackDaemon is the fake daemon with a provider whose stop record lives in a
// temp file, and the DOCKER_HOST pointed at it.
func rollbackDaemon(t *testing.T) (*harness.Docker, *DockerProvider, string) {
	t.Helper()
	docker := harness.NewDocker()
	t.Cleanup(docker.Close)
	t.Setenv("DOCKER_HOST", docker.Host())
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	record := filepath.Join(t.TempDir(), "stopped")
	return docker, gatedProvider(record), record
}

// plainEarner is an ordinary earner with a named volume and a published port.
func plainEarner() catalog.Service {
	return catalog.Service{
		Name: "Orderly",
		Slug: "orderly",
		Docker: catalog.DockerConfig{
			Image:   "example/orderly:1.0.0",
			Volumes: []string{"cashpilot-orderly-data:/data"},
			Ports:   []string{"28967:28967"},
		},
	}
}

// managedNames lists the containers of slug the fake holds, by name.
func managedNames(docker *harness.Docker, slug string) []string {
	var out []string
	for _, n := range docker.Names() {
		if n == "cashpilot-"+slug || strings.HasPrefix(n, "cashpilot-"+slug+"-") {
			out = append(out, n)
		}
	}
	return out
}

func readRecord(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read the stop record: %v", err)
	}
	return string(raw)
}

// THE RULE: when Docker refuses the replacement (the create, or the start of a
// container whose published port a host process holds), the node that ran keeps
// running: same container, same name, same volumes, and the user reads why.
func TestARefusedReplacementPutsTheNodeBack(t *testing.T) {
	services := map[string]func() catalog.Service{"plain": plainEarner, "fence-gated": mysteriumShaped}
	for kind, svcOf := range services {
		for _, step := range []string{"create", "start"} {
			for _, stoppedBefore := range []bool{false, true} {
				name := kind + " " + step
				if stoppedBefore {
					name += " stopped by the user"
				}
				t.Run(name, func(t *testing.T) {
					docker, p, record := rollbackDaemon(t)
					svc := svcOf()
					slug := svc.Slug
					node := containerName(slug)
					if _, err := p.Deploy(context.Background(), DeploySpec{Slug: slug, Service: svc}, nil); err != nil {
						t.Fatalf("first deploy: %v", err)
					}
					if stoppedBefore {
						if err := p.Stop(context.Background(), slug, 5); err != nil {
							t.Fatalf("Stop: %v", err)
						}
					}
					before, _ := docker.Container(node)
					recordBefore := readRecord(t, record)
					volumesBefore := docker.Volumes()

					if step == "create" {
						docker.FailCreate(node, portRefusal)
					} else {
						docker.FailStartOfNext(node, portRefusal)
					}
					var progress []string
					_, err := p.Deploy(context.Background(), DeploySpec{Slug: slug, Service: svc}, func(m string) { progress = append(progress, m) })
					if err == nil {
						t.Fatal("the redeploy succeeded although Docker refused the replacement")
					}
					if !strings.Contains(err.Error(), portRefusal) {
						t.Errorf("error %q lost Docker's reason", err)
					}
					if strings.ContainsAny(err.Error(), "\r\n") {
						t.Errorf("the error spans several lines: %q", err)
					}
					want := "put back and is running again"
					if stoppedBefore {
						want = "put back and is stopped, as it was"
					}
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not say %q", err, want)
					}

					after, ok := docker.Container(node)
					if !ok {
						t.Fatalf("no container under %s after the refused redeploy; the fake holds %v", node, docker.Names())
					}
					if after.ID != before.ID {
						t.Errorf("%s is container %s, want the old one %s back", node, after.ID, before.ID)
					}
					if after.State != before.State {
						t.Errorf("state = %q, want %q as before the redeploy", after.State, before.State)
					}
					if !slices.Equal(after.VolumeNames(), before.VolumeNames()) {
						t.Errorf("volumes = %v, want %v", after.VolumeNames(), before.VolumeNames())
					}
					if got := docker.Volumes(); !slices.Equal(got, volumesBefore) {
						t.Errorf("named volumes = %v, want %v kept", got, volumesBefore)
					}
					if got := managedNames(docker, slug); !slices.Equal(got, []string{node}) {
						t.Errorf("containers = %v, want only %s", got, node)
					}
					if got := readRecord(t, record); got != recordBefore {
						t.Errorf("stop record = %q, want %q as before", got, recordBefore)
					}
				})
			}
		}
	}
}

// A redeploy that works leaves no container set aside, and keeps the volumes.
func TestASuccessfulRedeployLeavesNothingSetAside(t *testing.T) {
	for kind, svc := range map[string]catalog.Service{"plain": plainEarner(), "fence-gated": mysteriumShaped()} {
		t.Run(kind, func(t *testing.T) {
			docker, p, _ := rollbackDaemon(t)
			node := containerName(svc.Slug)
			if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
				t.Fatalf("first deploy: %v", err)
			}
			old, _ := docker.Container(node)
			if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
				t.Fatalf("redeploy: %v", err)
			}
			now, _ := docker.Container(node)
			if now.ID == old.ID || now.State != "running" {
				t.Fatalf("after the redeploy %s is %s (%s), want a new running container", node, now.ID, now.State)
			}
			if got := managedNames(docker, svc.Slug); !slices.Equal(got, []string{node}) {
				t.Fatalf("containers = %v, want only %s", got, node)
			}
			if !slices.Equal(now.VolumeNames(), old.VolumeNames()) {
				t.Fatalf("volumes = %v, want %v", now.VolumeNames(), old.VolumeNames())
			}
		})
	}
}

// A roll-back that fails itself says where the old node is, and keeps it.
func TestAFailedRollBackNamesTheLeftover(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	old, _ := docker.Container(node)
	docker.FailStartOfNext(node, portRefusal)
	docker.FailRename(node + setAsideSuffix)
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil {
		t.Fatal("the redeploy succeeded although Docker refused the replacement")
	}
	for _, want := range []string{portRefusal, "could not be put back", "kept, stopped, as " + node + setAsideSuffix} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	kept, ok := docker.Container(node + setAsideSuffix)
	if !ok || kept.ID != old.ID {
		t.Fatalf("the old container is not kept as %s (holds %v)", node+setAsideSuffix, docker.Names())
	}
	if _, ok := docker.Container(node); ok {
		t.Fatalf("the refused replacement still holds %s", node)
	}
	if !slices.Contains(docker.Volumes(), "cashpilot-orderly-data") {
		t.Fatalf("the old node's volume went: %v", docker.Volumes())
	}
}

// The image is pulled before anything is touched: a pull that fails leaves the
// running node exactly as it was, with no stop and no rename.
func TestAFailedPullTouchesNothing(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	old, _ := docker.Container(node)
	docker.ResetCalls()
	docker.SetPullError("manifest unknown")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err == nil {
		t.Fatal("the redeploy succeeded although the pull failed")
	}
	for _, call := range docker.Calls() {
		if strings.HasSuffix(call, "/stop") || strings.HasSuffix(call, "/rename") || strings.HasPrefix(call, "DELETE /containers/") {
			t.Fatalf("a failed pull still did %s (calls %v)", call, docker.Calls())
		}
	}
	if now, _ := docker.Container(node); now.ID != old.ID || now.State != "running" {
		t.Fatalf("%s is %s (%s), want the old one running", node, now.ID, now.State)
	}
}

// A node Docker could not stop is not replaced: the replacement would run beside it.
func TestANodeThatWouldNotStopIsNotReplaced(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	old, _ := docker.Container(node)
	docker.FailStop(node)
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("Deploy = %v, want a refusal that says the node still runs", err)
	}
	if now, _ := docker.Container(node); now.ID != old.ID || now.State != "running" {
		t.Fatalf("%s is %s (%s), want the old one running", node, now.ID, now.State)
	}
	if got := managedNames(docker, svc.Slug); !slices.Equal(got, []string{node}) {
		t.Fatalf("containers = %v, want only %s", got, node)
	}
}

// A container of another service under the set-aside name is never touched, and
// the running node is not replaced.
func TestASetAsideNameAnotherServiceHoldsStopsTheDeploy(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	other := plainEarner()
	other.Slug, other.Docker.Volumes, other.Docker.Ports = svc.Slug+"-replaced", nil, nil
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: other.Slug, Service: other}, nil); err != nil {
		t.Fatalf("deploy %s: %v", other.Slug, err)
	}
	theirs, _ := docker.Container(node + setAsideSuffix)
	old, _ := docker.Container(node)
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil || !strings.Contains(err.Error(), "belongs to "+other.Slug) {
		t.Fatalf("Deploy = %v, want a refusal naming %s", err, other.Slug)
	}
	if now, _ := docker.Container(node); now.ID != old.ID || now.State != "running" {
		t.Fatalf("%s is %s (%s), want the old one running", node, now.ID, now.State)
	}
	if now, _ := docker.Container(node + setAsideSuffix); now.ID != theirs.ID || now.State != "running" {
		t.Fatalf("the other service's container was touched: %+v", now)
	}
}

// A deploy that died midway leaves the old node under the set-aside name. The next
// deploy, or the app's next start, puts it back.
func TestALeftoverFromADeployThatDiedIsPutBack(t *testing.T) {
	svc := plainEarner()
	node := containerName(svc.Slug)
	aside := node + setAsideSuffix

	t.Run("app start, the name is free", func(t *testing.T) {
		docker, p, _ := rollbackDaemon(t)
		if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
			t.Fatalf("deploy: %v", err)
		}
		old, _ := docker.Container(node)
		docker.SetState(node, "exited")
		docker.Rename(node, aside)
		p.RecoverSetAside(context.Background())
		if now, ok := docker.Container(node); !ok || now.ID != old.ID {
			t.Fatalf("the leftover was not put back under %s (holds %v)", node, docker.Names())
		}
		if got := managedNames(docker, svc.Slug); !slices.Equal(got, []string{node}) {
			t.Fatalf("containers = %v, want only %s", got, node)
		}
	})

	replacement := func(t *testing.T, started bool) string { return makeReplacement(t, svc, started) }

	t.Run("app start, a replacement that never started holds the name", func(t *testing.T) {
		docker, p, _ := rollbackDaemon(t)
		if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
			t.Fatalf("deploy: %v", err)
		}
		old, _ := docker.Container(node)
		docker.SetState(node, "exited")
		docker.Rename(node, aside)
		replacement(t, false)
		p.RecoverSetAside(context.Background())
		if now, ok := docker.Container(node); !ok || now.ID != old.ID {
			t.Fatalf("%s = %+v, want the old node back in place of the half-made one", node, now)
		}
		if got := managedNames(docker, svc.Slug); !slices.Equal(got, []string{node}) {
			t.Fatalf("containers = %v, want only %s", got, node)
		}
	})

	t.Run("app start, the replacement runs", func(t *testing.T) {
		docker, p, _ := rollbackDaemon(t)
		if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
			t.Fatalf("deploy: %v", err)
		}
		docker.SetState(node, "exited")
		docker.Rename(node, aside)
		id := replacement(t, true)
		p.RecoverSetAside(context.Background())
		if got := managedNames(docker, svc.Slug); !slices.Equal(got, []string{node}) {
			t.Fatalf("containers = %v, want only %s", got, node)
		}
		if now, _ := docker.Container(node); now.ID != id || now.State != "running" {
			t.Fatalf("%s = %+v, want the replacement running", node, now)
		}
		if !slices.Contains(docker.Volumes(), "cashpilot-orderly-data") {
			t.Fatalf("the leftover's volume went: %v", docker.Volumes())
		}
	})

	t.Run("next deploy", func(t *testing.T) {
		docker, p, _ := rollbackDaemon(t)
		if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
			t.Fatalf("deploy: %v", err)
		}
		old, _ := docker.Container(node)
		docker.SetState(node, "exited")
		docker.Rename(node, aside)
		docker.FailStartOfNext(node, portRefusal)
		_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
		if err == nil {
			t.Fatal("the redeploy succeeded although Docker refused the replacement")
		}
		if now, ok := docker.Container(node); !ok || now.ID != old.ID || now.State != "exited" {
			t.Fatalf("%s = %+v, want the leftover back, stopped as it was", node, now)
		}
		if got := managedNames(docker, svc.Slug); !slices.Equal(got, []string{node}) {
			t.Fatalf("containers = %v, want only %s", got, node)
		}
	})
}

// The guard never starts a fence-gated node that is set aside: it would take the
// ports its replacement is about to publish. The plain reconcile is the control
// (TestAReconcileStartsANodeLeftDownOnlyAfterTheFence).
func TestASetAsideNodeNeverWaitsForTheFence(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := mysteriumShaped()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	docker.SetState(node, "exited")
	docker.Rename(node, node+setAsideSuffix)
	p.ReconcileFence(context.Background())
	if now, _ := docker.Container(node + setAsideSuffix); now.State != "exited" {
		t.Fatalf("the set-aside node is %s, want it left stopped", now.State)
	}
	if w := p.FenceStatus().Waiting; len(w) != 0 {
		t.Fatalf("the set-aside node is named waiting: %v", w)
	}
}

// makeReplacement makes svc's container by hand under its name, as a deploy that
// died after its create (started false) or after its start (started true) leaves it.
func makeReplacement(t *testing.T, svc catalog.Service, started bool) string {
	t.Helper()
	cli, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	var mounts []mount.Mount
	for _, v := range svc.Docker.Volumes {
		source, target, _ := strings.Cut(v, ":")
		mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: source, Target: target})
	}
	made, err := cli.ContainerCreate(context.Background(), client.ContainerCreateOptions{
		Name:       containerName(svc.Slug),
		Config:     &container.Config{Image: svc.Docker.Image, Labels: gatedLabels(containerLabels(svc.Slug, 1), svc)},
		HostConfig: &container.HostConfig{Mounts: mounts},
	})
	if err != nil {
		t.Fatal(err)
	}
	if started {
		if _, err := cli.ContainerStart(context.Background(), made.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return made.ID
}

// leaveLeftover redeploys svc by hand the way a deploy that died after its start
// leaves it: the replacement runs under the name, the old node sits set aside.
func leaveLeftover(t *testing.T, docker *harness.Docker, p *DockerProvider, svc catalog.Service) (old harness.Container, replacement string) {
	t.Helper()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	old, _ = docker.Container(node)
	docker.SetState(node, "exited")
	docker.Rename(node, node+setAsideSuffix)
	return old, makeReplacement(t, svc, true)
}

// Removing a service takes the container a deploy left set aside beside it too.
// Left there, the next app start would give it the free name back, and the fence
// check would start a node the user removed.
func TestRemovingAServiceTakesItsLeftoverToo(t *testing.T) {
	for kind, svc := range map[string]catalog.Service{"plain": plainEarner(), "fence-gated": mysteriumShaped()} {
		t.Run(kind, func(t *testing.T) {
			docker, p, _ := rollbackDaemon(t)
			p.ReconcileFence(context.Background())
			leaveLeftover(t, docker, p, svc)
			volumes := docker.Volumes()
			if err := p.Remove(context.Background(), svc.Slug, RemoveOptions{DeleteData: true, Critical: map[string]string{}}); err != nil {
				t.Fatalf("Remove: %v", err)
			}
			if got := managedNames(docker, svc.Slug); len(got) != 0 {
				t.Fatalf("containers after Remove = %v, want none", got)
			}
			for _, v := range volumes {
				if strings.HasPrefix(v, "cashpilot-"+svc.Slug) && slices.Contains(docker.Volumes(), v) {
					t.Fatalf("volume %s survived a remove that deletes the data: %v", v, docker.Volumes())
				}
			}
			p.RecoverSetAside(context.Background())
			p.ReconcileFence(context.Background())
			if got := managedNames(docker, svc.Slug); len(got) != 0 {
				t.Fatalf("the removed service came back: %v", got)
			}
		})
	}
}

// The service list shows the replacement, not the leftover beside it: the
// dashboard row is the service's first container, and the last one listed wins.
func TestTheListHidesALeftoverBesideItsReplacement(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	_, id := leaveLeftover(t, docker, p, svc)
	infos, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var mine []ContainerInfo
	for _, info := range infos {
		if info.Slug == svc.Slug {
			mine = append(mine, info)
		}
	}
	if len(mine) != 1 || mine[0].ContainerID != id || mine[0].Status != "running" {
		t.Fatalf("List for %s = %+v, want only the running replacement %s", svc.Slug, mine, id)
	}
}

// A fence-gated node put back after a refused redeploy starts again only when it
// sits on the fence alone; on any other network it stays stopped.
func TestAGatedNodeOffTheFenceIsPutBackStopped(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := mysteriumShaped()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatal(err)
	}
	docker.AttachNetwork(node, "bridge")
	docker.FailStartOfNext(node, portRefusal)
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil || !strings.Contains(err.Error(), "kept stopped") {
		t.Fatalf("Deploy = %v, want the node kept stopped", err)
	}
	if now, _ := docker.Container(node); now.State == "running" {
		t.Fatalf("the gated node off the fence started again: %+v", now)
	}
}

// A node Docker would not rename keeps its name, and runs again if it ran.
func TestANodeThatCouldNotBeSetAsideRunsAgain(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatal(err)
	}
	old, _ := docker.Container(node)
	docker.FailRename(node)
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil || !strings.Contains(err.Error(), "could not be set aside") || !strings.Contains(err.Error(), "running again") {
		t.Fatalf("Deploy = %v", err)
	}
	if now, _ := docker.Container(node); now.ID != old.ID || now.State != "running" {
		t.Fatalf("%s = %+v, want the old node running", node, now)
	}
}

// A stop that errors although the node did stop does not block the redeploy.
func TestAStopErrorOnAStoppedNodeStillRedeploys(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatal(err)
	}
	old, _ := docker.Container(node)
	docker.SetState(node, "exited")
	docker.FailStop(node)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatalf("Deploy = %v, want the stopped node replaced", err)
	}
	if now, _ := docker.Container(node); now.ID == old.ID || now.State != "running" {
		t.Fatalf("%s = %+v, want a new running container", node, now)
	}
}

// When the refused container cannot be removed, the error says it still holds the
// name, and the old node stays set aside with its data.
func TestARefusedContainerThatWillNotGoKeepsTheOldOneAside(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatal(err)
	}
	old, _ := docker.Container(node)
	docker.FailStartOfNext(node, portRefusal)
	docker.FailRemove(node)
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil || !strings.Contains(err.Error(), "the refused one still holds "+node) {
		t.Fatalf("Deploy = %v, want it to say the refused one still holds %s", err, node)
	}
	if kept, ok := docker.Container(node + setAsideSuffix); !ok || kept.ID != old.ID {
		t.Fatalf("the old node is not kept as %s: %v", node+setAsideSuffix, docker.Names())
	}
}

// A create that made the container but still answered an error (a reply that never
// arrived) leaves no stray container: the old node comes back under its name.
func TestACreateThatFailedAfterMakingTheContainerPutsTheNodeBack(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatal(err)
	}
	old, _ := docker.Container(node)
	docker.FailAfterCreate(node, "context deadline exceeded")
	_, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil)
	if err == nil || !strings.Contains(err.Error(), "running again") {
		t.Fatalf("Deploy = %v, want the old node running again", err)
	}
	if now, _ := docker.Container(node); now.ID != old.ID || now.State != "running" {
		t.Fatalf("%s = %+v, want the old node %s running", node, now, old.ID)
	}
	if got := managedNames(docker, svc.Slug); !slices.Equal(got, []string{node}) {
		t.Fatalf("containers = %v, want only %s", got, node)
	}
}

// The app-start recovery leaves alone a container under the name that is not this
// copy of this service, and the set-aside node beside it.
func TestRecoveryLeavesAContainerNotOursAlone(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	node := containerName(svc.Slug)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc}, nil); err != nil {
		t.Fatal(err)
	}
	old, _ := docker.Container(node)
	docker.SetState(node, "exited")
	docker.Rename(node, node+setAsideSuffix)
	cli, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	theirs, err := cli.ContainerCreate(context.Background(), client.ContainerCreateOptions{
		Name: node, Config: &container.Config{Image: "someone/else:1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.RecoverSetAside(context.Background())
	if now, ok := docker.Container(node); !ok || now.ID != theirs.ID {
		t.Fatalf("%s = %+v, want the container that is not ours left there", node, now)
	}
	if now, ok := docker.Container(node + setAsideSuffix); !ok || now.ID != old.ID {
		t.Fatalf("the set-aside node was touched: %v", docker.Names())
	}
}

// A leftover beside a replacement that runs goes without its anonymous volumes:
// they may still hold data nobody said could go.
func TestRecoveryKeepsTheLeftoversAnonymousVolumes(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	old, _ := leaveLeftover(t, docker, p, plainEarner())
	docker.ResetCalls()
	p.RecoverSetAside(context.Background())
	removed := false
	for _, call := range docker.CallsWithQuery() {
		if strings.HasPrefix(call, "DELETE /") && strings.Contains(call, old.ID) {
			removed = true
			if strings.Contains(call, "v=1") {
				t.Fatalf("the leftover went with its anonymous volumes: %s", call)
			}
		}
	}
	if !removed {
		t.Fatalf("the leftover was not removed (calls %v)", docker.CallsWithQuery())
	}
}

// Only a container named its own copy's name plus the suffix is set aside: copy 2
// of foo under cashpilot-foo-replaced is not foo's copy 1 set aside.
func TestSetAsideBaseChecksTheCopy(t *testing.T) {
	if base, ok := setAsideBase("/cashpilot-foo-replaced", containerLabels("foo", 1)); !ok || base != "cashpilot-foo" {
		t.Fatalf("copy 1 = %q, %v; want cashpilot-foo, true", base, ok)
	}
	if _, ok := setAsideBase("/cashpilot-foo-replaced", containerLabels("foo", 2)); ok {
		t.Fatal("copy 2 of foo under cashpilot-foo-replaced counts as set aside")
	}
	if base, ok := setAsideBase("/cashpilot-foo-2-replaced", containerLabels("foo", 2)); !ok || base != "cashpilot-foo-2" {
		t.Fatalf("copy 2 = %q, %v; want cashpilot-foo-2, true", base, ok)
	}
}

// A copy's leftover counts once and goes with the copy, as the first copy's does.
func TestACopysLeftoverIsNeitherCountedNorKept(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	svc := plainEarner()
	svc.Docker.Ports, svc.Docker.Volumes = nil, nil
	name := copyName(svc.Slug, 2)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc, Instance: 2}, nil); err != nil {
		t.Fatalf("deploy copy 2: %v", err)
	}
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
	copies, err := p.Copies(context.Background(), svc.Slug)
	if err != nil || !slices.Equal(copies, []int{2}) {
		t.Fatalf("Copies = %v, %v; want [2]", copies, err)
	}
	if err := p.RemoveCopy(context.Background(), svc.Slug, 2, 5); err != nil {
		t.Fatalf("RemoveCopy: %v", err)
	}
	if got := managedNames(docker, svc.Slug); len(got) != 0 {
		t.Fatalf("containers after RemoveCopy = %v, want none", got)
	}
}
