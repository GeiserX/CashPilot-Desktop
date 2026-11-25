package main

import (
	"fmt"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
)

// checkFormFields refuses a deploy from the setup card while a field the card
// marks with * is empty, before anything is saved or started. The card shows the
// container's settings (docker.env) and the logins the earnings collector needs
// (collectors.Fields), and marks the required ones of both. ValidateDeploy checks
// only what the container needs, so a login marked * could be left empty and the
// deploy went on, admin prompt included.
//
// A deploy that sends no values (a redeploy, an imported service, a command from
// the server) uses what is saved and is not a form, so it is not checked here.
func (a *App) checkFormFields(slug string, values map[string]string) error {
	if len(values) == 0 || catalog.IsCustomSlug(slug) {
		return nil
	}
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return nil // ValidateDeploy names an unknown service.
	}
	missing := missingFormFields(svc, collectors.Fields(slug), values)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("nothing was deployed. Fill in every field marked *: %s", strings.Join(missing, ", "))
}

// missingFormFields lists, in the card's order, the labels of the fields the card
// marks required that values leaves empty. A container setting with a default is
// never missing: the deploy falls back to the default.
func missingFormFields(svc catalog.Service, login []collectors.Field, values map[string]string) []string {
	var missing []string
	inEnv := map[string]bool{}
	for _, item := range svc.Docker.Env {
		inEnv[item.Key] = true
		if item.Required && item.Default == "" && strings.TrimSpace(values[item.Key]) == "" {
			missing = append(missing, labelOr(item.Label, item.Key))
		}
	}
	for _, field := range login {
		if !inEnv[field.Key] && field.Required && strings.TrimSpace(values[field.Key]) == "" {
			missing = append(missing, labelOr(field.Label, field.Key))
		}
	}
	return missing
}

func labelOr(label, key string) string {
	if label != "" {
		return label
	}
	return key
}
