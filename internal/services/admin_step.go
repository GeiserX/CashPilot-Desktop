package services

import (
	"fmt"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// adminStepper is a runtime whose deploy can need the Windows admin prompt
// (runtime.NativeProcessProvider): it says when, and takes the person's approval.
type adminStepper interface {
	AdminStep(svc catalog.Service, slug string) (runtime.AdminStep, error)
	ApproveAdminStep(slug string)
	RevokeAdminStep(slug string)
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

// ApproveAdminStep records the person's Continue on the app's screen for slug's
// admin step, so its next deploy may raise the prompt once.
func (m *Manager) ApproveAdminStep(slug string) error {
	stepper, _, err := m.adminStepper(slug)
	if err != nil {
		return err
	}
	if stepper != nil {
		stepper.ApproveAdminStep(slug)
	}
	return nil
}

// RevokeAdminStep withdraws slug's approval once the deploy it was given for has
// ended, so a Continue serves that one deploy and nothing after it.
func (m *Manager) RevokeAdminStep(slug string) error {
	stepper, _, err := m.adminStepper(slug)
	if err != nil {
		return err
	}
	if stepper != nil {
		stepper.RevokeAdminStep(slug)
	}
	return nil
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
