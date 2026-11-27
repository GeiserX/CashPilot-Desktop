package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// The risk disclosure for desktop apps installed in the user's own session
// (frontend/src/render/session.ts). A manual-only provider has no container to fence:
// the user installs its closed-source app next to their files, their home network and
// the logins CashPilot keeps. The wizard shows that app's install steps only after the
// user accepts the disclosure for that one app. The frontend owns the text and its
// version; this side records which version was accepted and when, per app.

// maxConsentVersion bounds the version string the frontend sends. It is a short
// label such as "2026-11-24", never free text.
const maxConsentVersion = 64

// AcceptSessionRisk records that the user accepted the disclosure, at textVersion,
// for the manual-only service slug. Accepting again (a new version) replaces the
// entry. It returns every consent now recorded.
func (a *App) AcceptSessionRisk(slug, textVersion string) (map[string]config.SessionConsent, error) {
	if a.cfg == nil || a.catalog == nil {
		return nil, errStillStarting
	}
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return nil, errors.New(i18n.T("go.app.unknownService", "slug", slug))
	}
	if !svc.ManualOnly {
		return nil, errors.New(i18n.T("go.app.consent.container", "name", svc.Name))
	}
	if offComputer(svc.Platforms) {
		return nil, errors.New(i18n.T("go.app.consent.offComputer", "name", svc.Name))
	}
	if err := checkConsentVersion(textVersion); err != nil {
		return nil, err
	}
	entry := config.SessionConsent{TextVersion: textVersion, AcceptedAt: time.Now().UTC().Truncate(time.Second)}
	return a.updateSessionConsent(func(consents map[string]config.SessionConsent) { consents[slug] = entry })
}

// RevokeSessionRisk deletes the consent for slug, so the wizard asks again before it
// shows that app's install steps. It works for any slug, a service the catalog no
// longer lists included, and revoking a consent that does not exist is no error.
func (a *App) RevokeSessionRisk(slug string) (map[string]config.SessionConsent, error) {
	if a.cfg == nil {
		return nil, errStillStarting
	}
	return a.updateSessionConsent(func(consents map[string]config.SessionConsent) { delete(consents, slug) })
}

// updateSessionConsent changes a COPY of the consent map in one read-modify-write.
// Config() hands out the stored map by reference, so editing it in place would race
// every reader and change the live config before the file write succeeded.
func (a *App) updateSessionConsent(change func(map[string]config.SessionConsent)) (map[string]config.SessionConsent, error) {
	var next map[string]config.SessionConsent
	err := a.cfg.Update(func(c *config.AppConfig) {
		next = make(map[string]config.SessionConsent, len(c.SessionConsent)+1)
		for k, v := range c.SessionConsent {
			next[k] = v
		}
		change(next)
		if len(next) == 0 {
			c.SessionConsent = nil
			return
		}
		c.SessionConsent = next
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]config.SessionConsent, len(next))
	for k, v := range next {
		out[k] = v
	}
	return out, nil
}

// offComputerPlatforms are the platforms whose app never runs on this computer.
// The frontend's needsSessionConsent (render/session.ts) asks every manual-only app
// unless its list is non-empty and holds only these; this is the same rule.
var offComputerPlatforms = map[string]bool{"android": true, "ios": true, "hardware": true}

// offComputer reports whether a catalog platform list says the app never runs on
// this computer. An empty list says nothing, so it is false: the gate fails closed.
func offComputer(platforms []string) bool {
	if len(platforms) == 0 {
		return false
	}
	for _, p := range platforms {
		if !offComputerPlatforms[strings.ToLower(strings.TrimSpace(p))] {
			return false
		}
	}
	return true
}

func checkConsentVersion(version string) error {
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("the disclosure version is missing")
	}
	if len(version) > maxConsentVersion {
		return fmt.Errorf("the disclosure version is longer than %d characters", maxConsentVersion)
	}
	if strings.IndexFunc(version, unicode.IsControl) >= 0 {
		return fmt.Errorf("the disclosure version holds a control character")
	}
	return nil
}
