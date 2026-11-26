package runtime

import (
	"context"
	"errors"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// NoContainer is the deploy target when no container has the deploy's name, as the
// CashPilot worker protocol names it.
const NoContainer = "none"

// ErrNoDeployTarget is a runtime that cannot name what a deploy would replace, so a
// deploy that must replace only a container the caller saw cannot run on it.
var ErrNoDeployTarget = errors.New("this runtime cannot name the container a deploy would replace")

// DeployTargetChanged is a checked deploy refused because the container under the
// deploy's name is not the one the caller expected. Actual is the one there: its full
// ID, or NoContainer.
type DeployTargetChanged struct{ Actual string }

func (e *DeployTargetChanged) Error() string {
	return "The container on this worker no longer matches the deploy request. Review the current container and use Redeploy to confirm a replacement."
}

// DeployTargetReader is a runtime that names the container a deploy of slug would
// replace: its full ID, or NoContainer. It reads no logs.
type DeployTargetReader interface {
	DeployTarget(ctx context.Context, slug string) (string, error)
}

// DeployTarget names the container a deploy of slug's first copy would replace,
// "even if stopped", as the CashPilot worker reads it: one inspect, no logs.
func (p *DockerProvider) DeployTarget(ctx context.Context, slug string) (string, error) {
	cli, err := dockerClient()
	if err != nil {
		return "", err
	}
	defer cli.Close()
	live, err := cli.ContainerInspect(ctx, containerName(slug), client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return NoContainer, nil
	}
	if err != nil {
		return "", fmt.Errorf("could not look up the deploy container in Docker: %w", err)
	}
	return live.Container.ID, nil
}

// DeployTarget for a native program: a program has no container ID to name, so only
// a first deploy can be checked.
func (p *NativeProcessProvider) DeployTarget(_ context.Context, slug string) (string, error) {
	if p.hasProgram(slug) {
		return "", fmt.Errorf("%s runs as a program on this computer, not in a container: %w", slug, ErrNoDeployTarget)
	}
	return NoContainer, nil
}

// hasProgram reports whether slug has a program here: a registry entry, or a process
// this session launched whose registry write failed (launchProcess keeps it running).
func (p *NativeProcessProvider) hasProgram(slug string) bool {
	p.mu.Lock()
	_, launched := p.procs[slug]
	p.mu.Unlock()
	return launched || p.readRegistry().Entries[slug] != nil
}
