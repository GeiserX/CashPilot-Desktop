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

const (
	failedDirect = "2026-10-29T10:00:00Z DBG event={Identity:0xabc Method:manual Success:false}"
	punched      = "2026-10-29T10:00:01Z DBG event={Identity:0xabc Method:holepunching Success:true}"
	direct       = "2026-10-29T12:00:00Z DBG event={Identity:0xabc Method:manual Success:true}"
)

// failing is five failed direct attempts, the catalog's min_matches.
var failing = strings.Repeat(failedDirect+"\n"+punched+"\n", 5)

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
	reader.set(failing, false)
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
	reader.set(failing, false)
	if got := collect(t, reader, "running"); got.State != StateFailing {
		t.Fatalf("precondition: state = %s", got.State)
	}
	reader.set(failing+direct, false)
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
	reader.set(failing, false)
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

func TestAFewFailedAttemptsAreNotYetAPattern(t *testing.T) {
	freshWindows(t)
	reader := &fakeWindow{}
	reader.set(strings.Repeat(failedDirect+"\n", 4), false)
	if got := collect(t, reader, "running"); got.State == StateFailing {
		t.Fatalf("flagged on four attempts: %s", reasonText(got))
	}
}

func TestFillVarsKeepsAnUnknownPlaceholderAndNamesTheHost(t *testing.T) {
	freshWindows(t)
	got := fillVars("forward ${UDP_PORTS} to ${HOST_ADDRESS} (${NOPE})", map[string]string{"UDP_PORTS": "1:2"})
	if got != "forward 1:2 to 192.168.1.20 (${NOPE})" {
		t.Fatalf("fillVars = %q", got)
	}
}

func TestTunnelInterfacesAreNeverTheLANAddress(t *testing.T) {
	for _, name := range []string{"utun5", "tun0", "wg0", "tailscale0", "gpd0", "ppp0"} {
		if !tunnelName(name) {
			t.Errorf("%s not treated as a tunnel", name)
		}
	}
	for _, name := range []string{"en0", "eth0", "wlan0", "Ethernet"} {
		if tunnelName(name) {
			t.Errorf("%s treated as a tunnel", name)
		}
	}
	// Whatever this machine's network, the answer is an address or the generic text.
	if got := lanAddress(); got == "" {
		t.Fatal("lanAddress returned nothing")
	}
}

func TestABrokenSignalNeverFires(t *testing.T) {
	broken := []catalog.HealthSignal{
		{Pattern: "(", WindowHours: 1},
		{Pattern: "bad", Unless: "(", WindowHours: 1},
		{Pattern: "", WindowHours: 1},
	}
	for _, s := range broken {
		if WindowedHit("bad", s) {
			t.Errorf("%+v fired", s)
		}
	}
}

func TestARemoteDockerHostIsNeverNamedAsThisComputer(t *testing.T) {
	for host, remote := range map[string]bool{
		"":                                     false,
		"unix:///Users/me/.colima/docker.sock": false,
		"npipe:////./pipe/docker_engine":       false,
		"tcp://192.168.1.50:2375":              true,
		"ssh://me@nas":                         true,
	} {
		if remoteDocker(host) != remote {
			t.Errorf("remoteDocker(%q) = %v, want %v", host, !remote, remote)
		}
	}
	t.Setenv("DOCKER_HOST", "ssh://me@nas")
	if got := lanAddress(); got != "the LAN address of the machine running Docker" {
		t.Fatalf("lanAddress with a remote DOCKER_HOST = %q", got)
	}
}
