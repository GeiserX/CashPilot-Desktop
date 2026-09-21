// Package health answers a question the container runtime cannot: is this service
// actually EARNING, as distinct from merely running?
//
// Desktop's status column is built from the container's own state, which is computed
// from starts, stops and crashes. A service that has produced nothing for a month
// still shows a green "running" pill, because from the runtime's point of view
// nothing is wrong. "It runs but earns nothing" is the most common complaint in this
// whole product category, and container state is structurally incapable of seeing it.
//
// So this is a SEPARATE verdict, deliberately. A service can be a perfectly healthy
// container and a dead producer, and collapsing the two hides exactly the case the
// user cares about. It ports the idea from the web sibling's app/producer_state.py.
//
// Two signals feed it, and they are the two Desktop can actually observe:
//
//   - Log signals — the per-service regexes declared in the service catalog
//     (docker.health_signals). Service-specific knowledge lives in the catalog, never
//     here, and for the catalogued services with no earnings collector this is the
//     only signal there is.
//   - Restart loops — a container the runtime keeps restarting, or one that has
//     exited unexpectedly several times in the last day, spends most of its life
//     starting up rather than working.
//
// What is NOT here matters just as much. There is no "producing" verdict: nothing
// Desktop can see from a container proves money moved, and a green "earning" badge
// that means "we found no problem" is the false confidence this package exists to
// remove. The worst answer is a confident one we cannot support, so anything we
// could not check says so.
package health

import (
	"regexp"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// State is a producer verdict. There are only three, and the missing fourth is the
// point: "earning" is not among them because no signal here can prove it.
type State string

const (
	// StateNotChecked means we have no basis for a claim either way: the container is
	// not running, its service declares no log signals, its logs could not be read, or
	// they were read and showed nothing known. Not a clean bill of health.
	StateNotChecked State = "not-checked"
	// StateIdle means a declared signal says the service is up but doing no paid work.
	StateIdle State = "idle"
	// StateFailing means a declared signal says it is broken, or it is restart-looping.
	StateFailing State = "failing"
)

// rank orders the verdicts worst-last, so combining signals keeps the most serious
// one. Failing beats idle: a log line saying "login failed" is a concrete, actionable
// diagnosis, whereas idleness is only an observation. Both beat not-checked, which is
// the absence of a finding rather than a finding.
var rank = map[State]int{StateNotChecked: 0, StateIdle: 1, StateFailing: 2}

// NotEarning reports whether this verdict is a positive finding that the service is
// running without earning. StateNotChecked is deliberately false: we did not find it
// healthy, we found nothing.
func (s State) NotEarning() bool { return s == StateIdle || s == StateFailing }

// Container states, as both Docker and Podman report them.
const (
	containerRunning    = "running"
	containerRestarting = "restarting"
)

// restartLoopCrashes is how many unexpected exits in the caller's recent window count
// as a loop. One crash is an incident; three is a pattern, and a service that has to
// be restarted three times a day is not working for most of that day.
const restartLoopCrashes = 3

// maxLogChars caps how much of the log tail a pattern is matched against. Go's regexp
// runs in time linear in the input, so a hostile catalog pattern cannot hang the app,
// but there is no reason to scan a megabyte of history to answer "what is it doing
// now" either.
const maxLogChars = 200_000

// Report is the producer verdict for one service, ready to hand to the UI. Reasons is
// what the user is shown; it is populated for every state, including not-checked,
// because "why don't you know?" is the first question a bare "not checked" provokes.
type Report struct {
	Slug    string   `json:"slug"`
	State   State    `json:"state"`
	Reasons []string `json:"reasons"`
}

// Input is everything Assess is allowed to reason from, gathered by the caller.
type Input struct {
	Slug string
	// ContainerState is the runtime's own word for the container: "running",
	// "restarting", "exited", "created". An EMPTY string means we could not ask,
	// which is not the same claim as "it is stopped" — telling a user their service
	// is down when we simply could not look sends them to restart something that is
	// probably up.
	ContainerState string
	// Signals are the log patterns this service's catalog entry declares. None is the
	// normal case: most services declare none, and that makes them not-checked rather
	// than fine.
	Signals []catalog.HealthSignal
	// Logs is the recent log tail. LogsRead says whether reading them actually worked,
	// because an empty string is both "the read failed" and "the service is quiet",
	// and only one of those is worth telling the user about.
	Logs     string
	LogsRead bool
	// RecentCrashes is the number of unexpected exits in a SHORT window — Desktop
	// passes the last day. A week-long count would be wrong here: a service that
	// crashed three times last Tuesday and has run since is not looping today, and
	// flagging it as not earning all week is a false accusation.
	RecentCrashes int
}

// Hit is one declared signal that was found in the logs.
type Hit struct {
	Pattern string
	Means   string
	State   State
}

// MatchSignals returns the declared signals whose pattern appears in the logs. An
// invalid regex is skipped rather than fatal: a typo in one service's catalog entry
// must not break the verdict for every other service.
func MatchSignals(logs string, signals []catalog.HealthSignal) []Hit {
	if logs == "" || len(signals) == 0 {
		return nil
	}
	haystack := logs
	if len(haystack) > maxLogChars {
		haystack = haystack[len(haystack)-maxLogChars:]
	}
	var hits []Hit
	for _, signal := range signals {
		if signal.Pattern == "" {
			continue
		}
		// (?i): the catalog's patterns are matched case-insensitively, as the web
		// sibling does, because log capitalisation is not a stable contract.
		re, err := regexp.Compile("(?i)" + signal.Pattern)
		if err != nil || !re.MatchString(haystack) {
			continue
		}
		hits = append(hits, Hit{
			Pattern: signal.Pattern,
			Means:   meansOrDefault(signal.Means),
			State:   signalState(signal.State),
		})
	}
	return hits
}

// meansOrDefault is the explanation shown to the user, falling back to something true
// but vague when the catalog entry left it out. A matched pattern with no explanation
// is still worth surfacing; silently dropping it would hide a real finding.
func meansOrDefault(means string) string {
	if m := strings.TrimSpace(collapseSpace(means)); m != "" {
		return m
	}
	return "Its logs match a known problem for this service."
}

// collapseSpace folds the newlines and runs of spaces that YAML folded scalars leave
// in a multi-line means: into single spaces, so the text fits a one-line tooltip.
func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// signalState maps a catalog state word onto a verdict. The catalog allows "failing"
// and "idle"; anything else (including empty) is read as failing, which is the
// schema's own default and the safer way to be wrong — under-reporting a real failure
// is the mistake that costs money.
func signalState(state string) State {
	if strings.EqualFold(strings.TrimSpace(state), string(StateIdle)) {
		return StateIdle
	}
	return StateFailing
}

// Assess combines the available signals into one verdict for one service.
func Assess(in Input) Report {
	switch {
	case in.ContainerState == "":
		return report(in.Slug, StateNotChecked,
			"We could not tell whether this service is running, so nothing here is judged.")
	case in.ContainerState != containerRunning && in.ContainerState != containerRestarting:
		return report(in.Slug, StateNotChecked, "It is not running, so there is nothing to judge.")
	}

	state := StateNotChecked
	var reasons []string
	add := func(s State, reason string) {
		if rank[s] > rank[state] {
			state = s
		}
		reasons = append(reasons, reason)
	}

	// A container the runtime is restarting right now, and one that keeps exiting
	// unexpectedly, are the same finding seen from two places: it is starting up far
	// more than it is working. Either is enough on its own.
	if in.ContainerState == containerRestarting {
		add(StateFailing, "It keeps restarting, so it never runs long enough to earn.")
	} else if in.RecentCrashes >= restartLoopCrashes {
		add(StateFailing, "It has stopped unexpectedly several times today, so it is restarting more than it is working.")
	}

	switch {
	case len(in.Signals) == 0:
		add(StateNotChecked, "This service declares no log signals, so its logs cannot tell us whether it is earning.")
	case !in.LogsRead:
		add(StateNotChecked, "We could not read its logs, so nothing in them is judged.")
	default:
		hits := MatchSignals(in.Logs, in.Signals)
		for _, hit := range hits {
			add(hit.State, hit.Means)
		}
		if len(hits) == 0 {
			// Not a pass. The patterns only cover the problems somebody has seen
			// before, so their absence is the absence of evidence.
			add(StateNotChecked, "Its logs show none of the known problems, which is not proof that it is earning.")
		}
	}

	return Report{Slug: in.Slug, State: state, Reasons: reasons}
}

func report(slug string, state State, reason string) Report {
	return Report{Slug: slug, State: state, Reasons: []string{reason}}
}
