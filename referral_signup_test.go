package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
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

// How account_rules says there is no account: "no account", "no signup or
// account", "no sign-up", "there is no account in the usual sense".
var saysNoAccount = regexp.MustCompile(`(?i)\bno (?:sign-?up or )?account\b|\bno sign-?up\b`)

// cardText is every catalog text the UI receives for a service's card: the
// description, the disclosure rows, the rating's pros, cons, caps and notes, and
// the env fields' labels and hints. Read from the app state as the UI gets it.
type cardText struct {
	Slug             string `json:"slug"`
	Description      string `json:"description"`
	ShortDescription string `json:"shortDescription"`
	Referral         struct {
		SignupURL string `json:"signupUrl"`
		Account   *bool  `json:"account"`
	} `json:"referral"`
	Disclosure   map[string]string `json:"disclosure"`
	RatingResult *struct {
		Caps    []struct{ Reason string } `json:"caps"`
		Notes   map[string]string         `json:"notes"`
		Cons    []struct{ Text string }   `json:"cons"`
		Pros    []struct{ Text string }   `json:"pros"`
		TopCons []struct{ Text string }   `json:"topCons"`
	} `json:"ratingResult"`
	Docker struct {
		Env []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"env"`
	} `json:"docker"`
}

// fields names each text, so a failure says where the claim is.
func (c cardText) fields() map[string]string {
	out := map[string]string{"description": c.Description, "short_description": c.ShortDescription}
	for k, v := range c.Disclosure {
		out["disclosure."+k] = v
	}
	if r := c.RatingResult; r != nil {
		for i, x := range r.Caps {
			out[fmt.Sprintf("rating.caps[%d]", i)] = x.Reason
		}
		for k, v := range r.Notes {
			out["rating.notes."+k] = v
		}
		for i, x := range r.Cons {
			out[fmt.Sprintf("rating.cons[%d]", i)] = x.Text
		}
		for i, x := range r.TopCons {
			out[fmt.Sprintf("rating.topCons[%d]", i)] = x.Text
		}
		for i, x := range r.Pros {
			out[fmt.Sprintf("rating.pros[%d]", i)] = x.Text
		}
	}
	for i, e := range c.Docker.Env {
		out[fmt.Sprintf("docker.env[%d]", i)] = e.Label + " " + e.Description
	}
	return out
}

// A provider with no account to sign up for must not get a "Create account" button.
// Anyone Protocol's card said "No account is needed at all" next to one, and
// Golem's pros said "No account" next to one. The button reads referral.account,
// so an entry whose card text says there is no account anywhere has to say it
// there too, and the UI has to receive it.
func TestAnEntryWithNoAccountTellsTheUISo(t *testing.T) {
	raw, err := json.Marshal(AppState{Services: visibleCatalog(t).ListVisible()})
	if err != nil {
		t.Fatalf("marshal app state: %v", err)
	}
	var wire struct {
		Services []cardText `json:"services"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("read app state back: %v", err)
	}
	noAccount := map[string]bool{}
	read := 0
	for _, svc := range wire.Services {
		fields := svc.fields()
		read += len(fields)
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := fields[k]
			if strings.Contains(v, "needed at all") {
				t.Errorf("%s: %s says nothing is needed at all, while every entry needs a wallet, a node or both: %q", svc.Slug, k, v)
			}
			if !saysNoAccount.MatchString(v) || noAccount[svc.Slug] {
				continue
			}
			noAccount[svc.Slug] = true
			if svc.Referral.Account == nil || *svc.Referral.Account {
				t.Errorf("%s: %s says there is no account (%q), but the UI receives referral.account %v, so the card offers Create account",
					svc.Slug, k, v, svc.Referral.Account)
			}
		}
	}
	// Controls: the reader must have seen the texts, and the claims it exists for.
	for _, slug := range []string{"anyone-protocol", "golem"} {
		if !noAccount[slug] {
			t.Fatalf("%s no longer reads as having no account; this check proves nothing: %v", slug, noAccount)
		}
	}
	if read < 10*len(wire.Services) {
		t.Fatalf("read only %d texts for %d services; the card text did not reach the check", read, len(wire.Services))
	}
}
