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

func TestABandComparesTheExactShareNotTheRoundedOne(t *testing.T) {
	// 3 good and 1 fair at these points is 99.98%, shown as 100.0%. With A from
	// 99.99% the score is short of it, whatever the display says.
	src := strings.Replace(testRules, "points: {good: 2, fair: 1, poor: 0, unknown: 0}",
		"points: {good: 10000, fair: 9992, poor: 0, unknown: 0}", 1)
	src = strings.Replace(src, "bands:\n", "bands:\n  - {grade: A, min_percent: 99.99}\n", 1)
	rules, err := ParseRatingRules([]byte(src))
	if err != nil {
		t.Fatalf("rules refused: %v", err)
	}
	verdicts := all("good")
	verdicts["support"] = "fair"
	got := GradeService(svcWith(t, valid(verdicts)), rules)
	if got.Percent != 100 || got.Grade != "B" {
		t.Fatalf("want 100.0%% shown and grade B, got %+v", got)
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
		// YAML reads `1:` as an int; the web validator once crashed sorting it
		// next to a string key, so both sides pin "not rated, no crash".
		"mixed-type keys":         good + "  1: x\n  typo: y\n",
		"mixed-type verdict keys": strings.Replace(good, "  verdicts:\n", "  verdicts:\n    2: good\n    zz: fair\n", 1),
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

// A is the top band, as on the web; a letter outside A to F is refused, and
// with no rules nobody is graded.
func TestAnABandGradesTheTopAndAnUnknownLetterIsRefused(t *testing.T) {
	withA := strings.Replace(testRules, "  - {grade: B, min_percent: 75}", "  - {grade: A, min_percent: 90}\n  - {grade: B, min_percent: 75}", 1)
	rules, err := ParseRatingRules([]byte(withA))
	if err != nil {
		t.Fatalf("a rules file with an A band was refused: %v", err)
	}
	if got := GradeService(svcWith(t, valid(all("good"))), rules); got.Grade != "A" {
		t.Fatalf("every criterion good under an A band: %+v", got)
	}
	withZ := strings.Replace(testRules, "  - {grade: B, min_percent: 75}", "  - {grade: Z, min_percent: 90}\n  - {grade: B, min_percent: 75}", 1)
	if _, err := ParseRatingRules([]byte(withZ)); err == nil {
		t.Fatal("a rules file with a Z band was accepted")
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

// Every other way a block can be wrong is not rated too: the web validator's full
// list, one mutation each.
func TestEveryValidationRuleRefusesTheBlock(t *testing.T) {
	rules := mustRules(t)
	good := valid(all("good"))
	cases := map[string]string{
		"verdicts not a mapping": "rating:\n  reviewed_on: \"2026-11-04\"\n  verdicts: [good]\n",
		"unknown criterion":      strings.Replace(good, "    support: good\n", "    support: good\n    vibes: good\n", 1),
		"notes not a mapping":    good + "  notes: [x]\n",
		"note on unknown key":    good + "  notes: {vibes: Searched.}\n",
		"blank note":             good + "  notes: {support: \"  \"}\n",
		"cons not a list":        good + "  cons: nope\n",
		"item not a mapping":     good + "  cons: [just text]\n",
		"blank text":             strings.Replace(good, "text: A cited fact.", "text: \"\"", 1),
		"unpublished criterion":  good + item("vibes"),
		"source with a space":    strings.Replace(good, "https://example.com/terms", "https://example.com/a b", 1),
		"bad checked_on":         strings.Replace(good, "checked_on: 2026-11-04", "checked_on: last week", 1),
		"bad reviewed_on":        strings.Replace(good, "\"2026-11-04\"", "\"04/11/2026\"", 1),
		"good with no pro":       strings.Replace(good, item("support"), "", 1),
	}
	for name, block := range cases {
		if got := GradeService(svcWith(t, block), rules); got.Rated {
			t.Errorf("%s: graded %q, want not rated", name, got.Grade)
		}
	}
	// Blank sections are the same as absent ones.
	if got := GradeService(svcWith(t, good+"  cons: []\n  notes: \"\"\n"), rules); !got.Rated {
		t.Errorf("empty cons and notes refused a valid block: %+v", got)
	}
}

func TestARulesFileThisBuildCannotApplyIsRefused(t *testing.T) {
	cases := map[string]string{
		"not yaml":            "criteria: [",
		"no criteria":         strings.Replace(testRules, "criteria:", "unused:", 1),
		"duplicate key":       strings.Replace(testRules, "{key: support, title: Support}", "{key: terms_fit, title: Support}", 1),
		"no bands":            strings.Replace(testRules, "bands:", "unused_bands:", 1),
		"cap grade Z":         strings.Replace(testRules, "max_grade: D", "max_grade: Z", 1),
		"no unknown points":   strings.Replace(testRules, ", unknown: 0", "", 1),
		"no kinds":            strings.Replace(testRules, "kinds:", "unused_kinds:", 1),
		"bands out of order":  strings.Replace(testRules, "{grade: C, min_percent: 55}", "{grade: C, min_percent: 80}", 1),
		"duplicate band":      strings.Replace(testRules, "{grade: C, min_percent: 55}", "{grade: C, min_percent: 75}", 1),
		"negative points":     strings.Replace(testRules, "poor: 0", "poor: -1", 1),
		"no thin threshold":   strings.Replace(testRules, ", min_unknown: 3", "", 1),
		"zero thin threshold": strings.Replace(testRules, "min_unknown: 3", "min_unknown: 0", 1),
	}
	for name, raw := range cases {
		if _, err := ParseRatingRules([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestWithoutAMethodNobodyIsGraded(t *testing.T) {
	for name, apply := range map[string]func([]Service){
		"unreadable": func(s []Service) { applyRatings(s, nil, os.ErrNotExist) },
		"refused":    func(s []Service) { applyRatings(s, []byte("criteria: []"), nil) },
		"valid":      func(s []Service) { applyRatings(s, []byte(testRules), nil) },
	} {
		services := []Service{svcWith(t, valid(all("good")))}
		apply(services)
		want := name == "valid"
		if services[0].RatingResult == nil || services[0].RatingResult.Rated != want {
			t.Errorf("%s: %+v, want rated=%v", name, services[0].RatingResult, want)
		}
	}
}
