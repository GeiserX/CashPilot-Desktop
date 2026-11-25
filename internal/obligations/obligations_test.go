package obligations

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// These mirror the web app's tests/test_obligations.py on the same fixed "today",
// so the two engines are held to the same answers.

var today = date(2026, 11, 24)

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func ago(n int) time.Time { return addDays(today, -n) }

func ptr[T any](v T) *T { return &v }

func svc(slug string, rules ...catalog.Obligation) catalog.Service {
	return catalog.Service{
		Slug: slug, Name: string(slug[0]-32) + slug[1:], Status: "active",
		Website:     "https://" + slug + ".example",
		Cashout:     catalog.Cashout{Method: "redirect", DashboardURL: "https://app." + slug + ".example/pay", MinAmount: 20},
		Obligations: rules,
	}
}

func rule(id, action string, when catalog.ObligationWhen, warn ...int) catalog.Obligation {
	return catalog.Obligation{
		ID: id, Action: action, Title: "Do " + id, Loss: "The balance expires.",
		When: when, WarnDays: warn, Source: "https://acme.example/terms", Checked: "2026-11-24",
	}
}

func readings(pairs ...any) []Reading {
	var out []Reading
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, Reading{Day: pairs[i].(time.Time), Balance: pairs[i+1].(float64)})
	}
	return out
}

// facts uses the last reading as the balance, as the web test helper does.
func facts(rs []Reading, payouts ...time.Time) Facts {
	f := Facts{Readings: rs, Payouts: payouts}
	if len(rs) > 0 {
		f.Balance = ptr(rs[len(rs)-1].Balance)
	}
	return f
}

func run(t *testing.T, services []catalog.Service, f map[string]Facts, state map[Key]State, on time.Time) Evaluation {
	t.Helper()
	if state == nil {
		state = map[Key]State{}
	}
	return Evaluate(services, f, state, on)
}

func one(t *testing.T, ev Evaluation) Item {
	t.Helper()
	if len(ev.Items) != 1 {
		t.Fatalf("want one item, got %d: %+v", len(ev.Items), ev.Items)
	}
	return ev.Items[0]
}

func left(it Item) int {
	if it.DaysLeft == nil {
		return -999999
	}
	return *it.DaysLeft
}

// --- the fixed date form -----------------------------------------------------

var claim = rule("claim", "claim", catalog.ObligationWhen{Date: "2027-01-22"}, 60, 30, 7, 1)

func TestDateUpcomingInsideItsFirstWarning(t *testing.T) {
	it := one(t, run(t, []catalog.Service{svc("acme", claim)}, map[string]Facts{"acme": {}}, nil, today))
	if it.Due != "2027-01-22" || left(it) != 59 || it.Stage != "d60" {
		t.Fatalf("got due %s, %d days, stage %s", it.Due, left(it), it.Stage)
	}
	if it.URL != "https://app.acme.example/pay" {
		t.Errorf("a rule with no url goes to the cash-out dashboard, got %q", it.URL)
	}
}

func TestDateHiddenBeforeItsFirstWarning(t *testing.T) {
	ev := run(t, []catalog.Service{svc("acme", claim)}, map[string]Facts{"acme": {}}, nil, date(2026, 11, 1))
	if len(ev.Items) != 0 {
		t.Fatalf("shown before the 60-day warning: %+v", ev.Items)
	}
}

func TestDateDueTodayIsStillShown(t *testing.T) {
	it := one(t, run(t, []catalog.Service{svc("acme", claim)}, map[string]Facts{"acme": {}}, nil, date(2027, 1, 22)))
	if left(it) != 0 || it.Stage != "d1" {
		t.Fatalf("got %d days, stage %s", left(it), it.Stage)
	}
}

func TestDatePastIsDroppedTheDayAfter(t *testing.T) {
	ev := run(t, []catalog.Service{svc("acme", claim)}, map[string]Facts{"acme": {}}, nil, date(2027, 1, 23))
	if len(ev.Items) != 0 {
		t.Fatalf("a past claim date still shows: %+v", ev.Items)
	}
}

// --- the monthly window form ---------------------------------------------------

var pool = rule("pool", "claim", catalog.ObligationWhen{Every: "month", On: 1, DueDays: 30}, 30, 7)

type occ struct {
	Occurrence, Due, Stage string
	Left                   int
}

func occs(ev Evaluation) []occ {
	out := []occ{}
	for _, it := range ev.Items {
		out = append(out, occ{it.Occurrence, it.Due, it.Stage, left(it)})
	}
	return out
}

func TestMonthlyWindowOpensOnItsDay(t *testing.T) {
	// 1 Dec is also the day November's window closes, so both show.
	got := occs(run(t, []catalog.Service{svc("acme", pool)}, map[string]Facts{"acme": {}}, nil, date(2026, 12, 1)))
	want := []occ{{"2026-11-01", "2026-12-01", "d7", 0}, {"2026-12-01", "2026-12-31", "d30", 30}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMonthlyWindowStaysOnItsFirstStageBetweenWarnings(t *testing.T) {
	it := one(t, run(t, []catalog.Service{svc("acme", pool)}, map[string]Facts{"acme": {}}, nil, date(2026, 12, 10)))
	if it.Occurrence != "2026-12-01" || it.Stage != "d30" {
		t.Fatalf("got %s %s", it.Occurrence, it.Stage)
	}
}

func TestMonthlyWindowSevenDaysBeforeItCloses(t *testing.T) {
	it := one(t, run(t, []catalog.Service{svc("acme", pool)}, map[string]Facts{"acme": {}}, nil, date(2026, 12, 24)))
	if it.Stage != "d7" {
		t.Fatalf("got stage %s", it.Stage)
	}
}

func TestMonthlyWindowFebruaryStaysOpenIntoMarch(t *testing.T) {
	// 1 Feb + 30 days is 3 Mar: on 1 Mar February's window (2 days left) and
	// March's (just opened) are both open.
	got := occs(run(t, []catalog.Service{svc("acme", pool)}, map[string]Facts{"acme": {}}, nil, date(2027, 3, 1)))
	if len(got) != 2 || got[0].Occurrence != "2027-02-01" || got[0].Left != 2 || got[1].Occurrence != "2027-03-01" || got[1].Left != 30 {
		t.Fatalf("got %+v", got)
	}
}

// --- the clock form, each of its four starts ---------------------------------

var keep = rule("keep", "keep-active", catalog.ObligationWhen{Since: "last_activity", Days: 180}, 90, 30, 7)

func TestClockLastActivityStartsAtTheLastIncrease(t *testing.T) {
	f := facts(readings(ago(200), 1.0, ago(120), 2.0, ago(5), 2.0))
	it := one(t, run(t, []catalog.Service{svc("acme", keep)}, map[string]Facts{"acme": f}, nil, today))
	if it.Occurrence != ago(120).Format("2006-01-02") || left(it) != 60 || it.Stage != "d90" || it.Censored {
		t.Fatalf("got %+v", it)
	}
}

func TestClockLastActivityMovesOnAPayout(t *testing.T) {
	f := facts(readings(ago(200), 1.0, ago(170), 2.0, ago(5), 2.0), ago(150))
	it := one(t, run(t, []catalog.Service{svc("acme", keep)}, map[string]Facts{"acme": f}, nil, today))
	if it.Occurrence != iso(ago(150)) {
		t.Fatalf("a payout after the last increase must move the clock, got %s", it.Occurrence)
	}
}

func TestClockStopsAtZeroBalance(t *testing.T) {
	f := facts(readings(ago(300), 1.0, ago(5), 0.0))
	if ev := run(t, []catalog.Service{svc("acme", keep)}, map[string]Facts{"acme": f}, nil, today); len(ev.Items) != 0 {
		t.Fatalf("a zero balance has nothing to lose: %+v", ev.Items)
	}
}

func TestClockWithNoReadingIsUnknownNotDue(t *testing.T) {
	if ev := run(t, []catalog.Service{svc("acme", keep)}, map[string]Facts{"acme": {}}, nil, today); len(ev.Items) != 0 || len(ev.Anchors) != 0 {
		t.Fatalf("a clock with no reading must give nothing: %+v", ev)
	}
}

var writeOff = rule("write-off", "withdraw", catalog.ObligationWhen{Since: "over_minimum", Days: 730}, 180, 60, 30)

func TestClockOverMinimumStartsAtTheFirstReadingOverIt(t *testing.T) {
	f := facts(readings(ago(700), 10.0, ago(600), 21.0, ago(1), 40.0))
	f.Minimum = ptr(20.0)
	it := one(t, run(t, []catalog.Service{svc("acme", writeOff)}, map[string]Facts{"acme": f}, nil, today))
	if it.Occurrence != iso(ago(600)) || left(it) != 130 || it.Stage != "d180" {
		t.Fatalf("got %+v", it)
	}
}

func TestClockOverMinimumStopsUnderTheMinimum(t *testing.T) {
	f := facts(readings(ago(100), 25.0, ago(1), 19.0))
	f.Minimum = ptr(20.0)
	r := rule("write-off", "withdraw", catalog.ObligationWhen{Since: "over_minimum", Days: 30})
	if ev := run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, nil, today); len(ev.Items) != 0 {
		t.Fatalf("got %+v", ev.Items)
	}
}

func TestClockOverMinimumWithAnUnknownMinimumGivesNothing(t *testing.T) {
	f := facts(readings(ago(100), 25.0, ago(1), 25.0))
	r := rule("write-off", "withdraw", catalog.ObligationWhen{Since: "over_minimum", Days: 30})
	if ev := run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, nil, today); len(ev.Items) != 0 {
		t.Fatalf("got %+v", ev.Items)
	}
}

func TestClockLastPayoutWithNoPayoutIsCensored(t *testing.T) {
	r := rule("forfeit", "withdraw", catalog.ObligationWhen{Since: "last_payout", Days: 365}, 90)
	f := facts(readings(ago(300), 1.0, ago(1), 3.0))
	it := one(t, run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, nil, today))
	if it.Occurrence != iso(ago(300)) || !it.Censored {
		t.Fatalf("got %+v", it)
	}
}

func TestClockLastPayoutStartsAtThePayout(t *testing.T) {
	r := rule("forfeit", "withdraw", catalog.ObligationWhen{Since: "last_payout", Days: 365}, 90)
	f := facts(readings(ago(320), 1.0, ago(1), 3.0), ago(310), ago(290))
	it := one(t, run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, nil, today))
	if it.Occurrence != iso(ago(290)) || it.Censored || left(it) != 75 {
		t.Fatalf("got %+v", it)
	}
}

func TestClockLastIncreaseIgnoresAPartialRedemption(t *testing.T) {
	// Salad counts earning only: spending part of the balance does not reset its
	// 12 months. Earned until 10 Jan, redeemed part on 1 Jun.
	r := rule("earn", "keep-active", catalog.ObligationWhen{Since: "last_increase", Days: 365}, 90, 30, 7)
	f := facts(readings(date(2025, 12, 1), 10.0, date(2026, 1, 10), 13.0, date(2026, 6, 1), 3.0), date(2026, 6, 1))
	it := one(t, run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, nil, date(2027, 1, 10)))
	if it.Occurrence != "2026-01-10" || it.Due != "2027-01-10" || it.Stage != "d7" || it.Censored {
		t.Fatalf("got %+v", it)
	}
}

func TestClockLastIncreaseWithNoIncreaseIsCensored(t *testing.T) {
	r := rule("earn", "keep-active", catalog.ObligationWhen{Since: "last_increase", Days: 365}, 90)
	f := facts(readings(ago(300), 5.0, ago(100), 3.0), ago(100))
	it := one(t, run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, nil, today))
	if it.Occurrence != iso(ago(300)) || !it.Censored {
		t.Fatalf("got %+v", it)
	}
}

func TestClockOverdueStaysAndCannotBeSnoozed(t *testing.T) {
	f := facts(readings(ago(200), 1.0, ago(190), 2.0, ago(1), 2.0))
	it := one(t, run(t, []catalog.Service{svc("acme", keep)}, map[string]Facts{"acme": f}, nil, today))
	if it.Stage != "overdue" || left(it) != -10 || it.CanSnooze {
		t.Fatalf("got %+v", it)
	}
}

func TestRealCatalogSaladWarnsOnItsEarningClock(t *testing.T) {
	cat, err := catalog.LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatal(err)
	}
	salad, ok := cat.Get("salad")
	if !ok {
		t.Fatal("salad is not in the catalog")
	}
	f := facts(readings(date(2025, 12, 1), 10.0, date(2026, 1, 10), 13.0, date(2026, 6, 1), 3.0), date(2026, 6, 1))
	ev := run(t, []catalog.Service{salad}, map[string]Facts{"salad": f}, nil, date(2026, 12, 1))
	if len(ev.Items) != 1 || ev.Items[0].Rule != "earn-within-12-months" || ev.Items[0].Due != "2027-01-10" {
		t.Fatalf("got %+v", ev.Items)
	}
}

func TestStageIsOnlyTheCurrentWarning(t *testing.T) {
	for daysLeft, want := range map[int]string{200: "", 90: "d90", 31: "d90", 30: "d30", 8: "d30", 7: "d7", 0: "d7", -1: "overdue"} {
		if got := StageFor(daysLeft, []int{90, 30, 7}); got != want {
			t.Errorf("StageFor(%d) = %q, want %q", daysLeft, got, want)
		}
	}
}

func TestAMalformedRuleIsSkipped(t *testing.T) {
	bad := []catalog.Obligation{
		rule("no-form", "claim", catalog.ObligationWhen{}),
		rule("two-forms", "claim", catalog.ObligationWhen{Date: "2026-12-01", Since: "last_payout", Days: 30}),
		rule("day-29", "claim", catalog.ObligationWhen{Every: "month", On: 29, DueDays: 30}),
		rule("bad-clock", "claim", catalog.ObligationWhen{Since: "last_login", Days: 30}),
	}
	f := facts(readings(ago(300), 1.0, ago(1), 3.0))
	if ev := run(t, []catalog.Service{svc("acme", bad...)}, map[string]Facts{"acme": f}, nil, today); len(ev.Items) != 0 {
		t.Fatalf("a rule the web loader would drop was evaluated: %+v", ev.Items)
	}
}

// --- stored clock starts ----------------------------------------------------------

func TestAFirstSeenStartIsReturnedForStoring(t *testing.T) {
	f := facts(readings(ago(300), 10.0, ago(200), 21.0, ago(1), 30.0))
	f.Minimum = ptr(20.0)
	ev := run(t, []catalog.Service{svc("acme", writeOff)}, map[string]Facts{"acme": f}, nil, today)
	want := []Anchor{{Slug: "acme", Rule: "write-off", At: iso(ago(200))}}
	if !reflect.DeepEqual(ev.Anchors, want) {
		t.Fatalf("got %+v", ev.Anchors)
	}
}

func TestTheStoredStartSurvivesAPurgedHistory(t *testing.T) {
	f := facts(readings(ago(399), 25.0, ago(1), 30.0))
	f.Minimum = ptr(20.0)
	state := map[Key]State{{"acme", "write-off"}: {AnchorAt: iso(ago(600))}}
	ev := run(t, []catalog.Service{svc("acme", writeOff)}, map[string]Facts{"acme": f}, state, today)
	it := one(t, ev)
	if it.Occurrence != iso(ago(600)) || left(it) != 130 || it.Censored || len(ev.Anchors) != 0 {
		t.Fatalf("got %+v, anchors %+v", it, ev.Anchors)
	}
}

func TestARealEventMovesAStoredActivityClockForward(t *testing.T) {
	r := rule("keep", "keep-active", catalog.ObligationWhen{Since: "last_activity", Days: 180}, 180)
	f := facts(readings(ago(100), 1.0, ago(50), 2.0, ago(1), 2.0))
	state := map[Key]State{{"acme", "keep"}: {AnchorAt: iso(ago(100)), AnchorCensored: true}}
	ev := run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, state, today)
	if one(t, ev).Occurrence != iso(ago(50)) || !reflect.DeepEqual(ev.Anchors, []Anchor{{Slug: "acme", Rule: "keep", At: iso(ago(50))}}) {
		t.Fatalf("got %+v", ev)
	}
}

func TestAPayoutThatEmptiesTheBalanceClearsTheStoredStart(t *testing.T) {
	f := facts(readings(ago(300), 25.0, ago(1), 0.0), ago(1))
	f.Minimum = ptr(20.0)
	state := map[Key]State{{"acme", "write-off"}: {AnchorAt: iso(ago(300))}}
	ev := run(t, []catalog.Service{svc("acme", writeOff)}, map[string]Facts{"acme": f}, state, today)
	if len(ev.Items) != 0 || !reflect.DeepEqual(ev.Anchors, []Anchor{{Slug: "acme", Rule: "write-off"}}) {
		t.Fatalf("got %+v", ev)
	}
}

// --- Done, Snooze and Not for me ----------------------------------------------

func TestDoneHidesOnlyItsOccurrence(t *testing.T) {
	state := map[Key]State{{"acme", "pool"}: {DoneFor: "2027-02-01", DoneAt: "2027-02-02"}}
	got := occs(run(t, []catalog.Service{svc("acme", pool)}, map[string]Facts{"acme": {}}, state, date(2027, 3, 1)))
	if len(got) != 1 || got[0].Occurrence != "2027-03-01" {
		t.Fatalf("got %+v", got)
	}
}

func TestDoneOnTwoOpenWindowsHidesBoth(t *testing.T) {
	state := map[Key]State{{"acme", "pool"}: {DoneFor: "2026-11-01,2026-12-01", DoneAt: "2026-12-01"}}
	if ev := run(t, []catalog.Service{svc("acme", pool)}, map[string]Facts{"acme": {}}, state, date(2026, 12, 1)); len(ev.Items) != 0 {
		t.Fatalf("got %+v", ev.Items)
	}
}

func TestDoneOnADatedRuleDoesNotComeBack(t *testing.T) {
	state := map[Key]State{{"acme", "claim"}: {DoneFor: "2027-01-22", DoneAt: "2026-11-24"}}
	if ev := run(t, []catalog.Service{svc("acme", claim)}, map[string]Facts{"acme": {}}, state, date(2027, 1, 21)); len(ev.Items) != 0 {
		t.Fatalf("got %+v", ev.Items)
	}
}

// Done resets the clock: once a payout lands, last_payout starts again from it,
// so the old occurrence's Done no longer matters and the next deadline is a year on.
func TestDoneResetsTheClockOnceThePayoutLands(t *testing.T) {
	r := rule("forfeit", "withdraw", catalog.ObligationWhen{Since: "last_payout", Days: 365}, 90)
	occurrence := iso(ago(300))
	state := map[Key]State{{"acme", "forfeit"}: {AnchorAt: occurrence, AnchorCensored: true, DoneFor: occurrence, DoneAt: iso(ago(2))}}
	f := facts(readings(ago(300), 1.0, ago(1), 3.0), ago(1))
	ev := run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, state, today)
	if len(ev.Items) != 0 {
		t.Fatalf("the clock restarted at the payout, nothing is due: %+v", ev.Items)
	}
	if !reflect.DeepEqual(ev.Anchors, []Anchor{{Slug: "acme", Rule: "forfeit", At: iso(ago(1))}}) {
		t.Fatalf("the new start must be stored, got %+v", ev.Anchors)
	}
	// 275 days on, the new clock warns: the old Done does not hide it.
	later := addDays(today, 275)
	state[Key{"acme", "forfeit"}] = State{AnchorAt: iso(ago(1)), DoneFor: occurrence, DoneAt: iso(ago(2))}
	it := one(t, run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, state, later))
	if it.Occurrence != iso(ago(1)) || it.Stage != "d90" {
		t.Fatalf("got %+v", it)
	}
}

func TestAClockStillRunning14DaysAfterDoneComesBackAsRecheck(t *testing.T) {
	r := rule("forfeit", "withdraw", catalog.ObligationWhen{Since: "last_payout", Days: 365}, 90)
	f := facts(readings(ago(300), 1.0, ago(1), 3.0))
	occurrence := iso(ago(300))
	hidden := map[Key]State{{"acme", "forfeit"}: {DoneFor: occurrence, DoneAt: iso(ago(13))}}
	if ev := run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, hidden, today); len(ev.Items) != 0 {
		t.Fatalf("hidden for 14 days after Done, got %+v", ev.Items)
	}
	back := map[Key]State{{"acme", "forfeit"}: {DoneFor: occurrence, DoneAt: iso(ago(14))}}
	if it := one(t, run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, back, today)); it.Stage != "recheck" {
		t.Fatalf("got stage %s", it.Stage)
	}
}

func TestSnoozeHidesUntilItsDate(t *testing.T) {
	state := map[Key]State{{"acme", "claim"}: {SnoozedUntil: iso(addDays(today, 7))}}
	if ev := run(t, []catalog.Service{svc("acme", claim)}, map[string]Facts{"acme": {}}, state, today); len(ev.Items) != 0 {
		t.Fatalf("got %+v", ev.Items)
	}
	one(t, run(t, []catalog.Service{svc("acme", claim)}, map[string]Facts{"acme": {}}, state, addDays(today, 7)))
}

func TestSnoozeNeverRunsPastTheLastWarning(t *testing.T) {
	state := map[Key]State{{"acme", "claim"}: {SnoozedUntil: "2027-12-31"}}
	if ev := run(t, []catalog.Service{svc("acme", claim)}, map[string]Facts{"acme": {}}, state, date(2027, 1, 20)); len(ev.Items) != 0 {
		t.Fatalf("got %+v", ev.Items)
	}
	it := one(t, run(t, []catalog.Service{svc("acme", claim)}, map[string]Facts{"acme": {}}, state, date(2027, 1, 21)))
	if it.Stage != "d1" || it.CanSnooze {
		t.Fatalf("got %+v", it)
	}
}

func TestNotForMeMutesOneRule(t *testing.T) {
	state := map[Key]State{{"acme", "claim"}: {Muted: true}}
	ev := run(t, []catalog.Service{svc("acme", claim, pool)}, map[string]Facts{"acme": {}}, state, date(2027, 1, 1))
	if len(ev.Items) != 1 || ev.Items[0].Rule != "pool" {
		t.Fatalf("got %+v", ev.Items)
	}
	if !reflect.DeepEqual(ev.Muted, []Muted{{Slug: "acme", Rule: "claim", Name: "Acme", Title: "Do claim"}}) {
		t.Fatalf("got %+v", ev.Muted)
	}
}

func TestNotForMeOnTheWholeService(t *testing.T) {
	state := map[Key]State{{"acme", WholeService}: {Muted: true}}
	ev := run(t, []catalog.Service{svc("acme", claim, pool)}, map[string]Facts{"acme": {}}, state, date(2027, 1, 1))
	if len(ev.Items) != 0 || len(ev.Muted) != 1 || ev.Muted[0].Rule != WholeService {
		t.Fatalf("got %+v", ev)
	}
}

// --- the built-in cash-out reminder ---------------------------------------------

func cash(balance, usd float64) Facts {
	return Facts{Balance: ptr(balance), BalanceUSD: ptr(usd), Minimum: ptr(20.0), Currency: "USD", Collected: true}
}

func TestCashOutReadyAtTheMinimum(t *testing.T) {
	ev := run(t, []catalog.Service{svc("acme")}, map[string]Facts{"acme": cash(25, 25)}, nil, today)
	it := one(t, ev)
	if it.Rule != CashOut || it.Stage != "ready" || it.Occurrence != iso(today) || it.Due != "" || it.DaysLeft != nil {
		t.Fatalf("got %+v", it)
	}
	if !reflect.DeepEqual(ev.Anchors, []Anchor{{Slug: "acme", Rule: CashOut, At: iso(today)}}) {
		t.Fatalf("got %+v", ev.Anchors)
	}
}

func TestCashOutStillReadyAfter30Days(t *testing.T) {
	state := map[Key]State{{"acme", CashOut}: {AnchorAt: iso(ago(30))}}
	if it := one(t, run(t, []catalog.Service{svc("acme")}, map[string]Facts{"acme": cash(25, 25)}, state, today)); it.Stage != "still-ready" {
		t.Fatalf("got %s", it.Stage)
	}
}

func TestCashOutFloorHoldsBackATinyMinimum(t *testing.T) {
	tiny := func(b float64) Facts { f := cash(b, b); f.Minimum = ptr(0.05); return f }
	if ev := run(t, []catalog.Service{svc("acme")}, map[string]Facts{"acme": tiny(0.06)}, nil, today); len(ev.Items) != 0 {
		t.Fatalf("got %+v", ev.Items)
	}
	one(t, run(t, []catalog.Service{svc("acme")}, map[string]Facts{"acme": tiny(5)}, nil, today))
}

func TestCashOutGivesNothingWhenUnknownAutomaticOrUncollected(t *testing.T) {
	noMin := cash(25, 25)
	noMin.Minimum = nil
	noUSD := cash(25, 25)
	noUSD.BalanceUSD = nil
	uncollected := cash(25, 25)
	uncollected.Collected = false
	auto := svc("acme")
	auto.Cashout.Method = "auto"
	dead := svc("acme")
	dead.Status = "dead"
	for name, c := range map[string]struct {
		s catalog.Service
		f Facts
	}{"no minimum": {svc("acme"), noMin}, "no usd": {svc("acme"), noUSD}, "no collector": {svc("acme"), uncollected}, "auto": {auto, cash(25, 25)}, "dead": {dead, cash(25, 25)}} {
		if ev := run(t, []catalog.Service{c.s}, map[string]Facts{"acme": c.f}, nil, today); len(ev.Items) != 0 {
			t.Errorf("%s: got %+v", name, ev.Items)
		}
	}
}

func TestCashOutHiddenWhenAWithdrawRuleShows(t *testing.T) {
	r := rule("forfeit", "withdraw", catalog.ObligationWhen{Since: "last_payout", Days: 365}, 90)
	f := cash(25, 25)
	f.Readings = readings(ago(300), 25.0, ago(1), 25.0)
	ev := run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, nil, today)
	if len(ev.Items) != 1 || ev.Items[0].Rule != "forfeit" {
		t.Fatalf("got %+v", ev.Items)
	}
	f.Readings = readings(ago(10), 25.0, ago(1), 25.0)
	ev = run(t, []catalog.Service{svc("acme", r)}, map[string]Facts{"acme": f}, nil, today)
	if len(ev.Items) != 1 || ev.Items[0].Rule != CashOut {
		t.Fatalf("got %+v", ev.Items)
	}
}

func TestCashOutRequestedThenStillThereAfter14Days(t *testing.T) {
	start := iso(ago(20))
	state := map[Key]State{{"acme", CashOut}: {AnchorAt: start, DoneFor: start, DoneAt: iso(ago(14))}}
	if it := one(t, run(t, []catalog.Service{svc("acme")}, map[string]Facts{"acme": cash(25, 25)}, state, today)); it.Stage != "recheck" {
		t.Fatalf("got %s", it.Stage)
	}
}

func TestCashOutNotReadyClearsItsStart(t *testing.T) {
	state := map[Key]State{{"acme", CashOut}: {AnchorAt: iso(ago(3))}}
	ev := run(t, []catalog.Service{svc("acme")}, map[string]Facts{"acme": cash(1, 1)}, state, today)
	if len(ev.Items) != 0 || !reflect.DeepEqual(ev.Anchors, []Anchor{{Slug: "acme", Rule: CashOut}}) {
		t.Fatalf("got %+v", ev)
	}
}

func TestLargestCashOutFirst(t *testing.T) {
	ev := run(t, []catalog.Service{svc("small"), svc("big")}, map[string]Facts{"small": cash(25, 6), "big": cash(25, 60)}, nil, today)
	if len(ev.Items) != 2 || ev.Items[0].Slug != "big" {
		t.Fatalf("got %+v", ev.Items)
	}
}

// --- which services ---------------------------------------------------------------

func TestOnlyServicesInUseGetReminders(t *testing.T) {
	if ev := run(t, []catalog.Service{svc("acme", claim)}, map[string]Facts{}, nil, today); len(ev.Items) != 0 {
		t.Fatalf("a catalog service not in use got %+v", ev.Items)
	}
	custom := svc("custom-acme", claim)
	custom.Custom = true
	if ev := run(t, []catalog.Service{custom}, map[string]Facts{"custom-acme": {}}, nil, today); len(ev.Items) != 0 {
		t.Fatalf("a custom service got %+v", ev.Items)
	}
}

func TestOverdueComesFirst(t *testing.T) {
	soon := rule("soon", "claim", catalog.ObligationWhen{Date: iso(addDays(today, 20))})
	f := facts(readings(ago(200), 1.0, ago(190), 2.0, ago(1), 2.0))
	ev := run(t, []catalog.Service{svc("aaa", soon), svc("zzz", keep)}, map[string]Facts{"aaa": {}, "zzz": f}, nil, today)
	if len(ev.Items) != 2 || ev.Items[0].Stage != "overdue" {
		t.Fatalf("got %+v", ev.Items)
	}
}

// --- what an action writes ----------------------------------------------------------

func TestDecideDoneKeepsTheOtherOpenWindowDone(t *testing.T) {
	s := svc("acme", pool)
	on := date(2026, 12, 1)
	row := State{DoneFor: "2026-11-01", DoneAt: "2026-11-20"}
	ev := run(t, []catalog.Service{s}, map[string]Facts{"acme": {}}, map[Key]State{{"acme", "pool"}: row}, on)
	ch, err := Decide(s, ev, row, "pool", "done", "2026-12-01", 0, on)
	if err != nil || ch.DoneFor == nil || *ch.DoneFor != "2026-11-01,2026-12-01" || *ch.DoneAt != "2026-12-01" {
		t.Fatalf("got %+v, %v", ch, err)
	}
}

func TestDecideRefusals(t *testing.T) {
	s := svc("acme", claim)
	ev := run(t, []catalog.Service{s}, map[string]Facts{"acme": {}}, nil, date(2027, 1, 21))
	cases := []struct {
		rule, action, occ string
		days              int
		want              error
	}{
		{"nope", "done", "", 0, ErrUnknownReminder},
		{"claim", "explode", "", 0, ErrUnknownAction},
		{"claim", "done", "2020-01-01", 0, ErrNotDue},
		{"claim", "snooze", "", 3, ErrSnoozeDays},
		{"claim", "snooze", "", 1, ErrTooClose},
	}
	for _, c := range cases {
		if _, err := Decide(s, ev, State{}, c.rule, c.action, c.occ, c.days, date(2027, 1, 21)); err != c.want {
			t.Errorf("%s %s: got %v, want %v", c.rule, c.action, err, c.want)
		}
	}
	ch, err := Decide(s, ev, State{}, WholeService, "mute", "", 0, date(2027, 1, 21))
	if err != nil || ch.Muted == nil || !*ch.Muted {
		t.Fatalf("mute all: %+v, %v", ch, err)
	}
	early := run(t, []catalog.Service{s}, map[string]Facts{"acme": {}}, nil, today)
	ch, err = Decide(s, early, State{}, "claim", "snooze", "", 7, today)
	if err != nil || ch.SnoozedUntil == nil || *ch.SnoozedUntil != "2026-12-01" {
		t.Fatalf("snooze a week: %+v, %v", ch, err)
	}
}
