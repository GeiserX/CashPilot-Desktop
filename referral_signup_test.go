package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// THE RULE: a signup link earns only while it still carries its referral code, and
// it has to survive all the way to the button the user clicks.
//
// There are two ways to lose it and neither is visible. The catalog can migrate a
// provider to a new domain and drop the code on the way, which internal/catalog's
// anchor check catches. Or the app can hand the UI a cleaned-up link — a stripped
// query, a canonicalised URL, a fallback to the provider's front page — and every
// signup through it pays nothing while the catalog still looks correct. This file
// covers the second half: what the frontend actually receives.
//
// The UI half of the click (the button element carrying that URL untouched) is
// checked in frontend/scripts/details_check.mjs, against the same rule.

func visibleCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadEmbedded(os.DirFS("."))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return cat
}

// Every entry the app offers must carry attribution a provider will actually read.
func TestEveryVisibleReferralCodeIsAnchoredInItsSignupURL(t *testing.T) {
	checked := 0
	for _, svc := range visibleCatalog(t).ListVisible() {
		if svc.Referral.Code == "" {
			continue
		}
		checked++
		if !catalog.CodeAttributesURL(svc.Referral.Code, svc.Referral.SignupURL) {
			t.Errorf("%s: signup_url %q does not carry referral code %q where a provider reads one, so signups through it pay nothing",
				svc.Slug, svc.Referral.SignupURL, svc.Referral.Code)
		}
	}
	if checked < 20 {
		t.Fatalf("only %d visible entries declare a referral code; the catalog did not load, so this check proves nothing", checked)
	}
}

// And the URL the frontend receives is the catalog's own, character for character.
// The app state travels to the UI as JSON, so this marshals it the same way and
// reads the value back out of the wire format rather than trusting the struct.
func TestTheSignupURLTheUIReceivesIsTheCatalogsOwn(t *testing.T) {
	cat := visibleCatalog(t)
	raw, err := json.Marshal(AppState{Services: cat.ListVisible()})
	if err != nil {
		t.Fatalf("marshal app state: %v", err)
	}

	var wire struct {
		Services []struct {
			Slug     string `json:"slug"`
			Referral struct {
				SignupURL string `json:"signupUrl"`
				Code      string `json:"code"`
			} `json:"referral"`
		} `json:"services"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("read app state back: %v", err)
	}
	if len(wire.Services) != len(cat.ListVisible()) {
		t.Fatalf("the UI receives %d services; the catalog has %d visible", len(wire.Services), len(cat.ListVisible()))
	}

	delivered := 0
	for _, sent := range wire.Services {
		svc, ok := cat.Get(sent.Slug)
		if !ok {
			t.Errorf("the UI receives a service %q that is not in the catalog", sent.Slug)
			continue
		}
		if sent.Referral.SignupURL != svc.Referral.SignupURL {
			t.Errorf("%s: the UI receives signup URL %q, the catalog declares %q",
				sent.Slug, sent.Referral.SignupURL, svc.Referral.SignupURL)
		}
		if svc.Referral.Code == "" || sent.Referral.SignupURL == "" {
			continue
		}
		delivered++
		if !catalog.CodeAttributesURL(svc.Referral.Code, sent.Referral.SignupURL) {
			t.Errorf("%s: the URL the UI receives (%q) has lost the referral code %q", sent.Slug, sent.Referral.SignupURL, svc.Referral.Code)
		}
	}
	if delivered < 20 {
		t.Fatalf("only %d referral links reached the UI; the state did not build, so this check proves nothing", delivered)
	}
}
