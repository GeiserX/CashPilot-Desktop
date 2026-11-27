package runtime

import (
	"context"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Docker starts a container with restart policy unless-stopped or always when its
// daemon starts, and it tries once. A start that fails there is never tried again:
// the restart policy acts only when a running container exits. After an unclean
// shutdown that first start can fail and a second one work (a sandbox runtime that
// finds the files its last run left behind, a port another program held for a
// moment at boot), and the earner then stays down until someone starts it by hand.
//
// Docker leaves its own sign on such a container: it is exited, State.Error holds
// why the start failed, and its restart policy says Docker means it to run. A run
// that ended clears State.Error, and so does a start that worked. That sign is what
// FailedStarts looks for, the same as the web worker's start_failed_starts
// (app/orchestrator.py). Nothing else is listed: a fence-gated node and a proxied
// copy have policy "no" and a guard of their own, a tunnel belongs to its copy, and
// a container a deploy set aside is settled by RecoverSetAside.

// FailedStart is one managed earner Docker means to run but could not start.
type FailedStart struct {
	ID   string
	Name string
	Slug string
	// Instance is which copy of its service it is: 1 for the first.
	Instance int
	// Error is Docker's reason the start failed (State.Error).
	Error string
}

// FailedStarts lists the managed earners Docker could not start. It costs one
// container list, and one inspect for each managed container that is exited. An
// inspect that fails for any reason but "no such container" fails the whole list.
func (p *DockerProvider) FailedStarts(ctx context.Context) ([]FailedStart, error) {
	cli, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", LabelManaged+"=true", LabelRuntime+"="+desktopRuntime).Add("status", string(container.StateExited)),
	})
	if err != nil {
		return nil, err
	}
	var failed []FailedStart
	for _, c := range list.Items {
		// The daemon filtered already; checked again so a runtime that ignores a
		// filter cannot hand back a running container or someone else's.
		if c.State != container.StateExited || !IsDesktopContainer(c.Labels) || c.Labels[LabelService] == "" || IsProxied(c.Labels) {
			continue
		}
		name := strings.TrimPrefix(firstName(c.Names), "/")
		if _, setAside := setAsideBase(name, c.Labels); setAside || strings.HasSuffix(name, setAsideSuffix) {
			continue
		}
		got, err := cli.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			continue // removed since the list
		}
		if err != nil {
			// An incomplete pass is no answer: the caller would forget when it
			// last tried this container, and try it again at once.
			return nil, err
		}
		if f, ok := failedStart(got.Container); ok {
			failed = append(failed, f)
		}
	}
	return failed, nil
}

// failedStart reads Docker's sign of a start that failed off an inspected container.
func failedStart(c container.InspectResponse) (FailedStart, bool) {
	if c.State == nil || c.HostConfig == nil || c.Config == nil || c.State.Status != container.StateExited {
		return FailedStart{}, false
	}
	reason := strings.TrimSpace(c.State.Error)
	if reason == "" {
		return FailedStart{}, false
	}
	if policy := c.HostConfig.RestartPolicy.Name; policy != container.RestartPolicyUnlessStopped && policy != container.RestartPolicyAlways {
		return FailedStart{}, false
	}
	labels := c.Config.Labels
	if !IsDesktopContainer(labels) || labels[LabelService] == "" || IsProxied(labels) {
		return FailedStart{}, false
	}
	return FailedStart{
		ID:       c.ID,
		Name:     strings.TrimPrefix(c.Name, "/"),
		Slug:     labels[LabelService],
		Instance: instanceOf(labels),
		Error:    reason,
	}, true
}
