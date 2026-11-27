package health

import (
	"context"
	"strings"
	"testing"
)

// The lines a Pawns copy logged for hours in Nov 2026 while Pawns refused it
// because another live device held its IP, and the line a copy that shares logs.
const (
	pawnsStarting = `{"happened_at":"2026-11-27T17:08:09Z","name":"starting","parameters":{}}`
	pawnsIPUsed   = `{"happened_at":"2026-11-27T17:09:23Z","name":"not_running","parameters":{"error":"ip_used","message":"getting free port failed (ip has alive peer)"}}`
	pawnsRunning  = `{"happened_at":"2026-11-27T19:30:00Z","name":"running","parameters":{}}`
)

var pawnsBlocked = pawnsStarting + "\n" + strings.Repeat(pawnsIPUsed+"\n", 24)

func collectPawns(t *testing.T, reader *fakeWindow) Report {
	t.Helper()
	lookup := fakeCatalog{"iproyal": realService(t, "iproyal")}
	deployed := []Deployed{{Slug: "iproyal", ContainerState: "running", Runtime: "docker"}}
	Collect(context.Background(), reader, lookup, deployed)
	windows.wg.Wait()
	return Collect(context.Background(), reader, lookup, deployed)["iproyal"]
}

func TestAPawnsCopyRefusedForItsIPIsFlaggedWithWhatToDo(t *testing.T) {
	freshWindows(t)
	reader := &fakeWindow{}
	reader.set(pawnsBlocked, false)
	got := collectPawns(t, reader)
	if got.State != StateFailing {
		t.Fatalf("state = %s, want failing: %s", got.State, reasonText(got))
	}
	for _, want := range []string{"another live device on this IP address", "Stop any other Pawns app on this IP", "ask Pawns support to release it"} {
		if !strings.Contains(reasonText(got), want) {
			t.Errorf("reasons %q lack %q", reasonText(got), want)
		}
	}
}

func TestThePawnsHintClearsOnceTheCopyShares(t *testing.T) {
	now := freshWindows(t)
	reader := &fakeWindow{}
	reader.set(pawnsBlocked, false)
	if got := collectPawns(t, reader); got.State != StateFailing {
		t.Fatalf("precondition: state = %s", got.State)
	}
	reader.set(pawnsBlocked+pawnsRunning, false)
	*now = now.Add(windowEvery)
	if got := collectPawns(t, reader); got.State == StateFailing {
		t.Fatalf("still failing after a running line: %s", reasonText(got))
	}
}

func TestAFewPawnsRefusalsAtStartAreNotYetAHint(t *testing.T) {
	freshWindows(t)
	reader := &fakeWindow{}
	reader.set(pawnsStarting+"\n"+strings.Repeat(pawnsIPUsed+"\n", 4), false)
	if got := collectPawns(t, reader); got.State == StateFailing {
		t.Fatalf("flagged on four refusals: %s", reasonText(got))
	}
}
