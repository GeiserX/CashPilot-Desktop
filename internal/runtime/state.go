package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"

	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// StateProvider is a runtime that can fingerprint and read a service's
// irreplaceable state. Only Docker can: the native runtime keeps no volumes the
// catalog marks critical.
type StateProvider interface {
	// StateFingerprint is statebackup.Fingerprint for the service's container.
	StateFingerprint(ctx context.Context, slug string, targets []string) (string, error)
	// ReadState returns Docker's archive of each target, one after another, and the
	// targets read. The container is paused while it reads.
	ReadState(ctx context.Context, slug string, targets []string, limit int64) ([]byte, []string, error)
}

// stateClient is the part of the Docker client the state reads need, so a test can
// stand in for the daemon.
type stateClient interface {
	inspectClient
	VolumeInspect(ctx context.Context, volumeID string, options client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	ContainerPause(ctx context.Context, containerID string, options client.ContainerPauseOptions) (client.ContainerPauseResult, error)
	ContainerUnpause(ctx context.Context, containerID string, options client.ContainerUnpauseOptions) (client.ContainerUnpauseResult, error)
	CopyFromContainer(ctx context.Context, containerID string, options client.CopyFromContainerOptions) (client.CopyFromContainerResult, error)
}

func (p *DockerProvider) StateFingerprint(ctx context.Context, slug string, targets []string) (string, error) {
	cli, err := dockerClient()
	if err != nil {
		return "", err
	}
	defer cli.Close()
	return stateFingerprint(ctx, cli, containerName(slug), targets)
}

func (p *DockerProvider) ReadState(ctx context.Context, slug string, targets []string, limit int64) ([]byte, []string, error) {
	cli, err := dockerClient()
	if err != nil {
		return nil, nil, err
	}
	defer cli.Close()
	return readState(ctx, cli, slug, containerName(slug), targets, limit)
}

func stateFingerprint(ctx context.Context, cli stateClient, name string, targets []string) (string, error) {
	if len(targets) == 0 {
		return "", nil
	}
	inspect, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return "", err
	}
	mounts := map[string]statebackup.MountFact{}
	for _, m := range inspect.Container.Mounts {
		mounts[normaliseMountTarget(m.Destination)] = statebackup.MountFact{
			Mounted: true, Type: string(m.Type), Name: m.Name, Source: m.Source,
		}
	}
	facts := map[string]statebackup.MountFact{}
	for _, t := range targets {
		t = normaliseMountTarget(t)
		f := mounts[t]
		if f.Mounted && f.Type == string(mount.TypeVolume) {
			vol, err := cli.VolumeInspect(ctx, f.Name, client.VolumeInspectOptions{})
			if err != nil {
				// Unknown, not a change: the caller falls back to the time limit.
				return "", err
			}
			f.Created = vol.Volume.CreatedAt
		}
		facts[t] = f
	}
	return statebackup.Fingerprint(facts, inspect.Container.ID), nil
}

// readState reads only the given targets, never the whole container, with the
// container paused if it was running. A keystore copied while it is being written
// makes a backup that opens fine and restores a corrupt node, and that only shows
// on the day it is needed. Pause rather than stop, so a node keeps its uptime.
func readState(ctx context.Context, cli stateClient, slug, name string, targets []string, limit int64) (payload []byte, read []string, err error) {
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("%s declares no critical volumes, so there is no irreplaceable state to export", slug)
	}
	inspect, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return nil, nil, err
	}
	ctr := inspect.Container
	if ctr.Config == nil || ctr.Config.Labels[LabelManaged] != "true" {
		return nil, nil, fmt.Errorf("%s is not managed by CashPilot", name)
	}
	running := ctr.State != nil && ctr.State.Running && !ctr.State.Paused
	if running {
		if _, err := cli.ContainerPause(ctx, name, client.ContainerPauseOptions{}); err != nil {
			return nil, nil, fmt.Errorf("could not pause %s for a consistent copy: %w", name, err)
		}
		defer func() {
			// Unpause even when the read failed: a paused earner shows as up and
			// earns nothing. The context may be gone, so use a fresh one.
			if _, uerr := cli.ContainerUnpause(context.WithoutCancel(ctx), name, client.ContainerUnpauseOptions{}); uerr != nil {
				log.Printf("CRITICAL: %s is still paused and earning nothing (unpause failed: %v); run: docker unpause %s", name, uerr, name)
				if err == nil {
					// A good backup next to a silently paused node is worse than
					// no backup: say so instead of reporting success.
					payload, read = nil, nil
					err = fmt.Errorf("the backup was read but %s could not be unpaused: %w; run docker unpause %s", name, uerr, name)
				}
			}
		}()
	}

	sorted := make([]string, 0, len(targets))
	for _, t := range targets {
		sorted = append(sorted, normaliseMountTarget(t))
	}
	sort.Strings(sorted)
	var out []byte
	for _, target := range sorted {
		res, err := cli.CopyFromContainer(ctx, name, client.CopyFromContainerOptions{SourcePath: target})
		if err != nil {
			return nil, nil, fmt.Errorf("could not read %s: %w", target, err)
		}
		chunk, err := io.ReadAll(io.LimitReader(res.Content, limit-int64(len(out))+1))
		_ = res.Content.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("could not read %s: %w", target, err)
		}
		out = append(out, chunk...)
		if int64(len(out)) > limit {
			return nil, nil, fmt.Errorf("%s's state is larger than %d MB, too big to export here. That is a data folder, not an identity: Storj and Sia keep their whole storage folder there. Back up %s at the disk level with your own tools, then use I have a backup", slug, limit>>20, target)
		}
		read = append(read, target)
	}
	return out, read, nil
}

// ErrNoStateRuntime is returned when the service runs somewhere that cannot read
// container state.
var ErrNoStateRuntime = errors.New("this service's runtime cannot export its state")
