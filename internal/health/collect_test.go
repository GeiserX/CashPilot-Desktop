package health

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// fakeLogs records which services were actually asked for logs, which is the whole
// cost question: a log read is a round trip to the container runtime on every
// dashboard refresh.
type fakeLogs struct {
	mu   sync.Mutex
	got  []string
	out  string
	fail bool
}

func (f *fakeLogs) Logs(_ context.Context, slug string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, slug)
	if f.fail {
		return "", errors.New("container runtime is not answering")
	}
	return f.out, nil
}

func (f *fakeLogs) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

type fakeCatalog map[string]catalog.Service

func (f fakeCatalog) Get(slug string) (catalog.Service, bool) {
	svc, ok := f[slug]
	return svc, ok
}

func withSignals(signals ...catalog.HealthSignal) catalog.Service {
	var svc catalog.Service
	svc.Docker.HealthSignals = signals
	return svc
}

var loginFailed = catalog.HealthSignal{Pattern: "login failed", Means: "It rejected the saved credentials.", State: "failing"}

// TestCollectOnlyReadsLogsItCanUse. Reading a log tail for a service nobody has
// written a pattern for costs a runtime round trip per service per refresh and
// answers nothing; so does reading the logs of a container that is not up.
func TestCollectOnlyReadsLogsItCanUse(t *testing.T) {
	logs := &fakeLogs{out: "ERR login failed\n"}
	cat := fakeCatalog{
		"withsignals": withSignals(loginFailed),
		"nosignals":   withSignals(),
		"stopped":     withSignals(loginFailed),
	}

	Collect(context.Background(), logs, cat, []Deployed{
		{Slug: "withsignals", ContainerState: "running"},
		{Slug: "nosignals", ContainerState: "running"},
		{Slug: "stopped", ContainerState: "exited"},
	})

	asked := logs.asked()
	if len(asked) != 1 || asked[0] != "withsignals" {
		t.Fatalf("logs must be read only where a pattern can use them; asked %v", asked)
	}
}

// TestCollectReportsTheMatchItFound is the control for the test above: if the one
// read it does make were not wired through, every check here would be vacuous.
func TestCollectReportsTheMatchItFound(t *testing.T) {
	logs := &fakeLogs{out: "ERR login failed\n"}
	got := Collect(context.Background(), logs, fakeCatalog{"demo": withSignals(loginFailed)},
		[]Deployed{{Slug: "demo", ContainerState: "running"}})

	if got["demo"].State != StateFailing {
		t.Fatalf("the matched signal must reach the verdict; got %q", got["demo"].State)
	}
}

// TestCollectStillJudgesARestartLoopWithoutReadingLogs: the restart-loop signal needs
// no patterns and no log read, which is what makes it work for the services that
// declare neither.
func TestCollectStillJudgesARestartLoopWithoutReadingLogs(t *testing.T) {
	logs := &fakeLogs{}
	got := Collect(context.Background(), logs, fakeCatalog{"demo": withSignals()},
		[]Deployed{{Slug: "demo", ContainerState: "running", RecentCrashes: restartLoopCrashes}})

	if !got["demo"].State.NotEarning() {
		t.Fatalf("a crash-looping service must be reported; got %q", got["demo"].State)
	}
	if asked := logs.asked(); len(asked) != 0 {
		t.Errorf("no pattern to match means no log read; asked %v", asked)
	}
}

// TestALogReadFailureIsNotAVerdict: a container runtime that will not answer must
// cost a missing verdict, never a fabricated one — and the user must be told which
// of the two happened. An empty log tail is both "the read failed" and "the service
// is quiet", and only one of those is theirs to fix.
func TestALogReadFailureIsNotAVerdict(t *testing.T) {
	cat := fakeCatalog{"demo": withSignals(loginFailed)}
	deployed := []Deployed{{Slug: "demo", ContainerState: "running"}}

	failed := Collect(context.Background(), &fakeLogs{fail: true}, cat, deployed)["demo"]
	quiet := Collect(context.Background(), &fakeLogs{out: "all fine\n"}, cat, deployed)["demo"]

	if failed.State != StateNotChecked {
		t.Fatalf("a failed read supports no finding; got %q", failed.State)
	}
	if len(failed.Reasons) == 0 {
		t.Fatal("and it must say why it has nothing to report")
	}
	if strings.Join(failed.Reasons, " ") == strings.Join(quiet.Reasons, " ") {
		t.Errorf("a runtime that would not answer must not read like logs we read and found nothing in: %v", failed.Reasons)
	}
}

// TestCollectSurvivesMissingDependencies. Both are nil before startup finishes and in
// the headless paths, and a dashboard refresh that races them must degrade to "not
// checked" rather than panic.
func TestCollectSurvivesMissingDependencies(t *testing.T) {
	got := Collect(context.Background(), nil, nil, []Deployed{{Slug: "demo", ContainerState: "running"}})
	if got["demo"].State != StateNotChecked {
		t.Fatalf("no catalog and no log reader means no verdict; got %q", got["demo"].State)
	}

	// A nil context is the same race one layer down: reached through the real log
	// reader it used to be a panic inside context.WithTimeout.
	logs := &fakeLogs{out: "ERR login failed\n"}
	fromNilCtx := Collect(nil, logs, fakeCatalog{"demo": withSignals(loginFailed)}, //nolint:staticcheck // the nil context is the case under test
		[]Deployed{{Slug: "demo", ContainerState: "running"}})
	if fromNilCtx["demo"].State != StateFailing {
		t.Fatalf("a nil context must not cost the verdict; got %q", fromNilCtx["demo"].State)
	}
}

// TestCollectReportsNothingWhenNothingIsDeployed: an empty map and a nil map are the
// same to the UI, and nil is what "no deployments" should serialise as.
func TestCollectReportsNothingWhenNothingIsDeployed(t *testing.T) {
	if got := Collect(context.Background(), &fakeLogs{}, fakeCatalog{}, nil); got != nil {
		t.Fatalf("no deployments means no verdicts; got %v", got)
	}
}

// TestCollectIgnoresAnUnlabelledContainer: a container with no slug label cannot be
// tied to a catalog entry, and keying a verdict under "" would put a badge on nothing.
func TestCollectIgnoresAnUnlabelledContainer(t *testing.T) {
	got := Collect(context.Background(), &fakeLogs{}, fakeCatalog{}, []Deployed{{Slug: "", ContainerState: "running"}})
	if _, ok := got[""]; ok {
		t.Fatal("an unlabelled container must not get a verdict")
	}
}
