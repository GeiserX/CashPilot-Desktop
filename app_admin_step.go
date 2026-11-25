package main

import (
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// DeployAdminStep reports whether deploying slug now makes Windows show its
// admin prompt, and what that prompt changes. The setup card asks it before a
// deploy and shows its own screen first when one is needed. It only reads.
func (a *App) DeployAdminStep(slug string) (runtime.AdminStep, error) {
	if err := a.ready(); err != nil {
		return runtime.AdminStep{}, err
	}
	return a.services.AdminStepFor(slug)
}

// DeployServiceWithAdminApproval deploys with the firewall change the person
// approved on the card. The approval is scoped to this call.
func (a *App) DeployServiceWithAdminApproval(slug string, values map[string]string, image *string, instances *int, leaveRoute bool, approval runtime.AdminStep) (store.Deployment, error) {
	deployment, _, err := a.deployService(slug, values, services.DeployOptions{
		Image: image, Instances: instances, LeaveRoute: leaveRoute, AdminApproval: &approval,
	})
	return deployment, err
}
