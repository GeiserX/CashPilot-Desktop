package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Fence-gated earners (Mysterium) get no Docker restart policy. With one, Docker
// starts the node after a reboot or a daemon restart before CashPilot has written the
// LAN fence again, and in that window the node runs on a bridge that drops nothing
// it sends to this machine. So CashPilot starts them itself, and only right after it
// has applied the fence: at every reconcile (app start, every FenceRecheck, a change
// of the isolation setting, turning isolation on), and from the guard.
//
// The guard is what replaces the restart policy for a node that crashed. Every
// FenceGuard it lists the containers, which costs one Docker call, and runs a
// reconcile when a fence-gated node is down and waiting. That brings a crashed node
// back within FenceGuard, the interval of the web worker's guard (app/lan_fence.py
// GUARD_SECONDS), instead of the five minutes of the fence recheck. It runs only
// while the last reconcile applied the fence: a fence that fails to apply is retried
// every FenceRecheck as before, not every FenceGuard. A node whose start Docker
// refused (a published port another program holds) is also left to the FenceRecheck
// reconcile: each guard reconcile runs the fence helper, and a start that keeps
// failing would run it every FenceGuard, forever. The refused start is named in the
// status, and a start that works, from the recheck or the user, lifts the hold.
//
// A stop the user asked for stays a stop. Stop records the container's name, and
// neither the reconcile nor the guard starts a recorded one; Start, Restart and
// Deploy clear it. The record is a file in the app's data directory, read on every
// check, so the window and the background daemon share it (the web worker keeps
// /data/.fence_gated_stopped). Stops the fence makes itself (stopGated) are not
// recorded, so the node comes back once the fence does.
//
// While CashPilot is not running, nothing starts the node: after a reboot it stays
// stopped until the app or its background service starts.

// FenceGuard is how often the app looks for a fence-gated earner that is down and
// waiting for the fence.
const FenceGuard = 30 * time.Second

// SetGatedStoppedFile wires in the file that records the fence-gated containers the
// user stopped. Without one the record lives only in memory. Call it before the
// first deploy.
func (p *DockerProvider) SetGatedStoppedFile(path string) {
	p.fence.stoppedMu.Lock()
	defer p.fence.stoppedMu.Unlock()
	p.fence.stoppedPath = path
}

// GuardFenceGated starts a fence-gated earner that is down, through a full
// reconcile, so it starts only behind a fence applied just before. It does nothing
// unless isolation is on and the last check applied the fence. Never fails.
func (p *DockerProvider) GuardFenceGated(ctx context.Context) {
	if st := p.fence.Status(); st.Mode != FenceModeOn || st.State != "applied" {
		return
	}
	cli, err := dockerClient()
	if err != nil {
		return
	}
	nodes, err := p.fence.waitingNodes(ctx, cli)
	cli.Close()
	if err != nil {
		return
	}
	p.fence.setStatus(func(s *FenceStatus) { s.Waiting = serviceLabels(nodes) })
	refused := p.fence.refusedStarts()
	for _, c := range nodes {
		if !refused[containerLabel(c)] {
			p.ReconcileFence(ctx)
			return
		}
	}
}

// refusedStarts names the fence-gated containers whose last start from startGated
// Docker refused. The guard leaves them to the FenceRecheck reconcile.
func (f *fence) refusedStarts() map[string]bool {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return f.startRefused
}

// setRefusedStarts replaces the record of refused starts with this pass's.
func (f *fence) setRefusedStarts(names map[string]bool) {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	f.startRefused = names
}

// startedByUser lifts the guard's hold on a container the user just started or
// deployed, so a later crash brings it back within FenceGuard again.
func (f *fence) startedByUser(name string) {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	if f.startRefused[name] {
		next := make(map[string]bool, len(f.startRefused))
		for n := range f.startRefused {
			if n != name {
				next[n] = true
			}
		}
		f.startRefused = next
	}
}

// userStopped reads the names of the fence-gated containers the user stopped. An
// error means the record could not be read, and nothing may be started.
func (f *fence) userStopped() (map[string]bool, error) {
	f.stoppedMu.Lock()
	defer f.stoppedMu.Unlock()
	return f.readStoppedLocked()
}

func (f *fence) readStoppedLocked() (map[string]bool, error) {
	names := map[string]bool{}
	if f.stoppedPath == "" {
		for name := range f.stoppedMem {
			names[name] = true
		}
		return names, nil
	}
	raw, err := os.ReadFile(f.stoppedPath)
	if errors.Is(err, fs.ErrNotExist) {
		return names, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names[line] = true
		}
	}
	return names, nil
}

// markStopped records (stopped true) or clears the user's stop of a container.
func (f *fence) markStopped(name string, stopped bool) error {
	f.stoppedMu.Lock()
	defer f.stoppedMu.Unlock()
	names, err := f.readStoppedLocked()
	if err != nil {
		return err
	}
	if names[name] == stopped {
		return nil
	}
	if stopped {
		names[name] = true
	} else {
		delete(names, name)
	}
	if f.stoppedPath == "" {
		f.stoppedMem = names
		return nil
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	body := ""
	for _, n := range sorted {
		body += n + "\n"
	}
	// Written whole and renamed into place, so a crash mid-write never leaves a
	// record that forgot a stop.
	tmp, err := os.CreateTemp(filepath.Dir(f.stoppedPath), ".fence-gated-stopped-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), f.stoppedPath); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// containerLabel is a container's name without Docker's leading slash.
func containerLabel(c container.Summary) string {
	return strings.TrimPrefix(firstName(c.Names), "/")
}

// waitsForFence reports a fence-gated container CashPilot should start once the
// fence is applied: down (exited, or created and never started), not stopped by the
// user, and on the fenced network alone. A node made before the fence, on the host's
// network, is never started: it needs a redeploy (gatedStartProblem says so).
func (f *fence) waitsForFence(c container.Summary, stopped map[string]bool) bool {
	if !f.isGated(c.Labels) {
		return false
	}
	if c.State != container.StateExited && c.State != container.StateCreated {
		return false
	}
	if stopped[containerLabel(c)] {
		return false
	}
	return fencedAlone(earnerFromSummary(c))
}

func (f *fence) listManaged(ctx context.Context, cli fenceAPI) ([]container.Summary, error) {
	res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", LabelManaged+"=true")})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// waitingNodes lists the fence-gated containers that are down and waiting for the
// fence.
func (f *fence) waitingNodes(ctx context.Context, cli fenceAPI) ([]container.Summary, error) {
	items, err := f.listManaged(ctx, cli)
	if err != nil {
		return nil, err
	}
	stopped, err := f.userStopped()
	if err != nil {
		return nil, err
	}
	var out []container.Summary
	for _, c := range items {
		if f.waitsForFence(c, stopped) {
			out = append(out, c)
		}
	}
	return out, nil
}

// waiting names, by service, the fence-gated earners that are down and waiting for
// the fence.
func (f *fence) waiting(ctx context.Context, cli fenceAPI) ([]string, error) {
	nodes, err := f.waitingNodes(ctx, cli)
	if err != nil {
		return nil, err
	}
	return serviceLabels(nodes), nil
}

// serviceLabels names containers by service, each once, sorted.
func serviceLabels(nodes []container.Summary) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range nodes {
		label := c.Labels[LabelService]
		if label == "" {
			label = containerLabel(c)
		}
		if !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return out
}

// refreshWaiting puts the waiting earners in the status. A failed read leaves the
// last list.
func (f *fence) refreshWaiting(ctx context.Context, cli fenceAPI) {
	if waiting, err := f.waiting(ctx, cli); err == nil {
		f.setStatus(func(s *FenceStatus) { s.Waiting = waiting })
	}
}

// startGated starts every fence-gated container that waits for the fence. The
// caller holds f.reconcileMu and has just applied the fence.
func (f *fence) startGated(ctx context.Context, cli fenceAPI) error {
	items, err := f.listManaged(ctx, cli)
	if err != nil {
		return fmt.Errorf("could not list the fence-gated earners to start them: %w", err)
	}
	stopped, err := f.userStopped()
	if err != nil {
		return fmt.Errorf("could not read which fence-gated earners were stopped from CashPilot, so none was started: %w", err)
	}
	var failed []string
	refused := map[string]bool{}
	for _, c := range items {
		if !f.waitsForFence(c, stopped) {
			continue
		}
		if _, err := cli.ContainerStart(ctx, c.ID, client.ContainerStartOptions{}); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", containerLabel(c), err))
			refused[containerLabel(c)] = true
		}
	}
	f.setRefusedStarts(refused)
	if len(failed) > 0 {
		return fmt.Errorf("could not start %s behind the LAN fence", strings.Join(failed, ", "))
	}
	return nil
}

// pinGatedRestart moves each fence-gated container that still has a Docker restart
// policy (one deployed before this change, with unless-stopped) to restart policy
// "no", in place: docker update changes the policy without touching the container.
// One that is down at that moment with no start error is recorded as stopped by the
// user. Under unless-stopped Docker would not have started it either: the user
// stopped it, or the fence did and the old version left it for the user to start.
// The record keeps that, and Start lifts it. One that is down with a start error
// (State.Error, "network ... not found" after Unraid's boot removed our network) is
// one Docker tried to start: it is not recorded, so CashPilot starts it behind the
// fence (reattach, or startGated).
func (f *fence) pinGatedRestart(ctx context.Context, cli fenceAPI) error {
	items, err := f.listManaged(ctx, cli)
	if err != nil {
		return nil // the reconcile reports a Docker it cannot list; the next one tries again
	}
	var failed []string
	for _, c := range items {
		if !f.isGated(c.Labels) {
			continue
		}
		name := containerLabel(c)
		got, err := cli.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", name, err))
			continue
		}
		if got.Container.HostConfig == nil || got.Container.HostConfig.RestartPolicy.IsNone() {
			continue
		}
		st := got.Container.State
		if st == nil || (!st.Running && !st.Restarting && st.Error == "") {
			if err := f.markStopped(name, true); err != nil {
				failed = append(failed, fmt.Sprintf("%s (%v)", name, err))
				continue
			}
		}
		policy := container.RestartPolicy{Name: container.RestartPolicyDisabled}
		if _, err := cli.ContainerUpdate(ctx, c.ID, client.ContainerUpdateOptions{RestartPolicy: &policy}); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", name, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not take Docker's restart policy off %s, so Docker may start it after a reboot before the LAN fence is back", strings.Join(failed, ", "))
	}
	return nil
}
