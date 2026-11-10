package runtime

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness"
)

// gatedDaemon is the fake daemon with Mysterium deployed behind the fence and
// running, and a provider whose stop record lives in stoppedFile.
func gatedDaemon(t *testing.T, stoppedFile string) (*harness.Docker, *DockerProvider) {
	t.Helper()
	docker := harness.NewDocker()
	t.Cleanup(docker.Close)
	t.Setenv("DOCKER_HOST", docker.Host())
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	p := gatedProvider(stoppedFile)
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "mysterium", Service: mysteriumShaped()}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	return docker, p
}

func gatedProvider(stoppedFile string) *DockerProvider {
	p := NewDockerProvider()
	p.SetGatedStoppedFile(stoppedFile)
	return p
}

func nodeState(docker *harness.Docker) string {
	c, _ := docker.Container("cashpilot-mysterium")
	return c.State
}

// THE RULE: Docker never starts a fence-gated node by itself, so a reboot cannot
// bring it up before the fence. Every other earner keeps unless-stopped.
func TestAFenceGatedNodeHasNoRestartPolicy(t *testing.T) {
	host, err := buildHostConfig(mysteriumShaped(), nil, nil, "linux")
	if err != nil {
		t.Fatalf("buildHostConfig: %v", err)
	}
	if host.RestartPolicy.Name != "no" {
		t.Fatalf("restart policy = %q, want no: Docker would start the node after a reboot before the fence is back", host.RestartPolicy.Name)
	}
	plain := mysteriumShaped()
	plain.Docker.FenceGated = false
	if host, _ := buildHostConfig(plain, nil, nil, "linux"); host.RestartPolicy.Name != "unless-stopped" {
		t.Fatalf("an ordinary earner got restart policy %q, want unless-stopped", host.RestartPolicy.Name)
	}

	docker, _ := gatedDaemon(t, filepath.Join(t.TempDir(), "stopped"))
	if c, _ := docker.Container("cashpilot-mysterium"); c.RestartPolicy != "no" {
		t.Fatalf("the deployed node has restart policy %q, want no", c.RestartPolicy)
	}
}

// After a reboot the node is down; the app's first reconcile starts it, and only
// after the fence helper has applied the table.
func TestAReconcileStartsANodeLeftDownOnlyAfterTheFence(t *testing.T) {
	docker, p := gatedDaemon(t, filepath.Join(t.TempDir(), "stopped"))
	node, _ := docker.Container("cashpilot-mysterium")
	docker.SetState("cashpilot-mysterium", "exited") // the reboot
	docker.ResetCalls()

	p.ReconcileFence(context.Background())
	if got := nodeState(docker); got != "running" {
		t.Fatalf("state = %q after the reconcile, want running (status %+v)", got, p.FenceStatus())
	}
	calls := docker.Calls()
	helper := slices.Index(calls, "FENCE POST /containers/"+fenceHelperName+"/start")
	if helper < 0 {
		helper = slices.IndexFunc(calls, func(c string) bool {
			return strings.HasPrefix(c, "FENCE POST /containers/") && strings.HasSuffix(c, "/start")
		})
	}
	start := slices.Index(calls, "POST /containers/"+node.ID+"/start")
	if helper < 0 || start < 0 || start < helper {
		t.Fatalf("want the fence helper before the node's start, got %v", calls)
	}
	if st := p.FenceStatus(); len(st.Waiting) != 0 || st.Error != "" {
		t.Fatalf("status = %+v, want nothing waiting and no error", st)
	}
}

// A fence that fails to apply starts nothing, and the node is named as waiting.
func TestAFailedFenceStartsNothing(t *testing.T) {
	node := map[string]any{
		"Id": "myst", "Names": []string{"/cashpilot-mysterium"}, "State": "exited",
		"Labels":          map[string]string{LabelManaged: "true", LabelService: "mysterium", LabelFenceGated: "true"},
		"HostConfig":      map[string]any{"NetworkMode": FenceNetwork},
		"NetworkSettings": map[string]any{"Networks": map[string]any{FenceNetwork: map[string]any{"NetworkID": "fencenet"}}},
	}
	for _, tc := range []struct {
		exit    int
		started bool
	}{{exit: 0, started: true}, {exit: 1, started: false}} {
		fake := &fakeDocker{earners: []map[string]any{node}, fenceExit: tc.exit}
		fake.start(t)
		p := NewDockerProvider()
		p.SetIsolationSetting(func() string { return "on" })
		p.ReconcileFence(context.Background())
		started := slices.Contains(fake.recorded(), "POST /containers/myst/start")
		if started != tc.started {
			t.Fatalf("helper exit %d: node started = %v, want %v (calls %v)", tc.exit, started, tc.started, fake.recorded())
		}
		if !tc.started && !slices.Equal(p.FenceStatus().Waiting, []string{"mysterium"}) {
			t.Fatalf("helper exit %d: waiting = %v, want mysterium", tc.exit, p.FenceStatus().Waiting)
		}
	}
}

// THE RULE: a stop the user asked for stays a stop, across checks and across the
// window and the daemon (two providers sharing the record). Start lifts it, and a
// node that then crashes comes back.
func TestAStopFromTheUserStaysAStop(t *testing.T) {
	record := filepath.Join(t.TempDir(), "stopped")
	docker, p := gatedDaemon(t, record)
	if err := p.Stop(context.Background(), "mysterium", 5); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	p.ReconcileFence(context.Background())
	other := gatedProvider(record) // the background daemon, reading the same record
	other.ReconcileFence(context.Background())
	if got := nodeState(docker); got != "exited" {
		t.Fatalf("state = %q, want the user's stop kept", got)
	}
	if w := p.FenceStatus().Waiting; len(w) != 0 {
		t.Fatalf("a node the user stopped is named waiting: %v", w)
	}

	if err := p.Start(context.Background(), "mysterium"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	docker.SetState("cashpilot-mysterium", "exited") // a crash
	other.ReconcileFence(context.Background())
	if got := nodeState(docker); got != "running" {
		t.Fatalf("state = %q, want the crashed node started again after the user's Start", got)
	}
}

// A Stop whose record cannot be written does not stop the node: unrecorded, the
// next check would start it again.
func TestAStopThatCannotBeRecordedIsRefused(t *testing.T) {
	docker, p := gatedDaemon(t, filepath.Join(t.TempDir(), "missing-dir", "stopped"))
	err := p.Stop(context.Background(), "mysterium", 5)
	if err == nil || !strings.Contains(err.Error(), "could not be recorded") {
		t.Fatalf("Stop = %v, want a refusal", err)
	}
	if got := nodeState(docker); got != "running" {
		t.Fatalf("state = %q, want the node left running", got)
	}
}

// The guard brings a crashed node back without waiting for the five-minute
// recheck, and does nothing while the fence has not been applied.
func TestTheGuardStartsACrashedNode(t *testing.T) {
	record := filepath.Join(t.TempDir(), "stopped")
	docker, _ := gatedDaemon(t, record)
	docker.SetState("cashpilot-mysterium", "exited")
	p := gatedProvider(record) // the app just started: no check has run yet
	p.GuardFenceGated(context.Background())
	if got := nodeState(docker); got != "exited" {
		t.Fatalf("state = %q, want nothing started before a reconcile applied the fence", got)
	}

	p.ReconcileFence(context.Background())
	docker.SetState("cashpilot-mysterium", "exited") // a crash after the fence is up
	p.GuardFenceGated(context.Background())
	if got := nodeState(docker); got != "running" {
		t.Fatalf("state = %q, want the guard to start the crashed node", got)
	}
}

// Turning isolation off stops the node; that stop is the fence's, not the user's, so
// turning isolation on again starts it.
func TestTurningIsolationOnAgainStartsTheNode(t *testing.T) {
	docker, p := gatedDaemon(t, filepath.Join(t.TempDir(), "stopped"))
	setting := "off"
	p.SetIsolationSetting(func() string { return setting })
	p.ReconcileFence(context.Background())
	if got := nodeState(docker); got != "exited" {
		t.Fatalf("state = %q, want the node stopped with isolation off", got)
	}
	if w := p.FenceStatus().Waiting; !slices.Equal(w, []string{"mysterium"}) {
		t.Fatalf("waiting = %v, want mysterium", w)
	}
	setting = "on"
	p.ReconcileFence(context.Background())
	if got := nodeState(docker); got != "running" {
		t.Fatalf("state = %q, want the node started once isolation is on again", got)
	}
}

// An upgrade: a node made with unless-stopped is moved to restart policy "no" in
// place. A running one keeps running and comes back after a crash; a stopped one
// stays stopped, as Docker would have left it, until the user starts it.
func TestAnUpgradedNodeLosesItsRestartPolicy(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		docker, p := gatedDaemon(t, filepath.Join(t.TempDir(), "stopped"))
		docker.SetRestartPolicy("cashpilot-mysterium", "unless-stopped")
		p.ReconcileFence(context.Background())
		if c, _ := docker.Container("cashpilot-mysterium"); c.RestartPolicy != "no" || c.State != "running" {
			t.Fatalf("policy %q, state %q: want no and still running", c.RestartPolicy, c.State)
		}
		docker.SetState("cashpilot-mysterium", "exited")
		p.ReconcileFence(context.Background())
		if got := nodeState(docker); got != "running" {
			t.Fatalf("state = %q, want the crashed node started", got)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		docker, p := gatedDaemon(t, filepath.Join(t.TempDir(), "stopped"))
		docker.SetRestartPolicy("cashpilot-mysterium", "unless-stopped")
		docker.SetState("cashpilot-mysterium", "exited")
		p.ReconcileFence(context.Background())
		if c, _ := docker.Container("cashpilot-mysterium"); c.RestartPolicy != "no" || c.State != "exited" {
			t.Fatalf("policy %q, state %q: want no and still stopped", c.RestartPolicy, c.State)
		}
		if err := p.Start(context.Background(), "mysterium"); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if got := nodeState(docker); got != "running" {
			t.Fatalf("state = %q after Start", got)
		}
	})
}

// THE RULE: a fence that fails to apply never tells the user of a fence-gated
// earner to turn isolation off, which would refuse that earner outright. Other
// earners still get that advice.
func TestAFailedFenceDoesNotSendAGatedEarnerToIsolationOff(t *testing.T) {
	fake := &fakeDocker{fenceExit: 1}
	fake.start(t)
	cli, err := dockerClient()
	if err != nil {
		t.Fatalf("dockerClient: %v", err)
	}
	defer cli.Close()
	p := NewDockerProvider()
	p.SetIsolationSetting(func() string { return "on" })

	_, err = p.fence.networkForDeploy(context.Background(), cli, true)
	if err == nil || strings.Contains(err.Error(), "off in Settings") || !strings.Contains(err.Error(), "would not let it run") {
		t.Fatalf("gated: %v, want advice that does not point at isolation off", err)
	}
	_, err = p.fence.networkForDeploy(context.Background(), cli, false)
	if err == nil || !strings.Contains(err.Error(), "off in Settings") {
		t.Fatalf("ordinary: %v, want the isolation-off advice kept", err)
	}
}

// A stop record that cannot be read starts nothing: it may hold the user's stop.
func TestAnUnreadableStopRecordStartsNothing(t *testing.T) {
	record := t.TempDir() // a directory, so reading it as the record fails
	docker, p := gatedDaemon(t, record)
	docker.SetState("cashpilot-mysterium", "exited")
	p.ReconcileFence(context.Background())
	if got := nodeState(docker); got != "exited" {
		t.Fatalf("state = %q, want nothing started without the stop record", got)
	}
	if st := p.FenceStatus(); !strings.Contains(st.Error, "none was started") {
		t.Fatalf("status error = %q, want the unread record named", st.Error)
	}
}

// A node the daemon refuses to start is named in the status, not dropped.
func TestAStartTheDaemonRefusesIsNamed(t *testing.T) {
	node := map[string]any{
		"Id": "myst", "Names": []string{"/cashpilot-mysterium"}, "State": "exited",
		"Labels":          map[string]string{LabelManaged: "true", LabelService: "mysterium", LabelFenceGated: "true"},
		"HostConfig":      map[string]any{"NetworkMode": FenceNetwork},
		"NetworkSettings": map[string]any{"Networks": map[string]any{FenceNetwork: map[string]any{"NetworkID": "fencenet"}}},
	}
	fake := &fakeDocker{earners: []map[string]any{node}, startRefusal: "port is already allocated"}
	fake.start(t)
	p := NewDockerProvider()
	p.SetIsolationSetting(func() string { return "on" })
	p.ReconcileFence(context.Background())
	if st := p.FenceStatus(); !strings.Contains(st.Error, "could not start cashpilot-mysterium") || !strings.Contains(st.Error, "already allocated") {
		t.Fatalf("status error = %q, want the refused start named", st.Error)
	}
}

// A node whose restart policy cannot be read or changed is named: Docker may still
// start it after a reboot.
func TestARestartPolicyThatCannotBeChangedIsNamed(t *testing.T) {
	docker, p := gatedDaemon(t, filepath.Join(t.TempDir(), "stopped"))
	docker.FailInspect("cashpilot-mysterium")
	p.ReconcileFence(context.Background())
	if st := p.FenceStatus(); !strings.Contains(st.Error, "could not take Docker's restart policy off cashpilot-mysterium") {
		t.Fatalf("status error = %q, want the node named", st.Error)
	}
}
