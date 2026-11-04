package runtime

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// TestProxiedCopyFailsClosedLive drives a real Docker daemon: a proxied copy of a
// stand-in earner through a local SOCKS5 proxy with a login, then the proxy is
// stopped and a packet capture on the tunnel namespace's only real interface must
// see nothing leave but traffic to the proxy. The same capture is first shown to see
// a leak in a namespace opened on purpose, so a capture that cannot fail is caught.
//
// It needs a Linux container runtime, network access for the IP echo, and a tunnel
// image built from GeiserX/CashPilot's Dockerfile.tunnel:
//
//	CASHPILOT_PROXY_LIVE=1 CASHPILOT_TUNNEL_IMAGE=cashpilot-tunnel-local:0.0.0-test \
//	  go test ./internal/runtime -run TestProxiedCopyFailsClosedLive -v
func TestProxiedCopyFailsClosedLive(t *testing.T) {
	if os.Getenv("CASHPILOT_PROXY_LIVE") != "1" {
		t.Skip("set CASHPILOT_PROXY_LIVE=1 (and CASHPILOT_TUNNEL_IMAGE) to run against a real daemon")
	}
	const (
		gost     = "cp-mp-live-gost"
		slug     = "mp-live"
		pid      = int64(1)
		password = "live p@ss:word-SECRET"
	)
	earner, tunnel := proxiedName(slug, pid), tunnelName(slug, pid)
	dk := func(args ...string) (string, error) {
		out, err := exec.Command("docker", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := dk(args...)
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		return out
	}
	cleanup := func() {
		_, _ = dk("rm", "-f", "-v", earner, tunnel, gost)
	}
	cleanup()
	t.Cleanup(cleanup)

	// A capture image, built while the build still has a network.
	if _, err := dk("image", "inspect", "cp-mp-tcpdump:local"); err != nil {
		build := exec.Command("docker", "build", "-q", "-t", "cp-mp-tcpdump:local", "-")
		build.Stdin = strings.NewReader("FROM alpine:3.24.3\nRUN apk add --no-cache tcpdump\n")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build capture image: %v\n%s", err, out)
		}
	}

	// The proxy: gost with a user and a password that needs percent-encoding.
	must("run", "-d", "--name", gost, "gogost/gost:3.3.3", "-L", "socks5://puser:live%20p%40ss%3Aword-SECRET@:1080")
	gostIP := must("inspect", "-f", "{{.NetworkSettings.Networks.bridge.IPAddress}}", gost)
	if gostIP == "" {
		t.Fatal("the proxy got no bridge address")
	}

	svc := catalog.Service{Name: "Live earner", Slug: slug, Docker: catalog.DockerConfig{Image: "alpine:3.24.3", Command: "sleep 3600"}}
	p := NewDockerProvider()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	exit, err := p.DeployProxied(ctx, ProxiedDeploy{
		Slug: slug, ProxyID: pid, Service: svc,
		Proxy: ProxySpec{Kind: "socks5", Host: gostIP, Port: 1080, Username: "puser", Password: password},
	})
	if err != nil {
		t.Fatalf("DeployProxied: %v\ntunnel log:\n%s", err, func() string { out, _ := dk("logs", tunnel); return out }())
	}
	t.Logf("tunnel exit: %s %s", exit.IP, exit.Country)

	// Through the proxy, and only through it.
	ip, err := dk("exec", earner, "wget", "-qO-", "-T", "10", "https://api.ipify.org")
	if err != nil || ip == "" {
		t.Fatalf("the earner cannot reach the internet through the proxy: %v %s", err, ip)
	}
	if logs, _ := dk("logs", gost); !strings.Contains(logs, "api.ipify.org") {
		t.Fatalf("the proxy never saw the earner's connection:\n%s", logs)
	}
	if out, err := dk("exec", earner, "nslookup", "example.com"); err != nil {
		t.Fatalf("name lookups work through the tunnel: %v %s", err, out)
	}

	// No secret in what docker inspect shows of either container.
	for _, name := range []string{earner, tunnel} {
		if raw := must("inspect", name); strings.Contains(raw, "SECRET") || strings.Contains(raw, "p%40ss") {
			t.Fatalf("docker inspect %s shows the proxy password", name)
		}
	}
	if logs, _ := dk("logs", tunnel); strings.Contains(logs, "SECRET") || strings.Contains(logs, "api.ipify.org") {
		t.Fatalf("the tunnel log names a secret or a destination:\n%s", logs)
	}
	ins := must("inspect", "-f", "{{.HostConfig.NetworkMode}} {{.HostConfig.RestartPolicy.Name}} {{.HostConfig.Privileged}} {{.HostConfig.CapDrop}}", earner)
	if !strings.HasPrefix(ins, "container:") || !strings.Contains(ins, " no false [ALL]") {
		t.Fatalf("earner host config: %s", ins)
	}
	ins = must("inspect", "-f", "{{.HostConfig.CapAdd}} {{.HostConfig.Privileged}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}}", tunnel)
	// Docker reports added capabilities with or without the CAP_ prefix.
	if ins != "[NET_ADMIN] false true [ALL]" && ins != "[CAP_NET_ADMIN] false true [ALL]" {
		t.Fatalf("tunnel host config: %s", ins)
	}

	gateway := must("inspect", "-f", "{{.NetworkSettings.Networks.bridge.Gateway}}", tunnel)
	// leakTries is what an earner would do to get out on its own: the internet, the
	// bridge gateway, a LAN address, and DNS straight at a public resolver.
	leakTries := func() {
		for _, args := range [][]string{
			{"exec", earner, "wget", "-qO-", "-T", "3", "http://1.1.1.1"},
			{"exec", earner, "wget", "-qO-", "-T", "3", "http://" + gateway},
			{"exec", earner, "wget", "-qO-", "-T", "3", "http://192.168.10.1"},
			{"exec", earner, "nslookup", "-timeout=2", "leak-check-" + slug + ".example.org", "8.8.8.8"},
		} {
			_, _ = dk(args...)
		}
	}
	// capture counts IP packets on the namespace's real interface that are not the
	// tunnel's own connection to the proxy, while leakTries runs.
	capture := func() int {
		t.Helper()
		cmd := exec.Command("docker", "run", "--rm", "--net", "container:"+tunnel, "--cap-add", "NET_RAW", "--cap-add", "NET_ADMIN",
			"cp-mp-tcpdump:local", "timeout", "20", "tcpdump", "-n", "-l", "-i", "eth0", "ip and not (host "+gostIP+" and tcp port 1080)")
		var out strings.Builder
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		leakTries()
		_ = cmd.Wait()
		n := 0
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.Contains(line, " IP ") {
				n++
			}
		}
		t.Logf("capture saw %d packets", n)
		return n
	}

	// With the proxy down, nothing leaves.
	must("stop", gost)
	if out, err := dk("exec", earner, "wget", "-qO-", "-T", "5", "https://api.ipify.org"); err == nil {
		t.Fatalf("with the proxy down the earner still reached the internet: %s", out)
	}
	if n := capture(); n != 0 {
		t.Fatalf("with the proxy down, %d packets left the namespace other than to the proxy", n)
	}

	// The positive control: open the namespace on purpose and the same capture sees it.
	must("exec", tunnel, "nft", "delete", "table", "inet", "cashpilot")
	_, _ = dk("exec", tunnel, "ip", "route", "del", "default")
	must("exec", tunnel, "ip", "route", "add", "default", "via", gateway, "dev", "eth0")
	if n := capture(); n == 0 {
		t.Fatal("the capture saw nothing in a namespace opened on purpose, so its zero above proves nothing")
	}

	// A tunnel that comes back gets its earner back, but only once it is ready.
	must("start", gost)
	must("restart", tunnel)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := p.ProxiedStatus(ctx); err != nil {
			t.Fatal(err)
		}
		if out, err := dk("exec", earner, "wget", "-qO-", "-T", "5", "https://api.ipify.org"); err == nil && out != "" {
			t.Logf("earner back through the restarted tunnel: %s", out)
			removed, err := p.RemoveProxied(ctx, slug, pid, 2)
			if err != nil || len(removed) != 2 {
				t.Fatalf("remove: %v %v", removed, err)
			}
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatal("the earner never came back after its tunnel restarted")
}
