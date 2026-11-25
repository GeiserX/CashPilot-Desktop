package stall

import (
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// now sits 25 minutes into an hour, so the hour in progress is never judged.
const now = int64(1_800_000_000/3600*3600 + 1500)

// lastHour is the last whole hour before now, written as a literal so a judge
// that reads the hour in progress fails these tests rather than moving with them.
const lastHour = int64(1_800_000_000/3600*3600 - 3600)

var spec = Spec{Share: 0.05, PeerMinBytes: 10 * MB, Hours: 12}

// hours adds one row per hour for container, over the n whole hours that end at
// the last whole hour (and the hour in progress when current is set), sending
// perHour bytes in each.
func hours(container string, n int, perHour int64, current bool) []store.UsageHour {
	last := lastHour
	var rows []store.UsageHour
	for i := 0; i < n; i++ {
		tx := perHour
		rows = append(rows, store.UsageHour{Container: container, Slug: "honeygain", Hour: last - 3600*int64(i), TxBytes: &tx, NetSeconds: 3600})
	}
	if current {
		tx := perHour
		rows = append(rows, store.UsageHour{Container: container, Slug: "honeygain", Hour: last + 3600, TxBytes: &tx, NetSeconds: 1500})
	}
	return rows
}

func join(parts ...[]store.UsageHour) []store.UsageHour {
	var out []store.UsageHour
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestLastWholeHourIsTheHourThatEnded(t *testing.T) {
	if got, want := LastWholeHour(now), lastHour; got != want {
		t.Fatalf("LastWholeHour = %d, want %d", got, want)
	}
}

// One copy sends 100 kB an hour for 14 hours while two others send 30 MB: it is
// stalled, since the first of those hours, and the busy copies carry traffic.
func TestJudgeFindsTheQuietCopy(t *testing.T) {
	rows := join(hours("hg-1", 14, 30*MB, false), hours("hg-2", 14, 100_000, false), hours("hg-3", 14, 32*MB, false))
	stalled, carrying := Judge(rows, spec, now)
	got, ok := stalled["hg-2"]
	if !ok || len(stalled) != 1 {
		t.Fatalf("stalled = %+v, want hg-2 alone", stalled)
	}
	if got.Hours != 14 || got.Since != lastHour-13*3600 {
		t.Fatalf("hg-2 = %+v, want 14 hours since %d", got, lastHour-13*3600)
	}
	if got.Sent != 100_000 || got.Peers != 31*MB {
		t.Fatalf("hg-2 sent %v peers %v, want 100000 and 31 MB", got.Sent, got.Peers)
	}
	if !carrying["hg-1"] || !carrying["hg-3"] || carrying["hg-2"] {
		t.Fatalf("carrying = %v, want hg-1 and hg-3", carrying)
	}
}

// Eleven low hours are one short of the window: nothing yet.
func TestJudgeWaitsForTheWholeWindow(t *testing.T) {
	rows := join(hours("hg-1", 12, 30*MB, false), hours("hg-2", 11, 100_000, false))
	if stalled, _ := Judge(rows, spec, now); len(stalled) != 0 {
		t.Fatalf("stalled = %+v, want none after 11 hours", stalled)
	}
}

// A copy alone has no peers to compare with, and quiet peers say nothing.
func TestJudgeNeedsBusyPeers(t *testing.T) {
	if stalled, _ := Judge(hours("hg-1", 12, 0, false), spec, now); len(stalled) != 0 {
		t.Fatalf("a lone copy was judged: %+v", stalled)
	}
	rows := join(hours("hg-1", 12, 5*MB, false), hours("hg-2", 12, 0, false))
	if stalled, _ := Judge(rows, spec, now); len(stalled) != 0 {
		t.Fatalf("peers under the floor still judged a stall: %+v", stalled)
	}
}

// The hour in progress is never judged: a quiet copy whose current partial hour is
// busy is still stalled, and a busy copy with a quiet partial hour is not.
func TestJudgeIgnoresTheHourInProgress(t *testing.T) {
	quiet := hours("hg-2", 12, 0, false)
	tx := int64(50 * MB)
	quiet = append(quiet, store.UsageHour{Container: "hg-2", Slug: "honeygain", Hour: lastHour + 3600, TxBytes: &tx, NetSeconds: 1500})
	stalled, _ := Judge(join(hours("hg-1", 12, 30*MB, true), quiet), spec, now)
	if _, ok := stalled["hg-2"]; !ok {
		t.Fatalf("stalled = %+v, want hg-2 (its busy hour is still in progress)", stalled)
	}
}

// An hour with no counter is a gap, never a zero.
func TestJudgeTreatsAMissingCounterAsAGap(t *testing.T) {
	quiet := hours("hg-2", 12, 0, false)
	quiet[5].TxBytes = nil
	if stalled, _ := Judge(join(hours("hg-1", 12, 30*MB, false), quiet), spec, now); len(stalled) != 0 {
		t.Fatalf("stalled = %+v, want none across a gap", stalled)
	}
}

// The last whole hour clears a stall in either way the web counts as traffic.
func TestJudgeSeesTrafficComeBack(t *testing.T) {
	back := hours("hg-2", 12, 0, false)
	tx := int64(2 * MB) // under the floor, over 5% of the peers' 30 MB
	back[0].TxBytes = &tx
	stalled, carrying := Judge(join(hours("hg-1", 12, 30*MB, false), back), spec, now)
	if len(stalled) != 0 || !carrying["hg-2"] {
		t.Fatalf("stalled %+v carrying %v, want hg-2 carrying", stalled, carrying)
	}
}

func TestSpecOfFillsDefaultsAndRefusesBadBlocks(t *testing.T) {
	if SpecOf(nil) != nil {
		t.Fatal("no block gave a spec")
	}
	got := SpecOf(&catalog.TrafficStall{})
	if got == nil || *got != (Spec{Share: DefaultShare, PeerMinBytes: DefaultPeerMinMBPerHour * MB, Hours: DefaultHours}) {
		t.Fatalf("empty block = %+v, want the defaults", got)
	}
	for _, bad := range []catalog.TrafficStall{{Share: 1}, {Share: -0.1}, {PeerMinMBPerHour: -1}, {Hours: MaxHours + 1}, {Hours: -2}} {
		if SpecOf(&bad) != nil {
			t.Errorf("%+v gave a spec, want none", bad)
		}
	}
}
