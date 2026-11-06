package runtime

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/client"
)

// TestFenceIntegration proves the fence on a real daemon: an earner deployed on a fresh
// install lands on the fenced network, reaches the internet, and cannot reach private
// ranges, with the table's own counters as the evidence (nothing need answer on the LAN
// for the check to mean something, which is what makes it work on Docker Desktop).
//
// It writes an nftables table into the daemon's network namespace and creates a
// network, so it runs only when asked: CASHPILOT_FENCE_LIVE=1, on a throwaway daemon.
// CASHPILOT_TEST_FENCE_IMAGE points the helper at a local build of the worker image.
func TestFenceIntegration(t *testing.T) {
	if os.Getenv("CASHPILOT_FENCE_LIVE") != "1" {
		t.Skip("set CASHPILOT_FENCE_LIVE=1 to write the LAN fence on this daemon")
	}
	useAmbientDocker(t)
	if image := os.Getenv("CASHPILOT_TEST_FENCE_IMAGE"); image != "" {
		old := fenceHelperImage
		fenceHelperImage = image
		t.Cleanup(func() { fenceHelperImage = old })
	}
	ctx := context.Background()
	cli, err := dockerClient()
	if err != nil {
		t.Fatalf("dockerClient: %v", err)
	}
	defer cli.Close()

	const probeImage = "busybox:1.37.0"
	if err := pullImage(ctx, cli, probeImage, nil); err != nil {
		t.Skipf("could not pull %s: %v", probeImage, err)
	}
	slug := fmt.Sprintf("fence-it%d", time.Now().UnixNano())
	svc := catalog.Service{Name: "Fence probe", Docker: catalog.DockerConfig{Image: probeImage, Command: "sleep 600"}}

	p := &DockerProvider{}
	p.SetIsolationSetting(func() string { return "on" })
	info, err := p.Deploy(ctx, DeploySpec{Slug: slug, Service: svc}, nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	t.Cleanup(func() { _ = p.Remove(context.Background(), slug, RemoveOptions{DeleteData: true, AllowCritical: true}) })

	live, err := cli.ContainerInspect(ctx, info.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if mode := string(live.Container.HostConfig.NetworkMode); mode != FenceNetwork {
		t.Fatalf("the earner is on %q, not the fenced network", mode)
	}

	run := func(cmd ...string) int {
		_, code, err := execOne(ctx, cli, info.ContainerID, cmd, 15*time.Second)
		if err != nil {
			t.Fatalf("exec %v: %v", cmd, err)
		}
		return code
	}
	// The internet works: the fence's own masquerade is the only NAT on this bridge.
	if code := run("nc", "-z", "-w", "5", "1.1.1.1", "443"); code != 0 {
		t.Errorf("the earner cannot reach 1.1.1.1:443 (exit %d): the fence cut the internet too", code)
	}

	net, err := managedNetwork(ctx, cli)
	if err != nil || net == nil {
		t.Fatalf("our network is missing: %v", err)
	}
	subnet, _ := networkSubnet(net)
	read := func() map[string]int {
		res, err := runHelper(ctx, cli, "read", subnet, true)
		if err != nil {
			t.Fatalf("read the counters: %v", err)
		}
		return res.Counters
	}
	before := read()
	gateway := ""
	for _, c := range net.IPAM.Config {
		if c.Gateway.IsValid() {
			gateway = c.Gateway.String()
		}
	}
	probes := []struct{ rule, addr, port string }{
		{"rfc1918-10", "10.255.255.254", "80"},
		{"rfc1918-172", "172.31.255.254", "80"},
		{"rfc1918-192", "192.168.255.254", "80"},
		{"cgnat", "100.127.255.254", "80"},
		{"link-local", "169.254.169.254", "80"},
		{"host", gateway, "22"},
	}
	for _, probe := range probes {
		if code := run("nc", "-z", "-w", "2", probe.addr, probe.port); code == 0 {
			t.Errorf("the earner reached %s:%s", probe.addr, probe.port)
		}
	}
	after := read()
	for _, probe := range probes {
		if after[probe.rule] <= before[probe.rule] {
			t.Errorf("rule %s did not count the probe to %s (before %d, after %d): the drop is not what stopped it",
				probe.rule, probe.addr, before[probe.rule], after[probe.rule])
		}
	}
	if st := p.FenceStatus(); st.State != "applied" || st.Backend == "" {
		t.Errorf("status %+v", st)
	}
}
