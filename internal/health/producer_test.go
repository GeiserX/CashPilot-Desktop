package health

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// realService loads a service from the catalog this app actually ships, so these
// tests run against the patterns a user's machine will match — not against a fixture
// written next to the matcher, which would still pass if the catalog's own signals
// stopped parsing.
func realService(t *testing.T, slug string) catalog.Service {
	t.Helper()
	cat, err := catalog.LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("load the shipped catalog: %v", err)
	}
	svc, ok := cat.Get(slug)
	if !ok {
		t.Fatalf("%s is missing from the shipped catalog", slug)
	}
	return svc
}

func reasonText(r Report) string { return strings.Join(r.Reasons, " | ") }

// mysteriumLog is a log tail around the real failure the catalog's mysterium signal
// was written for: the node registers, is listed in discovery, and every session dies
// at setup because it cannot run sudo inside its container.
const mysteriumLog = `
2026-09-02T11:04:01Z INF Node started
2026-09-02T11:04:03Z INF Identity unlocked, registered
2026-09-02T11:05:12Z ERR sudo: PERM_SUDOERS: setresuid(0, -1, -1): Operation not permitted
2026-09-02T11:05:12Z ERR failed to configure firewall
`

// TestRealMysteriumSignalMakesARunningNodeNotEarning is the case the whole package
// exists for: the container is up, the runtime is happy, and the node is earning
// nothing. The verdict must be a positive finding, and it must carry the catalog's
// own explanation rather than a generic complaint.
func TestRealMysteriumSignalMakesARunningNodeNotEarning(t *testing.T) {
	svc := realService(t, "mysterium")
	if len(svc.Docker.HealthSignals) == 0 {
		t.Fatal("the shipped mysterium entry declares no health signals, so this test would prove nothing")
	}

	got := Assess(Input{
		Slug:           "mysterium",
		ContainerState: "running",
		Signals:        svc.Docker.HealthSignals,
		Logs:           mysteriumLog,
		LogsRead:       true,
	})

	if got.State != StateFailing {
		t.Fatalf("a node hitting its own declared failure signal must be failing, got %q (%s)", got.State, reasonText(got))
	}
	if !got.State.NotEarning() {
		t.Error("a failing node must read as not earning")
	}
	// The reason the user sees has to be the catalog's explanation of THIS failure.
	// Anything else sends them to look in the wrong place.
	want := collapseSpace(svc.Docker.HealthSignals[0].Means)
	if !strings.Contains(reasonText(got), want) {
		t.Errorf("the verdict must explain the failure in the catalog's own words.\n got: %s\nwant: %s", reasonText(got), want)
	}
}

// TestCleanLogsAreNotCheckedNotGreen: the patterns only cover problems somebody has
// already seen, so their absence is the absence of evidence. Reporting it as a clean
// bill of health is the false confidence this package exists to remove.
func TestCleanLogsAreNotCheckedNotGreen(t *testing.T) {
	svc := realService(t, "mysterium")
	got := Assess(Input{
		Slug:           "mysterium",
		ContainerState: "running",
		Signals:        svc.Docker.HealthSignals,
		Logs:           "2026-09-02T11:04:01Z INF Node started\n2026-09-02T11:44:01Z INF session completed\n",
		LogsRead:       true,
	})
	if got.State != StateNotChecked {
		t.Fatalf("quiet logs are not a verdict; got %q", got.State)
	}
	if got.State.NotEarning() {
		t.Error("quiet logs must not be reported as a problem either")
	}
	if reasonText(got) == "" {
		t.Error(`a bare "not checked" with no reason leaves the user nothing to act on`)
	}
}

// TestRealBitpingHasNoSignalsSoNothingIsJudged: most catalogued services declare no
// patterns at all. The matcher must not invent one from log text that merely looks
// alarming, and the verdict must say it did not look rather than say it is fine.
func TestRealBitpingHasNoSignalsSoNothingIsJudged(t *testing.T) {
	svc := realService(t, "bitping")
	if len(svc.Docker.HealthSignals) != 0 {
		t.Fatal("bitping now declares log signals, so it is no longer the 'declares none' case " +
			"this test needs: point it at one of the many services that still declare none")
	}

	alarming := "ERROR No active session\nERROR login failed\nERROR sudo: PERM_SUDOERS\n"
	got := Assess(Input{
		Slug:           "bitping",
		ContainerState: "running",
		Signals:        svc.Docker.HealthSignals,
		Logs:           alarming,
		LogsRead:       true,
	})
	if got.State != StateNotChecked {
		t.Fatalf("a service with no declared signals cannot be judged from its logs; got %q (%s)", got.State, reasonText(got))
	}

	// And the reason must be honest about WHY: "we have no patterns for this service"
	// is a different answer from "we looked and found nothing", which is what the
	// service above gets.
	withSignals := Assess(Input{
		Slug:           "mysterium",
		ContainerState: "running",
		Signals:        realService(t, "mysterium").Docker.HealthSignals,
		Logs:           "quiet\n",
		LogsRead:       true,
	})
	if reasonText(got) == reasonText(withSignals) {
		t.Error("having no patterns and finding no match are different answers and must read differently")
	}
}

// TestRestartLoopIsNotEarningEvenWithNoSignals: a container the runtime keeps
// restarting spends its life starting up. This is the signal that works for the 30+
// services that declare no patterns at all.
func TestRestartLoopIsNotEarningEvenWithNoSignals(t *testing.T) {
	got := Assess(Input{Slug: "bitping", ContainerState: "restarting"})
	if got.State != StateFailing {
		t.Fatalf("a restarting container must be reported as not earning; got %q (%s)", got.State, reasonText(got))
	}
	if reasonText(got) == "" {
		t.Error("the user must be told why")
	}
}

// TestRepeatedCrashesTodayAreARestartLoop, and its boundary: one bad night is an
// incident, not a loop. Flagging every service that ever crashed would make the badge
// noise the user learns to ignore.
func TestRepeatedCrashesTodayAreARestartLoop(t *testing.T) {
	looping := Assess(Input{Slug: "storj", ContainerState: "running", RecentCrashes: restartLoopCrashes})
	if looping.State != StateFailing {
		t.Errorf("%d unexpected exits today is a loop; got %q", restartLoopCrashes, looping.State)
	}

	occasional := Assess(Input{Slug: "storj", ContainerState: "running", RecentCrashes: restartLoopCrashes - 1})
	if occasional.State.NotEarning() {
		t.Errorf("%d exits is an incident, not a loop; got %q (%s)",
			restartLoopCrashes-1, occasional.State, reasonText(occasional))
	}
}

// TestCouldNotLookIsNotTheSameClaimAsStopped. Telling a user their service is not
// running when we simply could not ask sends them to restart something that is
// probably up; both are "no verdict", and they must not read the same.
func TestCouldNotLookIsNotTheSameClaimAsStopped(t *testing.T) {
	unknown := Assess(Input{Slug: "storj", ContainerState: ""})
	stopped := Assess(Input{Slug: "storj", ContainerState: "exited"})

	for _, r := range []Report{unknown, stopped} {
		if r.State != StateNotChecked {
			t.Errorf("neither case supports a finding; got %q", r.State)
		}
	}
	if reasonText(unknown) == reasonText(stopped) {
		t.Error(`"we could not look" and "it is stopped" are different claims and must read differently`)
	}
}

// TestUnreadableLogsSaySo: an empty log tail is both "the read failed" and "the
// service is quiet", and only one of those is the user's problem to fix.
func TestUnreadableLogsSaySo(t *testing.T) {
	signals := realService(t, "mysterium").Docker.HealthSignals
	failed := Assess(Input{Slug: "mysterium", ContainerState: "running", Signals: signals, LogsRead: false})
	quiet := Assess(Input{Slug: "mysterium", ContainerState: "running", Signals: signals, Logs: "all fine\n", LogsRead: true})

	if failed.State != StateNotChecked || quiet.State != StateNotChecked {
		t.Fatalf("neither is a finding; got %q and %q", failed.State, quiet.State)
	}
	if reasonText(failed) == reasonText(quiet) {
		t.Error("a failed log read must not read like a log read that found nothing")
	}
}

// TestFailingOutranksIdle: when both fire, the concrete diagnosis is the one worth
// showing, and neither reason may be dropped.
func TestFailingOutranksIdle(t *testing.T) {
	got := Assess(Input{
		Slug:           "demo",
		ContainerState: "running",
		Signals: []catalog.HealthSignal{
			{Pattern: "no sessions", Means: "Nobody is buying.", State: "idle"},
			{Pattern: "login failed", Means: "It rejected the saved credentials.", State: "failing"},
		},
		Logs:     "WARN no sessions for 6h\nERR login failed\n",
		LogsRead: true,
	})
	if got.State != StateFailing {
		t.Fatalf("a concrete failure outranks idleness; got %q", got.State)
	}
	for _, want := range []string{"Nobody is buying.", "It rejected the saved credentials."} {
		if !strings.Contains(reasonText(got), want) {
			t.Errorf("both findings must be shown; %q missing from %s", want, reasonText(got))
		}
	}
}

// TestIdleSignalIsNotEarningButNotBroken: the catalog's second state must survive the
// trip, otherwise every idle signal would be reported as a failure.
func TestIdleSignalIsNotEarningButNotBroken(t *testing.T) {
	got := Assess(Input{
		Slug:           "demo",
		ContainerState: "running",
		Signals:        []catalog.HealthSignal{{Pattern: "no sessions", Means: "Nobody is buying.", State: "idle"}},
		Logs:           "WARN no sessions for 6h\n",
		LogsRead:       true,
	})
	if got.State != StateIdle {
		t.Fatalf("an idle signal must stay idle; got %q", got.State)
	}
	if !got.State.NotEarning() {
		t.Error("idle still means it is not earning")
	}
}

// TestSignalWithNoStateIsTreatedAsFailing follows the catalog schema's own default.
func TestSignalWithNoStateIsTreatedAsFailing(t *testing.T) {
	got := Assess(Input{
		Slug:           "demo",
		ContainerState: "running",
		Signals:        []catalog.HealthSignal{{Pattern: "login failed", Means: "Bad credentials."}},
		Logs:           "ERR login failed\n",
		LogsRead:       true,
	})
	if got.State != StateFailing {
		t.Fatalf("a signal with no state declared defaults to failing; got %q", got.State)
	}
}

// TestOneBrokenPatternDoesNotSilenceTheRest: a typo in one catalog entry must not
// turn every other service's verdict off, and it must not crash.
func TestOneBrokenPatternDoesNotSilenceTheRest(t *testing.T) {
	hits := MatchSignals("ERR login failed\n", []catalog.HealthSignal{
		{Pattern: "([unclosed", Means: "Never compiles.", State: "failing"},
		{Pattern: "login failed", Means: "Bad credentials.", State: "failing"},
	})
	if len(hits) != 1 || hits[0].Means != "Bad credentials." {
		t.Fatalf("the valid pattern must still match; got %+v", hits)
	}
}

// TestMatchingIgnoresCase: log capitalisation is not a contract, and the real
// mysterium pattern is written in the case that image happened to use.
func TestMatchingIgnoresCase(t *testing.T) {
	hits := MatchSignals("err LOGIN FAILED\n", []catalog.HealthSignal{{Pattern: "login failed", Means: "Bad credentials."}})
	if len(hits) != 1 {
		t.Fatalf("matching must ignore case; got %d hits", len(hits))
	}
}

// TestOnlyTheRecentTailIsMatched: the question is what the service is doing NOW. A
// failure from the top of a huge log the container has long since recovered from
// would otherwise pin a red badge on a working service forever.
func TestOnlyTheRecentTailIsMatched(t *testing.T) {
	signal := []catalog.HealthSignal{{Pattern: "login failed", Means: "Bad credentials."}}
	filler := strings.Repeat("x", maxLogChars+1000)

	if hits := MatchSignals("ERR login failed\n"+filler, signal); len(hits) != 0 {
		t.Error("a failure older than the matched tail must not be reported as current")
	}
	if hits := MatchSignals(filler+"\nERR login failed\n", signal); len(hits) != 1 {
		t.Error("CONTROL: the same line at the end of the log must still match")
	}
}

// TestAMatchedSignalWithNoExplanationStillSurfaces: dropping it would hide a real
// finding because somebody left a catalog field blank.
func TestAMatchedSignalWithNoExplanationStillSurfaces(t *testing.T) {
	got := Assess(Input{
		Slug:           "demo",
		ContainerState: "running",
		Signals:        []catalog.HealthSignal{{Pattern: "login failed"}},
		Logs:           "ERR login failed\n",
		LogsRead:       true,
	})
	if got.State != StateFailing {
		t.Fatalf("an unexplained match is still a match; got %q", got.State)
	}
	if strings.TrimSpace(reasonText(got)) == "" {
		t.Error("the user must be given something to read")
	}
}

// TestFoldedCatalogTextFitsOneLine: the catalog writes means: as a folded YAML block,
// which arrives full of newlines. A tooltip is one line.
func TestFoldedCatalogTextFitsOneLine(t *testing.T) {
	got := Assess(Input{
		Slug:           "demo",
		ContainerState: "running",
		Signals:        []catalog.HealthSignal{{Pattern: "login failed", Means: "It rejected\nthe saved\ncredentials."}},
		Logs:           "ERR login failed\n",
		LogsRead:       true,
	})
	if strings.ContainsAny(reasonText(got), "\n\r") {
		t.Errorf("the reason must be one line, got %q", reasonText(got))
	}
	if !strings.Contains(reasonText(got), "It rejected the saved credentials.") {
		t.Errorf("folding must not lose words, got %q", reasonText(got))
	}
}
