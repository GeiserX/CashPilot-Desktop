package preflight

import (
	"context"
	"time"

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
// Works with Docker and with Podman, which serves the same API over DOCKER_HOST.
func DaemonArch(ctx context.Context) string {
	if ctx == nil {
		ctx = context.Background()
	}
	cli, err := client.NewClientWithOpts(client.FromEnv)
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
