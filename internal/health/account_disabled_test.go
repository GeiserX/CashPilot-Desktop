package health

import (
	"context"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

var userDisabled = catalog.HealthSignal{Pattern: "user is disabled", Means: "IPRoyal Pawns has disabled this account.", State: catalog.SignalAccountDisabled}

// pawns-cli prints the refusal with no newline and exits 1 on every start, so a
// crash-looping copy's log is the same words glued together.
const refusedLog = "user is disableduser is disableduser is disabled"

// fakeRunLogs is a log reader that can also read the last run on its own, as the
// Docker runtime does. lastRun is what that read returns.
type fakeRunLogs struct {
	fakeLogs
	lastRun string
	runs    []string
}

func (f *fakeRunLogs) LastRunLogs(_ context.Context, slug string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, slug)
	return f.lastRun, nil
}

func TestCollectReportsAccountDisabledOnAFinishedRun(t *testing.T) {
	for _, state := range []string{"restarting", "exited", "dead", "running"} {
		logs := &fakeRunLogs{lastRun: refusedLog}
		got := Collect(context.Background(), logs, fakeCatalog{"iproyal": withSignals(userDisabled)},
			[]Deployed{{Slug: "iproyal", ContainerState: state, Runtime: "docker"}})["iproyal"]
		if got.State != StateAccountDisabled || len(got.Reasons) != 1 || got.Reasons[0] != userDisabled.Means {
			t.Errorf("%s: got %+v, want one account_disabled verdict with the catalog's explanation", state, got)
		}
		if !got.State.NotEarning() {
			t.Errorf("account_disabled must count as not earning")
		}
	}
}

// A running copy is not judged on an old refusal in its log: the account may have
// been turned back on, and this run is the one that counts.
func TestRunningContainerIsNotJudgedOnAnOldRefusal(t *testing.T) {
	logs := &fakeLogs{out: refusedLog}
	got := Collect(context.Background(), logs, fakeCatalog{"iproyal": withSignals(userDisabled)},
		[]Deployed{{Slug: "iproyal", ContainerState: "running", Runtime: "docker"}})["iproyal"]
	if got.State == StateAccountDisabled || got.State == StateFailing {
		t.Errorf("a running container must not be called disabled from its log, got %+v", got)
	}
}

// The control for the two tests above: a stopped copy that did not print the
// refusal is not called disabled, and one without the signal costs no log read.
func TestStoppedContainerWithoutTheRefusalIsNotDisabled(t *testing.T) {
	logs := &fakeRunLogs{lastRun: "shutting down\n"}
	got := Collect(context.Background(), logs, fakeCatalog{
		"iproyal":   withSignals(userDisabled),
		"honeygain": withSignals(loginFailed),
	}, []Deployed{
		{Slug: "iproyal", ContainerState: "exited", Runtime: "docker"},
		{Slug: "honeygain", ContainerState: "exited", Runtime: "docker"},
	})
	if got["iproyal"].State != StateNotChecked {
		t.Errorf("no refusal in the log must leave it not-checked, got %+v", got["iproyal"])
	}
	if len(logs.runs) != 1 || logs.runs[0] != "iproyal" || len(logs.asked()) != 0 {
		t.Errorf("only a stopped service with an account_disabled signal is worth a last-run read; runs %v, tails %v", logs.runs, logs.asked())
	}
}

func TestNativeServiceIsNotJudgedForRefusal(t *testing.T) {
	logs := &fakeRunLogs{lastRun: refusedLog}
	got := Collect(context.Background(), logs, fakeCatalog{"iproyal": withSignals(userDisabled)},
		[]Deployed{{Slug: "iproyal", ContainerState: "exited", Runtime: NativeRuntime}})["iproyal"]
	if got.State == StateAccountDisabled || len(logs.runs) != 0 {
		t.Errorf("a native process is not judged by docker signals: %+v, read %v", got, logs.runs)
	}
}

// Only the end of the log is the last run; a refusal far above it is history.
func TestMatchRefusalReadsOnlyTheEnd(t *testing.T) {
	old := "user is disabled\n" + strings.Repeat("x", refusalChars)
	if got := MatchRefusal(old, []catalog.HealthSignal{userDisabled}); got != "" {
		t.Errorf("a refusal before the last %d chars must not count, got %q", refusalChars, got)
	}
	if got := MatchRefusal("USER IS DISABLED", []catalog.HealthSignal{userDisabled}); got != userDisabled.Means {
		t.Errorf("matching is case-insensitive, got %q", got)
	}
	if got := MatchRefusal("user is disabled", []catalog.HealthSignal{loginFailed}); got != "" {
		t.Errorf("a failing signal is not a refusal, got %q", got)
	}
}

// A tail match on a running container's log never uses an account_disabled signal.
func TestMatchSignalsSkipsAccountDisabled(t *testing.T) {
	if hits := MatchSignals(refusedLog, []catalog.HealthSignal{userDisabled}); len(hits) != 0 {
		t.Errorf("got %+v, want no tail hit for an account_disabled signal", hits)
	}
}

// The account was turned back on, a later run connected and shared bandwidth, and
// the user pressed Stop. The tail still holds the old refusal, but the last run was
// fine, so it must not be called disabled: that would hide Start until a Remove.
const reEnabledThenStopped = "user is disabled" + "2026-11-02T10:00:05Z connected\n2026-11-02T10:00:06Z running, sharing bandwidth\n"

func TestRefusalFromAnEarlierRunIsNotThisRunsVerdict(t *testing.T) {
	logs := &fakeLogs{out: reEnabledThenStopped}
	got := Collect(context.Background(), logs, fakeCatalog{"iproyal": withSignals(userDisabled)},
		[]Deployed{{Slug: "iproyal", ContainerState: "exited", Runtime: "docker"}})["iproyal"]
	if got.State == StateAccountDisabled {
		t.Fatalf("an earlier run's refusal was judged as the last run's: %+v", got)
	}
}
