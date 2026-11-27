// Package obligations warns people before a provider writes off their earnings.
//
// It ports the web app's reminders (CashPilot app/obligations.py, #814). Providers
// lose people money in a few ways that repeat: a balance that expires after months
// without activity, a claim window that closes, and money left at a provider after
// the minimum was reached. The catalog's `obligations:` rules describe the first
// two; the cash-out nudge is built in.
//
// The package is pure: no I/O and no clock. The caller reads the facts and the
// stored state, passes "today" in, and stores the clock starts that come back.
//
// Two rules hold everywhere. Unknown is never shown as safe: a clock whose start is
// older than the history is marked Censored ("by this date or earlier"), and a
// minimum that cannot be converted gives no cash-out item rather than a wrong one.
// And nothing here names a service: everything comes from the catalog.
package obligations

import (
	"errors"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

const (
	// CashoutFloorUSD is the least the cash-out nudge waits for, whatever the
	// catalog minimum. A provider with a 5-cent minimum would otherwise nag at the
	// first cent. When the catalog minimum is higher, the minimum wins.
	CashoutFloorUSD = 5.0
	// CashoutStillReadyDays is when a cash-out item that is still there turns
	// from "ready" to "still-ready".
	CashoutStillReadyDays = 30
	// RecheckDays is how long after Done a clock or cash-out item whose condition
	// still holds comes back once, as "did the payout arrive?".
	RecheckDays = 14

	// CashOut is the rule name of the built-in cash-out reminder.
	CashOut = "cash-out"
	// WholeService is the rule name that mutes every reminder of a service.
	WholeService = "*"

	KindRule    = "rule"
	KindCashOut = "cash-out"
)

// DefaultWarnDays are the days before a due date to warn when a rule names none.
var DefaultWarnDays = []int{30, 7, 1}

// SnoozeDays are the snoozes a user can pick: a day or a week.
var SnoozeDays = []int{1, 7}

// Reading is one balance per day, in the balance's own unit.
type Reading struct {
	Day     time.Time
	Balance float64
}

// Facts is what the app knows about one service in use.
type Facts struct {
	// Readings are one per day, oldest first, in the balance's unit.
	Readings []Reading
	// Balance is the latest balance; nil when it was never read.
	Balance    *float64
	Currency   string
	BalanceUSD *float64
	// Minimum is the provider minimum in the balance's unit; nil when unknown.
	Minimum *float64
	// Payouts are the days of every recorded cash-out.
	Payouts []time.Time
	// Collected is true when the service has a collector, so its balance is read.
	Collected bool
}

// State is what the user did with one (service, rule), and the clock start seen.
// Dates are "YYYY-MM-DD"; "" is none.
type State struct {
	AnchorAt       string
	AnchorCensored bool
	// DoneFor holds the occurrences marked done, comma separated: a monthly window
	// can still be open when the next opens, so two can be done at once.
	DoneFor      string
	DoneAt       string
	SnoozedUntil string
	Muted        bool
}

// Key names one State row.
type Key struct{ Slug, Rule string }

// Item is one reminder due now.
type Item struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Rule   string `json:"rule"`
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Title  string `json:"title"`
	Loss   string `json:"loss"`
	// Occurrence names this round of the rule (its start), "YYYY-MM-DD".
	Occurrence string `json:"occurrence"`
	// Stage is d<N> (inside the N-day warning), overdue, recheck, ready or still-ready.
	Stage string `json:"stage"`
	// Due is "YYYY-MM-DD", or "" for a cash-out item, which has no deadline.
	Due      string `json:"due"`
	DaysLeft *int   `json:"daysLeft"`
	// Censored means the clock started before the first reading kept, so the
	// due date is "this date or earlier".
	Censored  bool   `json:"censored"`
	How       string `json:"how"`
	URL       string `json:"url"`
	Source    string `json:"source"`
	Checked   string `json:"checked"`
	CanDone   bool   `json:"canDone"`
	CanSnooze bool   `json:"canSnooze"`
	// The balance shows only on a cash-out item.
	Balance    *float64 `json:"balance,omitempty"`
	Currency   string   `json:"currency,omitempty"`
	BalanceUSD *float64 `json:"balanceUsd,omitempty"`

	due    time.Time
	hasDue bool
}

// Muted is one reminder the user chose "Not for me" on.
type Muted struct {
	Slug  string `json:"slug"`
	Rule  string `json:"rule"`
	Name  string `json:"name"`
	Title string `json:"title"`
}

// Anchor is a clock start to store, or to clear (At "") once its condition ends.
type Anchor struct {
	Slug, Rule string
	At         string
	Censored   bool
}

// Evaluation is every reminder due today.
type Evaluation struct {
	Items   []Item
	Muted   []Muted
	Anchors []Anchor
}

// Day returns the calendar day of t as 00:00 UTC, the form every date here takes.
func Day(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// ParseDay reads the "YYYY-MM-DD" at the start of s.
func ParseDay(s string) (time.Time, bool) {
	if len(s) < 10 {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s[:10])
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func iso(t time.Time) string { return t.Format("2006-01-02") }

func addDays(t time.Time, n int) time.Time { return t.AddDate(0, 0, n) }

func daysBetween(from, to time.Time) int { return int(to.Sub(from).Hours() / 24) }

// StageFor is d<w> for the smallest warning still ahead, overdue below 0, and ""
// outside every warning.
func StageFor(daysLeft int, warnDays []int) string {
	if daysLeft < 0 {
		return "overdue"
	}
	best := -1
	for _, w := range warnDays {
		if daysLeft <= w && (best < 0 || w < best) {
			best = w
		}
	}
	if best < 0 {
		return ""
	}
	return "d" + strconv.Itoa(best)
}

// activity is a clock's state: running, stopped, or not known yet.
type activity int

const (
	unknown activity = iota
	inactive
	active
)

func lastPayout(f Facts) (time.Time, bool) {
	if len(f.Payouts) == 0 {
		return time.Time{}, false
	}
	last := f.Payouts[0]
	for _, p := range f.Payouts[1:] {
		if p.After(last) {
			last = p
		}
	}
	return last, true
}

// clockStart is where a clock starts, from the facts alone. censored means the
// start is the first reading still kept, so the real start may be earlier.
func clockStart(since string, f Facts) (time.Time, bool, activity) {
	readings := f.Readings
	if len(readings) == 0 || f.Balance == nil {
		return time.Time{}, false, unknown
	}
	balance := *f.Balance
	first := readings[0].Day
	paid, hasPaid := lastPayout(f)
	if since == "over_minimum" {
		if f.Minimum == nil {
			return time.Time{}, false, unknown
		}
		minimum := *f.Minimum
		if !(balance >= minimum && balance > 0) {
			return time.Time{}, false, inactive
		}
		for i, r := range readings {
			if hasPaid && r.Day.Before(paid) {
				continue
			}
			if r.Balance >= minimum {
				return r.Day, i == 0, active
			}
		}
		// The latest balance is over the minimum but no reading since the last
		// payout is: the payout was recorded after the newest reading.
		return readings[len(readings)-1].Day, false, active
	}
	if balance <= 0 {
		return time.Time{}, false, inactive
	}
	if since == "last_payout" {
		if hasPaid {
			return paid, false, active
		}
		return first, true, active
	}
	var increased time.Time
	hasIncrease := false
	for i := 1; i < len(readings); i++ {
		if readings[i].Balance > readings[i-1].Balance {
			increased, hasIncrease = readings[i].Day, true
		}
	}
	if since == "last_increase" {
		// Earning only: a partial redemption (a payout that leaves a balance) does
		// not reset a clock the terms count from the last earning.
		if hasIncrease {
			return increased, false, active
		}
		return first, true, active
	}
	// last_activity: the later of the last balance increase and the last payout.
	switch {
	case hasIncrease && hasPaid:
		if paid.After(increased) {
			return paid, false, active
		}
		return increased, false, active
	case hasIncrease:
		return increased, false, active
	case hasPaid:
		return paid, false, active
	}
	return first, true, active
}

// mergeAnchor combines the start the facts show with the start stored earlier.
// Old earnings are purged, and a clock can run for 730 days, so the first start
// seen is stored and used once the history behind it is gone: a censored live
// start never moves a stored one later. A real event moves the clock forward.
func mergeAnchor(live time.Time, censored bool, stored *State) (time.Time, bool) {
	if stored == nil {
		return live, censored
	}
	storedAt, ok := ParseDay(stored.AnchorAt)
	if !ok {
		return live, censored
	}
	if censored {
		if !storedAt.After(live) {
			return storedAt, stored.AnchorCensored
		}
		return live, censored
	}
	if !stored.AnchorCensored && storedAt.After(live) {
		return storedAt, false
	}
	return live, false
}

// Occurrence is one round of a dated or monthly rule: when it opened and its due day.
type Occurrence struct{ Opened, Due time.Time }

// OpenOccurrences are the rounds of a date or monthly rule not yet past, oldest first.
func OpenOccurrences(when catalog.ObligationWhen, today time.Time) []Occurrence {
	if when.Date != "" {
		due, ok := ParseDay(when.Date)
		if !ok || due.Before(today) {
			return nil
		}
		return []Occurrence{{due, due}}
	}
	if when.Every != "month" {
		return nil
	}
	var out []Occurrence
	year, month := today.Year(), today.Month()
	// Walk back far enough to catch every window still open.
	for i := 0; i < when.DueDays/28+2; i++ {
		opened := time.Date(year, month, when.On, 0, 0, 0, 0, time.UTC)
		due := addDays(opened, when.DueDays)
		if !opened.After(today) && !today.After(due) {
			out = append(out, Occurrence{opened, due})
		}
		month--
		if month == 0 {
			year, month = year-1, 12
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Opened.Before(out[j].Opened) })
	return out
}

// validWhen holds a rule to exactly one well-formed form. The web catalog's loader
// drops a bad entry; Desktop decodes the vendored catalog without that check, so
// the engine skips what the loader would have dropped.
func validWhen(w catalog.ObligationWhen) bool {
	forms := 0
	ok := true
	if w.Date != "" {
		forms++
		_, ok = ParseDay(w.Date)
	}
	if w.Every != "" {
		forms++
		ok = ok && w.Every == "month" && w.On >= 1 && w.On <= 28 && w.DueDays > 0
	}
	if w.Since != "" {
		forms++
		switch w.Since {
		case "over_minimum", "last_activity", "last_increase", "last_payout":
		default:
			ok = false
		}
		ok = ok && w.Days > 0
	}
	return forms == 1 && ok
}

// validRule is the rest of the web loader's check that the engine needs: a link
// must be https on the service's own domain (fake claim sites exist for exactly
// the services that have claim windows), and warn_days, when set, must be a
// descending list of whole days, 0 or more.
func validRule(svc catalog.Service, rule catalog.Obligation) bool {
	if rule.ID == "" || !validWhen(rule.When) {
		return false
	}
	if rule.URL != "" {
		domain := registrableDomain(rule.URL)
		if domain == "" {
			return false
		}
		own := false
		for _, u := range []string{svc.Website, svc.Cashout.DashboardURL, svc.Referral.SignupURL} {
			if d := registrableDomain(u); d != "" && d == domain {
				own = true
			}
		}
		if !own {
			return false
		}
	}
	for i, w := range rule.WarnDays {
		if w < 0 || (i > 0 && w >= rule.WarnDays[i-1]) {
			return false
		}
	}
	return true
}

// registrableDomain is the last two labels of an https URL's host, "" when the
// URL is not https or has no such host. Two labels, as on the web: a co.uk site
// reads as co.uk and matches nothing, so it fails closed.
func registrableDomain(raw string) string {
	if !strings.HasPrefix(raw, "https://") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	var labels []string
	for _, l := range strings.Split(strings.TrimRight(strings.ToLower(u.Hostname()), "."), ".") {
		if l != "" {
			labels = append(labels, l)
		}
	}
	if len(labels) < 2 {
		return ""
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

func isMuted(state map[Key]State, slug, rule string) bool {
	return state[Key{slug, WholeService}].Muted || state[Key{slug, rule}].Muted
}

// DoneOccurrences are the occurrences marked done in a row.
func DoneOccurrences(row State) map[string]bool {
	out := map[string]bool{}
	for _, o := range strings.Split(row.DoneFor, ",") {
		if o != "" {
			out[o] = true
		}
	}
	return out
}

// settle applies Done and Snooze to one candidate; false when it is hidden.
func settle(item *Item, row State, today time.Time, warnDays []int, recheck bool) bool {
	if item.CanDone && DoneOccurrences(row)[item.Occurrence] {
		doneAt, ok := ParseDay(row.DoneAt)
		if !recheck || !ok || today.Before(addDays(doneAt, RecheckDays)) {
			return false
		}
		item.Stage = "recheck"
	}
	// A snooze can delay a reminder but never cause the loss: it cannot run past
	// the last warning of a dated item.
	var limit time.Time
	hasLimit := item.hasDue && len(warnDays) > 0
	if hasLimit {
		limit = addDays(item.due, -slices.Min(warnDays))
	}
	item.CanSnooze = !hasLimit || limit.After(today)
	if until, ok := ParseDay(row.SnoozedUntil); ok {
		if hasLimit && limit.Before(until) {
			until = limit
		}
		if today.Before(until) {
			return false
		}
	}
	return true
}

func defaultURL(svc catalog.Service) string {
	if svc.Cashout.DashboardURL != "" {
		return svc.Cashout.DashboardURL
	}
	return svc.Website
}

func serviceName(svc catalog.Service) string {
	if svc.Name != "" {
		return svc.Name
	}
	return svc.Slug
}

func ruleItems(svc catalog.Service, f Facts, state map[Key]State, today time.Time, out *Evaluation) []Item {
	slug, name := svc.Slug, serviceName(svc)
	var items []Item
	for _, rule := range svc.Obligations {
		if !validRule(svc, rule) {
			continue
		}
		warn := rule.WarnDays
		if len(warn) == 0 {
			warn = DefaultWarnDays
		}
		url := rule.URL
		if url == "" {
			url = defaultURL(svc)
		}
		type candidate struct {
			occurrence, due time.Time
			censored        bool
		}
		var candidates []candidate
		stored, hasStored := state[Key{slug, rule.ID}]
		isClock := rule.When.Since != ""
		if isClock {
			live, censored, act := clockStart(rule.When.Since, f)
			if act == inactive {
				if hasStored && stored.AnchorAt != "" {
					out.Anchors = append(out.Anchors, Anchor{Slug: slug, Rule: rule.ID})
				}
				continue
			}
			if act == unknown {
				continue
			}
			var storedPtr *State
			if hasStored {
				storedPtr = &stored
			}
			start, censored := mergeAnchor(live, censored, storedPtr)
			storedAt, _ := ParseDay(stored.AnchorAt)
			if !hasStored || stored.AnchorAt == "" || !storedAt.Equal(start) || stored.AnchorCensored != censored {
				out.Anchors = append(out.Anchors, Anchor{Slug: slug, Rule: rule.ID, At: iso(start), Censored: censored})
			}
			candidates = append(candidates, candidate{start, addDays(start, rule.When.Days), censored})
		} else {
			for _, o := range OpenOccurrences(rule.When, today) {
				candidates = append(candidates, candidate{o.Opened, o.Due, false})
			}
		}
		if isMuted(state, slug, rule.ID) {
			continue
		}
		for _, c := range candidates {
			daysLeft := daysBetween(today, c.due)
			stage := StageFor(daysLeft, warn)
			if stage == "" {
				continue
			}
			left := daysLeft
			item := Item{
				Slug: slug, Name: name, Rule: rule.ID, Kind: KindRule,
				Action: rule.Action, Title: rule.Title, Loss: rule.Loss,
				How: rule.How, URL: url, Source: rule.Source, Checked: rule.Checked,
				Occurrence: iso(c.occurrence), Stage: stage,
				Due: iso(c.due), DaysLeft: &left, Censored: c.censored,
				CanDone: true, CanSnooze: true,
				due: c.due, hasDue: true,
			}
			if settle(&item, state[Key{slug, rule.ID}], today, warn, isClock) {
				items = append(items, item)
			}
		}
	}
	return items
}

func cashoutItem(svc catalog.Service, f Facts, state map[Key]State, today time.Time, out *Evaluation) *Item {
	slug := svc.Slug
	if !f.Collected || svc.Cashout.Method == "auto" {
		return nil
	}
	if f.Balance == nil || f.Minimum == nil || f.BalanceUSD == nil {
		// Unknown is never shown as "you can withdraw".
		return nil
	}
	stored := state[Key{slug, CashOut}]
	balance := *f.Balance
	ready := balance >= *f.Minimum && *f.BalanceUSD >= CashoutFloorUSD && balance > 0
	if !ready {
		if stored.AnchorAt != "" {
			out.Anchors = append(out.Anchors, Anchor{Slug: slug, Rule: CashOut})
		}
		return nil
	}
	start, ok := ParseDay(stored.AnchorAt)
	if !ok || start.After(today) {
		start = today
		out.Anchors = append(out.Anchors, Anchor{Slug: slug, Rule: CashOut, At: iso(start)})
	}
	if isMuted(state, slug, CashOut) {
		return nil
	}
	name := serviceName(svc)
	stage := "ready"
	if daysBetween(start, today) >= CashoutStillReadyDays {
		stage = "still-ready"
	}
	bal, usd := balance, *f.BalanceUSD
	item := Item{
		Slug: slug, Name: name, Rule: CashOut, Kind: KindCashOut, Action: "withdraw",
		Title:      i18n.T("go.obligations.cashOut.title", "name", name),
		Loss:       i18n.T("go.obligations.cashOut.loss"),
		How:        strings.TrimSpace(svc.Cashout.Notes),
		URL:        defaultURL(svc),
		Occurrence: iso(start), Stage: stage,
		CanDone: true, CanSnooze: true,
		Balance: &bal, Currency: f.Currency, BalanceUSD: &usd,
	}
	if !settle(&item, stored, today, nil, true) {
		return nil
	}
	return &item
}

func sortKey(it Item) (int, float64, int) {
	group := 3
	switch {
	case it.Stage == "overdue":
		group = 0
	case it.DaysLeft != nil && *it.DaysLeft <= 7:
		group = 1
	case it.Kind == KindCashOut:
		group = 2
	}
	money := 0.0
	if it.Kind == KindCashOut && it.BalanceUSD != nil {
		money = -*it.BalanceUSD
	}
	left := 1_000_000
	if it.DaysLeft != nil {
		left = *it.DaysLeft
	}
	return group, money, left
}

func less(a, b Item) bool {
	ga, ma, la := sortKey(a)
	gb, mb, lb := sortKey(b)
	if ga != gb {
		return ga < gb
	}
	if ma != mb {
		return ma < mb
	}
	if la != lb {
		return la < lb
	}
	if na, nb := strings.ToLower(a.Name), strings.ToLower(b.Name); na != nb {
		return na < nb
	}
	return a.Rule < b.Rule
}

// InUse reports whether a catalog service can get reminders at all: custom and
// dead services get nothing.
func InUse(svc catalog.Service) bool {
	return svc.Slug != "" && svc.Status != "dead" && !svc.Custom && !catalog.IsCustomSlug(svc.Slug)
}

// Evaluate returns every reminder due today, for the services in facts (the ones
// in use). services is the catalog and state the stored rows.
func Evaluate(services []catalog.Service, facts map[string]Facts, state map[Key]State, today time.Time) Evaluation {
	today = Day(today)
	out := Evaluation{Items: []Item{}, Muted: []Muted{}}
	bySlug := map[string]catalog.Service{}
	for _, svc := range services {
		f, ok := facts[svc.Slug]
		if !ok || !InUse(svc) {
			continue
		}
		bySlug[svc.Slug] = svc
		rules := ruleItems(svc, f, state, today, &out)
		out.Items = append(out.Items, rules...)
		cash := cashoutItem(svc, f, state, today, &out)
		// A visible withdraw rule says more than the generic nudge.
		if cash != nil && !slices.ContainsFunc(rules, func(r Item) bool { return r.Action == "withdraw" }) {
			out.Items = append(out.Items, *cash)
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool { return less(out.Items[i], out.Items[j]) })

	keys := make([]Key, 0, len(state))
	for k := range state {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Slug != keys[j].Slug {
			return keys[i].Slug < keys[j].Slug
		}
		return keys[i].Rule < keys[j].Rule
	})
	for _, k := range keys {
		svc, ok := bySlug[k.Slug]
		if !state[k].Muted || !ok {
			continue
		}
		title := ""
		switch k.Rule {
		case WholeService:
			title = i18n.T("go.obligations.muted.all")
		case CashOut:
			title = i18n.T("go.obligations.muted.cashOut")
		default:
			for _, r := range svc.Obligations {
				if r.ID == k.Rule {
					title = r.Title
				}
			}
		}
		if title != "" {
			out.Muted = append(out.Muted, Muted{Slug: k.Slug, Rule: k.Rule, Name: serviceName(svc), Title: title})
		}
	}
	return out
}

// Change is what one user action writes to a (service, rule) row. Nil keeps the
// stored value.
type Change struct {
	DoneFor, DoneAt, SnoozedUntil *string
	Muted                         *bool
}

// The answers a refused action gets, in plain words.
var (
	ErrUnknownReminder = errors.New("no such reminder")
	ErrNotDue          = errors.New("this reminder is not due now")
	ErrClearsItself    = errors.New("this one clears by itself once it is fixed")
	ErrSnoozeDays      = errors.New("a snooze is a day or a week")
	ErrTooClose        = errors.New("too close to its deadline to snooze")
	ErrUnknownAction   = errors.New("the action must be done, snooze, mute or unmute")
)

// KnownRule reports whether rule names a reminder of svc that can be acted on.
func KnownRule(svc catalog.Service, rule string) bool {
	if !InUse(svc) {
		return false
	}
	if rule == WholeService || rule == CashOut {
		return true
	}
	for _, r := range svc.Obligations {
		if r.ID == rule {
			return true
		}
	}
	return false
}

// doneFor is the DoneFor value once occurrence is marked done. A date or monthly
// rule keeps every occurrence already done that is still open, so closing
// December's window does not reopen November's on the day both are open. A clock
// and a cash-out have one occurrence at a time: a new one replaces the old.
func doneFor(svc catalog.Service, rule string, row State, occurrence string, today time.Time) string {
	var spec *catalog.Obligation
	for i := range svc.Obligations {
		if svc.Obligations[i].ID == rule {
			spec = &svc.Obligations[i]
		}
	}
	if spec == nil || spec.When.Since != "" {
		return occurrence
	}
	open := map[string]bool{}
	for _, o := range OpenOccurrences(spec.When, Day(today)) {
		open[iso(o.Opened)] = true
	}
	kept := map[string]bool{occurrence: true}
	for o := range DoneOccurrences(row) {
		if open[o] {
			kept[o] = true
		}
	}
	list := make([]string, 0, len(kept))
	for o := range kept {
		list = append(list, o)
	}
	sort.Strings(list)
	return strings.Join(list, ",")
}

// Decide turns one user action into the Change to store, or refuses it. ev is the
// evaluation of today; occurrence "" matches any occurrence of the rule.
func Decide(svc catalog.Service, ev Evaluation, row State, rule, action, occurrence string, days int, today time.Time) (Change, error) {
	if !KnownRule(svc, rule) {
		return Change{}, ErrUnknownReminder
	}
	switch action {
	case "mute", "unmute":
		muted := action == "mute"
		return Change{Muted: &muted}, nil
	case "done", "snooze":
	default:
		return Change{}, ErrUnknownAction
	}
	var item *Item
	for i := range ev.Items {
		it := &ev.Items[i]
		if it.Slug == svc.Slug && it.Rule == rule && (occurrence == "" || it.Occurrence == occurrence) {
			item = it
			break
		}
	}
	if item == nil {
		return Change{}, ErrNotDue
	}
	today = Day(today)
	if action == "done" {
		if !item.CanDone {
			return Change{}, ErrClearsItself
		}
		done, at := doneFor(svc, rule, row, item.Occurrence, today), iso(today)
		return Change{DoneFor: &done, DoneAt: &at}, nil
	}
	if !slices.Contains(SnoozeDays, days) {
		return Change{}, ErrSnoozeDays
	}
	if !item.CanSnooze {
		return Change{}, ErrTooClose
	}
	until := iso(addDays(today, days))
	return Change{SnoozedUntil: &until}, nil
}
