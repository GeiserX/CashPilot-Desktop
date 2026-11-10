package runtime

import (
	"context"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// A redeploy sets the running container aside instead of removing it: stopped, which
// frees its published ports, and renamed to <name>-replaced, which frees its name.
// The old container is removed only once its replacement runs. When Docker refuses
// the replacement (the create, or the start because a host process holds one of its
// published ports), the refused container goes and the old one gets its name back,
// started again if it ran. Its volumes are never touched. The web worker does the
// same (app/orchestrator.py _move_aside and _roll_back).
const setAsideSuffix = "-replaced"

// setAsideBase is the name a container set aside by a deploy had, and whether it is
// one: managed, not a proxied copy, and named its own copy's name plus the suffix.
// A service slugged "foo-replaced" is not foo's: its name is its own copy's name.
func setAsideBase(name string, labels map[string]string) (string, bool) {
	base, ok := strings.CutSuffix(strings.TrimPrefix(name, "/"), setAsideSuffix)
	if !ok || labels[LabelManaged] != "true" || labels[LabelService] == "" || IsProxied(labels) {
		return "", false
	}
	return base, base == copyName(labels[LabelService], instanceOf(labels))
}

// RecoverSetAside puts back every container a deploy set aside and did not finish
// with, because the app stopped in the middle. Run once at app start, before the
// first fence check. Never fails; what it cannot settle, the next deploy does.
func (p *DockerProvider) RecoverSetAside(ctx context.Context) {
	cli, err := dockerClient()
	if err != nil {
		return
	}
	defer cli.Close()
	res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", LabelManaged+"=true")})
	if err != nil {
		return
	}
	// A fence-gated node put back must not race a check that starts nodes.
	p.fence.reconcileMu.Lock()
	defer p.fence.reconcileMu.Unlock()
	for _, c := range res.Items {
		if base, ok := setAsideBase(firstName(c.Names), c.Labels); ok {
			_, _ = recoverSetAside(ctx, cli, base)
		}
	}
}

// recoverSetAside settles name's set-aside container, if a deploy that did not finish
// left one of this copy of this service:
//   - nothing holds name: the old container gets its name back, stopped;
//   - a replacement that never started holds it: that one goes, the old one comes back;
//   - a replacement that started holds it: the old container goes, its volumes stay.
//
// Anything not ours is left alone. The note says what was done, "" for nothing.
func recoverSetAside(ctx context.Context, cli *client.Client, name string) (string, error) {
	aside := name + setAsideSuffix
	left, err := cli.ContainerInspect(ctx, aside, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("could not check %s: %w", aside, err)
	}
	if left.Container.Config == nil {
		return "", nil
	}
	labels := left.Container.Config.Labels
	if base, ok := setAsideBase(aside, labels); !ok || base != name {
		return "", nil
	}
	held, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
	case err != nil:
		return "", fmt.Errorf("could not check %s: %w", name, err)
	case held.Container.Config == nil || held.Container.Config.Labels[LabelService] != labels[LabelService] ||
		instanceOf(held.Container.Config.Labels) != instanceOf(labels):
		return "", nil
	case held.Container.State == nil || held.Container.State.Status != container.StateCreated:
		// Not forced: one that runs is not a leftover.
		if _, err := cli.ContainerRemove(ctx, left.Container.ID, client.ContainerRemoveOptions{}); err != nil {
			return "", fmt.Errorf("could not remove %s, left by a deploy that did not finish: %w", aside, err)
		}
		return fmt.Sprintf("Removed %s, the container a deploy that did not finish had replaced. Its volumes were kept.", aside), nil
	default:
		if _, err := cli.ContainerRemove(ctx, held.Container.ID, client.ContainerRemoveOptions{}); err != nil {
			return "", fmt.Errorf("could not remove %s, which a deploy that did not finish made and never started: %w", name, err)
		}
	}
	if _, err := cli.ContainerRename(ctx, left.Container.ID, client.ContainerRenameOptions{NewName: name}); err != nil {
		return "", fmt.Errorf("could not put %s back as %s: %w", aside, name, err)
	}
	return fmt.Sprintf("Put back %s, which a deploy that did not finish had set aside as %s. It is stopped.", name, aside), nil
}

// putBack gives the old container its name back after Docker refused its replacement,
// and starts it again if it ran. old is its inspect from before it was set aside. It
// says what state the old container is in, as a sentence for the deploy's error.
func (p *DockerProvider) putBack(cli *client.Client, old container.InspectResponse, wasRunning, gated bool, name string) string {
	// A cancelled deploy still puts the old node back.
	ctx := context.Background()
	aside := name + setAsideSuffix
	now, err := cli.ContainerInspect(ctx, old.ID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Sprintf("The previous container could not be checked (%v): it is kept, stopped, as %s or %s, with its data. Deploy again", err, aside, name)
	}
	// A rename that failed after it happened (a timeout) left it set aside.
	if strings.TrimPrefix(now.Container.Name, "/") != name {
		if _, err := cli.ContainerRename(ctx, old.ID, client.ContainerRenameOptions{NewName: name}); err != nil {
			return fmt.Sprintf("The previous container could not be put back (%v): it is kept, stopped, as %s, with its data. Rename it back to %s, or deploy again", err, aside, name)
		}
	}
	if !wasRunning {
		return "The previous container was put back and is stopped, as it was"
	}
	if gated {
		// This deploy applied the fence and holds the reconcile lock, so the fence is
		// in place; the node must be on it alone.
		if problem := onlyOnFence(old); problem != "" {
			return fmt.Sprintf("The previous container was put back and is kept stopped: it runs only behind the LAN fence, but %s", problem)
		}
	}
	if _, err := cli.ContainerStart(ctx, old.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Sprintf("The previous container was put back but could not be started again (%v)", err)
	}
	return "The previous container was put back and is running again"
}

// refusedReplacement is Docker's refusal of a replacement, with what became of the
// container it was to replace. Docker's own reason comes first and is kept whole.
type refusedReplacement struct {
	err      error
	restored string
}

func (e refusedReplacement) Error() string {
	return strings.TrimSuffix(e.err.Error(), ".") + ". " + e.restored + "."
}

func (e refusedReplacement) Unwrap() error { return e.err }
