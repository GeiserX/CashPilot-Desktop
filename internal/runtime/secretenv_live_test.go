package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/client"
)

// TestEarnerSecretsLive deploys through the real Deploy on a real daemon, with
// CASHPILOT_EARNER_SECRETS=file and the real start program built for the daemon:
//
//   - first with the setting off, where docker inspect must show the password (the
//     check can fail);
//
//   - then on, where it must not, while the earner still gets the password in its
//     environment and on its command line, after docker restart too;
//
//   - then the real Honeygain image, whose user is the named appuser and whose
//     entrypoint is the relative ./honeygain, started with every capability dropped.
//
//     CASHPILOT_SECRETS_LIVE=1 go test ./internal/runtime -run TestEarnerSecretsLive -v
func TestEarnerSecretsLive(t *testing.T) {
	if os.Getenv("CASHPILOT_SECRETS_LIVE") != "1" {
		t.Skip("set CASHPILOT_SECRETS_LIVE=1 to deploy into a real Docker daemon")
	}
	useAmbientDocker(t)
	cli, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	family := ArchFamily(daemonFacts(ctx, cli).Architecture)

	// The program a release embeds, built here for the daemon's architecture.
	bin := filepath.Join(t.TempDir(), "cashpilot-env")
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", bin, "../../cmd/cashpilot-env")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+family)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the start program: %v\n%s", err, out)
	}
	shim, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	old := shimFor
	shimFor = func(f string) []byte {
		if f == family {
			return shim
		}
		return nil
	}
	t.Cleanup(func() { shimFor = old })

	const password = "live p@ss:word-SECRET"
	logs := func(name string) string {
		out, _ := exec.Command("docker", "logs", name).CombinedOutput()
		return string(out)
	}
	inspectHas := func(name string) int {
		got, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(got.Container)
		return strings.Count(string(raw), password)
	}
	waitFor := func(name, text string, times int) {
		t.Helper()
		for i := 0; i < 60; i++ {
			if strings.Count(logs(name), text) >= times {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s never logged %q %d times:\n%s", name, text, times, logs(name))
	}

	p := NewDockerProvider()
	const slug = "secrets-live-test"
	name := containerName(slug)
	svc := catalog.Service{Name: "Secrets live test", Slug: slug, Docker: catalog.DockerConfig{
		Image:       "alpine:3.22",
		NetworkMode: "none",
		Command:     `sh -c 'test "$LIVE_PASSWORD" = "$0" && echo "password in env and argv"; sleep 600' ${LIVE_PASSWORD}`,
		Env:         []catalog.EnvVar{{Key: "LIVE_PASSWORD", Secret: true}},
	}}
	remove := func(n string) {
		_, _ = cli.ContainerRemove(context.Background(), n, client.ContainerRemoveOptions{Force: true})
	}
	remove(name)
	t.Cleanup(func() { remove(name) })
	deploy := func() {
		t.Helper()
		if _, err := p.Deploy(ctx, DeploySpec{Slug: slug, Service: svc, Env: map[string]string{"LIVE_PASSWORD": password}}, func(m string) { t.Log(m) }); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv(EarnerSecretsEnv, "")
	deploy()
	waitFor(name, "password in env and argv", 1)
	if n := inspectHas(name); n == 0 {
		t.Fatal("with the setting off docker inspect shows no password, so the check below cannot fail")
	} else {
		t.Logf("setting off: the password is %d times in docker inspect", n)
	}

	t.Setenv(EarnerSecretsEnv, "file")
	deploy()
	waitFor(name, "password in env and argv", 1)
	if n := inspectHas(name); n != 0 {
		t.Fatalf("setting on: the password is %d times in docker inspect", n)
	}
	if _, err := cli.ContainerRestart(ctx, name, client.ContainerRestartOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(name, "password in env and argv", 2)
	t.Log("setting on: 0 times in docker inspect; the earner got it, after docker restart too")

	// The real Honeygain image: a named user and a relative entrypoint. -h prints the
	// usage and exits; the password rides on the command line, so the program must
	// read the file as appuser and fill it in before ./honeygain runs.
	const hgSlug = "secrets-live-honeygain"
	hgName := containerName(hgSlug)
	remove(hgName)
	t.Cleanup(func() { remove(hgName) })
	hg := catalog.Service{Name: "Secrets live Honeygain", Slug: hgSlug, Docker: catalog.DockerConfig{
		Image:       "honeygain/honeygain@sha256:159d120caac9f482074d3e8d82d4cc247cd07e65af37bcc8e6b89c60674d5ed4",
		NetworkMode: "none",
		Command:     "-h ${HONEYGAIN_PASSWORD}",
		Env:         []catalog.EnvVar{{Key: "HONEYGAIN_PASSWORD", Secret: true}},
	}}
	if _, err := p.Deploy(ctx, DeploySpec{Slug: hgSlug, Service: hg, Env: map[string]string{"HONEYGAIN_PASSWORD": password}}, func(m string) { t.Log(m) }); err != nil {
		t.Fatal(err)
	}
	waitFor(hgName, "Usage of ./honeygain", 1)
	if strings.Contains(logs(hgName), "cashpilot-env:") {
		t.Fatalf("the start program failed:\n%s", logs(hgName))
	}
	if n := inspectHas(hgName); n != 0 {
		t.Fatalf("Honeygain: the password is %d times in docker inspect", n)
	}
}
