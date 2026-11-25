package runtime

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"
)

// probeEndpoint dials the engine's own socket or pipe, after a failed ping, to
// tell "nothing there" from "there, but this account may not open it" from "a
// socket nobody listens on". It reads error numbers, which Windows does not
// translate, never error text. Any other endpoint (tcp, ssh) is not tried.
func probeEndpoint(ctx context.Context, host string) endpointState {
	scheme, rest, ok := strings.Cut(host, "://")
	if !ok {
		return endpointUnknown
	}
	switch scheme {
	case "unix":
		d := net.Dialer{Timeout: 2 * time.Second}
		conn, err := d.DialContext(ctx, "unix", rest)
		if err == nil {
			conn.Close()
		}
		return dialState(err)
	case "npipe":
		// npipe:////./pipe/docker_engine is \\.\pipe\docker_engine.
		return probePipe(strings.ReplaceAll(rest, "/", `\`))
	}
	return endpointUnknown
}

// dialState maps a dial error to an endpoint state by its error number.
func dialState(err error) endpointState {
	switch {
	case err == nil:
		return endpointPresent
	case errors.Is(err, fs.ErrNotExist):
		return endpointAbsent
	case errors.Is(err, fs.ErrPermission):
		return endpointDenied
	case errors.Is(err, syscall.ECONNREFUSED):
		return endpointRefused
	}
	return endpointUnknown
}

// installPaths are where the runtimes CashPilot guides to put their files on macOS
// and Linux. An app started from the Finder gets a PATH without /usr/local/bin or
// Homebrew, so an empty PATH search alone does not prove nothing is installed.
var installPaths = map[string][]string{
	"darwin": {
		"/Applications/Docker.app", "/usr/local/bin/docker", "/opt/homebrew/bin/docker",
		"/opt/homebrew/bin/colima", "/opt/homebrew/bin/limactl", "/opt/homebrew/bin/podman",
		"/usr/local/bin/colima", "/usr/local/bin/podman", "/opt/podman/bin/podman",
	},
	"linux": {"/usr/bin/docker", "/usr/local/bin/docker", "/usr/bin/podman", "/usr/bin/nerdctl", "/usr/bin/dockerd"},
}

// knownInstallPath reports whether any runtime's files sit at a known path. On
// Windows the host hints answer this instead.
func knownInstallPath() bool {
	for _, p := range installPaths[goruntime.GOOS] {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
