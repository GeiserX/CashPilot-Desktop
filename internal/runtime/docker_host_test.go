//go:build !windows

package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/client"
)

// dockerHostEnv clears every input the resolver reads and returns a short temp
// home: macOS caps a socket path at 104 bytes and its TempDir is long.
func dockerHostEnv(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "cpdh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	for _, key := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "COLIMA_HOME", "XDG_RUNTIME_DIR", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		t.Setenv(key, "")
	}
	saved := defaultDockerSocket
	defaultDockerSocket = filepath.Join(home, "no-default.sock")
	t.Cleanup(func() { defaultDockerSocket = saved })
	return home
}

// writeContext stores a context the way the docker CLI does.
func writeContext(t *testing.T, home, name, host string, current bool) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(home, ".docker", "contexts", "meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"Name":"` + name + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if current {
		cfg := `{"auths":{},"currentContext":"` + name + `"}`
		if err := os.WriteFile(filepath.Join(home, ".docker", "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeEngine serves /_ping on a unix socket, so the client can be shown to reach it.
func fakeEngine(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		w.Header().Set("Ostype", "linux")
		_, _ = w.Write([]byte("OK"))
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { srv.Close() })
}

func pingDefaultClient(t *testing.T) error {
	t.Helper()
	cli, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	_, err = cli.Ping(context.Background(), client.PingOptions{})
	return err
}

// A stock colima install: no DOCKER_HOST, no /var/run/docker.sock, and the
// current context "colima" pointing at ~/.colima/default/docker.sock. The app,
// started from the Finder, must reach that engine as the docker CLI does.
func TestDockerClientFollowsTheCurrentContext(t *testing.T) {
	home := dockerHostEnv(t)
	sock := filepath.Join(home, "vm", "docker.sock")
	fakeEngine(t, sock)
	writeContext(t, home, "colima", "unix://"+sock, true)

	if err := pingDefaultClient(t); err != nil {
		t.Fatalf("the client did not reach the current context's engine: %v", err)
	}
	if ep := resolveDockerEndpoint(); ep.Context != "colima" || ep.Host != "unix://"+sock {
		t.Fatalf("resolved %+v, want context colima at %s", ep, sock)
	}

	// DOCKER_CONTEXT wins over config.json, as in the CLI.
	writeContext(t, home, "other", "unix:///nowhere/docker.sock", false)
	t.Setenv("DOCKER_CONTEXT", "other")
	if got := resolveDockerEndpoint().Host; got != "unix:///nowhere/docker.sock" {
		t.Fatalf("DOCKER_CONTEXT=other resolved %q", got)
	}

	// DOCKER_HOST wins over every context.
	t.Setenv("DOCKER_HOST", "unix:///from/env.sock")
	cli, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if got := cli.DaemonHost(); got != "unix:///from/env.sock" {
		t.Fatalf("DaemonHost = %q with DOCKER_HOST set", got)
	}
}

// With the default context and no default socket, the client tries the sockets
// colima, Docker Desktop and rootless Docker leave in the home folder.
func TestDockerClientFindsARuntimeSocketInTheHomeFolder(t *testing.T) {
	cases := []struct {
		name string
		rel  string
	}{
		{"colima default profile", ".colima/default/docker.sock"},
		{"colima before profiles", ".colima/docker.sock"},
		{"another colima profile", ".colima/work/docker.sock"},
		{"Docker Desktop on macOS", ".docker/run/docker.sock"},
		{"Docker Desktop on Linux", ".docker/desktop/docker.sock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := dockerHostEnv(t)
			fakeEngine(t, filepath.Join(home, tc.rel))
			if err := pingDefaultClient(t); err != nil {
				t.Fatalf("the client did not reach %s: %v", tc.rel, err)
			}
		})
	}

	t.Run("rootless Docker", func(t *testing.T) {
		home := dockerHostEnv(t)
		t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))
		fakeEngine(t, filepath.Join(home, "run", "docker.sock"))
		if err := pingDefaultClient(t); err != nil {
			t.Fatalf("the client did not reach the rootless socket: %v", err)
		}
	})

	t.Run("an ssh context falls back to the sockets", func(t *testing.T) {
		home := dockerHostEnv(t)
		writeContext(t, home, "remote", "ssh://me@box", true)
		fakeEngine(t, filepath.Join(home, ".colima", "default", "docker.sock"))
		if err := pingDefaultClient(t); err != nil {
			t.Fatalf("an ssh context hid the local socket: %v", err)
		}
	})

	t.Run("the default socket keeps the client's default", func(t *testing.T) {
		home := dockerHostEnv(t)
		defaultDockerSocket = filepath.Join(home, "default.sock")
		fakeEngine(t, defaultDockerSocket)
		fakeEngine(t, filepath.Join(home, ".colima", "default", "docker.sock"))
		if got := resolveDockerEndpoint().Host; got != "" {
			t.Fatalf("resolved %q while the default socket exists, want the client's default", got)
		}
	})

	t.Run("nothing found keeps the client's default", func(t *testing.T) {
		dockerHostEnv(t)
		if got := resolveDockerEndpoint().Host; got != "" {
			t.Fatalf("resolved %q with no socket anywhere", got)
		}
	})
}
