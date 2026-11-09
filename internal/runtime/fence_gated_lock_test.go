package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// A fence-gated start holds the reconcile lock from the fence check to the start,
// so a reconcile that turns isolation off cannot reduce the fence in between.
func TestAFenceGatedStartWaitsForAReconcile(t *testing.T) {
	docker := harness.NewDocker()
	t.Cleanup(docker.Close)
	t.Setenv("DOCKER_HOST", docker.Host())
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")

	p := NewDockerProvider()
	svc := mysteriumShaped()
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "mysterium", Service: svc}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	docker.SetState("cashpilot-mysterium", "exited")

	p.fence.reconcileMu.Lock() // a reconcile in progress
	done := make(chan error, 1)
	go func() { done <- p.Start(context.Background(), "mysterium") }()
	time.Sleep(300 * time.Millisecond)
	if c, _ := docker.Container("cashpilot-mysterium"); c.State == "running" {
		p.fence.reconcileMu.Unlock()
		t.Fatal("the node started while a reconcile held the fence")
	}
	p.fence.reconcileMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start never finished after the reconcile")
	}
	if c, _ := docker.Container("cashpilot-mysterium"); c.State != "running" {
		t.Fatalf("state = %q, want running", c.State)
	}
}

// An ordinary earner's start takes no lock, so it never waits on the fence helper.
func TestAnOrdinaryStartDoesNotWaitForAReconcile(t *testing.T) {
	docker := harness.NewDocker()
	t.Cleanup(docker.Close)
	t.Setenv("DOCKER_HOST", docker.Host())
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")

	p := NewDockerProvider()
	svc := mysteriumShaped()
	svc.Slug, svc.Docker.FenceGated, svc.Docker.Runtime = "plain", false, ""
	svc.Docker.CapAdd, svc.Docker.Devices = nil, nil
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "plain", Service: svc}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	docker.SetState("cashpilot-plain", "exited")
	p.fence.reconcileMu.Lock()
	defer p.fence.reconcileMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- p.Start(context.Background(), "plain") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an ordinary start waited on the reconcile lock")
	}
}

func TestOnlyOnFence(t *testing.T) {
	on := func(mode string, nets ...string) container.InspectResponse {
		ns := map[string]*network.EndpointSettings{}
		for _, n := range nets {
			ns[n] = &network.EndpointSettings{}
		}
		return container.InspectResponse{
			HostConfig:      &container.HostConfig{NetworkMode: container.NetworkMode(mode)},
			NetworkSettings: &container.NetworkSettings{Networks: ns},
		}
	}
	if p := onlyOnFence(on(FenceNetwork, FenceNetwork)); p != "" {
		t.Fatalf("on the fence alone: %q", p)
	}
	for name, c := range map[string]container.InspectResponse{
		"host networking": on("host", "host"),
		"also elsewhere":  on(FenceNetwork, FenceNetwork, "bridge"),
		"not attached":    on(FenceNetwork),
		"no host config":  {},
		"no networks":     {HostConfig: &container.HostConfig{NetworkMode: FenceNetwork}},
	} {
		if onlyOnFence(c) == "" {
			t.Errorf("%s passed", name)
		}
	}
}

// A fence-gated deploy holds the same lock from the fence check to the start.
func TestAFenceGatedDeployWaitsForAReconcile(t *testing.T) {
	docker := harness.NewDocker()
	t.Cleanup(docker.Close)
	t.Setenv("DOCKER_HOST", docker.Host())
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")

	p := NewDockerProvider()
	p.fence.reconcileMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := p.Deploy(context.Background(), DeploySpec{Slug: "mysterium", Service: mysteriumShaped()}, nil)
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if _, ok := docker.Container("cashpilot-mysterium"); ok {
		p.fence.reconcileMu.Unlock()
		t.Fatal("the node was created while a reconcile held the fence")
	}
	p.fence.reconcileMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Deploy never finished after the reconcile")
	}
}
