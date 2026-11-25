package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/siaseed"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// TestLiveSeedFileRunsHostd runs the wallet steps against the real daemon with the
// catalog's pinned hostd: a new wallet made offline and written into a throwaway
// volume, then hostd started on that volume with no seed variable at all. hostd's
// own API must report the wallet the generator printed, which must also be the
// address siaseed derives. Nothing is printed or kept; everything made here is
// removed by name.
//
//	CASHPILOT_SEEDFILE_LIVE=1 go test -run TestLiveSeedFileRunsHostd ./internal/runtime/
func TestLiveSeedFileRunsHostd(t *testing.T) {
	if os.Getenv("CASHPILOT_SEEDFILE_LIVE") != "1" {
		t.Skip("set CASHPILOT_SEEDFILE_LIVE=1 to run against the real daemon")
	}
	cat, err := catalog.LoadEmbedded(os.DirFS("../.."))
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	svc, ok := cat.Get("sia-hostd")
	if !ok {
		t.Fatal("no sia-hostd")
	}
	ctx := context.Background()
	cli, err := dockerClient()
	if err != nil {
		t.Fatalf("docker: %v", err)
	}
	defer cli.Close()

	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "cpd-seedtest-" + hex.EncodeToString(suffix)
	volume := name + "-data"
	image := svc.Docker.Image
	if err := pullImage(ctx, cli, image, nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, n := range []string{name, name + "-wallet", name + "-wallet-new"} {
			_, _ = cli.ContainerRemove(ctx, n, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
		if _, err := cli.VolumeRemove(ctx, volume, client.VolumeRemoveOptions{}); err != nil {
			t.Errorf("could not remove %s: %v", volume, err)
		}
	})

	mounts := []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: "/data"}}
	result, err := prepareWallet(ctx, cli, svc, image, name, mounts, &WalletRequest{Mode: WalletGenerate}, "", nil)
	if err != nil {
		t.Fatalf("prepareWallet: %v", err)
	}
	if !result.Created || result.Phrase == "" || len(result.Address) != 76 {
		t.Fatalf("no new wallet came back (created %v, address %d chars)", result.Created, len(result.Address))
	}
	derived, err := siaseed.Address(result.Phrase)
	if err != nil || derived != result.Address {
		t.Fatalf("siaseed derives a different address from hostd's own generator (err %v)", err)
	}
	for _, n := range []string{name + "-wallet", name + "-wallet-new"} {
		if _, err := cli.ContainerInspect(ctx, n, client.ContainerInspectOptions{}); err == nil {
			t.Errorf("%s was left behind", n)
		}
	}

	// The file: root-only, holding the phrase, read back through a helper.
	helper, err := createFileHelper(ctx, cli, image, name+"-wallet", mounts[0], "/data")
	if err != nil {
		t.Fatalf("helper: %v", err)
	}
	res, err := cli.CopyFromContainer(ctx, helper, client.CopyFromContainerOptions{SourcePath: "/data/hostd.yml"})
	removeHelper(cli, helper)
	if err != nil {
		t.Fatalf("read the file: %v", err)
	}
	_ = res.Content.Close()
	if res.Stat.Mode.Perm() != 0o600 {
		t.Errorf("hostd.yml is mode %o, want 600", res.Stat.Mode.Perm())
	}

	// A second prepare keeps the file and makes nothing.
	again, err := prepareWallet(ctx, cli, svc, image, name, mounts, &WalletRequest{Mode: WalletGenerate}, "", nil)
	if err != nil || again.Created || again.Phrase != "" || again.Address != result.Address {
		t.Fatalf("a second prepare did not keep the wallet (err %v)", err)
	}

	// hostd on the volume, no seed variable, its API on a free loopback port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	api, _ := network.ParsePort("9980/tcp")
	env := envSlice(buildEnv(svc, map[string]string{"HOSTD_WALLET_SEED": "must never reach the container", "HOSTD_API_PASSWORD": "seedtest-password"}))
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        image,
			Env:          env,
			ExposedPorts: network.PortSet{api: {}},
			Labels:       map[string]string{"cashpilot.seedtest": "true"},
		},
		HostConfig: &container.HostConfig{
			Mounts:       mounts,
			PortBindings: network.PortMap{api: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: port}}},
			LogConfig:    container.LogConfig{Type: "json-file"},
		},
	})
	if err != nil {
		t.Fatalf("create hostd: %v", err)
	}
	inspected, err := cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect hostd: %v", err)
	}
	for _, kv := range inspected.Container.Config.Env {
		if strings.HasPrefix(kv, "HOSTD_WALLET_SEED=") || strings.Contains(kv, result.Phrase) {
			t.Fatal("the seed reached hostd's environment")
		}
	}
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start hostd: %v", err)
	}

	var got struct {
		Address string `json:"address"`
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+port+"/api/wallet", nil)
		req.SetBasicAuth("", "seedtest-password")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && json.Unmarshal(body, &got) == nil && got.Address != "" {
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if got.Address != result.Address {
		t.Fatalf("hostd runs wallet %q, want the one written into the file (%q)", got.Address, result.Address)
	}
	result.Phrase = ""
}
