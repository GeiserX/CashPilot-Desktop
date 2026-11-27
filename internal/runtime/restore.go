package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// Restore from a backup file (issue #298), the Desktop port of the web's restore
// (GeiserX/CashPilot #741, orchestrator.restore_state).
//
// Only into nothing: new volumes, a new container that is created and not started,
// the backup written into the volumes, read back and compared file by file, and only
// then started. Never over a live identity, and nothing half-restored is left
// running. Desktop talks to its own Docker daemon directly, so the write is a plain
// archive copy into the created container: Docker writes into a stopped container's
// volumes. The web goes through a helper container only because its socket proxy
// allows archive writes into one folder.

// LabelRestore marks a volume a restore created, with a random value only that
// restore knows. Docker names a volume and keeps no other identity, so this is how
// the undo, and a later Delete the partial restore, tell the restore's own volume
// from one another program made under the same name. Same label as the web.
const LabelRestore = "cashpilot.restore"

// volumeName is a literal named volume, never ${VAR} or a path. Same rule as the
// web's state_backup.restore_volumes.
var volumeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// RestoreTargets maps each critical target of svc to the named volume a restore
// creates for it. It refuses a service with a critical folder that is not on a named
// volume the catalog names literally (a host folder: Storj, Sia keep terabytes
// there, and a restore cannot prove a host folder is new), and one that declares
// nothing critical.
func RestoreTargets(svc catalog.Service) (map[string]string, error) {
	critical := CriticalTargets(svc)
	if len(critical) == 0 {
		return nil, errors.New(i18n.T("go.runtime.restore.nothingToRestore", "name", svc.Name))
	}
	sources := map[string]string{}
	for _, mapping := range svc.Docker.Volumes {
		parts := strings.Split(mapping, ":")
		if len(parts) >= 2 {
			sources[normaliseMountTarget(parts[1])] = parts[0]
		}
	}
	found := map[string]string{}
	folders := map[string]bool{}
	for target := range critical {
		source := sources[target]
		if !volumeName.MatchString(source) {
			return nil, errors.New(i18n.T("go.runtime.restore.hostFolder", "name", svc.Name))
		}
		folder := statebackup.FolderName(target)
		if folders[folder] {
			return nil, errors.New(i18n.T("go.runtime.restore.twoFolders", "name", svc.Name, "folder", folder))
		}
		folders[folder] = true
		found[target] = source
	}
	return found, nil
}

// RestoreRequest is one restore. Archives are statebackup.SplitArchives' output,
// keyed by each critical folder's last path component.
type RestoreRequest struct {
	Spec     DeploySpec
	Archives map[string][]byte
	// Hold records, before anything is made, that a restore of the slug is under way
	// and which volumes it will create with which LabelRestore value. A restore that
	// cannot record it makes nothing. The record outlives the app, so a crash
	// mid-restore leaves a service that Deploy and Start refuse.
	Hold func(volumes map[string]string) error
	// Release lifts that record once every folder read back as the backup, right
	// before the start. One that fails undoes the restore. When the start fails
	// after it and the undo leaves something, Hold is called again with the same
	// volumes.
	Release  func() error
	Progress func(string)
}

// RestoreResult is what a restore made.
type RestoreResult struct {
	ContainerID string   `json:"containerId"`
	Name        string   `json:"name"`
	Image       string   `json:"image"`
	Targets     []string `json:"targets"`
	Files       int      `json:"files"`
	// Started is false when a fence-gated node waits for the LAN fence: its data is
	// checked, and the fence guard starts it once the fence holds.
	Started bool `json:"started"`
	// Waiting says why a fence-gated node did not start yet.
	Waiting string `json:"waiting,omitempty"`
	// Left names what the undo of a failed restore could not remove: then the
	// restored data may still be on this machine, and the hold stays.
	Left []string `json:"left,omitempty"`
}

// RestoreProvider is a runtime that can restore a backup. Only Docker can.
type RestoreProvider interface {
	// RestoreRefusal says why slug cannot be restored on this machine now, or "".
	RestoreRefusal(ctx context.Context, slug string, svc catalog.Service) (string, error)
	RestoreState(ctx context.Context, req RestoreRequest) (RestoreResult, error)
	// DiscardRestore removes what a restore that did not finish left: the service's
	// container, and each volume that still carries the label value the restore made
	// it with. A volume under the name without that value is another program's and
	// stays. It returns what could not be removed.
	DiscardRestore(ctx context.Context, slug string, volumes map[string]string) ([]string, error)
}

// restoreClient is the part of the Docker client a restore needs, so a test can stand
// in for the daemon.
type restoreClient interface {
	inspectClient
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	VolumeInspect(ctx context.Context, volumeID string, options client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeCreate(ctx context.Context, options client.VolumeCreateOptions) (client.VolumeCreateResult, error)
	VolumeRemove(ctx context.Context, volumeID string, options client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
	CopyToContainer(ctx context.Context, containerID string, options client.CopyToContainerOptions) (client.CopyToContainerResult, error)
	CopyFromContainer(ctx context.Context, containerID string, options client.CopyFromContainerOptions) (client.CopyFromContainerResult, error)
}

// restoreSteps are the parts of a restore that need the whole provider: the deploy's
// create and the guarded start. A test replaces them.
type restoreSteps struct {
	create func(ctx context.Context) (ContainerInfo, error)
	// start starts the container; for a fence-gated one an error means it waits.
	start func(ctx context.Context) error
}

func (p *DockerProvider) RestoreRefusal(ctx context.Context, slug string, svc catalog.Service) (string, error) {
	targets, err := RestoreTargets(svc)
	if err != nil {
		return err.Error(), nil
	}
	cli, err := dockerClient()
	if err != nil {
		return "", err
	}
	defer cli.Close()
	return restoreRefusal(ctx, cli, slug, targets)
}

func (p *DockerProvider) RestoreState(ctx context.Context, req RestoreRequest) (RestoreResult, error) {
	cli, err := dockerClient()
	if err != nil {
		return RestoreResult{}, fmt.Errorf("%s: %w", i18n.T("go.runtime.restore.nothingRestored"), err)
	}
	defer cli.Close()
	name := containerName(req.Spec.Slug)
	steps := restoreSteps{
		create: func(ctx context.Context) (ContainerInfo, error) {
			spec := req.Spec
			spec.CreateOnly, spec.Instance = true, 1
			return p.Deploy(ctx, spec, req.Progress)
		},
		start: func(ctx context.Context) error {
			return p.startGuarded(ctx, cli, name, func() error {
				_, err := cli.ContainerStart(ctx, name, client.ContainerStartOptions{})
				return err
			})
		},
	}
	return p.restoreState(ctx, cli, req, steps)
}

func (p *DockerProvider) DiscardRestore(ctx context.Context, slug string, volumes map[string]string) ([]string, error) {
	cli, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	return discardRestore(ctx, cli, slug, volumes)
}

// restoreRefusal says why slug cannot be restored now, or "". Any container of the
// service (any copy, any state, one set aside, a proxied copy) or under its names
// refuses, and so does any of its critical volumes existing at all: a restore never
// writes over something that is there. A volume that cannot be checked counts as
// there.
func restoreRefusal(ctx context.Context, cli restoreClient, slug string, targets map[string]string) (string, error) {
	listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", LabelService+"="+slug)})
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("go.runtime.restore.listFailed"), err)
	}
	if len(listed.Items) > 0 {
		return i18n.T("go.runtime.restore.why.hasContainer", "slug", slug, "container", strings.TrimPrefix(firstName(listed.Items[0].Names), "/")), nil
	}
	name := containerName(slug)
	for _, candidate := range []string{name, name + setAsideSuffix} {
		_, err := cli.ContainerInspect(ctx, candidate, client.ContainerInspectOptions{})
		if err == nil {
			return i18n.T("go.runtime.restore.why.nameExists", "name", candidate), nil
		}
		if !cerrdefs.IsNotFound(err) {
			return i18n.T("go.runtime.restore.why.nameUnchecked", "name", candidate, "error", err), nil
		}
	}
	for _, target := range sortedNames(targets) {
		volume := targets[target]
		_, err := cli.VolumeInspect(ctx, volume, client.VolumeInspectOptions{})
		if err == nil {
			return i18n.T("go.runtime.restore.why.volumeExists", "volume", volume), nil
		}
		if !cerrdefs.IsNotFound(err) {
			return i18n.T("go.runtime.restore.why.volumeUnchecked", "volume", volume, "error", err), nil
		}
	}
	return "", nil
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// restoreError is a restore that failed after it began making things, with what the
// undo left.
type restoreError struct {
	msg  string
	left []string
}

func (e *restoreError) Error() string { return e.msg }

func (p *DockerProvider) restoreState(ctx context.Context, cli restoreClient, req RestoreRequest, steps restoreSteps) (RestoreResult, error) {
	svc := req.Spec.Service
	slug := req.Spec.Slug
	name := containerName(slug)
	targets, err := RestoreTargets(svc)
	if err != nil {
		return RestoreResult{}, err
	}
	byFolder := map[string]string{}
	for target := range targets {
		byFolder[statebackup.FolderName(target)] = target
	}
	if !sameKeys(byFolder, req.Archives) {
		have := make([]string, 0, len(req.Archives))
		for k := range req.Archives {
			have = append(have, k)
		}
		sort.Strings(have)
		want := sortedNames(byFolder)
		shown := strings.Join(have, ", ")
		if shown == "" {
			shown = "nothing"
		}
		return RestoreResult{}, errors.New(i18n.T("go.runtime.restore.wrongBackup", "holds", shown, "name", svc.Name, "keeps", strings.Join(want, ", ")))
	}
	expected := map[string]map[string]statebackup.ManifestEntry{}
	writes := map[string][]byte{}
	for folder, archive := range req.Archives {
		target := byFolder[folder]
		if expected[target], err = statebackup.TreeManifest(archive); err != nil {
			return RestoreResult{}, err
		}
		if writes[target], err = statebackup.WritableArchive(archive); err != nil {
			return RestoreResult{}, err
		}
	}

	if !p.restoring.TryLock() {
		return RestoreResult{}, errors.New(i18n.T("go.runtime.restore.busy"))
	}
	defer p.restoring.Unlock()

	why, err := restoreRefusal(ctx, cli, slug, targets)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("%s: %w", i18n.T("go.runtime.restore.nothingRestored"), err)
	}
	if why != "" {
		return RestoreResult{}, errors.New(i18n.T("go.runtime.restore.refused", "why", strings.TrimSuffix(why, ".")))
	}

	// The critical volumes are made new, and so is any other named volume the service
	// mounts that is not there yet (Anyone's config), so the undo removes only volumes
	// this restore proved it made. One that is already there is used as a deploy uses
	// it, and never removed.
	critical := map[string]bool{}
	nonces := map[string]string{}
	for _, volume := range targets {
		critical[volume] = true
	}
	for _, m := range buildMounts(svc.Docker.Volumes, buildEnv(svc, req.Spec.Env)) {
		if string(m.Type) != "volume" || critical[m.Source] {
			continue
		}
		if _, err := cli.VolumeInspect(ctx, m.Source, client.VolumeInspectOptions{}); cerrdefs.IsNotFound(err) {
			nonces[m.Source] = ""
		}
	}
	for volume := range critical {
		nonces[volume] = ""
	}
	for volume := range nonces {
		if nonces[volume], err = newNonce(); err != nil {
			return RestoreResult{}, fmt.Errorf("%s: %w", i18n.T("go.runtime.restore.nothingRestored"), err)
		}
	}
	if err := req.Hold(nonces); err != nil {
		return RestoreResult{}, errors.New(i18n.T("go.runtime.restore.recordFailed", "name", svc.Name, "error", err))
	}

	gated := svc.Docker.FenceGated
	wasStopped := false
	released := false
	made := map[string]string{}
	var createdID string
	result := RestoreResult{Name: name, Targets: sortedNames(targets)}
	failure := func() error {
		if gated {
			stopped, err := p.fence.userStopped()
			if err != nil {
				return errors.New(i18n.T("go.runtime.restore.heldUnread", "error", err))
			}
			wasStopped = stopped[name]
			// On disk before the container exists: the fence guard starts a created
			// node that waits for the fence, and must never start this one
			// half-written, now or after the app died mid-restore.
			if err := p.fence.markStopped(name, true); err != nil {
				return errors.New(i18n.T("go.runtime.restore.holdFailed", "name", name, "error", err))
			}
		}
		for _, volume := range sortedNames(nonces) {
			created, err := cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volume, Labels: map[string]string{LabelRestore: nonces[volume]}})
			if err != nil {
				// Docker may have made it all the same (an answer cut off). The undo
				// removes it only when it carries this restore's label.
				made[volume] = nonces[volume]
				return errors.New(i18n.T("go.runtime.restore.volumeCreateFailed", "volume", volume, "error", err))
			}
			if created.Volume.Labels[LabelRestore] != nonces[volume] {
				// Docker hands back the volume that is there for a name that exists.
				if critical[volume] {
					return errors.New(i18n.T("go.runtime.restore.volumeExisted", "volume", volume))
				}
				continue
			}
			made[volume] = nonces[volume]
		}

		info, err := steps.create(ctx)
		createdID = info.ContainerID
		if err != nil {
			return errors.New(i18n.T("go.runtime.restore.createFailed", "name", name, "error", err))
		}
		result.ContainerID, result.Image = info.ContainerID, info.Image
		if why := notCreated(ctx, cli, createdID); why != "" {
			return errors.New(i18n.T("go.runtime.restore.notWritten", "name", name, "why", why))
		}

		for _, target := range result.Targets {
			volume := targets[target]
			got, err := cli.VolumeInspect(ctx, volume, client.VolumeInspectOptions{})
			if err != nil || got.Volume.Labels[LabelRestore] != made[volume] {
				return errors.New(i18n.T("go.runtime.restore.volumeReplaced", "volume", volume))
			}
			// Into the folder that holds the mount point: the archive's entries sit
			// under the folder's own name, so its first entry gives the volume's root
			// the backup's owner and mode.
			if _, err := cli.CopyToContainer(ctx, createdID, client.CopyToContainerOptions{DestinationPath: path.Dir(target), Content: bytes.NewReader(writes[target])}); err != nil {
				return errors.New(i18n.T("go.runtime.restore.volumeWriteFailed", "volume", volume, "error", err))
			}
		}
		for _, target := range result.Targets {
			back, err := readBack(ctx, cli, createdID, target)
			if err != nil {
				return errors.New(i18n.T("go.runtime.restore.readBackFailed", "target", target, "error", err))
			}
			found, err := statebackup.TreeManifest(back)
			if err != nil {
				return errors.New(i18n.T("go.runtime.restore.readBackFailed", "target", target, "error", err))
			}
			if diff := statebackup.ManifestDifferences(expected[target], found); len(diff) > 0 {
				shown := strings.Join(diff[:min(5, len(diff))], "; ")
				if len(diff) > 5 {
					shown += "; " + i18n.T("go.runtime.restore.andMore", "n", len(diff)-5)
				}
				return errors.New(i18n.T("go.runtime.restore.readBackDiffers", "target", target, "differences", shown))
			}
			for _, e := range found {
				if e.Kind == "file" {
					result.Files++
				}
			}
		}
		if why := notCreated(ctx, cli, createdID); why != "" {
			return errors.New(i18n.T("go.runtime.restore.changedBeforeFinish", "name", name, "why", why))
		}
		// Every folder read back as the backup: the hold goes now, right before the start.
		if err := req.Release(); err != nil {
			return errors.New(i18n.T("go.runtime.restore.finishNotRecorded", "name", svc.Name, "error", err))
		}
		released = true
		if !gated {
			if err := steps.start(ctx); err != nil {
				return errors.New(i18n.T("go.runtime.restore.startFailed", "name", name, "error", withGPUHint(err)))
			}
			result.Started = true
			return nil
		}
		// Last before the start: from here the fence guard may start it too.
		if err := p.fence.markStopped(name, false); err != nil {
			return errors.New(i18n.T("go.runtime.restore.releaseFailed", "name", name, "error", err))
		}
		if err := steps.start(ctx); err != nil {
			// Its data is checked: it waits for the fence, and the guard starts it.
			result.Waiting = err.Error()
		} else {
			result.Started = true
		}
		return nil
	}()
	if failure == nil {
		return result, nil
	}
	left := undoRestore(context.WithoutCancel(ctx), cli, slug, createdID, made)
	if gated && (len(left) == 0 || left[0] != name) {
		// No container of ours is left, so the stop record goes back to what it was.
		_ = p.fence.markStopped(name, wasStopped)
	}
	msg := i18n.T("go.runtime.restore.undone", "error", strings.TrimSuffix(failure.Error(), "."))
	if len(left) > 0 && released {
		// The hold went right before the start, and the start or the fence step after
		// it failed. What the undo left needs the record back, or nothing lists it
		// under Settings and nothing deletes it.
		if err := req.Hold(nonces); err != nil {
			msg += " " + i18n.T("go.runtime.restore.undoHoldFailed", "left", strings.Join(left, ", "), "name", svc.Name, "error", err)
			return RestoreResult{Left: left}, &restoreError{msg: msg, left: left}
		}
	}
	if len(left) > 0 {
		msg += " " + i18n.T("go.runtime.restore.undoHeld", "left", strings.Join(left, ", "), "name", svc.Name)
	}
	return RestoreResult{Left: left}, &restoreError{msg: msg, left: left}
}

func sameKeys(a map[string]string, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			return false
		}
	}
	return true
}

// notCreated says how the container is no longer the one this restore created and
// never started, or "".
func notCreated(ctx context.Context, cli restoreClient, id string) string {
	got, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return i18n.T("go.runtime.restore.notCreated.unchecked", "error", err)
	}
	if got.Container.State == nil || got.Container.State.Status != "created" {
		if got.Container.State != nil {
			return i18n.T("go.runtime.restore.notCreated.status", "status", string(got.Container.State.Status))
		}
		return i18n.T("go.runtime.restore.notCreated.unknown")
	}
	return ""
}

// readBack is Docker's archive of target in the created container, capped like an
// export.
func readBack(ctx context.Context, cli restoreClient, id, target string) ([]byte, error) {
	res, err := cli.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: target})
	if err != nil {
		return nil, err
	}
	defer res.Content.Close()
	data, err := io.ReadAll(io.LimitReader(res.Content, statebackup.MaxStateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > statebackup.MaxStateBytes {
		return nil, errors.New(i18n.T("go.runtime.restore.tooLarge"))
	}
	return data, nil
}

// undoRestore removes what a failed restore made: its container (and one a create
// that failed after Docker made it left under the name, never started), then each
// volume that still carries the label value this restore made it with. It returns
// what could not be removed, the container's name first.
func undoRestore(ctx context.Context, cli restoreClient, slug, createdID string, made map[string]string) []string {
	var left []string
	name := containerName(slug)
	ids := []string{}
	if createdID != "" {
		ids = append(ids, createdID)
	}
	if got, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{}); err == nil && got.Container.ID != createdID &&
		got.Container.Config != nil && IsDesktopContainer(got.Container.Config.Labels) && got.Container.Config.Labels[LabelService] == slug &&
		got.Container.State != nil && got.Container.State.Status == "created" {
		ids = append(ids, got.Container.ID)
	}
	for _, id := range ids {
		if _, err := cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			left = append(left, name)
			break
		}
	}
	for _, volume := range sortedNames(made) {
		if !removeOwnVolume(ctx, cli, volume, made[volume]) {
			left = append(left, volume)
		}
	}
	return left
}

// removeOwnVolume removes volume when it still carries nonce. False only when it is
// the restore's and could not be removed: one that is gone, or another program's
// under the name, is never the restore's to remove.
func removeOwnVolume(ctx context.Context, cli restoreClient, volume, nonce string) bool {
	got, err := cli.VolumeInspect(ctx, volume, client.VolumeInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return true
	}
	if err != nil {
		return false
	}
	if nonce == "" || got.Volume.Labels[LabelRestore] != nonce {
		return true
	}
	if _, err := cli.VolumeRemove(ctx, volume, client.VolumeRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		return false
	}
	return true
}

func discardRestore(ctx context.Context, cli restoreClient, slug string, volumes map[string]string) ([]string, error) {
	name := containerName(slug)
	got, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	switch {
	case err == nil:
		if got.Container.Config == nil || !IsDesktopContainer(got.Container.Config.Labels) || got.Container.Config.Labels[LabelService] != slug {
			return nil, errors.New(i18n.T("go.runtime.restore.partial.foreign", "name", name))
		}
		if runsOrRestarts(got.Container) {
			return nil, errors.New(i18n.T("go.runtime.restore.partial.running", "name", name))
		}
		if _, err := cli.ContainerRemove(ctx, got.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return []string{name}, fmt.Errorf("%s: %w", i18n.T("go.runtime.restore.partial.removeFailed", "name", name), err)
		}
	case !cerrdefs.IsNotFound(err):
		return nil, fmt.Errorf("%s: %w", i18n.T("go.runtime.restore.partial.checkFailed", "name", name), err)
	}
	var left []string
	for _, volume := range sortedNames(volumes) {
		if !removeOwnVolume(ctx, cli, volume, volumes[volume]) {
			left = append(left, volume)
		}
	}
	if len(left) > 0 {
		return left, errors.New(i18n.T("go.runtime.restore.partial.volumeDeleteFailed", "volumes", strings.Join(left, ", ")))
	}
	return nil, nil
}
