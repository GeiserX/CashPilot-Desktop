package catalog

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// THE RULE: a referral code counts only where a provider would read it.
//
// The check used to be strings.Contains(signup_url, code), which passes on any
// coincidence: "grass" is inside "app.grass.io", "ref" is inside "/referral". A
// provider reading such a URL sees no attribution at all, so the check would report
// a link as earning while every signup through it paid nothing — the silent failure
// referral.code exists to make loud.
func TestCodeAttributesURLRequiresARealPlacement(t *testing.T) {
	anchored := []struct{ code, url, placement string }{
		{"Brl4z3", "https://bytebenefit.io/invited?ref=Brl4z3", "query value"},
		{"7xgZ", "https://packetstream.io/?psr=7xgZ", "query value on a bare host"},
		{"f3bc51", "https://spide.network/register.html?f3bc51", "bare query key"},
		{"TSMD9wSm", "https://earnapp.com/i/TSMD9wSm", "path segment"},
		{"SERGIB4014", "https://dashboard.honeygain.com/ref/SERGIB4014", "path segment behind a prefix"},
		{"19266874", "https://pawns.app?r=19266874", "query with no path at all"},
	}
	for _, tc := range anchored {
		if !CodeAttributesURL(tc.code, tc.url) {
			t.Errorf("code %q in %q (%s) was not recognised as attribution", tc.code, tc.url, tc.placement)
		}
	}

	unanchored := []struct{ code, url, why string }{
		{"grass", "https://app.grass.io/register?referralCode=kn8FNEPnUr2tMqE", "the code is only the hostname"},
		{"ref", "https://example.com/referral/signup", "the code is only part of a path segment"},
		{"abc", "https://example.com/?ref=abcdef", "the code is only a prefix of the value"},
		{"CODE", "https://example.com/signup", "the URL lost the code in a migration"},
		{"CODE", "", "there is no URL to carry it"},
		{"", "https://example.com/?ref=CODE", "there is no code"},
		{"CODE", "https://example.com/#CODE", "a fragment is never sent to the server"},
	}
	for _, tc := range unanchored {
		if CodeAttributesURL(tc.code, tc.url) {
			t.Errorf("code %q in %q was counted as attribution, but %s", tc.code, tc.url, tc.why)
		}
	}
}

// And the rule has to hold for the catalog that actually ships, not only for
// fixtures: every declared code must sit somewhere a provider reads.
func TestEveryDeclaredReferralCodeIsAnchoredInItsURL(t *testing.T) {
	cat, err := LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	checked := 0
	for _, svc := range cat.List() {
		if svc.Referral.Code == "" {
			continue
		}
		checked++
		if !CodeAttributesURL(svc.Referral.Code, svc.Referral.SignupURL) {
			t.Errorf("%s: referral.code %q is not anchored in signup_url %q",
				svc.Slug, svc.Referral.Code, svc.Referral.SignupURL)
		}
	}
	if checked < 20 {
		t.Fatalf("only %d entries declare a referral code; the catalog did not load, so this check proves nothing", checked)
	}
}

// referral.code_entry is the code the UI asks a user to type by hand, where the
// signup never reads it from the link (Dawn's extension, Gradient's form). It is
// read from YAML, and wherever the shipped catalog carries it, it must be the code
// itself: any other value credits nobody. This is the Go half of the web repo's
// scripts/referral_check.py rule. It also passes on a catalog synced before the
// field existed, where no entry carries it.
func TestCodeEntryIsReadAndEqualsTheCode(t *testing.T) {
	var svc Service
	raw := []byte("slug: dawn\nreferral:\n  signup_url: \"https://dawninternet.com/?code=2QLQV97F\"\n  code: \"2QLQV97F\"\n  code_entry: \"2QLQV97F\"\n")
	if err := yaml.Unmarshal(raw, &svc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if svc.Referral.CodeEntry != "2QLQV97F" {
		t.Fatalf("referral.code_entry was not read, got %q", svc.Referral.CodeEntry)
	}

	cat, err := LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	for _, s := range cat.List() {
		if s.Referral.CodeEntry != "" && s.Referral.CodeEntry != s.Referral.Code {
			t.Errorf("%s: referral.code_entry %q must equal referral.code %q", s.Slug, s.Referral.CodeEntry, s.Referral.Code)
		}
	}
}
