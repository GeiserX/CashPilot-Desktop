package catalog

// Provider grades, computed from published criteria. Port of app/ratings.py in the
// web CashPilot (GeiserX/CashPilot#572); the two must grade every provider the same.
//
// The method lives in services/_ratings.yml, vendored from the web catalog: the
// criteria, what each verdict is worth, the grade bands and the ceilings. Each service
// YAML records one verdict per criterion in its `rating:` block, and every verdict
// rests on a cited con or pro. This file turns the verdicts into a letter.
//
// Three rules hold the design together, the same as on the web:
//
//   - Nobody types a grade. The rating block has no grade, override, sponsor or
//     featured field, and validateRating rejects any key it does not know, so the
//     only way to move a grade is to change a verdict and cite why.
//   - There is no A. The bands stop at B, and a rules file that declares any other
//     letter is refused, which leaves every provider "Not rated yet".
//   - Missing evidence never reads as good. "unknown" scores the same as "poor",
//     four unknowns cap the grade, and a service with no block (or a block that does
//     not validate) is "Not rated yet", never a neutral grade.

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RatingRulesFile is the published method, beside the service files.
const RatingRulesFile = "_ratings.yml"

// NotRated is what a provider without a valid rating block shows.
const NotRated = "Not rated yet"

// ratingAllowedKeys are the keys a rating block may hold. Anything else (grade,
// override, sponsored, featured, paid...) is an error, which keeps grades
// criteria-only.
var ratingAllowedKeys = map[string]bool{"reviewed_on": true, "verdicts": true, "notes": true, "cons": true, "pros": true}

var ratingVerdicts = map[string]bool{"good": true, "fair": true, "poor": true, "unknown": true}

// gradeOrder is best first. A ceiling of D means the grade is D or anything after it.
var gradeOrder = []string{"B", "C", "D", "F"}

func gradeIndex(g string) int {
	for i, v := range gradeOrder {
		if v == g {
			return i
		}
	}
	return -1
}

// Rating is a service's raw `rating:` block. It is kept as a YAML node and checked
// at load rather than decoded into a struct, so a bad block (a typed grade, a typo)
// makes the provider "Not rated yet" instead of failing the whole catalog.
type Rating struct {
	node *yaml.Node
}

// UnmarshalYAML keeps the block for validateRating.
func (r *Rating) UnmarshalYAML(n *yaml.Node) error {
	copied := *n
	r.node = &copied
	return nil
}

// RatingRules is services/_ratings.yml, the part the grade is computed from.
type RatingRules struct {
	Criteria []struct {
		Key   string `yaml:"key"`
		Title string `yaml:"title"`
	} `yaml:"criteria"`
	Points map[string]int `yaml:"points"`
	Bands  []struct {
		Grade      string  `yaml:"grade"`
		MinPercent float64 `yaml:"min_percent"`
	} `yaml:"bands"`
	Caps []struct {
		Key        string `yaml:"key"`
		MaxGrade   string `yaml:"max_grade"`
		Reason     string `yaml:"reason"`
		MinUnknown int    `yaml:"min_unknown"`
	} `yaml:"caps"`
	Kinds map[string]string `yaml:"kinds"`
}

// knownCaps are the ceilings this code knows how to test. The web raises on any
// other key; here an unknown one refuses the rules file, so nobody is graded on a
// method this build cannot apply.
var knownCaps = map[string]bool{"container_prohibited": true, "poor_payout_or_safety": true, "thin_evidence": true}

// ParseRatingRules reads the published method and refuses one this build cannot apply
// faithfully: no criteria, no bands, a letter outside B to F, or an unknown ceiling.
func ParseRatingRules(raw []byte) (*RatingRules, error) {
	var rules RatingRules
	if err := yaml.Unmarshal(raw, &rules); err != nil {
		return nil, err
	}
	if len(rules.Criteria) == 0 {
		return nil, fmt.Errorf("no criteria")
	}
	seen := map[string]bool{}
	for _, c := range rules.Criteria {
		if c.Key == "" || c.Title == "" || seen[c.Key] {
			return nil, fmt.Errorf("criterion %q needs a unique key and a title", c.Key)
		}
		seen[c.Key] = true
	}
	if len(rules.Bands) == 0 {
		return nil, fmt.Errorf("no grade bands")
	}
	for _, b := range rules.Bands {
		if gradeIndex(b.Grade) < 0 {
			return nil, fmt.Errorf("band grade %q is not one of %v", b.Grade, gradeOrder)
		}
	}
	for _, c := range rules.Caps {
		if !knownCaps[c.Key] {
			return nil, fmt.Errorf("unknown cap %q", c.Key)
		}
		if gradeIndex(c.MaxGrade) < 0 {
			return nil, fmt.Errorf("cap %q grade %q is not one of %v", c.Key, c.MaxGrade, gradeOrder)
		}
	}
	for v := range ratingVerdicts {
		if _, ok := rules.Points[v]; !ok {
			return nil, fmt.Errorf("points has no value for %q", v)
		}
	}
	if len(rules.Kinds) == 0 {
		return nil, fmt.Errorf("no kinds")
	}
	return &rules, nil
}

// RatingItem is one cited con or pro.
type RatingItem struct {
	Text      string `json:"text"`
	Criterion string `json:"criterion"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	CheckedOn string `json:"checkedOn"`
}

// RatingVerdict is one criterion's verdict, with its title.
type RatingVerdict struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Verdict string `json:"verdict"`
}

// RatingCap is a ceiling that lowered the grade the score earned.
type RatingCap struct {
	Key      string `json:"key"`
	MaxGrade string `json:"maxGrade"`
	Reason   string `json:"reason"`
}

// RatingResult is a provider's grade with everything that explains it, the same
// fields app/ratings.py for_service returns (camel-cased for the frontend).
type RatingResult struct {
	Rated        bool              `json:"rated"`
	Grade        string            `json:"grade"`
	BandGrade    string            `json:"bandGrade"`
	Score        int               `json:"score"`
	Max          int               `json:"max"`
	Percent      float64           `json:"percent"`
	Caps         []RatingCap       `json:"caps"`
	UnknownCount int               `json:"unknownCount"`
	ThinEvidence bool              `json:"thinEvidence"`
	ReviewedOn   string            `json:"reviewedOn"`
	Verdicts     []RatingVerdict   `json:"verdicts"`
	Notes        map[string]string `json:"notes"`
	Cons         []RatingItem      `json:"cons"`
	Pros         []RatingItem      `json:"pros"`
	TopCons      []RatingItem      `json:"topCons"`
	Summary      string            `json:"summary"`
}

func notRated() *RatingResult { return &RatingResult{Summary: NotRated} }

// isoDate reads an ISO date from a string, or from the timestamp YAML makes of an
// unquoted one. "" when it is neither.
func isoDate(v any) string {
	switch d := v.(type) {
	case time.Time:
		return d.Format("2006-01-02")
	case string:
		t, err := time.Parse("2006-01-02", strings.TrimSpace(d))
		if err != nil {
			return ""
		}
		return t.Format("2006-01-02")
	}
	return ""
}

// empty is Python's falsiness for the values `x or default` replaces in the web
// validator: an absent, blank or empty notes, cons or pros counts as none.
func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// validateRating is every problem with one service's rating block, in plain words.
// Empty means valid. It is app/ratings.py validate with check_future=False, which is
// what the web app runs at load: a box whose clock is behind must not drop every
// block reviewed today. The web CI keeps the future-date check.
func validateRating(block any, svc Service, rules *RatingRules) []string {
	rating, ok := block.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("rating must be a mapping, not %T", block)}
	}
	var errs []string

	var extra []string
	for k := range rating {
		if !ratingAllowedKeys[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		errs = append(errs, fmt.Sprintf("rating has key(s) %v that are not allowed; a grade comes only from the verdicts", extra))
	}
	if isoDate(rating["reviewed_on"]) == "" {
		errs = append(errs, "rating.reviewed_on must be an ISO date (YYYY-MM-DD)")
	}

	keys := map[string]bool{}
	for _, c := range rules.Criteria {
		keys[c.Key] = true
	}
	verdicts, ok := rating["verdicts"].(map[string]any)
	if !ok {
		errs = append(errs, "rating.verdicts must be a mapping of criterion to verdict")
		verdicts = map[string]any{}
	}
	for _, c := range rules.Criteria {
		if _, ok := verdicts[c.Key]; !ok {
			errs = append(errs, fmt.Sprintf("rating.verdicts is missing %s; every criterion needs a verdict", c.Key))
		}
	}
	for k, v := range verdicts {
		if !keys[k] {
			errs = append(errs, fmt.Sprintf("rating.verdicts names unknown criterion %s", k))
		}
		if s, isStr := v.(string); !isStr || !ratingVerdicts[s] {
			errs = append(errs, fmt.Sprintf("rating.verdicts.%s must be good, fair, poor or unknown, got %v", k, v))
		}
	}

	notes := map[string]any{}
	if !empty(rating["notes"]) {
		if m, ok := rating["notes"].(map[string]any); ok {
			notes = m
		} else {
			errs = append(errs, "rating.notes must be a mapping of criterion to text")
		}
	}
	for k, v := range notes {
		if !keys[k] {
			errs = append(errs, fmt.Sprintf("rating.notes names unknown criterion %q", k))
		} else if s, isStr := v.(string); !isStr || strings.TrimSpace(s) == "" {
			errs = append(errs, fmt.Sprintf("rating.notes.%s must be text", k))
		}
	}

	tagged := map[string]map[string]bool{"cons": {}, "pros": {}}
	for _, section := range []string{"cons", "pros"} {
		if empty(rating[section]) {
			continue
		}
		items, ok := rating[section].([]any)
		if !ok {
			errs = append(errs, fmt.Sprintf("rating.%s must be a list", section))
			continue
		}
		for i, raw := range items {
			where := fmt.Sprintf("rating.%s[%d]", section, i)
			item, ok := raw.(map[string]any)
			if !ok {
				errs = append(errs, where+" must be a mapping")
				continue
			}
			if s, isStr := item["text"].(string); !isStr || strings.TrimSpace(s) == "" {
				errs = append(errs, where+".text must be text")
			}
			if criterion, isStr := item["criterion"].(string); isStr && keys[criterion] {
				tagged[section][criterion] = true
			} else {
				errs = append(errs, fmt.Sprintf("%s.criterion %v is not a published criterion", where, item["criterion"]))
			}
			if kind, isStr := item["kind"].(string); !isStr || rules.Kinds[kind] == "" {
				errs = append(errs, fmt.Sprintf("%s.kind %v is not a published kind", where, item["kind"]))
			}
			source, isStr := item["source"].(string)
			if !isStr || !strings.HasPrefix(source, "https://") || strings.Contains(strings.TrimSpace(source), " ") {
				errs = append(errs, where+".source must be an https:// link")
			}
			if isoDate(item["checked_on"]) == "" {
				errs = append(errs, where+".checked_on must be an ISO date (YYYY-MM-DD)")
			}
		}
	}

	for k, v := range verdicts {
		if !keys[k] {
			continue
		}
		switch v {
		case "fair", "poor":
			if !tagged["cons"][k] {
				errs = append(errs, fmt.Sprintf("rating.verdicts.%s is %v but no con cites %s", k, v, k))
			}
		case "good":
			if !tagged["pros"][k] {
				errs = append(errs, fmt.Sprintf("rating.verdicts.%s is good but no pro cites %s", k, k))
			}
		case "unknown":
			if _, ok := notes[k]; !ok {
				errs = append(errs, fmt.Sprintf("rating.verdicts.%s is unknown but rating.notes.%s does not say what was searched", k, k))
			}
		}
	}

	if svc.Requirements.ContainerProhibited && verdicts["terms_fit"] != "poor" {
		errs = append(errs, "requirements.container_prohibited is true, so rating.verdicts.terms_fit must be poor")
	}
	sort.Strings(errs) // map order would make the log differ run to run
	return errs
}

func ratingItems(v any) []RatingItem {
	list, _ := v.([]any)
	out := make([]RatingItem, 0, len(list))
	for _, raw := range list {
		m, _ := raw.(map[string]any)
		str := func(k string) string { s, _ := m[k].(string); return s }
		out = append(out, RatingItem{
			Text: str("text"), Criterion: str("criterion"), Kind: str("kind"),
			Source: strings.TrimSpace(str("source")), CheckedOn: isoDate(m["checked_on"]),
		})
	}
	return out
}

// GradeService is the grade of one catalog service, computed from rules. A service
// with no rating block, or one that does not validate, is not rated. rules nil
// (the method could not be read) leaves everyone not rated.
func GradeService(svc Service, rules *RatingRules) *RatingResult {
	if rules == nil || svc.Custom || svc.Rating.node == nil {
		return notRated()
	}
	var block any
	if err := svc.Rating.node.Decode(&block); err != nil {
		log.Printf("catalog: %s: rating could not be read (shown as not rated): %v", svc.Slug, err)
		return notRated()
	}
	if errs := validateRating(block, svc, rules); len(errs) > 0 {
		for _, e := range errs {
			log.Printf("catalog: %s: %s (shown as not rated)", svc.Slug, e)
		}
		return notRated()
	}
	rating := block.(map[string]any)
	raw := rating["verdicts"].(map[string]any)

	verdicts := make([]RatingVerdict, 0, len(rules.Criteria))
	byKey := map[string]string{}
	score, best, unknowns := 0, 0, 0
	for _, p := range rules.Points {
		best = max(best, p)
	}
	for _, c := range rules.Criteria {
		v, _ := raw[c.Key].(string)
		if v == "" {
			v = "unknown"
		}
		byKey[c.Key] = v
		verdicts = append(verdicts, RatingVerdict{Key: c.Key, Title: c.Title, Verdict: v})
		score += rules.Points[v]
		if v == "unknown" {
			unknowns++
		}
	}
	maximum := best * len(rules.Criteria)
	percent := 0.0
	if maximum > 0 {
		percent = math.Round(1000*float64(score)/float64(maximum)) / 10
	}

	band := gradeOrder[len(gradeOrder)-1]
	for _, b := range rules.Bands {
		if percent >= b.MinPercent {
			band = b.Grade
			break
		}
	}
	thin := len(rules.Criteria) + 1
	for _, c := range rules.Caps {
		if c.Key == "thin_evidence" {
			thin = c.MinUnknown
			break
		}
	}

	grade := band
	caps := []RatingCap{}
	for _, c := range rules.Caps {
		holds := false
		switch c.Key {
		case "container_prohibited":
			holds = svc.Requirements.ContainerProhibited
		case "poor_payout_or_safety":
			holds = byKey["payout_record"] == "poor" || byKey["account_safety"] == "poor"
		case "thin_evidence":
			holds = unknowns >= c.MinUnknown
		}
		// A ceiling is reported only when it lowers the grade the score earned.
		if holds && gradeIndex(c.MaxGrade) > gradeIndex(band) {
			caps = append(caps, RatingCap{Key: c.Key, MaxGrade: c.MaxGrade, Reason: c.Reason})
			if gradeIndex(c.MaxGrade) > gradeIndex(grade) {
				grade = c.MaxGrade
			}
		}
	}

	notes := map[string]string{}
	if m, ok := rating["notes"].(map[string]any); ok {
		for k, v := range m {
			notes[k], _ = v.(string)
		}
	}
	cons := ratingItems(rating["cons"])
	top := cons
	if len(top) > 3 {
		top = top[:3]
	}
	summary := fmt.Sprintf("%s, %d of %d", grade, score, maximum)
	if unknowns >= thin {
		summary += fmt.Sprintf(", thin evidence: %d of %d criteria unchecked", unknowns, len(rules.Criteria))
	}
	return &RatingResult{
		Rated: true, Grade: grade, BandGrade: band, Score: score, Max: maximum, Percent: percent,
		Caps: caps, UnknownCount: unknowns, ThinEvidence: unknowns >= thin,
		ReviewedOn: isoDate(rating["reviewed_on"]), Verdicts: verdicts, Notes: notes,
		Cons: cons, Pros: ratingItems(rating["pros"]), TopCons: top, Summary: summary,
	}
}

// applyRatings grades every service from the rules file's bytes. A missing or
// unreadable rules file leaves every provider not rated, never graded on a guess.
func applyRatings(services []Service, raw []byte, readErr error) {
	var rules *RatingRules
	if readErr != nil {
		log.Printf("catalog: %s not read, no provider is graded: %v", RatingRulesFile, readErr)
	} else if parsed, err := ParseRatingRules(raw); err != nil {
		log.Printf("catalog: %s refused, no provider is graded: %v", RatingRulesFile, err)
	} else {
		rules = parsed
	}
	for i := range services {
		services[i].RatingResult = GradeService(services[i], rules)
	}
}
