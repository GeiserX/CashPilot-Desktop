package health

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	failedDirect = "2026-10-29T10:00:00Z DBG event={Identity:0xabc Method:manual Success:false}"
	punched      = "2026-10-29T10:00:01Z DBG event={Identity:0xabc Method:holepunching Success:true}"
	direct       = "2026-10-29T12:00:00Z DBG event={Identity:0xabc Method:manual Success:true}"
)

// fakeWindow is a runtime that can read a window of logs. It filters `window` with
// the regex it is handed, as the Docker provider does.
type fakeWindow struct {
	fakeLogs
	mu     sync.Mutex
	window string
	fail   bool
	since  time.Duration
	vars   map[string]string
}

func (f *fakeWindow) LogsMatching(_ context.Context, _ string, since time.Duration, match *regexp.Regexp, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.since = since
	if f.fail {
		return "", errors.New("container runtime is not answering")
	}
	var kept []string
	for _, line := range strings.Split(f.window, "\n") {
		if match.MatchString(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n"), nil
}

func (f *fakeWindow) SignalVars(string) map[string]string { return f.vars }

func (f *fakeWindow) set(window string, fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.window, f.fail = window, fail
}

// freshWindows gives each test its own cache and clock.
func freshWindows(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	old := windows
	windows = &windowCache{entries: map[string]*windowEntry{}, now: func() time.Time { return now }}
	saved := lanAddress
	lanAddress = func() string { return "192.168.1.20" }
	t.Cleanup(func() { windows, lanAddress = old, saved })
	return &now
}

// collect runs one dashboard refresh, waits for the background check it starts,
// then reads the verdict the next refresh would show.
func collect(t *testing.T, reader *fakeWindow, state string) Report {
	t.Helper()
	lookup := fakeCatalog{"mysterium": realService(t, "mysterium")}
	deployed := []Deployed{{Slug: "mysterium", ContainerState: state, Runtime: "docker"}}
	Collect(context.Background(), reader, lookup, deployed)
	windows.wg.Wait()
	return Collect(context.Background(), reader, lookup, deployed)["mysterium"]
}

func TestOnlyFailedDirectConnectionsFlagTheNodeWithTheRuleToCreate(t *testing.T) {
	freshWindows(t)
	reader := &fakeWindow{vars: map[string]string{"UDP_PORTS": "56000:56100"}}
	reader.set(strings.Join([]string{failedDirect, punched, failedDirect}, "\n"), false)
	got := collect(t, reader, "running")
	if got.State != StateFailing {
		t.Fatalf("state = %s, want failing: %s", got.State, reasonText(got))
	}
	for _, want := range []string{"forward the UDP port range 56000:56100", "to 192.168.1.20"} {
		if !strings.Contains(reasonText(got), want) {
			t.Errorf("reasons %q lack %q", reasonText(got), want)
		}
	}
	if reader.since != 6*time.Hour {
		t.Errorf("window = %s, want the catalog's 6h", reader.since)
	}
}

func TestItClearsOnceASessionConnectsDirectly(t *testing.T) {
	now := freshWindows(t)
	reader := &fakeWindow{}
	reader.set(failedDirect, false)
	if got := collect(t, reader, "running"); got.State != StateFailing {
		t.Fatalf("precondition: state = %s", got.State)
	}
	reader.set(failedDirect+"\n"+direct, false)
	*now = now.Add(windowEvery)
	if got := collect(t, reader, "running"); got.State == StateFailing {
		t.Fatalf("still failing after a direct session: %s", reasonText(got))
	}
}

func TestANodeWithNoSessionsIsNotFlagged(t *testing.T) {
	freshWindows(t)
	reader := &fakeWindow{}
	reader.set(punched, false)
	if got := collect(t, reader, "running"); got.State == StateFailing {
		t.Fatalf("flagged with no direct attempt: %s", reasonText(got))
	}
}

func TestAFailedReadKeepsTheVerdictAndAStoppedNodeDropsIt(t *testing.T) {
	now := freshWindows(t)
	reader := &fakeWindow{}
	reader.set(failedDirect, false)
	collect(t, reader, "running")
	reader.set("", true)
	*now = now.Add(windowEvery)
	if got := collect(t, reader, "running"); got.State != StateFailing {
		t.Fatalf("a runtime blip cleared a real problem: %s", reasonText(got))
	}
	Collect(context.Background(), reader, fakeCatalog{"mysterium": realService(t, "mysterium")},
		[]Deployed{{Slug: "mysterium", ContainerState: "exited", Runtime: "docker"}})
	if _, ok := windows.entries["mysterium"]; ok {
		t.Fatal("a stopped node kept its verdict")
	}
}

func TestTheTailMatcherLeavesWindowedSignalsAlone(t *testing.T) {
	if hits := MatchSignals(failedDirect, realService(t, "mysterium").Docker.HealthSignals); len(hits) != 0 {
		t.Fatalf("the tail judged a windowed signal: %+v", hits)
	}
}

func TestARuntimeThatCannotReadAWindowJudgesNothing(t *testing.T) {
	freshWindows(t)
	logs := &fakeLogs{out: failedDirect}
	lookup := fakeCatalog{"mysterium": realService(t, "mysterium")}
	got := Collect(context.Background(), logs, lookup, []Deployed{{Slug: "mysterium", ContainerState: "running"}})
	if got["mysterium"].State == StateFailing {
		t.Fatalf("flagged from a tail: %s", reasonText(got["mysterium"]))
	}
}
