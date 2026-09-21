package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

func readmeBody(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	return string(raw)
}

func repoCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadEmbedded(os.DirFS("."))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return cat
}

// THE RULE: every live service's referral link is in the README.
//
// A referral link is revenue. A service that ships in the catalog but whose signup
// link is missing from the README sends every reader who finds it there to the
// provider's bare front page instead, and that signup pays nothing. Bytebenefit was
// exactly that: active in the catalog with code Brl4z3, absent from the README.
//
// The assertion is on the URL the catalog declares, character for character, because
// a link that survives as a bare domain is the same lost revenue as no link at all.
func TestEveryLiveReferralLinkIsInTheReadme(t *testing.T) {
	readme := readmeBody(t)
	for _, svc := range repoCatalog(t).ListVisible() {
		url := strings.TrimSpace(svc.Referral.SignupURL)
		if url == "" {
			continue
		}
		if !strings.Contains(readme, url) {
			t.Errorf("%s (%s) ships signup_url %q, which the README does not link", svc.Slug, svc.Status, url)
		}
	}
}

// A referral code the catalog carries must survive into the README's link, so a
// rewritten or "cleaned up" URL that drops the code is caught.
func TestReadmeReferralLinksCarryTheirCode(t *testing.T) {
	readme := readmeBody(t)
	for _, svc := range repoCatalog(t).ListVisible() {
		code := strings.TrimSpace(svc.Referral.Code)
		url := strings.TrimSpace(svc.Referral.SignupURL)
		if code == "" || url == "" || !catalog.CodeAttributesURL(code, url) {
			continue
		}
		for _, link := range regexp.MustCompile(`https?://[^\s)|]+`).FindAllString(readme, -1) {
			if strings.HasPrefix(link, url) || !strings.HasPrefix(link, strings.SplitN(url, "?", 2)[0]) {
				continue
			}
			t.Errorf("%s: the README links %q, which drops the referral code %q that %q carries",
				svc.Slug, link, code, url)
		}
	}
}

// The README states how many services ship. Retired entries stay in the catalog (the
// sync vendors the web catalog whole) but the app hides them and refuses to deploy
// them, so the honest headline number is the visible one — and a count written by
// hand goes stale on the next sync unless something checks it.
func TestReadmeServiceCountsMatchTheCatalog(t *testing.T) {
	readme := readmeBody(t)
	cat := repoCatalog(t)
	visible := len(cat.ListVisible())
	retired := len(cat.List()) - visible

	for _, tc := range []struct {
		what  string
		re    *regexp.Regexp
		count int
	}{
		{"active", regexp.MustCompile(`catalog of (\d+) active`), visible},
		{"retired", regexp.MustCompile(`carries (\d+) retired`), retired},
	} {
		match := tc.re.FindStringSubmatch(readme)
		if match == nil {
			t.Fatalf("README no longer states its %s service count (pattern %s)", tc.what, tc.re)
		}
		stated, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("parse %s count %q: %v", tc.what, match[1], err)
		}
		if stated != tc.count {
			t.Errorf("README says %d %s services; the catalog has %d", stated, tc.what, tc.count)
		}
	}
}
