package main

import (
	"maps"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
)

// CredentialView contains ordinary settings and the presence of saved secrets.
type CredentialView struct {
	Values       map[string]string `json:"values"`
	SavedSecrets map[string]bool   `json:"savedSecrets"`
}

func (a *App) GetCredentials(slug string) (CredentialView, error) {
	if err := a.ready(); err != nil {
		return CredentialView{}, err
	}
	stored, err := a.store.GetCredentials(slug)
	if err != nil {
		return CredentialView{}, err
	}
	view := CredentialView{Values: map[string]string{}, SavedSecrets: map[string]bool{}}
	for key, private := range a.credentialFields(slug) {
		value, ok := stored[key]
		if private {
			if strings.TrimSpace(value) != "" {
				view.SavedSecrets[key] = true
			}
		} else if ok {
			view.Values[key] = value
		}
	}
	return view, nil
}

// credentialFields lists the known fields and whether they are secret. A
// seed-file variable is handled by the wallet flow and excluded altogether.
func (a *App) credentialFields(slug string) map[string]bool {
	svc, ok := a.catalog.Get(slug)
	if !ok || catalog.IsCustomSlug(slug) {
		return nil
	}
	fields := map[string]bool{}
	for _, env := range [][]catalog.EnvVar{svc.Docker.Env, svc.Native.Env} {
		for _, field := range env {
			fields[field.Key] = fields[field.Key] || field.Secret
		}
	}
	for _, field := range collectors.Fields(slug) {
		fields[field.Key] = fields[field.Key] || field.Secret
	}
	if seed, ok := svc.SeedVar(); ok {
		delete(fields, seed.Key)
	}
	return fields
}

func (a *App) SaveCredentials(slug string, values map[string]string) error {
	if err := a.ready(); err != nil {
		return err
	}
	a.credentialsMu.Lock()
	defer a.credentialsMu.Unlock()
	creds, err := a.withSavedSecrets(slug, values, false)
	if err != nil {
		return err
	}
	return a.saveCredentials(slug, creds)
}

// withSavedSecrets fills in omitted or blank secret fields. An empty deploy
// request reuses every saved setting. A nonsecret field in a submitted form
// still follows the form's value, including an explicit blank.
func (a *App) withSavedSecrets(slug string, values map[string]string, deploy bool) (map[string]string, error) {
	stored, err := a.store.GetCredentials(slug)
	if err != nil {
		return nil, err
	}
	if deploy && len(values) == 0 {
		return stored, nil
	}
	merged := make(map[string]string, len(values))
	maps.Copy(merged, values)
	for key, private := range a.credentialFields(slug) {
		if private && strings.TrimSpace(merged[key]) == "" {
			delete(merged, key)
			if strings.TrimSpace(stored[key]) != "" {
				merged[key] = stored[key]
			}
		}
	}
	return merged, nil
}

// saveCredentials stores the prepared settings, keeping any legacy seed
// until its migration to the wallet file has finished.
func (a *App) saveCredentials(slug string, values map[string]string) error {
	toSave, err := a.services.CredentialsToSave(slug, values)
	if err != nil {
		return err
	}
	return a.store.SaveCredentials(slug, toSave)
}

func (a *App) prepareDeployCredentials(slug string, values map[string]string, image *string) (map[string]string, error) {
	a.credentialsMu.Lock()
	defer a.credentialsMu.Unlock()
	// Custom services deploy their saved definition without a credential form.
	var creds map[string]string
	if !catalog.IsCustomSlug(slug) {
		var err error
		creds, err = a.withSavedSecrets(slug, values, true)
		if err != nil {
			return nil, err
		}
	}
	// A rejected deploy must leave saved credentials as they were.
	if err := a.services.ValidateDeploy(slug, creds); err != nil {
		a.emitError("deploy", err)
		return nil, err
	}
	if image != nil {
		if err := services.ValidateImage(*image); err != nil {
			a.emitError("deploy", err)
			return nil, err
		}
	}
	if len(values) > 0 && !catalog.IsCustomSlug(slug) {
		if err := a.saveCredentials(slug, creds); err != nil {
			return nil, err
		}
	}
	return creds, nil
}

// A worker deploy saves the nonblank fields supplied for the successful attempt.
// The current stored values fill the other fields, as they do for the worker spec.
func (a *App) saveWorkerCredentials(slug string, values map[string]string) error {
	typed := map[string]string{}
	for key, value := range values {
		if strings.TrimSpace(value) != "" {
			typed[key] = value
		}
	}
	if len(typed) == 0 {
		return nil
	}
	a.credentialsMu.Lock()
	defer a.credentialsMu.Unlock()
	stored, err := a.store.GetCredentials(slug)
	if err != nil {
		return err
	}
	merged := make(map[string]string, len(stored)+len(typed))
	maps.Copy(merged, stored)
	maps.Copy(merged, typed)
	return a.saveCredentials(slug, merged)
}
