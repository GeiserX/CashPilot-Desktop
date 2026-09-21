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

// storjLog is a log tail around the real failure the catalog's storj signal was
// written for: the node is up and the container is healthy, but every satellite that
// tries to dial it back at its advertised address times out.
const storjLog = `
2026-08-14T09:12:40Z INFO piecestore download started
2026-08-14T09:13:02Z ERROR contact:service ping satellite failed {"Satellite ID": "12EayRS2V1k", "error": "check-in ratelimit: failed to dial storage node (ID: 1MzVZ) at address 81.44.12.7:28967: rpc: dial tcp 81.44.12.7:28967: i/o timeout"}
2026-08-14T09:14:02Z ERROR contact:service ping satellite failed {"Satellite ID": "12L9ZFwhzVp", "error": "failed to dial storage node (ID: 1MzVZ) at address 81.44.12.7:28967"}
`

// TestRealStorjSignalMakesARunningNodeNotEarning. This is the only shipped pattern
// with a regex metacharacter in it, so it is the one whose behaviour is least
// obviously the same in Go as in the Python the catalog was written against.
func TestRealStorjSignalMakesARunningNodeNotEarning(t *testing.T) {
	svc := realService(t, "storj")
	if len(svc.Docker.HealthSignals) == 0 {
		t.Fatal("the shipped storj entry declares no health signals, so this test would prove nothing")
	}

	got := Assess(Input{
		Slug:           "storj",
		ContainerState: "running",
		Signals:        svc.Docker.HealthSignals,
		Logs:           storjLog,
		LogsRead:       true,
	})

	if got.State != StateFailing {
		t.Fatalf("a node no satellite can dial back is not earning; got %q (%s)", got.State, reasonText(got))
	}
	want := collapseSpace(svc.Docker.HealthSignals[0].Means)
	if !strings.Contains(reasonText(got), want) {
		t.Errorf("the verdict must explain it in the catalog's own words.\n got: %s\nwant: %s", reasonText(got), want)
	}
}

// TestStorjNoiseDoesNotMatchTheDialFailure is the control. The pattern spans two
// phrases with `.*` between them, which matches anything on one line -- including,
// if it were written loosely, an ordinary successful ping and an unrelated dial
// error from somewhere else in the log.
func TestStorjNoiseDoesNotMatchTheDialFailure(t *testing.T) {
	svc := realService(t, "storj")
	quiet := "2026-08-14T09:12:40Z INFO contact:service ping satellite success\n" +
		"2026-08-14T09:12:55Z INFO piecestore upload started\n"

	got := Assess(Input{
		Slug:           "storj",
		ContainerState: "running",
		Signals:        svc.Docker.HealthSignals,
		Logs:           quiet,
		LogsRead:       true,
	})

	if got.State.NotEarning() {
		t.Fatalf("a successful ping is not a dial failure; got %q (%s)", got.State, reasonText(got))
	}
}

// TestANativeProcessIsNotJudgedByContainerSignals. The catalog's signals live under a
// service's docker: stanza and explain themselves as a container -- mysterium's ends
// "redeploy Mysterium from the dashboard", which is not something the user of a
// supervised process can do. Mysterium is the one service Desktop runs either way.
func TestANativeProcessIsNotJudgedByContainerSignals(t *testing.T) {
	svc := realService(t, "mysterium")
	in := Input{
		Slug:           "mysterium",
		ContainerState: "running",
		Signals:        svc.Docker.HealthSignals,
		Logs:           mysteriumLog,
		LogsRead:       true,
	}

	inContainer := Assess(in)
	in.Native = true
	natively := Assess(in)

	if inContainer.State != StateFailing {
		t.Fatalf("CONTROL: the same logs in a container must still be a finding; got %q", inContainer.State)
	}
	if natively.State != StateNotChecked {
		t.Fatalf("a container's explanation must not be told to someone with no container; got %q (%s)",
			natively.State, reasonText(natively))
	}
	if strings.Contains(reasonText(natively), "container was created") {
		t.Errorf("and the container-specific advice must not leak through: %s", reasonText(natively))
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

// TestTheReasonsAreWrittenForTheUserNotTheCatalog. Almost every running row shows one
// of these, because almost no service has a known failure to look for. "This service
// declares no log signals" described the catalog file: `declares` is what a YAML entry
// does, `signals` is the name of a field in it, and neither is anything the person
// reading the tooltip can see or act on.
func TestTheReasonsAreWrittenForTheUserNotTheCatalog(t *testing.T) {
	running := func(in Input) Report {
		in.Slug, in.ContainerState = "demo", "running"
		return Assess(in)
	}
	shown := []Report{
		running(Input{}),             // no known failure to look for
		running(Input{Native: true}), // not in a container
		running(Input{Signals: []catalog.HealthSignal{{Pattern: "x"}}}), // logs unreadable
		running(Input{Signals: []catalog.HealthSignal{{Pattern: "x"}}, Logs: "quiet\n", LogsRead: true}),
		Assess(Input{Slug: "demo", ContainerState: "exited"}),
		Assess(Input{Slug: "demo"}),
	}
	// Words that only mean something to someone reading the service catalog.
	jargon := []string{"signal", "declare", "catalog", "regex", "pattern", "slug", "runtime"}

	for _, report := range shown {
		text := strings.ToLower(reasonText(report))
		if strings.TrimSpace(text) == "" {
			t.Errorf("%q leaves the user nothing to read", report.State)
			continue
		}
		for _, word := range jargon {
			if strings.Contains(text, word) {
				t.Errorf("%q is the catalog's vocabulary, not the user's: %s", word, reasonText(report))
			}
		}
	}
}
