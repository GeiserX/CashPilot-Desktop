package health

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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
		[]Deployed{{Slug: "demo", ContainerState: "restarting"}})

	if !got["demo"].State.NotEarning() {
		t.Fatalf("a restart-looping service must be reported; got %q", got["demo"].State)
	}
	if asked := logs.asked(); len(asked) != 0 {
		t.Errorf("no pattern to match means no log read; asked %v", asked)
	}
}

// TestANativelyRunServiceIsNotAskedForLogs. A supervised process keeps its log file
// across a stop and a start, so a line the catalog matched an hour ago is still there
// after the user fixed it; and the explanation it would show describes a container
// that does not exist here. Neither is worth a read.
func TestANativelyRunServiceIsNotAskedForLogs(t *testing.T) {
	logs := &fakeLogs{out: "ERR login failed\n"}
	cat := fakeCatalog{"demo": withSignals(loginFailed)}

	got := Collect(context.Background(), logs, cat,
		[]Deployed{{Slug: "demo", ContainerState: "running", Runtime: NativeRuntime}})

	if asked := logs.asked(); len(asked) != 0 {
		t.Errorf("a native process's logs must not be matched against container signals; asked %v", asked)
	}
	if got["demo"].State.NotEarning() {
		t.Fatalf("and it must not be condemned by them either; got %q %v", got["demo"].State, got["demo"].Reasons)
	}

	// CONTROL: the identical service under a container runtime is still judged.
	inContainer := Collect(context.Background(), &fakeLogs{out: "ERR login failed\n"}, cat,
		[]Deployed{{Slug: "demo", ContainerState: "running", Runtime: "docker"}})
	if inContainer["demo"].State != StateFailing {
		t.Fatalf("CONTROL: the same signal in a container must still fire; got %q", inContainer["demo"].State)
	}
}

// deadlineLogs is a log reader that reports the budget its caller gave it, and then
// behaves like a runtime that will not answer.
type deadlineLogs struct {
	hadDeadline bool
	budget      time.Duration
}

func (d *deadlineLogs) Logs(ctx context.Context, _ string, _ int) (string, error) {
	if deadline, ok := ctx.Deadline(); ok {
		d.hadDeadline = true
		d.budget = time.Until(deadline)
	}
	return "", errors.New("container runtime is not answering")
}

// TestEachLogReadIsBounded. GetAppState reads these logs one service after another,
// and it is the first call the window makes at boot, so a container runtime that has
// wedged must cost a missing badge rather than a window that never paints. Without a
// deadline the read waits on the runtime's own timeout, which can be forever.
func TestEachLogReadIsBounded(t *testing.T) {
	reader := &deadlineLogs{}

	got := Collect(context.Background(), reader, fakeCatalog{"demo": withSignals(loginFailed)},
		[]Deployed{{Slug: "demo", ContainerState: "running"}})

	if !reader.hadDeadline {
		t.Fatal("a log read with no deadline lets one wedged container freeze the dashboard")
	}
	if reader.budget <= 0 || reader.budget > logTimeout {
		t.Errorf("the budget for one read must be at most %s; got %s", logTimeout, reader.budget)
	}
	if got["demo"].State != StateNotChecked {
		t.Errorf("a read that ran out of time supports no finding; got %q", got["demo"].State)
	}
}

// TestABoundedReadStillHonoursASoonerCaller: the caller's own deadline wins when it
// is the tighter one, so a refresh that is already being cancelled does not sit here
// for another three seconds per service.
func TestABoundedReadStillHonoursASoonerCaller(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	reader := &deadlineLogs{}

	Collect(ctx, reader, fakeCatalog{"demo": withSignals(loginFailed)},
		[]Deployed{{Slug: "demo", ContainerState: "running"}})

	if !reader.hadDeadline {
		t.Fatal("the read must still carry a deadline")
	}
	if reader.budget > 20*time.Millisecond {
		t.Errorf("the caller's tighter deadline must survive; got a budget of %s", reader.budget)
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
