package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
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
	if !ok || !IsDesktopContainer(labels) || labels[LabelService] == "" || IsProxied(labels) {
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
	res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", LabelManaged+"=true", LabelRuntime+"="+desktopRuntime)})
	if err != nil {
		return
	}
	// A fence-gated node put back must not race a check that starts nodes.
	p.fence.reconcileMu.Lock()
	defer p.fence.reconcileMu.Unlock()
	for _, c := range res.Items {
		if base, ok := setAsideBase(firstName(c.Names), c.Labels); ok {
			// Not while a deploy of the same container runs (lockReplacing).
			unlock := p.lockReplacing(base)
			_, _ = p.recoverSetAside(ctx, cli, base)
			unlock()
		}
	}
}

// recoverSetAside settles name's set-aside container, if a deploy that did not finish
// left one of this copy of this service:
//   - nothing holds name: the old container gets its name back, stopped;
//   - a replacement that never started holds it: that one goes, the old one comes back;
//   - an image update's check period did not finish (settling.go): the replacement
//     goes, made, running or gone, and the old one comes back and runs again;
//   - a replacement that started holds it: the old container goes, its volumes stay.
//
// Anything not ours is left alone. The note says what was done, "" for nothing.
func (p *DockerProvider) recoverSetAside(ctx context.Context, cli *client.Client, name string) (string, error) {
	note, err := recoverSetAside(ctx, cli, name, p.settling.pending(name))
	if err == nil {
		// Settled: a mark left with it is of a deploy that is over.
		_ = p.settling.clear(name)
	}
	return note, err
}

func recoverSetAside(ctx context.Context, cli *client.Client, name string, unchecked bool) (string, error) {
	aside := name + setAsideSuffix
	left, err := cli.ContainerInspect(ctx, aside, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("go.runtime.aside.checkFailed", "name", aside), err)
	}
	if left.Container.Config == nil {
		return "", nil
	}
	labels := left.Container.Config.Labels
	if base, ok := setAsideBase(aside, labels); !ok || base != name {
		return "", nil
	}
	held, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	found := err == nil
	switch {
	case cerrdefs.IsNotFound(err):
	case err != nil:
		return "", fmt.Errorf("%s: %w", i18n.T("go.runtime.aside.checkFailed", "name", name), err)
	case held.Container.Config == nil || held.Container.Config.Labels[LabelService] != labels[LabelService] ||
		instanceOf(held.Container.Config.Labels) != instanceOf(labels):
		return "", nil
	}
	if unchecked {
		// Its check never passed: the replacement goes, made, running or gone, and the
		// old container comes back. An update replaces only one that ran, so it runs
		// again; a fence-gated one is left to the fence guard, which starts it behind
		// the fence.
		if found {
			if _, err := cli.ContainerRemove(ctx, held.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
				return "", fmt.Errorf("%s: %w", i18n.T("go.runtime.aside.removeUncheckedFailed", "name", name), err)
			}
		}
		if _, err := cli.ContainerRename(ctx, left.Container.ID, client.ContainerRenameOptions{NewName: name}); err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("go.runtime.aside.putBackFailed", "aside", aside, "name", name), err)
		}
		if labels[LabelFenceGated] != "true" {
			if _, err := cli.ContainerStart(ctx, left.Container.ID, client.ContainerStartOptions{}); err != nil {
				return "", errors.New(i18n.T("go.runtime.aside.previousNotStarted", "error", err))
			}
		}
		return i18n.T("go.runtime.aside.updateUndone", "name", name), nil
	}
	if found {
		if held.Container.State == nil || held.Container.State.Status != container.StateCreated {
			// Not forced: one that runs is not a leftover.
			if _, err := cli.ContainerRemove(ctx, left.Container.ID, client.ContainerRemoveOptions{}); err != nil {
				return "", fmt.Errorf("%s: %w", i18n.T("go.runtime.aside.removeLeftFailed", "name", aside), err)
			}
			return i18n.T("go.runtime.aside.removedLeft", "name", aside), nil
		}
		if _, err := cli.ContainerRemove(ctx, held.Container.ID, client.ContainerRemoveOptions{}); err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("go.runtime.aside.removeUnstartedFailed", "name", name), err)
		}
	}
	if _, err := cli.ContainerRename(ctx, left.Container.ID, client.ContainerRenameOptions{NewName: name}); err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("go.runtime.aside.putBackFailed", "aside", aside, "name", name), err)
	}
	return i18n.T("go.runtime.aside.putBack", "name", name, "aside", aside), nil
}

// removeSetAside removes name's set-aside container, if a deploy that did not finish
// left one of this copy of this service, with its anonymous volumes when volumes is
// set. A removal of the service calls it, or the next app start would put it back.
func removeSetAside(ctx context.Context, cli *client.Client, name string, volumes bool) error {
	aside := name + setAsideSuffix
	left, err := cli.ContainerInspect(ctx, aside, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("go.runtime.aside.checkFailed", "name", aside), err)
	}
	if left.Container.Config == nil {
		return nil
	}
	if base, ok := setAsideBase(aside, left.Container.Config.Labels); !ok || base != name {
		return nil
	}
	if _, err := cli.ContainerRemove(ctx, left.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: volumes}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%s: %w", i18n.T("go.runtime.aside.removeAsideFailed", "name", aside), err)
	}
	return nil
}

// onlySetAside reports whether copy n of slug exists only set aside: nothing holds
// name, and the container under name's set-aside name is that copy's own (a roll-back
// that could not rename it back left it there). Its inspect comes with it.
func onlySetAside(ctx context.Context, cli inspectClient, name, slug string, n int) (container.InspectResponse, bool, error) {
	if _, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		return container.InspectResponse{}, false, err
	}
	aside := name + setAsideSuffix
	left, err := cli.ContainerInspect(ctx, aside, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return container.InspectResponse{}, false, nil
	}
	if err != nil {
		return container.InspectResponse{}, false, fmt.Errorf("%s: %w", i18n.T("go.runtime.aside.checkFailed", "name", aside), err)
	}
	if left.Container.Config == nil {
		return container.InspectResponse{}, false, nil
	}
	labels := left.Container.Config.Labels
	if base, ok := setAsideBase(aside, labels); !ok || base != name || labels[LabelService] != slug || instanceOf(labels) != n {
		return container.InspectResponse{}, false, nil
	}
	return left.Container, true, nil
}

// withoutSetAside drops each container set aside beside the one that now holds its
// name, so a listing shows a service's running container, not the one it replaced.
func withoutSetAside(items []container.Summary) []container.Summary {
	names := make(map[string]bool, len(items))
	for _, c := range items {
		names[strings.TrimPrefix(firstName(c.Names), "/")] = true
	}
	out := items[:0:0]
	for _, c := range items {
		if base, ok := setAsideBase(firstName(c.Names), c.Labels); ok && names[base] {
			continue
		}
		out = append(out, c)
	}
	return out
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
		return i18n.T("go.runtime.aside.previousUnchecked", "error", err, "aside", aside, "name", name)
	}
	// A rename that failed after it happened (a timeout) left it set aside.
	if strings.TrimPrefix(now.Container.Name, "/") != name {
		if _, err := cli.ContainerRename(ctx, old.ID, client.ContainerRenameOptions{NewName: name}); err != nil {
			return i18n.T("go.runtime.aside.previousNotPutBack", "error", err, "aside", aside, "name", name)
		}
	}
	if !wasRunning {
		return i18n.T("go.runtime.aside.previousStopped")
	}
	if gated {
		// This deploy applied the fence and holds the reconcile lock, so the fence is
		// in place; the node must be on it alone.
		if problem := onlyOnFence(old); problem != "" {
			return i18n.T("go.runtime.aside.previousFenced", "problem", problem)
		}
	}
	if _, err := cli.ContainerStart(ctx, old.ID, client.ContainerStartOptions{}); err != nil {
		return i18n.T("go.runtime.aside.previousNotStarted", "error", err)
	}
	return i18n.T("go.runtime.aside.previousRunning")
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
