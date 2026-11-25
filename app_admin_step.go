package main

import "github.com/GeiserX/CashPilot-Desktop/internal/runtime"

// DeployAdminStep reports whether deploying slug now makes Windows show its
// admin prompt, and what that prompt changes. The setup card asks it before a
// deploy and shows its own screen first when one is needed. It only reads.
func (a *App) DeployAdminStep(slug string) (runtime.AdminStep, error) {
	if err := a.ready(); err != nil {
		return runtime.AdminStep{}, err
	}
	return a.services.AdminStepFor(slug)
}

// ApproveDeployAdminStep is the Continue button of that screen: the next deploy
// of slug may raise the admin prompt, once. Without it a deploy that needs the
// prompt stops and says so, and Windows shows nothing.
func (a *App) ApproveDeployAdminStep(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.services.ApproveAdminStep(slug)
}

// RevokeDeployAdminStep withdraws that approval once the card's deploy has ended,
// however it ended. A deploy that used it is unchanged; one that stopped first (a
// later question was cancelled) leaves no approval another path could use to
// raise the prompt with no screen first.
func (a *App) RevokeDeployAdminStep(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.services.RevokeAdminStep(slug)
}
