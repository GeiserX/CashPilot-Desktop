package main

import (
	"errors"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
	"github.com/GeiserX/CashPilot-Desktop/internal/obligations"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// The dashboard's "Money waiting" panel: reminders before a provider writes off
// your earnings (internal/obligations, a port of the web app's #814).

// reminderNow is the clock the reminders read. A variable so a test can pin "today".
var reminderNow = time.Now

// RemindersView is what the Money waiting panel shows.
type RemindersView struct {
	// Joined is true while this computer is joined to a CashPilot server. The
	// server owns the reminders then, so Items stays empty and the panel says so.
	Joined    bool   `json:"joined"`
	ServerURL string `json:"serverUrl"`
	// Unknown is true when the check failed. It is never shown as an all-clear.
	Unknown bool                `json:"unknown"`
	Items   []obligations.Item  `json:"items"`
	Muted   []obligations.Muted `json:"muted"`
}

// remindersServer reports whether this computer is joined to a CashPilot server,
// and that server's URL. Joined is a.joined() (Mode follower): the server collects the
// earnings then, so it is the one that knows the balances the reminders read. It
// reads the saved config again first, so a join or a switch that the other
// process (the window or the background helper) saved holds here too. The URL is
// only for the panel's note.
func (a *App) remindersServer() (joined bool, serverURL string) {
	a.refreshConfig()
	if !a.joined() {
		return false, ""
	}
	return true, strings.TrimRight(strings.TrimSpace(a.cfg.Config().UpstreamURL), "/")
}

// reminders evaluates the panel for GetAppState. An error is logged and shown as
// Unknown, so the rest of the dashboard still loads.
func (a *App) reminders() RemindersView {
	view, err := a.remindersView()
	if err != nil {
		log.Printf("reminders: could not check: %v", err)
	}
	return view
}

func (a *App) remindersView() (RemindersView, error) {
	view := RemindersView{Items: []obligations.Item{}, Muted: []obligations.Muted{}}
	if joined, server := a.remindersServer(); joined {
		view.Joined, view.ServerURL = true, server
		return view, nil
	}
	ev, _, err := a.evaluateReminders(reminderNow())
	if err != nil {
		view.Unknown = true
		return view, err
	}
	view.Items, view.Muted = ev.Items, ev.Muted
	return view, nil
}

// GetReminders returns the Money waiting panel.
func (a *App) GetReminders() (RemindersView, error) {
	if err := a.ready(); err != nil {
		return RemindersView{}, err
	}
	return a.remindersView()
}

// ReminderAction marks a reminder done, snoozes it for days (1 or 7), or mutes or
// unmutes it ("Not for me"; rule "*" is every reminder of the service).
// occurrence "" matches the reminder's current occurrence.
func (a *App) ReminderAction(slug, rule, action, occurrence string, days int) (RemindersView, error) {
	if err := a.ready(); err != nil {
		return RemindersView{}, err
	}
	if joined, _ := a.remindersServer(); joined {
		return RemindersView{}, errors.New(i18n.T("go.app.reminders.joined"))
	}
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return RemindersView{}, obligations.ErrUnknownReminder
	}
	now := reminderNow()
	ev, state, err := a.evaluateReminders(now)
	if err != nil {
		return RemindersView{}, err
	}
	change, err := obligations.Decide(svc, ev, state[obligations.Key{Slug: slug, Rule: rule}], rule, action, occurrence, days, now)
	if err != nil {
		return RemindersView{}, err
	}
	if err := a.store.SetObligationAction(slug, rule, change.DoneFor, change.DoneAt, change.SnoozedUntil, change.Muted); err != nil {
		return RemindersView{}, err
	}
	return a.remindersView()
}

// evaluateReminders evaluates every reminder now and stores the clock starts seen
// for the first time. The panel calls it on each read, so there is no cache to go
// stale and nothing to restore after a restart.
func (a *App) evaluateReminders(now time.Time) (obligations.Evaluation, map[obligations.Key]obligations.State, error) {
	rows, err := a.store.ListObligationState()
	if err != nil {
		return obligations.Evaluation{}, nil, err
	}
	state := make(map[obligations.Key]obligations.State, len(rows))
	for _, r := range rows {
		state[obligations.Key{Slug: r.Slug, Rule: r.Rule}] = obligations.State{
			AnchorAt: r.AnchorAt, AnchorCensored: r.AnchorCensored, DoneFor: r.DoneFor,
			DoneAt: r.DoneAt, SnoozedUntil: r.SnoozedUntil, Muted: r.Muted,
		}
	}
	facts, err := a.reminderFacts()
	if err != nil {
		return obligations.Evaluation{}, nil, err
	}
	// The rules' ids and dates are the same in every language; only their title,
	// loss and how-to text are translated, and the panel shows those.
	ev := obligations.Evaluate(a.catalog.Localized(a.catalog.List(), i18n.Current()), facts, state, now)
	for _, anchor := range ev.Anchors {
		if err := a.store.SetObligationAnchor(anchor.Slug, anchor.Rule, anchor.At, anchor.Censored); err != nil {
			return obligations.Evaluation{}, nil, err
		}
	}
	return ev, state, nil
}

// reminderFacts is what the engine needs for every catalog service this computer
// uses. In use: deployed here, run by a worker or phone that reports to this
// computer and is online, or holding a balance above 0, even after it was
// removed, because an inactivity clock starts when it stops.
func (a *App) reminderFacts() (map[string]obligations.Facts, error) {
	// The strict reads: a failed read is "could not check", never "nothing due".
	deployments, err := a.store.Deployments()
	if err != nil {
		return nil, err
	}
	devices, err := a.store.FleetDevices()
	if err != nil {
		return nil, err
	}
	balances, err := a.store.DailyBalances(a.retentionDays() + 1)
	if err != nil {
		return nil, err
	}

	inUse := map[string]bool{}
	for _, d := range deployments {
		inUse[d.Slug] = true
	}
	for _, d := range devices {
		if store.EffectiveFleetStatus(d.Status, d.LastSeen, fleetOfflineAfterFor(d.Kind)) != "online" {
			continue
		}
		for _, slug := range d.Services {
			inUse[slug] = true
		}
	}

	// One reading per day per platform, oldest first, from successful reads only.
	byPlatform := map[string][]store.DailyBalance{}
	for _, b := range balances {
		byPlatform[b.Platform] = append(byPlatform[b.Platform], b)
	}
	for slug, days := range byPlatform {
		if days[len(days)-1].Balance > 0 {
			inUse[slug] = true
		}
	}

	slugs := make([]string, 0, len(inUse))
	for slug := range inUse {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	out := map[string]obligations.Facts{}
	for _, slug := range slugs {
		svc, ok := a.catalog.Get(slug)
		if !ok || !obligations.InUse(svc) {
			continue
		}
		f := obligations.Facts{Collected: a.collectors != nil && a.collectors.Supports(slug)}
		if days := byPlatform[slug]; len(days) > 0 {
			latest := days[len(days)-1]
			balance, currency := latest.Balance, strings.ToUpper(latest.Currency)
			f.Balance, f.Currency = &balance, currency
			f.BalanceUSD = a.toUSD(balance, currency)
			f.Minimum = a.minimumIn(svc.Cashout, currency)
			for _, d := range days {
				if !strings.EqualFold(d.Currency, currency) {
					continue
				}
				if day, ok := obligations.ParseDay(d.Day); ok {
					f.Readings = append(f.Readings, obligations.Reading{Day: day, Balance: d.Balance})
				}
			}
			// Payouts only for a service with a clock rule, as on the web.
			if hasClock(svc) {
				payouts, err := a.store.ListPayouts(slug)
				if err != nil {
					return nil, err
				}
				for _, p := range payouts {
					if day, ok := obligations.ParseDay(p.PaidOn); ok {
						f.Payouts = append(f.Payouts, day)
					}
				}
			}
		}
		out[slug] = f
	}
	return out, nil
}

func hasClock(svc catalog.Service) bool {
	for _, r := range svc.Obligations {
		if r.When.Since != "" {
			return true
		}
	}
	return false
}

func (a *App) toUSD(amount float64, currency string) *float64 {
	if currency == "USD" {
		return &amount
	}
	if a.exchange == nil {
		return nil
	}
	if usd, ok := a.exchange.ToUSD(amount, currency); ok {
		return &usd
	}
	return nil
}

// minimumIn is the provider minimum in the balance's unit, nil when unknown. The
// catalog declares it in the unit the provider pays out in, which is not always
// the unit the collector reports. Desktop reads an undocumented minimum as 0, so 0
// is unknown here, as it is on the earnings card.
func (a *App) minimumIn(cash catalog.Cashout, currency string) *float64 {
	minimum := cash.MinAmount
	if minimum <= 0 {
		return nil
	}
	declared := strings.ToUpper(strings.TrimSpace(cash.Currency))
	if declared == "" || currency == "" || declared == currency {
		return &minimum
	}
	if a.exchange == nil {
		return nil
	}
	if v, ok := a.exchange.ToDisplay(minimum, declared, currency); ok {
		return &v
	}
	return nil
}
