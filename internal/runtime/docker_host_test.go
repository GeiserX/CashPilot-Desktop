//go:build !windows

package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/dockerhost"
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
	saved := dockerhost.DefaultSocket
	dockerhost.DefaultSocket = filepath.Join(home, "no-default.sock")
	t.Cleanup(func() { dockerhost.DefaultSocket = saved })
	return home
}

// writeContext stores a context the way the docker CLI does.
func writeContext(t *testing.T, home, name, host string, current bool) {
	t.Helper()
	writeContextTLS(t, home, name, host, false, current)
}

// writeContextTLS is writeContext with the context's SkipTLSVerify, and returns
// the folder the context's TLS files go in.
func writeContextTLS(t *testing.T, home, name, host string, skipVerify, current bool) string {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(home, ".docker", "contexts", "meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"Name":"` + name + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":` + strconv.FormatBool(skipVerify) + `}}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if current {
		cfg := `{"auths":{},"currentContext":"` + name + `"}`
		if err := os.WriteFile(filepath.Join(home, ".docker", "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tls := filepath.Join(home, ".docker", "contexts", "tls", hex.EncodeToString(sum[:]), "docker")
	if err := os.MkdirAll(tls, 0o755); err != nil {
		t.Fatal(err)
	}
	return tls
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
	if ep := dockerhost.Resolve(); ep.Context != "colima" || ep.Host != "unix://"+sock {
		t.Fatalf("resolved %+v, want context colima at %s", ep, sock)
	}

	// DOCKER_CONTEXT wins over config.json, as in the CLI.
	writeContext(t, home, "other", "unix:///nowhere/docker.sock", false)
	t.Setenv("DOCKER_CONTEXT", "other")
	if got := dockerhost.Resolve().Host; got != "unix:///nowhere/docker.sock" {
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

	t.Run("the default socket keeps the client's default", func(t *testing.T) {
		home := dockerHostEnv(t)
		dockerhost.DefaultSocket = filepath.Join(home, "default.sock")
		fakeEngine(t, dockerhost.DefaultSocket)
		fakeEngine(t, filepath.Join(home, ".colima", "default", "docker.sock"))
		if got := dockerhost.Resolve().Host; got != "" {
			t.Fatalf("resolved %q while the default socket exists, want the client's default", got)
		}
	})

	t.Run("nothing found keeps the client's default", func(t *testing.T) {
		dockerHostEnv(t)
		if got := dockerhost.Resolve().Host; got != "" {
			t.Fatalf("resolved %q with no socket anywhere", got)
		}
	})
}

// A current context that connects over ssh is refused with words that name it.
// Falling back to a local colima or Docker Desktop socket would run the earners on
// this computer while the user's docker CLI points at another machine.
func TestAnSSHContextIsRefusedNotSwappedForALocalSocket(t *testing.T) {
	home := dockerHostEnv(t)
	writeContext(t, home, "nas", "ssh://me@nas", true)
	fakeEngine(t, filepath.Join(home, ".colima", "default", "docker.sock"))

	var ssh *SSHContextError
	if _, err := dockerClient(); !errors.As(err, &ssh) || ssh.Context != "nas" {
		t.Fatalf("dockerClient = %v, want the ssh context refused", err)
	}
	st := NewDockerProvider().Status(context.Background())
	if st.Available || st.Reason != ReasonUnreachable || !strings.Contains(st.Message, "context nas connects over ssh") || st.Context != "nas" {
		t.Fatalf("Status = %+v, want the ssh context named as unusable", st)
	}
}

// A tcp context that does not answer gets the unreachable-endpoint words, as a
// DOCKER_HOST that does not answer does, not the local "start your runtime" text.
func TestATCPContextThatDoesNotAnswerIsUnreachable(t *testing.T) {
	home := dockerHostEnv(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	writeContext(t, home, "box", "tcp://"+addr, true)

	st := NewDockerProvider().Status(context.Background())
	if st.Reason != ReasonUnreachable || !strings.Contains(st.Message, "your Docker context names") {
		t.Fatalf("Status = %+v, want the unreachable-endpoint words", st)
	}
}

// A tcp context with SkipTLSVerify reaches an engine whose certificate this
// computer does not trust, as the docker CLI does. Its cert.pem without a key.pem
// is left out instead of failing the client.
func TestATCPContextHonoursSkipTLSVerify(t *testing.T) {
	home := dockerHostEnv(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		_, _ = w.Write([]byte("OK"))
	}))
	t.Cleanup(srv.Close)
	tls := writeContextTLS(t, home, "box", "tcp://"+strings.TrimPrefix(srv.URL, "https://"), true, true)
	if err := os.WriteFile(filepath.Join(tls, "cert.pem"), []byte("a certificate without its key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pingDefaultClient(t); err != nil {
		t.Fatalf("the client did not reach the context's TLS engine: %v", err)
	}
}
