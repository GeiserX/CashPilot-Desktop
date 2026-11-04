package catalog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A small published method: three criteria, so every band and ceiling is reachable
// with a handful of verdicts. The real one is services/_ratings.yml.
const testRules = `
criteria:
  - {key: payout_record, title: Payout record}
  - {key: account_safety, title: Account safety}
  - {key: terms_fit, title: Fit}
  - {key: support, title: Support}
points: {good: 2, fair: 1, poor: 0, unknown: 0}
bands:
  - {grade: B, min_percent: 75}
  - {grade: C, min_percent: 55}
  - {grade: D, min_percent: 35}
  - {grade: F, min_percent: 0}
caps:
  - {key: container_prohibited, max_grade: D, reason: Forbids containers.}
  - {key: poor_payout_or_safety, max_grade: C, reason: Poor payout or safety.}
  - {key: thin_evidence, max_grade: C, min_unknown: 3, reason: Thin evidence.}
kinds: {terms: Terms, user_reports: Reports}
`

func mustRules(t *testing.T) *RatingRules {
	t.Helper()
	rules, err := ParseRatingRules([]byte(testRules))
	if err != nil {
		t.Fatalf("test rules refused: %v", err)
	}
	return rules
}

// svcWith decodes a service whose rating block is the given YAML, strictly, the
// way the strict-decode test reads the vendored catalog.
func svcWith(t *testing.T, extra string) Service {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader([]byte("name: X\nslug: x\n" + extra)))
	decoder.KnownFields(true)
	var svc Service
	if err := decoder.Decode(&svc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return svc
}

func item(criterion string) string {
	return "  - {text: A cited fact., criterion: " + criterion + ", kind: terms, source: \"https://example.com/terms\", checked_on: 2026-11-04}\n"
}

// valid builds a rating block with these verdicts and exactly the cons, pros and
// notes they need to validate.
func valid(verdicts map[string]string) string {
	var b strings.Builder
	b.WriteString("rating:\n  reviewed_on: \"2026-11-04\"\n  verdicts:\n")
	var cons, pros, notes strings.Builder
	for _, key := range []string{"payout_record", "account_safety", "terms_fit", "support"} {
		v := verdicts[key]
		b.WriteString("    " + key + ": " + v + "\n")
		switch v {
		case "good":
			pros.WriteString(item(key))
		case "fair", "poor":
			cons.WriteString(item(key))
		case "unknown":
			notes.WriteString("    " + key + ": Nothing found either way.\n")
		}
	}
	if notes.Len() > 0 {
		b.WriteString("  notes:\n" + notes.String())
	}
	if cons.Len() > 0 {
		b.WriteString("  cons:\n" + cons.String())
	}
	if pros.Len() > 0 {
		b.WriteString("  pros:\n" + pros.String())
	}
	return b.String()
}

func all(v string) map[string]string {
	return map[string]string{"payout_record": v, "account_safety": v, "terms_fit": v, "support": v}
}

func TestEveryGoodVerdictIsAB(t *testing.T) {
	got := GradeService(svcWith(t, valid(all("good"))), mustRules(t))
	if !got.Rated || got.Grade != "B" || got.Score != 8 || got.Max != 8 || got.Percent != 100 {
		t.Fatalf("all good: %+v", got)
	}
	if got.Summary != "B, 8 of 8" {
		t.Fatalf("summary %q", got.Summary)
	}
	if len(got.Pros) != 4 || len(got.Cons) != 0 || len(got.Caps) != 0 {
		t.Fatalf("pros/cons/caps: %+v", got)
	}
}

func TestBandsFollowThePercentage(t *testing.T) {
	rules := mustRules(t)
	cases := []struct {
		verdicts map[string]string
		want     string
	}{
		// 6 of 8 = 75%: the B floor is inclusive.
		{map[string]string{"payout_record": "good", "account_safety": "good", "terms_fit": "fair", "support": "fair"}, "B"},
		// 5 of 8 = 62.5%.
		{map[string]string{"payout_record": "good", "account_safety": "good", "terms_fit": "fair", "support": "poor"}, "C"},
		// 3 of 8 = 37.5%.
		{map[string]string{"payout_record": "fair", "account_safety": "fair", "terms_fit": "fair", "support": "poor"}, "D"},
		// 2 of 8 = 25%.
		{map[string]string{"payout_record": "fair", "account_safety": "fair", "terms_fit": "poor", "support": "poor"}, "F"},
	}
	for _, c := range cases {
		if got := GradeService(svcWith(t, valid(c.verdicts)), rules); got.Grade != c.want {
			t.Errorf("%v: grade %s (%s), want %s", c.verdicts, got.Grade, got.Summary, c.want)
		}
	}
}

func TestCeilingsLowerTheGradeAndSayWhy(t *testing.T) {
	rules := mustRules(t)

	// Poor payout record: the score earns B (6 of 8), the ceiling holds it at C.
	poor := map[string]string{"payout_record": "poor", "account_safety": "good", "terms_fit": "good", "support": "good"}
	got := GradeService(svcWith(t, valid(poor)), rules)
	if got.BandGrade != "B" || got.Grade != "C" || len(got.Caps) != 1 || got.Caps[0].Key != "poor_payout_or_safety" {
		t.Fatalf("poor payout: %+v", got)
	}

	// A provider that forbids containers: terms_fit must be poor, and it is D at best.
	forbid := map[string]string{"payout_record": "good", "account_safety": "good", "terms_fit": "poor", "support": "good"}
	got = GradeService(svcWith(t, "requirements: {container_prohibited: true}\n"+valid(forbid)), rules)
	if got.Grade != "D" || len(got.Caps) != 1 || got.Caps[0].Key != "container_prohibited" {
		t.Fatalf("container prohibited: %+v", got)
	}

	// Three unknowns: thin evidence, and unknown scores like poor.
	thin := map[string]string{"payout_record": "good", "account_safety": "unknown", "terms_fit": "unknown", "support": "unknown"}
	got = GradeService(svcWith(t, valid(thin)), rules)
	if got.Score != 2 || !got.ThinEvidence || got.UnknownCount != 3 || got.Grade != "F" {
		t.Fatalf("thin: %+v", got)
	}
	// The ceiling is above the band here, so it is not reported as lowering anything.
	if len(got.Caps) != 0 {
		t.Fatalf("a ceiling that lowered nothing was reported: %+v", got.Caps)
	}
	if !strings.Contains(got.Summary, "thin evidence: 3 of 4 criteria unchecked") {
		t.Fatalf("summary %q", got.Summary)
	}
}

// THE RULE: a grade comes only from the verdicts. A block that types one, or says
// it is sponsored, or is incomplete, is not rated, never graded.
func TestABlockThatIsNotJustEvidenceIsNotRated(t *testing.T) {
	rules := mustRules(t)
	good := valid(all("good"))
	cases := map[string]string{
		"typed grade":     good + "  grade: B\n",
		"override":        good + "  override: B\n",
		"sponsored":       good + "  sponsored: true\n",
		"missing verdict": strings.Replace(good, "    support: good\n", "", 1),
		"bad verdict":     strings.Replace(good, "support: good", "support: great", 1),
		"no reviewed_on":  strings.Replace(good, "  reviewed_on: \"2026-11-04\"\n", "", 1),
		"http source":     strings.Replace(good, "https://example.com", "http://example.com", -1),
		"unknown kind":    strings.Replace(good, "kind: terms", "kind: vibes", -1),
		"not a mapping":   "rating: B\n",
		// fair with no con citing it
		"uncited verdict": strings.Replace(good, "    support: good\n", "    support: fair\n", 1),
		// unknown with no note saying what was searched
		"unknown, no note": strings.Replace(good, "    support: good\n", "    support: unknown\n", 1),
		// forbids containers but says the terms fit
		"container prohibited, fit": "requirements: {container_prohibited: true}\n" + good,
	}
	for name, block := range cases {
		got := GradeService(svcWith(t, block), rules)
		if got.Rated || got.Grade != "" || got.Summary != NotRated {
			t.Errorf("%s: graded %q, want not rated", name, got.Grade)
		}
	}
	// The baseline the cases mutate is itself graded, or every case passes vacuously.
	if got := GradeService(svcWith(t, good), rules); !got.Rated {
		t.Fatal("the unmutated block is not rated, so the cases above prove nothing")
	}
}

func TestNoBlockNoRulesAndCustomAreNotRated(t *testing.T) {
	rules := mustRules(t)
	if got := GradeService(svcWith(t, ""), rules); got.Rated {
		t.Errorf("no block: %+v", got)
	}
	if got := GradeService(svcWith(t, valid(all("good"))), nil); got.Rated {
		t.Errorf("no rules: %+v", got)
	}
	custom := svcWith(t, valid(all("good")))
	custom.Custom = true
	if got := GradeService(custom, rules); got.Rated {
		t.Errorf("custom service: %+v", got)
	}
}

// There is no A: a rules file with an A band (or any letter outside B to F) is
// refused, and with no rules nobody is graded.
func TestARulesFileWithAnABandIsRefused(t *testing.T) {
	withA := strings.Replace(testRules, "  - {grade: B, min_percent: 75}", "  - {grade: A, min_percent: 90}\n  - {grade: B, min_percent: 75}", 1)
	if _, err := ParseRatingRules([]byte(withA)); err == nil {
		t.Fatal("a rules file with an A band was accepted")
	}
	unknownCap := strings.Replace(testRules, "key: thin_evidence", "key: paid_placement", 1)
	if _, err := ParseRatingRules([]byte(unknownCap)); err == nil {
		t.Fatal("a rules file with a ceiling this build cannot apply was accepted")
	}
}

func TestTopConsAreTheFirstThree(t *testing.T) {
	block := valid(all("poor"))
	got := GradeService(svcWith(t, block), mustRules(t))
	if len(got.Cons) != 4 || len(got.TopCons) != 3 || got.TopCons[0].Criterion != "payout_record" {
		t.Fatalf("cons %d, top %d: %+v", len(got.Cons), len(got.TopCons), got.TopCons)
	}
	if got.TopCons[0].CheckedOn != "2026-11-04" || got.TopCons[0].Source != "https://example.com/terms" {
		t.Fatalf("item fields: %+v", got.TopCons[0])
	}
}

// The vendored catalog, graded the way the app loads it: the method is found, every
// active and beta service is rated (the web catalog requires a block for each), no
// grade is an A, and EarnApp, which forbids containers, is held at D or worse.
func TestTheVendoredCatalogIsGraded(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "services", RatingRulesFile)); err != nil {
		t.Fatalf("services/%s is not vendored: %v", RatingRulesFile, err)
	}
	cat, err := LoadEmbedded(os.DirFS(root))
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	rated := 0
	for _, svc := range cat.List() {
		r := svc.RatingResult
		if r == nil {
			t.Errorf("%s: no rating result at all", svc.Slug)
			continue
		}
		if svc.Status == "active" || svc.Status == "beta" {
			if !r.Rated {
				t.Errorf("%s (%s) is not rated", svc.Slug, svc.Status)
			}
		}
		if !r.Rated {
			continue
		}
		rated++
		if gradeIndex(r.Grade) < 0 {
			t.Errorf("%s: grade %q is outside B to F", svc.Slug, r.Grade)
		}
		if svc.Requirements.ContainerProhibited && gradeIndex(r.Grade) < gradeIndex("D") {
			t.Errorf("%s forbids containers but grades %s", svc.Slug, r.Grade)
		}
	}
	if rated < 20 {
		t.Fatalf("only %d services graded; the method or the blocks did not load", rated)
	}
	earnapp, ok := cat.Get("earnapp")
	if !ok || earnapp.RatingResult == nil || gradeIndex(earnapp.RatingResult.Grade) < gradeIndex("D") {
		t.Fatalf("earnapp: %+v", earnapp.RatingResult)
	}
}
