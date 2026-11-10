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

	// A replacement made by hand under the name, as a deploy that died after its
	// create (started false) or after its start (started true) leaves it.
	replacement := func(t *testing.T, started bool) string {
		t.Helper()
		cli, err := dockerClient()
		if err != nil {
			t.Fatal(err)
		}
		defer cli.Close()
		made, err := cli.ContainerCreate(context.Background(), client.ContainerCreateOptions{
			Name:   node,
			Config: &container.Config{Image: svc.Docker.Image, Labels: containerLabels(svc.Slug, 1)},
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
