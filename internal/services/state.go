package services

import (
	"context"
	"sort"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
)

// CriticalVolumes is what the catalog marks irreplaceable for slug, sorted by path,
// with the catalog's words for what each holds. Empty when it declares nothing or
// has no entry for slug.
func (m *Manager) CriticalVolumes(slug string) []statebackup.Volume {
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return nil
	}
	var out []statebackup.Volume
	for target, holds := range runtime.CriticalTargets(svc) {
		if holds == "" {
			holds = "Irreplaceable service state."
		}
		out = append(out, statebackup.Volume{Target: target, Holds: holds})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

func (m *Manager) stateTargets(slug string) []string {
	var targets []string
	for _, v := range m.CriticalVolumes(slug) {
		targets = append(targets, v.Target)
	}
	return targets
}

func (m *Manager) stateProvider(slug string) (runtime.StateProvider, error) {
	sp, ok := m.providerForSlug(slug).(runtime.StateProvider)
	if !ok {
		return nil, runtime.ErrNoStateRuntime
	}
	return sp, nil
}

// StateFingerprint is where slug's irreplaceable state lives right now, as a short
// hash (see statebackup.Fingerprint). "" with an error when it cannot be read.
func (m *Manager) StateFingerprint(ctx context.Context, slug string) (string, error) {
	sp, err := m.stateProvider(slug)
	if err != nil {
		return "", err
	}
	return sp.StateFingerprint(ctx, slug, m.stateTargets(slug))
}

// ReadState reads slug's critical volumes, and only those, refusing more than
// statebackup.MaxStateBytes.
func (m *Manager) ReadState(ctx context.Context, slug string) ([]byte, []string, error) {
	sp, err := m.stateProvider(slug)
	if err != nil {
		return nil, nil, err
	}
	return sp.ReadState(ctx, slug, m.stateTargets(slug), statebackup.MaxStateBytes)
}
