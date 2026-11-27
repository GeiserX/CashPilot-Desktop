package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

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
	has, err := p.hasProgram(slug)
	if err != nil {
		return "", err
	}
	if has {
		return "", fmt.Errorf("%s runs as a program on this computer, not in a container: %w", slug, ErrNoDeployTarget)
	}
	return NoContainer, nil
}

// hasProgram reports whether slug has a program here: a registry entry, or a process
// this session launched whose registry write failed (launchProcess keeps it running).
// A registry that exists but cannot be read is an error, never "no program": it may
// record one from an earlier session.
func (p *NativeProcessProvider) hasProgram(slug string) (bool, error) {
	p.mu.Lock()
	_, launched := p.procs[slug]
	p.mu.Unlock()
	if launched {
		return true, nil
	}
	reg, err := p.readRegistryStrict()
	if err != nil {
		return false, fmt.Errorf("cannot read the record of programs on this computer, so nothing was replaced: %w", err)
	}
	return reg.Entries[slug] != nil, nil
}

// readRegistryStrict reads the registry like readRegistry, but answers a read or
// decode error instead of an empty registry. A missing file is an empty registry.
func (p *NativeProcessProvider) readRegistryStrict() (*nativeRegistry, error) {
	p.regMu.Lock()
	defer p.regMu.Unlock()
	reg := &nativeRegistry{Entries: map[string]*nativeRegistryEntry{}}
	raw, err := os.ReadFile(p.statePath)
	if errors.Is(err, fs.ErrNotExist) {
		return reg, nil
	}
	if err != nil {
		return nil, err
	}
	// A bare null decodes into the empty registry above without an error, but the
	// registry never writes it, so it is as unreadable as broken JSON.
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("the record of programs holds null")
	}
	if err := json.Unmarshal(raw, reg); err != nil {
		return nil, err
	}
	if reg.Entries == nil {
		reg.Entries = map[string]*nativeRegistryEntry{}
	}
	return reg, nil
}
