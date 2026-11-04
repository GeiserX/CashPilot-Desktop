package runtime_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// A custom service is the user's own, unreviewed image. What the daemon is sent for
// it must be exactly the hardening every catalog service gets and nothing more: every
// capability dropped, no-new-privileges, a PID ceiling, its memory limit, never
// privileged, the bridge network, no devices, no published ports, and one mount, its
// own named volume. The assertions are on the HostConfig, not on the definition.
func TestCustomServiceHostConfigIsHardened(t *testing.T) {
	def := services.CustomDefinition{
		Name: "Node", Image: "org/node:1", DataPath: "/data", MemLimit: "256m",
		Env: []services.CustomEnv{{Key: "TOKEN", Value: "s3cret", Secret: true}},
	}
	svc := services.CustomAsService("custom-node", def, true)
	ports, bindings, err := runtime.BuildPorts(svc.Docker.Ports)
	if err != nil {
		t.Fatal(err)
	}
	env := runtime.BuildEnv(svc, nil)
	mounts := runtime.BuildMounts(svc.Docker.Volumes, env)
	hc, err := runtime.BuildHostConfig(svc, bindings, mounts, "linux")
	if err != nil {
		t.Fatalf("BuildHostConfig: %v", err)
	}
	if !slices.Equal(hc.CapDrop, []string{"ALL"}) || len(hc.CapAdd) != 0 {
		t.Fatalf("capabilities: drop %v add %v", hc.CapDrop, hc.CapAdd)
	}
	if !slices.Contains(hc.SecurityOpt, "no-new-privileges:true") {
		t.Fatalf("SecurityOpt = %v", hc.SecurityOpt)
	}
	if hc.PidsLimit == nil || *hc.PidsLimit <= 0 {
		t.Fatalf("PidsLimit = %v", hc.PidsLimit)
	}
	if hc.Memory != 256<<20 {
		t.Fatalf("Memory = %d, want 256 MiB", hc.Memory)
	}
	if hc.Privileged || string(hc.NetworkMode) != "bridge" || len(hc.Resources.Devices) != 0 {
		t.Fatalf("privileged %v network %q devices %v", hc.Privileged, hc.NetworkMode, hc.Resources.Devices)
	}
	if len(ports) != 0 || len(hc.PortBindings) != 0 {
		t.Fatalf("ports published: %v %v", ports, hc.PortBindings)
	}
	if len(hc.Mounts) != 1 || hc.Mounts[0].Type != mount.TypeVolume || hc.Mounts[0].Source != "cashpilot-custom-node-data" || hc.Mounts[0].Target != "/data" {
		t.Fatalf("mounts = %+v, want only its own named volume", hc.Mounts)
	}
	if env["TOKEN"] != "s3cret" {
		t.Fatalf("env = %v, want the saved value", env)
	}
}

// The command's ${NAME} is replaced by the variable's value, and the whole line is
// split into arguments, never handed to a shell.
func TestCustomServiceCommand(t *testing.T) {
	svc := services.CustomAsService("custom-node", services.CustomDefinition{
		Name: "Node", Image: "org/node:1", Command: "run --token ${TOKEN}; rm -rf /",
		Env: []services.CustomEnv{{Key: "TOKEN", Value: "abc"}},
	}, true)
	args := runtime.BuildCommand(svc.Docker.Command, runtime.BuildEnv(svc, nil))
	if strings.Join(args, "|") != "run|--token|abc;|rm|-rf|/" {
		t.Fatalf("args = %q", args)
	}
}

func TestCustomContainerLabel(t *testing.T) {
	if got := runtime.ContainerLabels("custom-node", 1)[runtime.LabelCustom]; got != "true" {
		t.Fatalf("custom label = %q, want true", got)
	}
	if _, ok := runtime.ContainerLabels("honeygain", 1)[runtime.LabelCustom]; ok {
		t.Fatal("a catalog service carries the custom label")
	}
}

// The same rule against a real daemon: only the daemon can say it accepted and
// applied what CashPilot sent. A custom service is deployed, its container inspected,
// then removed with its data.
func TestCustomServiceDeployIsHardenedIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker integration test in -short mode")
	}
	runtime.UseAmbientDocker(t)
	ctx := context.Background()
	const probeImage = "busybox:1.37.0" // pinned tag, never :latest
	cli, err := runtime.DockerClient()
	if err != nil {
		t.Fatalf("dockerClient: %v", err)
	}
	defer cli.Close()
	if err := runtime.PullImage(ctx, cli, probeImage, nil); err != nil {
		t.Skipf("could not pull %s: %v", probeImage, err)
	}

	slug := fmt.Sprintf("custom-it%d", time.Now().UnixNano())
	svc := services.CustomAsService(slug, services.CustomDefinition{
		Name: "Integration", Image: probeImage, DataPath: "/data", MemLimit: "64m",
		Command: "sh -c 'trap \"exit 0\" TERM; echo $TOKEN > /data/token; while true; do sleep 1; done'",
		Env:     []services.CustomEnv{{Key: "TOKEN", Value: "s3cret", Secret: true}},
	}, true)
	p := &runtime.DockerProvider{}
	defer func() {
		_ = p.Remove(context.Background(), slug, runtime.RemoveOptions{DeleteData: true})
	}()
	if _, err := p.Deploy(ctx, runtime.DeploySpec{Slug: slug, Service: svc}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	name := "cashpilot-" + slug
	runtime.AssertHardened(t, ctx, name)

	result, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	hc, cfg := result.Container.HostConfig, result.Container.Config
	if len(hc.CapAdd) != 0 || hc.Privileged || string(hc.NetworkMode) != "bridge" || len(hc.PortBindings) != 0 || len(hc.Resources.Devices) != 0 {
		t.Fatalf("grants on the live container: capadd %v privileged %v network %q ports %v devices %v",
			hc.CapAdd, hc.Privileged, hc.NetworkMode, hc.PortBindings, hc.Resources.Devices)
	}
	if hc.Memory != 64<<20 {
		t.Fatalf("Memory = %d, want 64 MiB", hc.Memory)
	}
	mounts := result.Container.Mounts
	if len(mounts) != 1 || string(mounts[0].Type) != "volume" || mounts[0].Name != services.CustomVolumeName(slug) || mounts[0].Destination != "/data" {
		t.Fatalf("mounts = %+v, want only %s at /data", mounts, services.CustomVolumeName(slug))
	}
	if cfg.Labels[runtime.LabelCustom] != "true" {
		t.Fatalf("labels = %v, want %s=true", cfg.Labels, runtime.LabelCustom)
	}
	if !slices.Contains(cfg.Env, "TOKEN=s3cret") {
		t.Fatalf("env = %v, want the saved variable", cfg.Env)
	}
	if err := p.Remove(ctx, slug, runtime.RemoveOptions{DeleteData: true}); err != nil {
		t.Fatalf("Remove with its data: %v", err)
	}
	if _, err := cli.VolumeInspect(ctx, services.CustomVolumeName(slug), client.VolumeInspectOptions{}); err == nil {
		t.Fatal("the data volume survived a remove that asked for it to go")
	}
}
