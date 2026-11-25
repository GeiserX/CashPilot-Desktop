package services

import (
	"fmt"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// adminStepper is a runtime whose deploy can need the Windows admin prompt
// (runtime.NativeProcessProvider).
type adminStepper interface {
	AdminStep(svc catalog.Service, slug string) (runtime.AdminStep, error)
}

// AdminStepFor reports whether deploying slug now makes Windows show its admin
// prompt, and what for, so the app can explain it before it shows. A service the
// runtime that would deploy it never elevates for needs no step.
func (m *Manager) AdminStepFor(slug string) (runtime.AdminStep, error) {
	stepper, svc, err := m.adminStepper(slug)
	if err != nil || stepper == nil {
		return runtime.AdminStep{}, err
	}
	return stepper.AdminStep(svc, slug)
}

// namedAdminApproval gives an AdminApprovalNeeded from slug's runtime the
// Catalog name, for the starts that carry only the slug.
func (m *Manager) namedAdminApproval(slug string, err error) error {
	if svc, ok := m.catalog.Get(slug); ok {
		return runtime.NameAdminApproval(err, svc.Name)
	}
	return err
}

// adminStepper is the runtime that would deploy slug, when it is one that can
// need the admin prompt, else nil.
func (m *Manager) adminStepper(slug string) (adminStepper, catalog.Service, error) {
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return nil, catalog.Service{}, fmt.Errorf("unknown service: %s", slug)
	}
	provider, _ := m.resolveProvider(m.kindForService(svc))
	stepper, _ := provider.(adminStepper)
	return stepper, svc, nil
}
