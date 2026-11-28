package services

import (
	"context"
	"errors"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// shapeReader is a runtime that can say what its containers were created with.
// Only the Docker provider can; the native runtime runs processes, not containers.
type shapeReader interface {
	ContainerShapes(ctx context.Context) ([]runtime.ContainerShape, error)
}

// ContainerDrift is, per deployed slug, how its running container differs from what
// a redeploy would build from the current catalog (see runtime.ShapeDrift). A
// service with several copies reports its first copy that differs. A runtime that
// cannot be read reports nothing rather than a guess.
func (m *Manager) ContainerDrift(ctx context.Context) map[string][]string {
	reader, ok := m.providers[m.defaultKind].(shapeReader)
	if !ok || m.catalog == nil {
		return nil
	}
	shapes, err := reader.ContainerShapes(ctx)
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	for _, shape := range shapes {
		if _, done := out[shape.Slug]; done {
			continue
		}
		svc, inCatalog := m.catalog.Get(shape.Slug)
		if !inCatalog {
			continue
		}
		custom := ""
		if dep, found, err := m.store.GetDeployment(shape.Slug); err == nil && found {
			custom = dep.CustomImage
		}
		if found := runtime.ShapeDrift(svc, shape, custom); len(found) > 0 {
			out[shape.Slug] = found
		}
	}
	return out
}

// LooseMounts is, per container ID of an app, the folders it keeps outside
// what a redeploy mounts (see runtime.LooseMounts). Every container that was
// read has a key, with no folders when it has none; one that could not be read
// has none. An error means nothing was read.
func (m *Manager) LooseMounts(ctx context.Context) (map[string][]string, error) {
	reader, ok := m.providers[m.defaultKind].(shapeReader)
	if !ok || m.catalog == nil {
		return nil, errors.New("this runtime cannot say how its containers were created")
	}
	shapes, err := reader.ContainerShapes(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, shape := range shapes {
		svc, found := m.catalog.Get(shape.Slug)
		if !found {
			continue
		}
		out[shape.ID] = runtime.LooseMounts(svc, shape)
	}
	return out, nil
}
