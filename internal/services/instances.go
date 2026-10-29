package services

import (
	"context"
	"fmt"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// MaxInstances is the most copies of one service this machine will run. A ceiling
// against a typo, not a recommendation: on one connection most providers pay for
// one. The web app uses the same number.
const MaxInstances = 10

// ScalingLimit is how many copies of a service this machine may run, and why not
// more. Reason is "" when more than one is allowed.
type ScalingLimit struct {
	Max    int    `json:"max"`
	Reason string `json:"reason"`
}

// ScalingLimit decides the Instances box for svc. One copy where the provider allows
// one device per IP (a second copy behind the same address earns nothing), where the
// service has host volumes or published ports (copies would share the directories
// and fight over the ports), where a typed device id (DeviceLink) names the device,
// since every copy would carry the same one, and where it runs as a native program
// here, which this app starts once. Otherwise MaxInstances. The reasons are the web app's wording.
func (m *Manager) ScalingLimit(svc catalog.Service) ScalingLimit {
	name := svc.Name
	if name == "" {
		name = svc.Slug
	}
	if perIP := svc.Requirements.DevicesPerIP; perIP != nil && *perIP == 1 {
		return ScalingLimit{1, name + " allows one device per IP address, so a second copy on one machine earns nothing."}
	}
	if len(svc.Docker.Volumes) > 0 {
		return ScalingLimit{1, name + " keeps data in a host directory, which two copies would share."}
	}
	if len(svc.Docker.Ports) > 0 {
		return ScalingLimit{1, name + " publishes a port on the host, which two copies cannot share."}
	}
	if link := svc.DeviceLink; link != nil && link.IDEnv != "" {
		// Copies get the same env, so they would all report one device id.
		label := link.IDEnv
		for _, item := range svc.Docker.Env {
			if item.Key == link.IDEnv && item.Label != "" {
				label = item.Label
			}
		}
		return ScalingLimit{1, name + " knows each device by its " + label + ", which two copies would share."}
	}
	if m.kindForService(svc) == nativeRuntimeKind {
		return ScalingLimit{1, name + " runs here as a program rather than a container, and CashPilot runs it once."}
	}
	return ScalingLimit{MaxInstances, ""}
}

// ScalingLimits is ScalingLimit for every service in the catalog, by slug, for the
// deploy form.
func (m *Manager) ScalingLimits(services []catalog.Service) map[string]ScalingLimit {
	out := make(map[string]ScalingLimit, len(services))
	for _, svc := range services {
		out[svc.Slug] = m.ScalingLimit(svc)
	}
	return out
}

// Copies returns copies 2..N of every service, by slug, as last seen.
func (m *Manager) Copies() map[string][]runtime.ContainerInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]runtime.ContainerInfo, len(m.copies))
	for slug, copies := range m.copies {
		out[slug] = append([]runtime.ContainerInfo(nil), copies...)
	}
	return out
}

func (m *Manager) setCopies(slug string, copies []runtime.ContainerInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.copies == nil {
		m.copies = make(map[string][]runtime.ContainerInfo)
	}
	if len(copies) == 0 {
		delete(m.copies, slug)
		return
	}
	m.copies[slug] = copies
}

// copier is the runtime that holds slug's copies 2..N, refusing a copy number that
// cannot exist. Copy 1 is the service itself and goes through Stop/Start/Logs.
func (m *Manager) copier(slug string, n int) (runtime.CopyProvider, error) {
	if n < 2 || n > MaxInstances {
		return nil, fmt.Errorf("copy must be between 2 and %d", MaxInstances)
	}
	copier, ok := m.providerForSlug(slug).(runtime.CopyProvider)
	if !ok {
		return nil, fmt.Errorf("%s runs once on this runtime", slug)
	}
	return copier, nil
}

func (m *Manager) StopCopy(ctx context.Context, slug string, n int) error {
	copier, err := m.copier(slug, n)
	if err != nil {
		return err
	}
	if err := copier.StopCopy(ctx, slug, n, m.stopTimeout(slug)); err != nil {
		m.store.RecordEvent(slug, "stop_error", fmt.Sprintf("copy %d: %v", n, err))
		return err
	}
	m.store.RecordEvent(slug, "stopped", fmt.Sprintf("copy %d", n))
	return nil
}

func (m *Manager) StartCopy(ctx context.Context, slug string, n int) error {
	copier, err := m.copier(slug, n)
	if err != nil {
		return err
	}
	if err := copier.StartCopy(ctx, slug, n); err != nil {
		m.store.RecordEvent(slug, "start_error", fmt.Sprintf("copy %d: %v", n, err))
		return err
	}
	m.store.RecordEvent(slug, "started", fmt.Sprintf("copy %d", n))
	return nil
}

func (m *Manager) LogsCopy(ctx context.Context, slug string, n, lines int) (string, error) {
	copier, err := m.copier(slug, n)
	if err != nil {
		return "", err
	}
	return copier.LogsCopy(ctx, slug, n, lines)
}
