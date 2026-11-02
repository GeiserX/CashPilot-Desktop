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

func TestCollectReportsAccountDisabledOnAFinishedRun(t *testing.T) {
	for _, state := range []string{"restarting", "exited", "dead"} {
		logs := &fakeLogs{out: refusedLog}
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
	logs := &fakeLogs{out: "shutting down\n"}
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
	if asked := logs.asked(); len(asked) != 1 || asked[0] != "iproyal" {
		t.Errorf("only a stopped service with an account_disabled signal is worth a log read; asked %v", asked)
	}
}

func TestNativeServiceIsNotJudgedForRefusal(t *testing.T) {
	logs := &fakeLogs{out: refusedLog}
	got := Collect(context.Background(), logs, fakeCatalog{"iproyal": withSignals(userDisabled)},
		[]Deployed{{Slug: "iproyal", ContainerState: "exited", Runtime: NativeRuntime}})["iproyal"]
	if got.State == StateAccountDisabled || len(logs.asked()) != 0 {
		t.Errorf("a native process is not judged by docker signals: %+v, asked %v", got, logs.asked())
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
