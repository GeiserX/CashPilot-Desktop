package health

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// fakeCopies is a runtime with several copies of one service, each with its own
// window of logs, that can restart any of them. It also serves the first copy, so a
// test can run Collect and CheckCopies side by side.
type fakeCopies struct {
	fakeRestarter
	cmu      sync.Mutex
	windows  map[string]string // Copy.Label() -> its logs
	reads    []string
	restarts []string
}

func newFakeCopies() *fakeCopies { return &fakeCopies{windows: map[string]string{}} }

func (f *fakeCopies) setCopy(c Copy, logs string) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	f.windows[c.Label()] = logs
}

func (f *fakeCopies) CopyLogsMatching(_ context.Context, slug string, instance int, proxyID int64, _ time.Duration, match *regexp.Regexp, _ int) (string, error) {
	label := Copy{Slug: slug, Instance: instance, ProxyID: proxyID}.Label()
	f.cmu.Lock()
	defer f.cmu.Unlock()
	f.reads = append(f.reads, label)
	window, ok := f.windows[label]
	if !ok {
		return "", errors.New("no such copy")
	}
	var kept []string
	for _, line := range strings.Split(window, "\n") {
		if match.MatchString(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n"), nil
}

func (f *fakeCopies) RestartStuckCopy(_ context.Context, slug string, instance int, proxyID int64, _ time.Time, reason string) error {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	f.restarts = append(f.restarts, Copy{Slug: slug, Instance: instance, ProxyID: proxyID}.Label()+": "+reason)
	return nil
}

func (f *fakeCopies) restarted() []string {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	return append([]string(nil), f.restarts...)
}

func (f *fakeCopies) copyReads() []string {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	return append([]string(nil), f.reads...)
}

// checkCopies runs one round of CheckCopies and waits for its background checks.
func checkCopies(t *testing.T, f *fakeCopies, svc catalog.Service, copies ...Copy) {
	t.Helper()
	CheckCopies(f, fakeCatalog{svc.Slug: svc}, copies)
	windows.wg.Wait()
}

var (
	copy2   = Copy{Slug: "proxybase", Instance: 2, State: "running"}
	copy3   = Copy{Slug: "proxybase", Instance: 3, State: "running"}
	proxied = Copy{Slug: "proxybase", ProxyID: 4, State: "running"}
)

// The gap this closes: a stuck copy 2 was never read and never restarted, only the
// first copy was.
func TestAStuckSecondCopyIsRestarted(t *testing.T) {
	freshWindows(t)
	f := newFakeCopies()
	f.setCopy(copy2, deadRelay)
	f.setCopy(copy3, deadRelay+"Balance: $0.42\n")
	checkCopies(t, f, realService(t, "proxybase"), copy2, copy3)
	got := f.restarted()
	if len(got) != 1 || !strings.HasPrefix(got[0], "proxybase copy 2: ProxyBase has not reached its relay") {
		t.Fatalf("restarts = %v, want copy 2 alone, with the signal's means", got)
	}
}

// A copy through a proxy is read through its own earner and restarted the same way.
func TestAStuckProxiedCopyIsRestarted(t *testing.T) {
	freshWindows(t)
	f := newFakeCopies()
	f.setCopy(proxied, deadRelay)
	checkCopies(t, f, realService(t, "proxybase"), proxied)
	got := f.restarted()
	if len(got) != 1 || !strings.HasPrefix(got[0], "proxybase through proxy 4: ") {
		t.Fatalf("restarts = %v, want the copy through proxy 4", got)
	}
}

func TestHealthyAndStoppedCopiesAreLeftAlone(t *testing.T) {
	freshWindows(t)
	f := newFakeCopies()
	earning := deadRelay + "Balance: $0.42\n"
	f.setCopy(copy2, earning)
	f.setCopy(proxied, earning)
	f.setCopy(copy3, deadRelay)
	stopped := copy3
	stopped.State = "exited"
	tunnelDown := Copy{Slug: "proxybase", ProxyID: 5, State: "tunnel exited"}
	f.setCopy(tunnelDown, deadRelay)
	checkCopies(t, f, realService(t, "proxybase"), copy2, proxied, stopped, tunnelDown)
	if got := f.restarted(); len(got) != 0 {
		t.Fatalf("restarted a copy that earns or is not up: %v", got)
	}
	for _, read := range f.copyReads() {
		if read == stopped.Label() || read == tunnelDown.Label() {
			t.Fatalf("read the logs of a copy that is not up: %v", f.copyReads())
		}
	}
}

// Each copy has its own 6-hour wait. A restart of one copy, or of the first copy,
// does not hold back another, and none restarts twice inside its wait.
func TestTheWaitBetweenRestartsIsPerCopy(t *testing.T) {
	now := freshWindows(t)
	svc := realService(t, "proxybase")
	f := newFakeCopies()
	f.set(deadRelay, false) // the first copy, read through Collect
	f.setCopy(copy2, deadRelay)
	f.setCopy(proxied, deadRelay)
	f.setCopy(copy3, deadRelay+"Balance: $0.42\n")

	check(t, f, svc, "running")
	checkCopies(t, f, svc, copy2, copy3, proxied)
	if f.count() != 1 {
		t.Fatalf("first copy restarts = %v, want one", f.fakeRestarter.restarts)
	}
	if got := f.restarted(); len(got) != 2 {
		t.Fatalf("restarts = %v, want copy 2 and the proxied copy", got)
	}

	// Half an hour on, copy 3 is stuck too: it restarts although the others are inside their wait.
	*now = now.Add(windowEvery)
	f.setCopy(copy3, deadRelay)
	check(t, f, svc, "running")
	checkCopies(t, f, svc, copy2, copy3, proxied)
	if f.count() != 1 {
		t.Fatalf("the first copy restarted again inside its wait: %v", f.fakeRestarter.restarts)
	}
	got := f.restarted()
	if len(got) != 3 || !strings.HasPrefix(got[2], "proxybase copy 3: ") {
		t.Fatalf("restarts = %v, want copy 3 added and no second restart of the others", got)
	}

	// Once copy 2's wait is over, it restarts again; copy 3's wait is not over yet.
	*now = now.Add(restartEvery - windowEvery)
	checkCopies(t, f, svc, copy2, copy3, proxied)
	got = f.restarted()
	want := map[string]bool{"proxybase copy 2": true, "proxybase through proxy 4": true}
	if len(got) != 5 {
		t.Fatalf("restarts after copy 2's wait = %v, want copy 2 and the proxied copy again", got)
	}
	for _, r := range got[3:] {
		label, _, _ := strings.Cut(r, ":")
		if !want[label] {
			t.Fatalf("restarted %q inside its wait: %v", label, got)
		}
	}
}

// No page shows a verdict for these copies, so a signal that only warns is never
// worth hours of their logs.
func TestASignalThatOnlyWarnsIsNotReadForACopy(t *testing.T) {
	freshWindows(t)
	f := newFakeCopies()
	mysterium := realService(t, "mysterium")
	for _, c := range []Copy{{Slug: "mysterium", Instance: 2, State: "running"}, {Slug: "mysterium", ProxyID: 4, State: "running"}} {
		f.setCopy(c, failing)
	}
	CheckCopies(f, fakeCatalog{"mysterium": mysterium}, []Copy{{Slug: "mysterium", Instance: 2, State: "running"}, {Slug: "mysterium", ProxyID: 4, State: "running"}})
	windows.wg.Wait()
	if reads := f.copyReads(); len(reads) != 0 {
		t.Fatalf("read a copy for a signal without restart: true: %v", reads)
	}
}

func TestCopyKeysNeverMeetASlug(t *testing.T) {
	for _, c := range []Copy{copy2, proxied} {
		if k := c.key(); k == c.Slug || !strings.ContainsAny(k, "#@") {
			t.Fatalf("key %q could be a slug", k)
		}
	}
	if a, b := (Copy{Slug: "x", Instance: 2}).key(), (Copy{Slug: "x", ProxyID: 2}).key(); a == b {
		t.Fatalf("copy 2 and the copy through proxy 2 share key %q", a)
	}
}
