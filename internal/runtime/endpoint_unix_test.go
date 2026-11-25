//go:build !windows

package runtime

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The endpoint probe tells the socket states apart by error number. Each case
// builds the real state on disk.
func TestProbeEndpointUnixStates(t *testing.T) {
	// Short path: macOS caps a socket path at 104 bytes and its TempDir is long.
	dir, err := os.MkdirTemp("/tmp", "cpd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx := context.Background()

	if got := probeEndpoint(ctx, "unix://"+filepath.Join(dir, "missing.sock")); got != endpointAbsent {
		t.Errorf("no socket file: %v, want absent", got)
	}

	live := filepath.Join(dir, "live.sock")
	l, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	if got := probeEndpoint(ctx, "unix://"+live); got != endpointPresent {
		t.Errorf("a listening socket: %v, want present", got)
	}

	if os.Geteuid() != 0 {
		if err := os.Chmod(live, 0); err != nil {
			t.Fatal(err)
		}
		if got := probeEndpoint(ctx, "unix://"+live); got != endpointDenied {
			t.Errorf("a socket this account may not open: %v, want denied", got)
		}
		_ = os.Chmod(live, 0o600)
	}

	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if got := probeEndpoint(ctx, "unix://"+live); got != endpointRefused {
		t.Errorf("a stale socket file: %v, want refused", got)
	}

	if got := probeEndpoint(ctx, "tcp://127.0.0.1:1"); got != endpointUnknown {
		t.Errorf("tcp is not probed: %v", got)
	}
	if got := probeEndpoint(ctx, "npipe:////./pipe/docker_engine"); got != endpointUnknown {
		t.Errorf("a pipe outside Windows: %v", got)
	}
}
