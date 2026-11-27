package preflight

import (
	"context"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/moby/moby/client"
)

// daemonProbeTimeout bounds the one call this package makes over the runtime
// socket. The preflight is shown while the user is looking at the deploy step, so
// a runtime that is installed but wedged has to fall back to "not checked" quickly
// instead of freezing the wizard.
const daemonProbeTimeout = 3 * time.Second

// DaemonArch reports the CPU architecture of the container runtime, as the runtime
// itself reports it ("arm64", "x86_64", "amd64").
//
// The container runs on the DAEMON, not on the Desktop binary, and the two are not
// always the same machine: a remote Docker context, a Lima or Colima VM, or Podman
// in a virtual machine all move the real target elsewhere. Reading the Desktop
// binary's own architecture instead would be a guess dressed as a check, so an
// unreachable runtime returns "" and the assessment says the CPU was not checked.
//
// It asks the same daemon the rest of the app uses (runtime.NewDockerClient), so a
// colima or other context-only engine is found here too.
func DaemonArch(ctx context.Context) string {
	if ctx == nil {
		ctx = context.Background()
	}
	cli, err := runtime.NewDockerClient()
	if err != nil {
		return ""
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
	defer cancel()
	version, err := cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return ""
	}
	return version.Arch
}
