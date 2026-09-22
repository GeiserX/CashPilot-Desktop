package preflight

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// pointDockerAt makes the container-runtime client in this package talk to a test
// server instead of a real daemon. DOCKER_HOST is how both Docker and Podman are
// redirected in the field, so this is the same lever a user pulls.
func pointDockerAt(t *testing.T, rawURL string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parsing %q: %v", rawURL, err)
	}
	t.Setenv("DOCKER_HOST", "tcp://"+parsed.Host)
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
}

// The container runs on the DAEMON, and the daemon is not always this machine: a
// remote context, a Lima/Colima VM or Podman in a VM all move the real target
// elsewhere. Reading the Desktop binary's own architecture would be a guess dressed
// as a check, so the answer has to come off the wire.
func TestDaemonArchIsWhatTheRuntimeReports(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/version") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Version":"27.3.1","ApiVersion":"1.47","Os":"linux","Arch":"arm64"}`)
	}))
	t.Cleanup(daemon.Close)
	pointDockerAt(t, daemon.URL)

	if got := DaemonArch(context.Background()); got != "arm64" {
		t.Fatalf("DaemonArch = %q, want the architecture the daemon reported (arm64)", got)
	}
}

// A runtime that is not installed, not running, or wedged must produce "", which
// the assessment reports as "we did not check your CPU". Any other answer would
// invent a fact.
func TestDaemonArchIsEmptyWhenTheRuntimeCannotBeReached(t *testing.T) {
	// Port 1 refuses immediately on every platform, so this does not sit out the
	// three-second probe timeout.
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")

	if got := DaemonArch(context.Background()); got != "" {
		t.Fatalf("DaemonArch = %q with no runtime reachable, want \"\"", got)
	}
}

// A nil context is what a Wails binding hands over before Startup has run. It must
// not panic.
func TestDaemonArchAcceptsANilContext(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")

	//nolint:staticcheck // the nil context is the case under test
	if got := DaemonArch(nil); got != "" {
		t.Fatalf("DaemonArch(nil) = %q, want \"\"", got)
	}
}
