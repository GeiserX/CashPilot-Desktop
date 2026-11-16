package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// Restore from a backup file (issue #298): the manager's side. It decides whether a
// service may be restored here, records the hold that keeps a half-written service
// from starting, and records the deployment once the runtime restored it.

// restoreTarget is the catalog service a backup restores, and the runtime that can
// restore it. It refuses what a restore from the app cannot do.
func (m *Manager) restoreTarget(slug string) (catalog.Service, runtime.RestoreProvider, map[string]string, error) {
	svc, ok := m.catalog.Get(slug)
	if !ok || svc.Custom || catalog.IsCustomSlug(slug) {
		return catalog.Service{}, nil, nil, errors.New("this backup is not of a service in this app's catalog")
	}
	if svc.ManualOnly {
		return svc, nil, nil, fmt.Errorf("%s is tracked manually and has no Docker image", svc.Name)
	}
	if err := refuseRetired(svc); err != nil {
		return svc, nil, nil, err
	}
	targets, err := runtime.RestoreTargets(svc)
	if err != nil {
		return svc, nil, nil, err
	}
	if m.kindForService(svc) == nativeRuntimeKind {
		return svc, nil, nil, fmt.Errorf("%s runs as a native program on this machine, so its identity is not in a Docker volume and the app cannot restore it. Use the command line steps in the backup guide", svc.Name)
	}
	provider, ok := m.providers[m.defaultKind].(runtime.RestoreProvider)
	if !ok {
		return svc, nil, nil, errors.New("this machine's container runtime cannot restore a backup")
	}
	return svc, provider, targets, nil
}

// RestoreVolumes maps each critical folder of slug to the named volume a restore
// creates for it, or says why the app cannot restore slug at all.
func (m *Manager) RestoreVolumes(slug string) (map[string]string, error) {
	_, _, targets, err := m.restoreTarget(slug)
	return targets, err
}

// RestoreRefusal says why slug cannot be restored on this machine now, or "". It runs
// every check a restore runs before it makes anything.
func (m *Manager) RestoreRefusal(ctx context.Context, slug string, credentials map[string]string) (string, error) {
	svc, provider, _, err := m.restoreTarget(slug)
	if err != nil {
		return err.Error(), nil
	}
	if why, err := m.restoreRefusal(svc, slug, credentials); why != "" || err != nil {
		return why, err
	}
	return provider.RestoreRefusal(ctx, slug, svc)
}

// restoreRefusal is the part of the refusal the manager knows: the service is not
// deployed here, no earlier restore of it is unfinished, it may run from this
// machine's own IP, and its settings are filled in.
func (m *Manager) restoreRefusal(svc catalog.Service, slug string, credentials map[string]string) (string, error) {
	if _, deployed, err := m.store.GetDeployment(slug); err != nil {
		return "", err
	} else if deployed {
		return fmt.Sprintf("%s is already deployed on this machine. A restore only creates a new one: back the running one up, remove it and say yes to deleting its data, then restore", svc.Name), nil
	}
	if err := m.refuseHeld(slug, "restored"); err != nil {
		return err.Error(), nil
	}
	if _, _, err := m.directAllowed(svc, slug, false); err != nil {
		return err.Error(), nil
	}
	if err := validateRequired(svc, credentials); err != nil {
		return fmt.Sprintf("%s needs its settings first (%v). Fill them in on its deploy form and save them, then restore", svc.Name, err), nil
	}
	if err := checkPublished(svc, credentials); err != nil {
		return err.Error(), nil
	}
	return "", nil
}

// refuseHeld refuses slug while a restore of it is under way or did not finish: its
// volume may be half-written, and a node started on it would run half an identity.
// An unreadable record refuses too.
func (m *Manager) refuseHeld(slug, what string) error {
	hold, err := m.store.RestoreHeld(slug)
	if err != nil {
		return fmt.Errorf("could not read whether a restore of %s is unfinished (%v), so it was not %s", slug, err, what)
	}
	if hold != nil {
		return fmt.Errorf("a restore of %s is running or did not finish, so its data may be half-written and it was not %s. If no restore is running, delete the partial restore under Settings, Restore from a backup, then restore the backup again", slug, what)
	}
	return nil
}

// Restore creates slug from its catalog entry with archives (statebackup.SplitArchives'
// output) in its critical volumes, and starts it once they read back as the backup.
// It holds the service's deploy lock throughout, so no deploy or removal of it runs
// in between. The hold on disk is written before anything is made and lifted only
// when the data read back right, or when the undo removed everything it made.
func (m *Manager) Restore(ctx context.Context, slug string, credentials map[string]string, archives map[string][]byte) (runtime.RestoreResult, error) {
	defer m.lockDeploy(slug)()
	svc, provider, _, err := m.restoreTarget(slug)
	if err != nil {
		return runtime.RestoreResult{}, err
	}
	if why, err := m.restoreRefusal(svc, slug, credentials); err != nil {
		return runtime.RestoreResult{}, err
	} else if why != "" {
		return runtime.RestoreResult{}, fmt.Errorf("nothing was restored: %s", strings.TrimSuffix(why, "."))
	}
	held := false
	m.store.RecordEvent(slug, "restore_start", "")
	res, err := provider.RestoreState(ctx, runtime.RestoreRequest{
		Spec:     runtime.DeploySpec{Slug: slug, Service: svc, Env: credentials},
		Archives: archives,
		Hold: func(volumes map[string]string) error {
			if err := m.store.HoldRestore(slug, volumes, time.Now()); err != nil {
				return err
			}
			held = true
			return nil
		},
		Release:  func() error { return m.store.ReleaseRestore(slug) },
		Progress: func(message string) { m.store.RecordEvent(slug, "runtime_progress", message) },
	})
	if err != nil {
		if held && len(res.Left) == 0 {
			// The undo removed everything the restore made, so nothing is half-written.
			if rerr := m.store.ReleaseRestore(slug); rerr != nil {
				err = fmt.Errorf("%w The app could not lift its hold on %s (%v): delete the partial restore under Settings, Restore from a backup", err, svc.Name, rerr)
			}
		}
		m.store.RecordEvent(slug, "restore_error", err.Error())
		return res, err
	}
	status := "running"
	if !res.Started {
		status = "created"
	}
	deployment := store.Deployment{Slug: slug, ContainerID: res.ContainerID, Name: res.Name, Image: res.Image, Status: status, Runtime: defaultRuntimeKind}
	if err := m.store.UpsertDeployment(deployment); err != nil {
		return res, fmt.Errorf("%s was restored and is on this machine, but the app could not record it (%v). Refresh the dashboard", svc.Name, err)
	}
	_ = m.store.SetCustomImage(slug, "")
	_ = m.store.SetInstances(slug, 1)
	m.store.RecordEvent(slug, "restored", fmt.Sprintf("%d files in %s", res.Files, strings.Join(res.Targets, ", ")))
	return res, nil
}

// UnfinishedRestores lists the restores that are running or did not finish.
func (m *Manager) UnfinishedRestores() ([]store.RestoreHold, error) {
	return m.store.RestoreHolds()
}

// DiscardRestore deletes what a restore of slug that did not finish left: its
// container and the volumes it made, never a volume another program put under the
// same name. Then Deploy and Start accept the service again.
func (m *Manager) DiscardRestore(ctx context.Context, slug string) error {
	defer m.lockDeploy(slug)()
	hold, err := m.store.RestoreHeld(slug)
	if err != nil {
		return err
	}
	if hold == nil {
		return fmt.Errorf("no restore of %s is unfinished on this machine", slug)
	}
	provider, ok := m.providers[m.defaultKind].(runtime.RestoreProvider)
	if !ok {
		return errors.New("this machine's container runtime cannot restore a backup")
	}
	if _, err := provider.DiscardRestore(ctx, slug, hold.Volumes); err != nil {
		m.store.RecordEvent(slug, "restore_error", err.Error())
		return fmt.Errorf("the partial restore of %s was not deleted: %w", slug, err)
	}
	if err := m.store.ReleaseRestore(slug); err != nil {
		return err
	}
	m.store.RecordEvent(slug, "restore_discarded", "")
	return nil
}
